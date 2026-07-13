package instance

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	databaseOwnerProtocolVersion = 1
	databaseOwnerPortBase        = 20000
	databaseOwnerPortSpan        = 20000
	databaseOwnerPortCount       = 1
	defaultDatabaseLockTimeout   = 500 * time.Millisecond
	maximumDatabaseLockTimeout   = 30 * time.Second
)

var (
	ErrDatabaseIdentityInvalid  = errors.New("DATABASE_IDENTITY_INVALID")
	ErrDatabaseOwnerLocked      = errors.New("DATABASE_OWNER_LOCKED")
	ErrDatabaseOwnerUnavailable = errors.New("DATABASE_OWNER_UNAVAILABLE")
	ErrDatabaseOwnerRelease     = errors.New("DATABASE_OWNER_RELEASE_FAILED")
)

type DatabaseLockOptions struct {
	Target      string
	AllowCreate bool
	Timeout     time.Duration
}

type databaseIdentity struct {
	canonicalPath string
	databaseID    string
	host          string
	ports         []int
}

type databaseOwnerRecord struct {
	Version     int    `json:"version"`
	DatabaseID  string `json:"database_id"`
	PIDDigest   string `json:"pid_digest"`
	OwnerDigest string `json:"owner_digest"`
	Ports       []int  `json:"ports"`
}

// DatabaseLock holds OS-owned loopback listeners derived from the canonical
// database identity. Metadata is diagnostic only: a stale file never owns the
// lock, while the listeners are released automatically if the process exits.
type DatabaseLock struct {
	listeners    []net.Listener
	metadata     string
	metadataInfo os.FileInfo
	databaseID   string
	ownerDigest  string
	once         sync.Once
	result       error
}

