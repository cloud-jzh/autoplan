// Package encryptedfile provides the policy-controlled fallback store. It
// keeps encrypted envelopes and installation key material outside the business
// database and authenticates every record against its owner binding.
package encryptedfile

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	domainsecrets "github.com/lyming99/autoplan/backend/internal/domain/secrets"
	platformsecrets "github.com/lyming99/autoplan/backend/internal/platform/secrets"
)

const (
	providerName       = "encrypted-file-v1"
	keyFileName        = "autoplan-secrets.key"
	installationIDName = "autoplan-secrets.installation"
	recordDirectory    = "records"
	envelopeMagic      = "APS1"
	envelopeVersion    = byte(1)
	nonceSize          = 12
	maximumEnvelope    = (1 << 20) + 128
)

var (
	ErrInvalidOptions = errors.New("encrypted secret store options are invalid")
	ErrKeyMissing     = errors.New("encrypted secret store key is missing")
)

type Options struct {
	// Root stores encrypted envelopes. KeyRoot is an installation-private root
	// for the master key and installation binding; it must not be the DB path
	// or the envelope root.
	Root    string
	KeyRoot string
}

type Provider struct {
	root           string
	recordsRoot    string
	key            []byte
	installationID []byte
	mu             sync.RWMutex
}

func New(options Options) (*Provider, error) {
	root, err := secureDirectory(options.Root)
	if err != nil {
		return nil, err
	}
	keyRoot, err := secureDirectory(options.KeyRoot)
	if err != nil {
		return nil, err
	}
	if samePath(root, keyRoot) {
		return nil, ErrInvalidOptions
	}
	recordsRoot, err := secureDirectory(filepath.Join(root, recordDirectory))
	if err != nil {
		return nil, err
	}
	hasRecords, err := hasFiles(recordsRoot)
	if err != nil {
		return nil, err
	}
	key, err := loadOrCreate(keyRoot, keyFileName, hasRecords)
	if err != nil {
		return nil, err
	}
	installationID, err := loadOrCreate(keyRoot, installationIDName, hasRecords)
	if err != nil {
		clear(key)
		return nil, err
	}
	if len(key) != 32 || len(installationID) != 32 {
		clear(key)
		clear(installationID)
		return nil, platformsecrets.ErrCorrupt
	}
	return &Provider{root: root, recordsRoot: recordsRoot, key: key, installationID: installationID}, nil
}

func (provider *Provider) Name() string { return providerName }

func (provider *Provider) Capabilities(ctx context.Context) (platformsecrets.Capability, error) {
	if provider == nil || len(provider.key) != 32 || len(provider.installationID) != 32 {
		return platformsecrets.Capability{}, platformsecrets.ErrUnavailable
	}
	if err := ctx.Err(); err != nil {
		return platformsecrets.Capability{}, err
	}
	return platformsecrets.Capability{Available: true, ProtectedAtRest: true, Fallback: true}, nil
}

