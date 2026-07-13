package filesystem

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	domainfiles "github.com/lyming99/autoplan/backend/internal/domain/files"
)

var (
	ErrAttachmentStoreInvalid = errors.New("attachment store is invalid")
	ErrAttachmentStoreExists  = errors.New("attachment store target already exists")
	ErrAttachmentStoreUnsafe  = errors.New("attachment store path is unsafe")
)

type AttachmentStoreOptions struct {
	Root            string
	ReparseDetector ReparseDetector
}

type StagedAttachment = domainfiles.StagedAttachment
type AttachmentFile = domainfiles.StoredAttachmentFile

// AttachmentStore only accepts generated storage keys. It has no API taking a
// client path, display name, URL, or arbitrary destination.
type AttachmentStore struct {
	root     string
	detector ReparseDetector
}

func NewAttachmentStore(options AttachmentStoreOptions) (*AttachmentStore, error) {
	if strings.TrimSpace(options.Root) == "" {
		return nil, ErrAttachmentStoreInvalid
	}
	root, err := ResolveExistingRoot(options.Root)
	if err != nil {
		return nil, err
	}
	store := &AttachmentStore{root: root.Real, detector: options.ReparseDetector}
	for _, directory := range []string{"staged", "ready", "quarantine"} {
		if err := store.ensureDirectory(filepath.Join(store.root, directory)); err != nil {
			return nil, err
		}
	}
	return store, nil
}

func (store *AttachmentStore) Stage(
	ctx context.Context,
	operationID string,
	reader io.Reader,
	maximum int64,
) (StagedAttachment, error) {
	if err := ctx.Err(); err != nil {
		return StagedAttachment{}, err
	}
	if store == nil || reader == nil || maximum <= 0 || !validOperationID(operationID) {
		return StagedAttachment{}, ErrAttachmentStoreInvalid
	}
	stageKey := filepath.ToSlash(filepath.Join("staged", operationID+".part"))
	readyKey := filepath.ToSlash(filepath.Join("ready", operationID+".blob"))
	stagePath, err := store.pathFor(stageKey, true)
	if err != nil {
		return StagedAttachment{}, err
	}
	file, err := os.OpenFile(stagePath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, fs.ErrExist) {
		return StagedAttachment{}, ErrAttachmentStoreExists
	}
	if err != nil {
		return StagedAttachment{}, ErrAttachmentStoreUnsafe
	}
	completed := false
	defer func() {
		if !completed {
			_ = file.Close()
			_ = os.Remove(stagePath)
		}
	}()
	hash := sha256.New()
	buffer := make([]byte, 32<<10)
	sample := make([]byte, 0, 512)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return StagedAttachment{}, err
		}
		read, readErr := reader.Read(buffer)
		if read > 0 {
			if int64(read) > maximum-total {
				return StagedAttachment{}, domainfiles.ErrAttachmentTooLarge
			}
			if len(sample) < cap(sample) {
				remaining := cap(sample) - len(sample)
				if remaining > read {
					remaining = read
				}
				sample = append(sample, buffer[:remaining]...)
			}
			written, writeErr := file.Write(buffer[:read])
			if writeErr != nil || written != read {
				return StagedAttachment{}, ErrAttachmentStoreUnsafe
			}
			if _, hashErr := hash.Write(buffer[:read]); hashErr != nil {
				return StagedAttachment{}, ErrAttachmentStoreUnsafe
			}
			total += int64(read)
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return StagedAttachment{}, readErr
		}
		if read == 0 {
			return StagedAttachment{}, ErrAttachmentStoreUnsafe
		}
	}
	if total == 0 {
		return StagedAttachment{}, domainfiles.ErrAttachmentContent
	}
	if err := file.Sync(); err != nil {
		return StagedAttachment{}, ErrAttachmentStoreUnsafe
	}
	if err := file.Close(); err != nil {
		return StagedAttachment{}, ErrAttachmentStoreUnsafe
	}
	if err := store.syncDirectory(filepath.Dir(stagePath)); err != nil {
		return StagedAttachment{}, err
	}
	completed = true
	return StagedAttachment{
		StageKey: stageKey, ReadyKey: readyKey, Size: total,
		SHA256: hex.EncodeToString(hash.Sum(nil)), Sample: append([]byte(nil), sample...),
	}, nil
}