func AcquireDatabaseLock(ctx context.Context, options DatabaseLockOptions) (*DatabaseLock, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	identity, err := resolveDatabaseIdentity(options.Target, options.AllowCreate)
	if err != nil {
		return nil, err
	}
	timeout := options.Timeout
	if timeout <= 0 {
		timeout = defaultDatabaseLockTimeout
	}
	if timeout > maximumDatabaseLockTimeout {
		return nil, ErrDatabaseOwnerUnavailable
	}
	deadline := time.Now().Add(timeout)
	for {
		listeners, listenErr := listenDatabaseOwnerPorts(identity.host, identity.ports)
		if listenErr == nil {
			lock, metadataErr := createDatabaseOwnerMetadata(identity, listeners)
			if metadataErr != nil {
				closeDatabaseOwnerListeners(listeners)
				return nil, metadataErr
			}
			return lock, nil
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !time.Now().Before(deadline) {
			return nil, ErrDatabaseOwnerLocked
		}
		remaining := time.Until(deadline)
		if remaining > 25*time.Millisecond {
			remaining = 25 * time.Millisecond
		}
		timer := time.NewTimer(remaining)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func (lock *DatabaseLock) DatabaseID() string {
	if lock == nil {
		return ""
	}
	return lock.databaseID
}

func (lock *DatabaseLock) Close(context.Context) error {
	if lock == nil {
		return nil
	}
	lock.once.Do(func() {
		validMetadata := false
		content, err := os.ReadFile(lock.metadata)
		currentInfo, statErr := os.Lstat(lock.metadata)
		if err == nil && statErr == nil && lock.metadataInfo != nil &&
			os.SameFile(lock.metadataInfo, currentInfo) && len(content) <= 4096 {
			var record databaseOwnerRecord
			if json.Unmarshal(content, &record) == nil && record.Version == databaseOwnerProtocolVersion &&
				record.DatabaseID == lock.databaseID && record.OwnerDigest == lock.ownerDigest {
				validMetadata = true
			}
		}
		if !validMetadata || os.Remove(lock.metadata) != nil {
			lock.result = ErrDatabaseOwnerRelease
		}
		if closeDatabaseOwnerListeners(lock.listeners) != nil {
			lock.result = ErrDatabaseOwnerRelease
		}
		lock.listeners = nil
	})
	return lock.result
}

func ResolveDatabaseID(target string, allowCreate bool) (string, error) {
	identity, err := resolveDatabaseIdentity(target, allowCreate)
	if err != nil {
		return "", err
	}
	return identity.databaseID, nil
}

func resolveDatabaseIdentity(target string, allowCreate bool) (databaseIdentity, error) {
	if strings.TrimSpace(target) == "" || strings.TrimSpace(target) != target {
		return databaseIdentity{}, ErrDatabaseIdentityInvalid
	}
	absolute, err := filepath.Abs(target)
	if err != nil {
		return databaseIdentity{}, ErrDatabaseIdentityInvalid
	}
	absolute = filepath.Clean(absolute)
	info, lstatErr := os.Lstat(absolute)
	canonical := ""
	switch {
	case lstatErr == nil:
		resolved, resolveErr := filepath.EvalSymlinks(absolute)
		if resolveErr != nil {
			return databaseIdentity{}, ErrDatabaseIdentityInvalid
		}
		resolvedInfo, statErr := os.Stat(resolved)
		if statErr != nil || !resolvedInfo.Mode().IsRegular() || info.IsDir() {
			return databaseIdentity{}, ErrDatabaseIdentityInvalid
		}
		canonical = filepath.Clean(resolved)
	case os.IsNotExist(lstatErr) && allowCreate:
		parent := filepath.Dir(absolute)
		parentInfo, parentErr := os.Lstat(parent)
		if parentErr != nil || !parentInfo.IsDir() || parentInfo.Mode()&os.ModeSymlink != 0 {
			return databaseIdentity{}, ErrDatabaseIdentityInvalid
		}
		resolvedParent, resolveErr := filepath.EvalSymlinks(parent)
		if resolveErr != nil {
			return databaseIdentity{}, ErrDatabaseIdentityInvalid
		}
		canonical = filepath.Join(resolvedParent, filepath.Base(absolute))
	default:
		return databaseIdentity{}, ErrDatabaseIdentityInvalid
	}
	identityPath := normalizeDatabaseIdentityPath(canonical)
	digest := sha256.Sum256([]byte("autoplan-database-owner-v1\x00" + identityPath))
	return databaseIdentity{
		canonicalPath: canonical,
		databaseID:    hex.EncodeToString(digest[:8]),
		host:          databaseOwnerHost(digest, runtime.GOOS),
		ports:         databaseOwnerPorts(digest),
	}, nil
}

func databaseOwnerHost(digest [sha256.Size]byte, goos string) string {
	if goos == "darwin" {
		// Some Darwin environments expose only 127.0.0.1 as a bindable
		// loopback address. The path-derived port still provides the database
		// identity while remaining compatible with macOS hosted runners.
		return "127.0.0.1"
	}
	return net.IPv4(127, 1+digest[2]%254, digest[3], digest[4]).String()
}

func normalizeDatabaseIdentityPath(value string) string {
	result := filepath.Clean(value)
	if runtime.GOOS == "windows" {
		if strings.HasPrefix(result, `\\?\UNC\`) {
			result = `\\` + strings.TrimPrefix(result, `\\?\UNC\`)
		} else {
			result = strings.TrimPrefix(result, `\\?\`)
		}
	}
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		result = strings.ToLower(result)
	}
	return result
}

func databaseOwnerPorts(digest [sha256.Size]byte) []int {
	ports := make([]int, 0, databaseOwnerPortCount)
	seen := make(map[int]struct{}, databaseOwnerPortCount)
	for index := 0; len(ports) < databaseOwnerPortCount; index += 2 {
		if index+1 >= len(digest) {
			index = 0
		}
		port := databaseOwnerPortBase + int(binary.BigEndian.Uint16(digest[index:index+2]))%databaseOwnerPortSpan
		for {
			if _, duplicate := seen[port]; !duplicate {
				break
			}
			port = databaseOwnerPortBase + (port-databaseOwnerPortBase+1)%databaseOwnerPortSpan
		}
		seen[port] = struct{}{}
		ports = append(ports, port)
	}
	sort.Ints(ports)
	return ports
}

func listenDatabaseOwnerPorts(host string, ports []int) ([]net.Listener, error) {
	listeners := make([]net.Listener, 0, len(ports))
	for _, port := range ports {
		listener, err := net.Listen("tcp4", net.JoinHostPort(host, strconv.Itoa(port)))
		if err != nil {
			_ = closeDatabaseOwnerListeners(listeners)
			return nil, err
		}
		listeners = append(listeners, listener)
	}
	return listeners, nil
}

func createDatabaseOwnerMetadata(identity databaseIdentity, listeners []net.Listener) (*DatabaseLock, error) {
	owner := make([]byte, 16)
	if _, err := rand.Read(owner); err != nil {
		return nil, ErrDatabaseOwnerUnavailable
	}
	ownerHash := sha256.Sum256(owner)
	ownerDigest := hex.EncodeToString(ownerHash[:8])
	pidHash := sha256.Sum256([]byte(strconv.Itoa(os.Getpid()) + "\x00" + ownerDigest))
	record := databaseOwnerRecord{
		Version: databaseOwnerProtocolVersion, DatabaseID: identity.databaseID,
		PIDDigest: hex.EncodeToString(pidHash[:8]), OwnerDigest: ownerDigest,
		Ports: append([]int(nil), identity.ports...),
	}
	content, err := json.Marshal(record)
	if err != nil {
		return nil, ErrDatabaseOwnerUnavailable
	}
	content = append(content, '\n')
	metadata := identity.canonicalPath + ".autoplan-owner.lock"
	if info, err := os.Lstat(metadata); err == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || os.Remove(metadata) != nil {
			return nil, ErrDatabaseOwnerUnavailable
		}
	} else if !os.IsNotExist(err) {
		return nil, ErrDatabaseOwnerUnavailable
	}
	file, err := os.OpenFile(metadata, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, ErrDatabaseOwnerUnavailable
	}
	written, writeErr := file.Write(content)
	syncErr := file.Sync()
	metadataInfo, statErr := file.Stat()
	closeErr := file.Close()
	if writeErr != nil || written != len(content) || syncErr != nil || statErr != nil || closeErr != nil {
		_ = os.Remove(metadata)
		return nil, ErrDatabaseOwnerUnavailable
	}
	return &DatabaseLock{
		listeners: listeners, metadata: metadata, metadataInfo: metadataInfo,
		databaseID: identity.databaseID, ownerDigest: ownerDigest,
	}, nil
}

func closeDatabaseOwnerListeners(listeners []net.Listener) error {
	var result error
	for index := len(listeners) - 1; index >= 0; index-- {
		if err := listeners[index].Close(); err != nil {
			result = ErrDatabaseOwnerRelease
		}
	}
	return result
}
