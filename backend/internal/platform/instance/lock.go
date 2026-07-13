// Package instance provides a filesystem-backed unique-instance gate scoped to
// an explicitly authorized temporary runtime directory or database copy.
package instance

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

type TargetKind string

const (
	TargetTemporary    TargetKind = "temporary"
	TargetDatabaseCopy TargetKind = "database-copy"
)

var (
	ErrUnsafeTarget   = errors.New("instance target is unsafe")
	ErrAlreadyRunning = errors.New("another instance owns the target")
	ErrAcquireFailed  = errors.New("instance lock acquisition failed")
	ErrReleaseFailed  = errors.New("instance lock release failed")
)

type Options struct {
	Target        string
	Kind          TargetKind
	TemporaryRoot string
}

type Lock struct {
	file             *os.File
	path             string
	createdDirectory string
	once             sync.Once
	result           error
}

func Acquire(options Options) (*Lock, error) {
	if options.Target == "" || !filepath.IsAbs(options.Target) {
		return nil, ErrUnsafeTarget
	}
	target := filepath.Clean(options.Target)
	lockPath := ""
	createdDirectory := ""
	switch options.Kind {
	case TargetTemporary:
		created, err := prepareTemporaryDirectory(target, options.TemporaryRoot)
		if err != nil {
			return nil, err
		}
		if created {
			createdDirectory = target
		}
		lockPath = filepath.Join(target, "autoplan-server.lock")
	case TargetDatabaseCopy:
		if err := validateDatabaseCopy(target); err != nil {
			return nil, err
		}
		lockPath = target + ".autoplan-server.lock"
	default:
		return nil, ErrUnsafeTarget
	}

	file, err := os.OpenFile(lockPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if createdDirectory != "" {
			_ = os.Remove(createdDirectory)
		}
		if os.IsExist(err) {
			return nil, ErrAlreadyRunning
		}
		return nil, ErrAcquireFailed
	}
	return &Lock{file: file, path: lockPath, createdDirectory: createdDirectory}, nil
}

func prepareTemporaryDirectory(target, temporaryRoot string) (bool, error) {
	if temporaryRoot == "" || !filepath.IsAbs(temporaryRoot) || !within(target, temporaryRoot) {
		return false, ErrUnsafeTarget
	}
	rootResolved, err := filepath.EvalSymlinks(filepath.Clean(temporaryRoot))
	if err != nil {
		return false, ErrUnsafeTarget
	}
	info, err := os.Lstat(target)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return false, ErrUnsafeTarget
		}
		resolved, err := filepath.EvalSymlinks(target)
		if err != nil || !withinOrEqual(resolved, rootResolved) {
			return false, ErrUnsafeTarget
		}
		return false, nil
	}
	if !os.IsNotExist(err) {
		return false, ErrAcquireFailed
	}
	parent := filepath.Dir(target)
	parentInfo, err := os.Lstat(parent)
	if err != nil || parentInfo.Mode()&os.ModeSymlink != 0 || !parentInfo.IsDir() {
		return false, ErrUnsafeTarget
	}
	parentResolved, err := filepath.EvalSymlinks(parent)
	if err != nil || !withinOrEqual(parentResolved, rootResolved) {
		return false, ErrUnsafeTarget
	}
	if err := os.Mkdir(target, 0o700); err != nil {
		if os.IsExist(err) {
			return false, ErrAlreadyRunning
		}
		return false, ErrAcquireFailed
	}
	return true, nil
}

func validateDatabaseCopy(target string) error {
	base := strings.ToLower(filepath.Base(target))
	if base == "autoplan.sqlite" || !(strings.HasSuffix(base, ".copy") ||
		strings.HasSuffix(base, ".backup") || strings.HasSuffix(base, ".bak")) {
		return ErrUnsafeTarget
	}
	info, err := os.Lstat(target)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return ErrUnsafeTarget
	}
	return nil
}

// Close removes only the same lock file this process created. Replacement of
// the lock path is treated as a release failure rather than deleting it.
func (lock *Lock) Close(context.Context) error {
	if lock == nil {
		return nil
	}
	lock.once.Do(func() {
		owned, statErr := lock.file.Stat()
		current, pathErr := os.Lstat(lock.path)
		closeErr := lock.file.Close()
		if statErr != nil || pathErr != nil || closeErr != nil || !os.SameFile(owned, current) {
			lock.result = ErrReleaseFailed
			return
		}
		if err := os.Remove(lock.path); err != nil {
			lock.result = ErrReleaseFailed
			return
		}
		if lock.createdDirectory != "" {
			if err := os.Remove(lock.createdDirectory); err != nil && !os.IsNotExist(err) {
				lock.result = ErrReleaseFailed
			}
		}
	})
	return lock.result
}

func within(target, root string) bool {
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(target))
	return err == nil && relative != "." && relative != ".." && !filepath.IsAbs(relative) &&
		!strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func withinOrEqual(target, root string) bool {
	if filepath.Clean(target) == filepath.Clean(root) {
		return true
	}
	return within(target, root)
}