func (store *AttachmentStore) Promote(ctx context.Context, stageKey, readyKey string) error {
	return store.move(ctx, stageKey, readyKey, "staged", "ready")
}

func (store *AttachmentStore) Quarantine(ctx context.Context, readyKey, quarantineKey string) error {
	return store.move(ctx, readyKey, quarantineKey, "ready", "quarantine")
}

func (store *AttachmentStore) Remove(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	path, err := store.pathFor(key, true)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return ErrAttachmentStoreUnsafe
	}
	return store.syncDirectory(filepath.Dir(path))
}

func (store *AttachmentStore) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path, err := store.pathFor(key, true)
	if err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fs.ErrNotExist
		}
		return nil, ErrAttachmentStoreUnsafe
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, ErrAttachmentStoreUnsafe
	}
	return file, nil
}

func (store *AttachmentStore) Inspect(ctx context.Context, key string) (AttachmentFile, string, error) {
	if err := ctx.Err(); err != nil {
		return AttachmentFile{}, "", err
	}
	path, err := store.pathFor(key, true)
	if err != nil {
		return AttachmentFile{}, "", err
	}
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return AttachmentFile{}, "", fs.ErrNotExist
		}
		return AttachmentFile{}, "", ErrAttachmentStoreUnsafe
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return AttachmentFile{}, "", ErrAttachmentStoreUnsafe
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return AttachmentFile{}, "", ErrAttachmentStoreUnsafe
	}
	return AttachmentFile{Key: key, Size: info.Size(), ModifiedAt: info.ModTime().UTC()}, hex.EncodeToString(hash.Sum(nil)), nil
}

func (store *AttachmentStore) Sample(ctx context.Context, key string, maximum int) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if maximum <= 0 || maximum > 4096 {
		return nil, ErrAttachmentStoreInvalid
	}
	file, err := store.Open(ctx, key)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	buffer := make([]byte, maximum)
	count, readErr := file.Read(buffer)
	if readErr != nil && readErr != io.EOF {
		return nil, ErrAttachmentStoreUnsafe
	}
	return append([]byte(nil), buffer[:count]...), nil
}

func (store *AttachmentStore) List(ctx context.Context) ([]AttachmentFile, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if store == nil {
		return nil, ErrAttachmentStoreInvalid
	}
	result := make([]AttachmentFile, 0)
	for _, directory := range []string{"staged", "ready", "quarantine"} {
		path, err := store.pathFor(filepath.ToSlash(filepath.Join(directory, "placeholder")), true)
		if err != nil {
			return nil, err
		}
		root := filepath.Dir(path)
		entries, err := os.ReadDir(root)
		if err != nil {
			return nil, ErrAttachmentStoreUnsafe
		}
		for _, entry := range entries {
			key := filepath.ToSlash(filepath.Join(directory, entry.Name()))
			if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || !validStoredName(entry.Name()) {
				result = append(result, AttachmentFile{Key: key, Size: -1})
				continue
			}
			info, infoErr := entry.Info()
			if infoErr != nil || !info.Mode().IsRegular() {
				result = append(result, AttachmentFile{Key: key, Size: -1})
				continue
			}
			result = append(result, AttachmentFile{
				Key: key, Size: info.Size(), ModifiedAt: info.ModTime().UTC(),
			})
		}
	}
	sort.SliceStable(result, func(left, right int) bool { return result[left].Key < result[right].Key })
	return result, nil
}