func (provider *Provider) Put(ctx context.Context, binding domainsecrets.Binding, value []byte) (string, error) {
	if provider == nil || platformsecrets.ValidateBinding(binding) != nil || domainsecrets.ValidateSecret(value) != nil {
		return "", platformsecrets.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	provider.mu.RLock()
	defer provider.mu.RUnlock()
	for attempt := 0; attempt < 4; attempt++ {
		reference, err := platformsecrets.NewOpaqueReference()
		if err != nil {
			return "", err
		}
		envelope, err := provider.seal(binding, reference, value)
		if err != nil {
			return "", err
		}
		writeErr := provider.writeEnvelope(ctx, reference, envelope)
		clear(envelope)
		if errors.Is(writeErr, fs.ErrExist) {
			continue
		}
		if writeErr != nil {
			return "", writeErr
		}
		return reference, nil
	}
	return "", platformsecrets.ErrUnavailable
}

func (provider *Provider) Get(ctx context.Context, binding domainsecrets.Binding, reference string) ([]byte, error) {
	if provider == nil || platformsecrets.ValidateBinding(binding) != nil || platformsecrets.ValidateReference(reference) != nil {
		return nil, platformsecrets.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	provider.mu.RLock()
	defer provider.mu.RUnlock()
	path, err := provider.recordPath(reference)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, platformsecrets.ErrNotFound
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maximumEnvelope {
		return nil, platformsecrets.ErrCorrupt
	}
	envelope, err := os.ReadFile(path)
	if err != nil {
		return nil, platformsecrets.ErrUnavailable
	}
	defer clear(envelope)
	return provider.open(binding, reference, envelope)
}

func (provider *Provider) Delete(ctx context.Context, binding domainsecrets.Binding, reference string) error {
	if provider == nil || platformsecrets.ValidateBinding(binding) != nil || platformsecrets.ValidateReference(reference) != nil {
		return platformsecrets.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	path, err := provider.recordPath(reference)
	if err != nil {
		return err
	}
	if err := os.Remove(path); errors.Is(err, fs.ErrNotExist) {
		return platformsecrets.ErrNotFound
	} else if err != nil {
		return platformsecrets.ErrUnavailable
	}
	return syncDirectory(provider.recordsRoot)
}

func (provider *Provider) Exists(ctx context.Context, binding domainsecrets.Binding, reference string) (bool, error) {
	if provider == nil || platformsecrets.ValidateBinding(binding) != nil || platformsecrets.ValidateReference(reference) != nil {
		return false, platformsecrets.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	provider.mu.RLock()
	defer provider.mu.RUnlock()
	path, err := provider.recordPath(reference)
	if err != nil {
		return false, err
	}
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil || !info.Mode().IsRegular() {
		return false, platformsecrets.ErrCorrupt
	}
	return true, nil
}

func (provider *Provider) seal(binding domainsecrets.Binding, reference string, value []byte) ([]byte, error) {
	block, err := aes.NewCipher(provider.key)
	if err != nil {
		return nil, platformsecrets.ErrUnavailable
	}
	aead, err := cipher.NewGCM(block)
	if err != nil || aead.NonceSize() != nonceSize {
		return nil, platformsecrets.ErrUnavailable
	}
	nonce := make([]byte, nonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return nil, platformsecrets.ErrUnavailable
	}
	ciphertext := aead.Seal(nil, nonce, value, provider.associatedData(binding, reference))
	envelope := make([]byte, 0, len(envelopeMagic)+1+len(nonce)+len(ciphertext))
	envelope = append(envelope, envelopeMagic...)
	envelope = append(envelope, envelopeVersion)
	envelope = append(envelope, nonce...)
	envelope = append(envelope, ciphertext...)
	clear(nonce)
	clear(ciphertext)
	return envelope, nil
}

func (provider *Provider) open(binding domainsecrets.Binding, reference string, envelope []byte) ([]byte, error) {
	minimum := len(envelopeMagic) + 1 + nonceSize + 16
	if len(envelope) < minimum || !bytes.Equal(envelope[:len(envelopeMagic)], []byte(envelopeMagic)) ||
		envelope[len(envelopeMagic)] != envelopeVersion {
		return nil, platformsecrets.ErrCorrupt
	}
	block, err := aes.NewCipher(provider.key)
	if err != nil {
		return nil, platformsecrets.ErrUnavailable
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, platformsecrets.ErrUnavailable
	}
	nonceStart := len(envelopeMagic) + 1
	nonce := envelope[nonceStart : nonceStart+nonceSize]
	plaintext, err := aead.Open(nil, nonce, envelope[nonceStart+nonceSize:], provider.associatedData(binding, reference))
	if err != nil || domainsecrets.ValidateSecret(plaintext) != nil {
		clear(plaintext)
		return nil, platformsecrets.ErrCorrupt
	}
	return plaintext, nil
}

func (provider *Provider) associatedData(binding domainsecrets.Binding, reference string) []byte {
	return []byte(strings.Join([]string{
		"autoplan-secret-envelope", string(binding.Kind), binding.Owner.Type, binding.Owner.ID,
		strconv.FormatInt(binding.Version, 10), reference, hex.EncodeToString(provider.installationID),
	}, "\x00"))
}

func (provider *Provider) writeEnvelope(ctx context.Context, reference string, envelope []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	path, err := provider.recordPath(reference)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(path); err == nil {
		return fs.ErrExist
	} else if !errors.Is(err, fs.ErrNotExist) {
		return platformsecrets.ErrUnavailable
	}
	temporary := path + ".tmp"
	file, err := os.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, fs.ErrExist) {
		return fs.ErrExist
	}
	if err != nil {
		return platformsecrets.ErrUnavailable
	}
	completed := false
	defer func() {
		if !completed {
			_ = file.Close()
			_ = os.Remove(temporary)
		}
	}()
	if _, err := file.Write(envelope); err != nil {
		return platformsecrets.ErrUnavailable
	}
	if err := file.Sync(); err != nil {
		return platformsecrets.ErrUnavailable
	}
	if err := file.Close(); err != nil {
		return platformsecrets.ErrUnavailable
	}
	if err := os.Rename(temporary, path); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return fs.ErrExist
		}
		return platformsecrets.ErrUnavailable
	}
	completed = true
	return syncDirectory(provider.recordsRoot)
}

func (provider *Provider) recordPath(reference string) (string, error) {
	if platformsecrets.ValidateReference(reference) != nil {
		return "", platformsecrets.ErrInvalid
	}
	digest := sha256.Sum256([]byte(reference))
	return filepath.Join(provider.recordsRoot, hex.EncodeToString(digest[:])+".bin"), nil
}

func secureDirectory(value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		return "", ErrInvalidOptions
	}
	abs, err := filepath.Abs(value)
	if err != nil {
		return "", ErrInvalidOptions
	}
	if err := os.MkdirAll(abs, 0o700); err != nil {
		return "", platformsecrets.ErrUnavailable
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil || !samePath(abs, resolved) {
		return "", ErrInvalidOptions
	}
	info, err := os.Lstat(abs)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", ErrInvalidOptions
	}
	if err := os.Chmod(abs, 0o700); err != nil && !errors.Is(err, fs.ErrPermission) {
		return "", platformsecrets.ErrUnavailable
	}
	return abs, nil
}

