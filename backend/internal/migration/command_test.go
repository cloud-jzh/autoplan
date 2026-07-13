package migration_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	applicationmigration "github.com/lyming99/autoplan/backend/internal/application/migration"
	coremigration "github.com/lyming99/autoplan/backend/internal/migration"
)

type fixedClock struct{ value time.Time }

func (clock fixedClock) Now() time.Time { return clock.value }

func syntheticSQLite(version int, marker byte) []byte {
	content := make([]byte, 512)
	copy(content, []byte("SQLite format 3\x00"))
	binary.BigEndian.PutUint16(content[16:18], 512)
	content[18], content[19] = 1, 1
	binary.BigEndian.PutUint32(content[28:32], 1)
	binary.BigEndian.PutUint32(content[44:48], 4)
	binary.BigEndian.PutUint32(content[56:60], 1)
	binary.BigEndian.PutUint32(content[60:64], uint32(version))
	content[100] = marker
	return content
}

func writeSyntheticDatabase(t *testing.T, name string, version int, marker byte) []byte {
	t.Helper()
	content := syntheticSQLite(version, marker)
	if err := os.WriteFile(name, content, 0o600); err != nil {
		t.Fatal(err)
	}
	return content
}

func authorizedFixture(t *testing.T) (string, string, string) {
	t.Helper()
	root := t.TempDir()
	backup := filepath.Join(root, "backups")
	if err := os.Mkdir(backup, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "fixture.sqlite.copy")
	writeSyntheticDatabase(t, target, 0, 'A')
	return root, backup, target
}

func preflightOptions(root, backup, target string) coremigration.PreflightOptions {
	return coremigration.PreflightOptions{
		Target: target, AllowedRoot: root, BackupDirectory: backup, SanitizedCopy: true,
		EvidenceCheck:  func(string) error { return nil },
		AvailableBytes: func(string) (int64, error) { return 1 << 30, nil },
	}
}

