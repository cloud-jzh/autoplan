// Package filesystem provides fail-closed path authorization primitives.
package filesystem

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	domainfiles "github.com/lyming99/autoplan/backend/internal/domain/files"
)

type ResolvedPath struct {
	Lexical  string
	Real     string
	Existing bool
}

func ResolveExistingRoot(value string) (ResolvedPath, error) {
	lexical, err := validateAbsolutePath(value)
	if err != nil {
		return ResolvedPath{}, err
	}
	before, err := os.Lstat(lexical)
	resolvedInfo, statErr := os.Stat(lexical)
	if err != nil || statErr != nil || !resolvedInfo.IsDir() {
		return ResolvedPath{}, domainfiles.NewError(domainfiles.CodeResolutionFailed, domainfiles.ErrResolutionFailed)
	}
	real, err := filepath.EvalSymlinks(lexical)
	if err != nil {
		return ResolvedPath{}, domainfiles.NewError(domainfiles.CodeResolutionFailed, domainfiles.ErrResolutionFailed)
	}
	real, err = filepath.Abs(real)
	if err != nil {
		return ResolvedPath{}, domainfiles.NewError(domainfiles.CodeResolutionFailed, domainfiles.ErrResolutionFailed)
	}
	after, err := os.Lstat(lexical)
	if err != nil || !os.SameFile(before, after) {
		return ResolvedPath{}, domainfiles.NewError(domainfiles.CodeRaceDetected, domainfiles.ErrRaceDetected)
	}
	return ResolvedPath{Lexical: lexical, Real: filepath.Clean(real), Existing: true}, nil
}

// ResolveTarget resolves an existing target or, for controlled writes, the
// nearest existing parent. Resolution failure never falls back to lexical
// authorization. Re-statting the parent detects directory replacement during
// the authorization window.
func ResolveTarget(value string, allowMissing bool) (ResolvedPath, error) {
	lexical, err := validateAbsolutePath(value)
	if err != nil {
		return ResolvedPath{}, err
	}
	info, statErr := os.Lstat(lexical)
	if statErr == nil {
		real, err := filepath.EvalSymlinks(lexical)
		if err != nil {
			return ResolvedPath{}, domainfiles.NewError(domainfiles.CodeResolutionFailed, domainfiles.ErrResolutionFailed)
		}
		after, err := os.Lstat(lexical)
		if err != nil || !os.SameFile(info, after) {
			return ResolvedPath{}, domainfiles.NewError(domainfiles.CodeRaceDetected, domainfiles.ErrRaceDetected)
		}
		return ResolvedPath{Lexical: lexical, Real: filepath.Clean(real), Existing: true}, nil
	}
	if !allowMissing || !errors.Is(statErr, fs.ErrNotExist) {
		return ResolvedPath{}, domainfiles.NewError(domainfiles.CodeResolutionFailed, domainfiles.ErrResolutionFailed)
	}
	ancestor, missing, ancestorInfo, err := nearestExistingAncestor(lexical)
	if err != nil {
		return ResolvedPath{}, err
	}
	realAncestor, err := filepath.EvalSymlinks(ancestor)
	if err != nil {
		return ResolvedPath{}, domainfiles.NewError(domainfiles.CodeResolutionFailed, domainfiles.ErrResolutionFailed)
	}
	after, err := os.Lstat(ancestor)
	if err != nil || !os.SameFile(ancestorInfo, after) {
		return ResolvedPath{}, domainfiles.NewError(domainfiles.CodeRaceDetected, domainfiles.ErrRaceDetected)
	}
	real := realAncestor
	for index := len(missing) - 1; index >= 0; index-- {
		real = filepath.Join(real, missing[index])
	}
	return ResolvedPath{Lexical: lexical, Real: filepath.Clean(real), Existing: false}, nil
}

func nearestExistingAncestor(value string) (string, []string, fs.FileInfo, error) {
	current := value
	missing := make([]string, 0)
	for {
		info, err := os.Lstat(current)
		if err == nil {
			resolvedInfo, statErr := os.Stat(current)
			if statErr != nil || !resolvedInfo.IsDir() {
				return "", nil, nil, domainfiles.NewError(domainfiles.CodeResolutionFailed, domainfiles.ErrResolutionFailed)
			}
			return current, missing, info, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", nil, nil, domainfiles.NewError(domainfiles.CodeResolutionFailed, domainfiles.ErrResolutionFailed)
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", nil, nil, domainfiles.NewError(domainfiles.CodeResolutionFailed, domainfiles.ErrResolutionFailed)
		}
		missing = append(missing, filepath.Base(current))
		current = parent
	}
}

func validateAbsolutePath(value string) (string, error) {
	if value == "" || strings.TrimSpace(value) != value || strings.ContainsRune(value, 0) || hasParentComponent(value) {
		return "", domainfiles.NewError(domainfiles.CodeInvalidPath, domainfiles.ErrInvalidPath)
	}
	if runtime.GOOS == "windows" {
		if _, err := NormalizeWindowsPath(value); err != nil {
			return "", domainfiles.NewError(domainfiles.CodeInvalidPath, domainfiles.ErrInvalidPath)
		}
	}
	if !filepath.IsAbs(value) {
		return "", domainfiles.NewError(domainfiles.CodeInvalidPath, domainfiles.ErrInvalidPath)
	}
	absolute, err := filepath.Abs(value)
	if err != nil {
		return "", domainfiles.NewError(domainfiles.CodeInvalidPath, domainfiles.ErrInvalidPath)
	}
	return filepath.Clean(absolute), nil
}

func hasParentComponent(value string) bool {
	for _, component := range strings.FieldsFunc(value, func(character rune) bool { return character == '/' || character == '\\' }) {
		if component == ".." {
			return true
		}
	}
	return false
}

func SameOrWithin(root, target string) bool {
	if runtime.GOOS == "windows" {
		return WindowsPathWithin(root, target)
	}
	relative, err := filepath.Rel(root, target)
	return err == nil && (relative == "." || relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative))
}
