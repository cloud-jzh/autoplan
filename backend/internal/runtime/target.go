package runtime

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// TargetKind records why a migration target is safe to inspect. It is never
// inferred from Electron locations or an existing autoplan.sqlite.
type TargetKind string

const (
	TargetFixture      TargetKind = "fixture"
	TargetTemporary    TargetKind = "temporary"
	TargetDatabaseCopy TargetKind = "database-copy"
)

// Target is an explicit migration target declaration. P001 validates it
// lexically but deliberately does not stat, open, create, or write the path.
type Target struct {
	Path string
	Kind TargetKind
}

var ErrUnsafeTarget = errors.New("unsafe migration target")

// ValidateTarget enforces the P001 safe-target boundary without filesystem IO.
func ValidateTarget(target Target, repositoryRoot string) error {
	if target.Path == "" || !filepath.IsAbs(target.Path) {
		return ErrUnsafeTarget
	}
	clean := filepath.Clean(target.Path)
	switch target.Kind {
	case TargetFixture:
		return requireWithin(clean, filepath.Join(repositoryRoot, "fixtures"))
	case TargetTemporary:
		return requireWithin(clean, os.TempDir())
	case TargetDatabaseCopy:
		base := strings.ToLower(filepath.Base(clean))
		if base == "autoplan.sqlite" {
			return ErrUnsafeTarget
		}
		if !(strings.HasSuffix(base, ".copy") || strings.HasSuffix(base, ".backup") || strings.HasSuffix(base, ".bak")) {
			return ErrUnsafeTarget
		}
		return nil
	default:
		return ErrUnsafeTarget
	}
}

func requireWithin(target, root string) error {
	relative, err := filepath.Rel(filepath.Clean(root), target)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return ErrUnsafeTarget
	}
	return nil
}