func (store *AttachmentStore) move(ctx context.Context, sourceKey, targetKey, sourceDirectory, targetDirectory string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if filepath.ToSlash(filepath.Dir(sourceKey)) != sourceDirectory || filepath.ToSlash(filepath.Dir(targetKey)) != targetDirectory {
		return ErrAttachmentStoreUnsafe
	}
	source, err := store.pathFor(sourceKey, true)
	if err != nil {
		return err
	}
	target, err := store.pathFor(targetKey, true)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(target); err == nil {
		if _, sourceErr := os.Lstat(source); errors.Is(sourceErr, fs.ErrNotExist) {
			return nil
		}
		return ErrAttachmentStoreExists
	} else if !errors.Is(err, fs.ErrNotExist) {
		return ErrAttachmentStoreUnsafe
	}
	if err := os.Rename(source, target); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			if _, targetErr := os.Lstat(target); targetErr == nil {
				return nil
			}
			return fs.ErrNotExist
		}
		return ErrAttachmentStoreUnsafe
	}
	if err := store.syncDirectory(filepath.Dir(source)); err != nil {
		return err
	}
	return store.syncDirectory(filepath.Dir(target))
}

func (store *AttachmentStore) ensureDirectory(path string) error {
	if err := store.revalidateRoot(); err != nil {
		return err
	}
	if !SameOrWithin(store.root, path) || filepath.Dir(filepath.Clean(path)) != filepath.Clean(store.root) {
		return ErrAttachmentStoreUnsafe
	}
	if err := os.Mkdir(path, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return ErrAttachmentStoreUnsafe
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || store.isReparse(path, info) {
		return ErrAttachmentStoreUnsafe
	}
	return store.syncDirectory(store.root)
}

func (store *AttachmentStore) pathFor(key string, allowMissing bool) (string, error) {
	if store == nil || !domainfiles.StorageKeyValid(key) {
		return "", ErrAttachmentStoreUnsafe
	}
	if err := store.revalidateRoot(); err != nil {
		return "", err
	}
	path := filepath.Join(store.root, filepath.FromSlash(key))
	resolved, err := ResolveTarget(path, allowMissing)
	if err != nil || !SameOrWithin(store.root, resolved.Real) {
		return "", ErrAttachmentStoreUnsafe
	}
	if err := store.rejectLinks(path, allowMissing); err != nil {
		return "", err
	}
	return path, nil
}

func (store *AttachmentStore) revalidateRoot() error {
	if store == nil {
		return ErrAttachmentStoreInvalid
	}
	root, err := ResolveExistingRoot(store.root)
	if err != nil || root.Real != store.root {
		return ErrAttachmentStoreUnsafe
	}
	info, err := os.Lstat(store.root)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || store.isReparse(store.root, info) {
		return ErrAttachmentStoreUnsafe
	}
	return nil
}

func (store *AttachmentStore) rejectLinks(path string, allowMissing bool) error {
	relative, err := filepath.Rel(store.root, path)
	if err != nil || relative == "." || strings.HasPrefix(relative, "..") {
		return ErrAttachmentStoreUnsafe
	}
	current := store.root
	for _, component := range strings.Split(relative, string(filepath.Separator)) {
		current = filepath.Join(current, component)
		info, statErr := os.Lstat(current)
		if errors.Is(statErr, fs.ErrNotExist) && allowMissing {
			return nil
		}
		if statErr != nil || info.Mode()&os.ModeSymlink != 0 || store.isReparse(current, info) {
			return ErrAttachmentStoreUnsafe
		}
	}
	return nil
}

func (store *AttachmentStore) isReparse(path string, info fs.FileInfo) bool {
	return store.detector != nil && store.detector(path, info)
}

func (store *AttachmentStore) syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return ErrAttachmentStoreUnsafe
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		// Windows does not expose a portable directory fsync through os.File.
		// File data has already been synced before rename; retaining a durable
		// operation record makes the remaining directory entry recoverable.
		if runtime.GOOS == "windows" {
			return nil
		}
		return ErrAttachmentStoreUnsafe
	}
	return nil
}

func validOperationID(value string) bool {
	if value == "" || len(value) > 160 {
		return false
	}
	for _, character := range value {
		if !(character >= 'a' && character <= 'z') && !(character >= 'A' && character <= 'Z') &&
			!(character >= '0' && character <= '9') && character != '-' && character != '_' {
			return false
		}
	}
	return true
}

func validStoredName(value string) bool {
	return value != "" && len(value) <= 240 && !strings.Contains(value, string(filepath.Separator)) &&
		!strings.ContainsAny(value, "\\/") && !strings.ContainsRune(value, 0)
}