func TestPreflightAuthorizesOnlyExplicitStableSQLiteCopies(t *testing.T) {
	root, backup, target := authorizedFixture(t)
	report, err := coremigration.Preflight(context.Background(), preflightOptions(root, backup, target))
	if err != nil {
		t.Fatalf("Preflight() error = %v", err)
	}
	expected := sha256.Sum256(syntheticSQLite(0, 'A'))
	if report.SHA256 != hex.EncodeToString(expected[:]) || report.UserVersion != 0 || report.Size != 512 ||
		report.StableDatabaseID == "" || report.RequiredBytes <= report.Size {
		t.Fatalf("unexpected report: %#v", report)
	}

	unsafe := preflightOptions(root, backup, filepath.Join(root, "autoplan.sqlite"))
	writeSyntheticDatabase(t, unsafe.Target, 0, 'B')
	if _, err := coremigration.Preflight(context.Background(), unsafe); !errors.Is(err, coremigration.ErrPreflightUnsafeTarget) {
		t.Fatalf("production-name Preflight() error = %v", err)
	}

	insufficient := preflightOptions(root, backup, target)
	insufficient.AvailableBytes = func(string) (int64, error) { return 1, nil }
	if _, err := coremigration.Preflight(context.Background(), insufficient); !errors.Is(err, coremigration.ErrPreflightInsufficientSpace) {
		t.Fatalf("low-space Preflight() error = %v", err)
	}

	if err := os.WriteFile(target+"-wal", []byte("active"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := coremigration.Preflight(context.Background(), preflightOptions(root, backup, target)); !errors.Is(err, coremigration.ErrPreflightSidecarActive) {
		t.Fatalf("active-sidecar Preflight() error = %v", err)
	}
}

func TestBackupIsExclusiveChecksummedAndManifestVerified(t *testing.T) {
	root, backup, target := authorizedFixture(t)
	if err := os.WriteFile(target+".bak", syntheticSQLite(0, 'B'), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target+".mirror", syntheticSQLite(0, 'C'), 0o600); err != nil {
		t.Fatal(err)
	}
	preflight, err := coremigration.Preflight(context.Background(), preflightOptions(root, backup, target))
	if err != nil {
		t.Fatal(err)
	}
	options := coremigration.BackupOptions{
		Preflight: preflight,
		Clock:     fixedClock{value: time.Date(2026, 7, 11, 4, 30, 0, 123, time.UTC)},
		RunID:     "fixture-run",
	}
	set, err := coremigration.CreateBackup(context.Background(), options)
	if err != nil {
		t.Fatalf("CreateBackup() error = %v", err)
	}
	if len(set.Artifacts) != 3 || set.ManifestID == "" || len(set.ManifestSHA256) != 64 {
		t.Fatalf("unexpected backup set: %#v", set)
	}
	manifest, digest, err := coremigration.LoadAndVerifyManifest(context.Background(), set.ManifestPath, backup)
	if err != nil || digest != set.ManifestSHA256 || manifest.SourceSHA256 != preflight.SHA256 || manifest.DatabaseContent {
		t.Fatalf("manifest verification = %#v, %s, %v", manifest, digest, err)
	}
	if _, err := coremigration.CreateBackup(context.Background(), options); !errors.Is(err, coremigration.ErrBackupExists) {
		t.Fatalf("duplicate CreateBackup() error = %v", err)
	}
}

func TestRestoreUsesVerifiedManifestAndPreservesBackupBytes(t *testing.T) {
	root, backup, target := authorizedFixture(t)
	original := syntheticSQLite(0, 'A')
	preflight, err := coremigration.Preflight(context.Background(), preflightOptions(root, backup, target))
	if err != nil {
		t.Fatal(err)
	}
	set, err := coremigration.CreateBackup(context.Background(), coremigration.BackupOptions{
		Preflight: preflight, Clock: fixedClock{value: time.Date(2026, 7, 11, 4, 31, 0, 0, time.UTC)}, RunID: "restore-run",
	})
	if err != nil {
		t.Fatal(err)
	}
	manifest, _, err := coremigration.LoadAndVerifyManifest(context.Background(), set.ManifestPath, backup)
	if err != nil {
		t.Fatal(err)
	}
	var backupFile string
	for _, artifact := range manifest.Artifacts {
		if artifact.Role == "database" {
			backupFile = filepath.Join(backup, artifact.File)
		}
	}
	backupBefore, err := os.ReadFile(backupFile)
	if err != nil {
		t.Fatal(err)
	}
	writeSyntheticDatabase(t, target, 1, 'Z')
	result, err := coremigration.RestoreBackup(context.Background(), coremigration.RestoreOptions{
		ManifestPath: set.ManifestPath, BackupRoot: backup, Target: target,
		AllowedTargetRoot: root, RunID: "restore-attempt",
		VerifyDatabase: func(_ context.Context, name string) error {
			content, err := os.ReadFile(name)
			if err != nil || !bytes.Equal(content[:16], []byte("SQLite format 3\x00")) {
				return errors.New("invalid")
			}
			return nil
		},
	})
	if err != nil || result.UserVersion != 0 {
		t.Fatalf("RestoreBackup() = %#v, %v", result, err)
	}
	restored, err := os.ReadFile(target)
	if err != nil || !bytes.Equal(restored, original) {
		t.Fatalf("restored bytes mismatch: %v", err)
	}
	backupAfter, err := os.ReadFile(backupFile)
	if err != nil || !bytes.Equal(backupBefore, backupAfter) {
		t.Fatalf("backup changed: %v", err)
	}
}

func TestManifestTamperingFailsWithoutChangingTarget(t *testing.T) {
	root, backup, target := authorizedFixture(t)
	preflight, err := coremigration.Preflight(context.Background(), preflightOptions(root, backup, target))
	if err != nil {
		t.Fatal(err)
	}
	set, err := coremigration.CreateBackup(context.Background(), coremigration.BackupOptions{
		Preflight: preflight, Clock: fixedClock{value: time.Date(2026, 7, 11, 4, 32, 0, 0, time.UTC)}, RunID: "tamper-run",
	})
	if err != nil {
		t.Fatal(err)
	}
	manifest, _, err := coremigration.LoadAndVerifyManifest(context.Background(), set.ManifestPath, backup)
	if err != nil {
		t.Fatal(err)
	}
	for _, artifact := range manifest.Artifacts {
		if artifact.Role == "database" {
			file, err := os.OpenFile(filepath.Join(backup, artifact.File), os.O_WRONLY|os.O_APPEND, 0)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = file.Write([]byte("tampered"))
			_ = file.Close()
		}
	}
	targetBefore, _ := os.ReadFile(target)
	if _, _, err := coremigration.LoadAndVerifyManifest(context.Background(), set.ManifestPath, backup); !errors.Is(err, coremigration.ErrBackupVerification) {
		t.Fatalf("tampered manifest verification error = %v", err)
	}
	targetAfter, _ := os.ReadFile(target)
	if !bytes.Equal(targetBefore, targetAfter) {
		t.Fatal("target changed after rejected tampered backup")
	}
}

func TestPreflightCommandReportIsStructuredAndPathFree(t *testing.T) {
	root, backup, target := authorizedFixture(t)
	report, err := applicationmigration.Execute(context.Background(), applicationmigration.Request{
		Command: applicationmigration.CommandPreflight, Database: target, AllowedRoot: root,
		BackupDirectory: backup, SanitizedCopy: true,
	}, applicationmigration.Dependencies{
		RepositoryRoot: root,
		EvidenceCheck:  func(string) error { return nil },
		AvailableBytes: func(string) (int64, error) { return 1 << 30, nil },
	})
	if err != nil || report.Status != "ok" || report.Code != "preflight_ok" || report.WritePerformed {
		t.Fatalf("Execute(preflight) = %#v, %v", report, err)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte(root)) || bytes.Contains(encoded, []byte(target)) ||
		bytes.Contains(encoded, []byte(backup)) {
		t.Fatalf("report leaked an absolute path: %s", encoded)
	}
}
