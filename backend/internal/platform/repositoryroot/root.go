// Package repositoryroot locates checked-in prerequisite evidence without
// consulting Electron userData or any operating-system application directory.
package repositoryroot

import (
	"errors"
	"os"
	"path/filepath"
)

var ErrNotFound = errors.New("repository root not found")

// Find walks only from the process working directory toward the filesystem
// root. It never searches user profiles or opens a database.
func Find() (string, error) {
	current, err := os.Getwd()
	if err != nil {
		return "", ErrNotFound
	}
	for {
		if regular(filepath.Join(current, "package.json")) &&
			regular(filepath.Join(current, "docs", "migration", "p00", "capability-matrix.json")) {
			return current, nil
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", ErrNotFound
		}
		current = parent
	}
}

func regular(name string) bool {
	info, err := os.Stat(name)
	return err == nil && info.Mode().IsRegular()
}