func loadOrCreate(root, name string, requireExisting bool) ([]byte, error) {
	path := filepath.Join(root, name)
	value, err := os.ReadFile(path)
	if err == nil {
		if len(value) != 32 {
			clear(value)
			return nil, platformsecrets.ErrCorrupt
		}
		return value, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return nil, platformsecrets.ErrUnavailable
	}
	if requireExisting {
		return nil, ErrKeyMissing
	}
	value = make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return nil, platformsecrets.ErrUnavailable
	}
	temporary := path + ".tmp"
	file, err := os.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		clear(value)
		return nil, platformsecrets.ErrUnavailable
	}
	completed := false
	defer func() {
		if !completed {
			_ = file.Close()
			_ = os.Remove(temporary)
		}
	}()
	if _, err := file.Write(value); err != nil || file.Sync() != nil || file.Close() != nil || os.Rename(temporary, path) != nil {
		clear(value)
		return nil, platformsecrets.ErrUnavailable
	}
	completed = true
	if err := os.Chmod(path, 0o600); err != nil && !errors.Is(err, fs.ErrPermission) {
		clear(value)
		return nil, platformsecrets.ErrUnavailable
	}
	return value, syncDirectory(root)
}

func hasFiles(root string) (bool, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return false, platformsecrets.ErrUnavailable
	}
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 {
			return false, platformsecrets.ErrCorrupt
		}
		if !entry.IsDir() {
			return true, nil
		}
	}
	return false, nil
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return platformsecrets.ErrUnavailable
	}
	defer directory.Close()
	// Windows does not consistently support directory Sync. File-level Sync is
	// mandatory above; directory Sync is best effort on platforms that allow it.
	_ = directory.Sync()
	return nil
}

func samePath(left, right string) bool {
	return strings.EqualFold(filepath.Clean(left), filepath.Clean(right))
}

var _ platformsecrets.Provider = (*Provider)(nil)
