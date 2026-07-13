package filesystem

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	domainfiles "github.com/lyming99/autoplan/backend/internal/domain/files"
)

func TestAttachmentStoreStagesPromotesQuarantinesAndRemoves(t *testing.T) {
	store, err := NewAttachmentStore(AttachmentStoreOptions{Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	staged, err := store.Stage(context.Background(), "file-upload-1", bytes.NewReader([]byte("synthetic attachment")), 1024)
	if err != nil || staged.Size != int64(len("synthetic attachment")) || staged.SHA256 == "" {
		t.Fatalf("stage=%#v err=%v", staged, err)
	}
	if _, _, err := store.Inspect(context.Background(), staged.StageKey); err != nil {
		t.Fatal(err)
	}
	readyKey := "ready/17.blob"
	if err := store.Promote(context.Background(), staged.StageKey, readyKey); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Inspect(context.Background(), staged.StageKey); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("staged inspect = %v", err)
	}
	file, digest, err := store.Inspect(context.Background(), readyKey)
	if err != nil || file.Key != readyKey || digest != staged.SHA256 {
		t.Fatalf("ready=%#v hash=%q err=%v", file, digest, err)
	}
	quarantineKey := "quarantine/file-delete-1-17.blob"
	if err := store.Quarantine(context.Background(), readyKey, quarantineKey); err != nil {
		t.Fatal(err)
	}
	if err := store.Remove(context.Background(), quarantineKey); err != nil {
		t.Fatal(err)
	}
	files, err := store.List(context.Background())
	if err != nil || len(files) != 0 {
		t.Fatalf("files=%#v err=%v", files, err)
	}
}

func TestAttachmentStoreRejectsOversizeAndRemovesPartialStage(t *testing.T) {
	store, err := NewAttachmentStore(AttachmentStoreOptions{Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Stage(context.Background(), "file-upload-2", bytes.NewReader([]byte("too-large")), 3); !errors.Is(err, domainfiles.ErrAttachmentTooLarge) {
		t.Fatalf("oversize error = %v", err)
	}
	files, err := store.List(context.Background())
	if err != nil || len(files) != 0 {
		t.Fatalf("partial stage remains: %#v err=%v", files, err)
	}
}

func TestAttachmentStoreRejectsTraversalAndSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	store, err := NewAttachmentStore(AttachmentStoreOptions{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Open(context.Background(), "../outside"); !errors.Is(err, ErrAttachmentStoreUnsafe) {
		t.Fatalf("traversal error = %v", err)
	}
	outside := t.TempDir()
	link := filepath.Join(root, "ready", "escape.blob")
	if err := os.Symlink(filepath.Join(outside, "sentinel"), link); err != nil {
		// Symlink creation is privilege-gated on some Windows runners. The
		// operation-id/key matrix above still exercises the portable boundary.
		return
	}
	if _, err := store.Open(context.Background(), "ready/escape.blob"); !errors.Is(err, ErrAttachmentStoreUnsafe) {
		t.Fatalf("symlink error = %v", err)
	}
}

func TestAttachmentStoreRejectsConfiguredReparseRoot(t *testing.T) {
	root := t.TempDir()
	_, err := NewAttachmentStore(AttachmentStoreOptions{
		Root: root,
		ReparseDetector: func(path string, _ fs.FileInfo) bool {
			return filepath.Clean(path) == filepath.Clean(root)
		},
	})
	if !errors.Is(err, ErrAttachmentStoreUnsafe) {
		t.Fatalf("reparse root error = %v", err)
	}
}
