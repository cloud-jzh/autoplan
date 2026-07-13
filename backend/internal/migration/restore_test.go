package migration

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func databaseBackupPath(t *testing.T, set BackupSet, backupRoot string) string {
	t.Helper()
	for _, artifact := range set.Artifacts {
		if artifact.Role == "database" {
			return filepath.Join(backupRoot, artifact.File)
		}
	}
	t.Fatal("backup set has no database artifact")
	return ""
}

func restoreOptionsForFixture(
	root string,
	backup string,
	target string,
	manifest string,
	runID string,
	expected []byte,
) RestoreOptions {
	return RestoreOptions{
		ManifestPath: manifest, BackupRoot: backup, Target: target,
		AllowedTargetRoot: root, RunID: runID,
		Mode: RestoreModeTruncatingReplace,
		Truncation: TruncationConfirmation{
			Confirmed: true, Point: "test-boundary", AffectedMutations: []string{"test-mutation"},
		},
		VerifyDatabase: func(_ context.Context, name string) error {
			actual, err := os.ReadFile(name)
			if err != nil || !bytes.Equal(actual, expected) {
				return errors.New("synthetic_restore_verification_failed")
			}
			return nil
		},
	}
}

func TestEverySuccessfulFixtureRestoresExactSchemaRowsAndAuditInputFromImmutableBackup(t *testing.T) {
	root, manifest := generateP04Fixtures(t)
	for index, artifact := range manifest.Artifacts {
		if artifact.ExpectedResult != "migrated" && artifact.ExpectedResult != "no-op" {
			continue
		}
		artifact := artifact
		t.Run(artifact.ID, func(t *testing.T) {
			target := filepath.Join(root, artifact.File)
			original, err := os.ReadFile(target)
			if err != nil {
				t.Fatal(err)
			}
			backup := filepath.Join(root, "backups-"+artifact.ID)
			if err := os.Mkdir(backup, 0o700); err != nil {
				t.Fatal(err)
			}
			version := 0
			if artifact.SourceUserVersion != nil {
				version = *artifact.SourceUserVersion
			}
			set, err := CreateBackup(context.Background(), BackupOptions{
				Preflight: p04PreflightReport(target, backup, original, version),
				Clock: fixedClock{value: time.Date(
					2026, 7, 11, 6, index, 0, 0, time.UTC,
				)},
				RunID: "restore-" + artifact.ID,
			})
			if err != nil {
				t.Fatalf("CreateBackup() error = %v", err)
			}
			backupPath := databaseBackupPath(t, set, backup)
			backupBefore, err := os.ReadFile(backupPath)
			if err != nil {
				t.Fatal(err)
			}

			changed := p04SyntheticSQLite(1-version, 'Z')
			if err := os.WriteFile(target, changed, 0o600); err != nil {
				t.Fatal(err)
			}
			result, err := RestoreBackup(context.Background(), restoreOptionsForFixture(
				root, backup, target, set.ManifestPath, "attempt-"+artifact.ID, original,
			))
			if err != nil {
				t.Fatalf("RestoreBackup() error = %v", err)
			}
			restored, readErr := os.ReadFile(target)
			if readErr != nil || !bytes.Equal(restored, original) ||
				result.SHA256 != artifact.SHA256 || result.Size != artifact.ByteSize ||
				result.UserVersion != version {
				t.Fatalf("restored fixture mismatch: result=%#v read=%v", result, readErr)
			}
			backupAfter, readErr := os.ReadFile(backupPath)
			if readErr != nil || !bytes.Equal(backupAfter, backupBefore) {
				t.Fatalf("immutable backup changed: %v", readErr)
			}
			for _, suffix := range []string{
				".restore.attempt-" + artifact.ID + ".tmp",
				".restore.attempt-" + artifact.ID + ".previous",
			} {
				if _, err := os.Lstat(target + suffix); !os.IsNotExist(err) {
					t.Fatalf("restore left owned intermediate %s", filepath.Base(target+suffix))
				}
			}
		})
	}
}

func TestRestoreVerifierFailurePreservesCurrentTargetAndImmutableBackup(t *testing.T) {
	root := t.TempDir()
	backup := filepath.Join(root, "backups")
	if err := os.Mkdir(backup, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "fixture.sqlite.copy")
	original := p04SyntheticSQLite(0, 'A')
	current := p04SyntheticSQLite(1, 'C')
	if err := os.WriteFile(target, original, 0o600); err != nil {
		t.Fatal(err)
	}
	set, err := CreateBackup(context.Background(), BackupOptions{
		Preflight: p04PreflightReport(target, backup, original, 0),
		Clock:     fixedClock{value: time.Date(2026, 7, 11, 6, 30, 0, 0, time.UTC)},
		RunID:     "restore-verifier-fault",
	})
	if err != nil {
		t.Fatal(err)
	}
	backupPath := databaseBackupPath(t, set, backup)
	backupBefore, _ := os.ReadFile(backupPath)
	if err := os.WriteFile(target, current, 0o600); err != nil {
		t.Fatal(err)
	}
	options := restoreOptionsForFixture(root, backup, target, set.ManifestPath, "verifier-fault", original)
	options.VerifyDatabase = func(context.Context, string) error {
		return errors.New("synthetic_post_restore_audit_failure")
	}
	if _, err := RestoreBackup(context.Background(), options); !errors.Is(err, ErrRestoreVerification) {
		t.Fatalf("RestoreBackup() error = %v", err)
	}
	targetAfter, targetErr := os.ReadFile(target)
	backupAfter, backupErr := os.ReadFile(backupPath)
	if targetErr != nil || backupErr != nil || !bytes.Equal(targetAfter, current) ||
		!bytes.Equal(backupAfter, backupBefore) {
		t.Fatalf("failed restore changed target or backup: target=%v backup=%v", targetErr, backupErr)
	}
}

func TestRestoreStageCollisionsCancellationAndChecksumDamageFailClosed(t *testing.T) {
	cases := []struct {
		name          string
		prepare       func(string, string, BackupSet)
		cancelContext bool
	}{
		{name: "restore-staging", prepare: func(target, _ string, _ BackupSet) {
			_ = os.WriteFile(target+".restore.stage-fault.tmp", []byte("owned-by-other-run"), 0o600)
		}},
		{name: "restore-atomic-replace", prepare: func(target, _ string, _ BackupSet) {
			_ = os.WriteFile(target+".restore.stage-fault.previous", []byte("owned-by-other-run"), 0o600)
		}},
		{name: "checksum-damage", prepare: func(_ string, backup string, set BackupSet) {
			file := databaseBackupPath(t, set, backup)
			handle, err := os.OpenFile(file, os.O_WRONLY|os.O_APPEND, 0)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = handle.Write([]byte("synthetic-checksum-damage"))
			_ = handle.Close()
		}},
		{name: "cancelled", prepare: func(string, string, BackupSet) {}, cancelContext: true},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			root := t.TempDir()
			backup := filepath.Join(root, "backups")
			if err := os.Mkdir(backup, 0o700); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(root, "fixture.sqlite.copy")
			original := p04SyntheticSQLite(0, 'A')
			current := p04SyntheticSQLite(1, 'C')
			if err := os.WriteFile(target, original, 0o600); err != nil {
				t.Fatal(err)
			}
			set, err := CreateBackup(context.Background(), BackupOptions{
				Preflight: p04PreflightReport(target, backup, original, 0),
				Clock:     fixedClock{value: time.Date(2026, 7, 11, 6, 31, 0, 0, time.UTC)},
				RunID:     "restore-stage-fault",
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(target, current, 0o600); err != nil {
				t.Fatal(err)
			}
			item.prepare(target, backup, set)
			backupPath := databaseBackupPath(t, set, backup)
			backupBefore, _ := os.ReadFile(backupPath)
			targetBefore, _ := os.ReadFile(target)
			ctx := context.Background()
			if item.cancelContext {
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelled
			}
			_, err = RestoreBackup(ctx, restoreOptionsForFixture(
				root, backup, target, set.ManifestPath, "stage-fault", original,
			))
			if err == nil {
				t.Fatal("RestoreBackup() unexpectedly succeeded")
			}
			targetAfter, _ := os.ReadFile(target)
			backupAfter, _ := os.ReadFile(backupPath)
			if !bytes.Equal(targetAfter, targetBefore) || !bytes.Equal(backupAfter, backupBefore) {
				t.Fatal("failed restore changed current target or backup baseline")
			}
		})
	}
}

func TestIndependentRestoreRehydratesDeclaredAttachmentAndPlanArtifacts(t *testing.T) {
	root := t.TempDir()
	backup := filepath.Join(root, "backups")
	if err := os.Mkdir(backup, 0o700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "fixture.sqlite.copy")
	database := p04SyntheticSQLite(0, 'R')
	if err := os.WriteFile(source, database, 0o600); err != nil {
		t.Fatal(err)
	}
	attachments := filepath.Join(root, "attachments")
	if err := os.Mkdir(attachments, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(attachments, "note.txt"), []byte("attachment-fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	plan := filepath.Join(root, "plan.md")
	if err := os.WriteFile(plan, []byte("# sanitized plan\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	attachmentInfo, err := os.Stat(attachments)
	if err != nil {
		t.Fatal(err)
	}
	attachmentEntries, attachmentSize, attachmentDigest, err := inspectResourceDirectory(context.Background(), attachments)
	if err != nil {
		t.Fatal(err)
	}
	planInfo, err := os.Stat(plan)
	if err != nil {
		t.Fatal(err)
	}
	planDigest, planSize, err := hashRegularFile(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	preflight := p04PreflightReport(source, backup, database, 0)
	preflight.Resources = []ResourceReport{
		{Role: "attachments", Path: attachments, Size: attachmentSize, SHA256: attachmentDigest, SourceInfo: attachmentInfo, Directory: true, Entries: attachmentEntries},
		{Role: "plan", Path: plan, Size: planSize, SHA256: planDigest, SourceInfo: planInfo, Entries: []ResourceEntry{{Path: plan, Relative: "plan.md", Size: planSize, SHA256: planDigest, SourceInfo: planInfo}}},
	}
	set, err := CreateBackup(context.Background(), BackupOptions{
		Preflight: preflight, Clock: fixedClock{value: time.Date(2026, 7, 11, 7, 0, 0, 0, time.UTC)}, RunID: "independent-restore",
	})
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "restored.sqlite.copy")
	attachmentTarget := filepath.Join(root, "restored-attachments")
	planTarget := filepath.Join(root, "restored-plan.md")
	result, err := RestoreBackup(context.Background(), RestoreOptions{
		ManifestPath: set.ManifestPath, BackupRoot: backup, Target: target, AllowedTargetRoot: root, RunID: "independent",
		Mode: RestoreModeIndependentCopy,
		ArtifactTargets: []RestoreArtifactTarget{
			{Role: "resource-attachments", Target: attachmentTarget},
			{Role: "resource-plan", Target: planTarget},
		},
		VerifyDatabase: func(_ context.Context, name string) error {
			actual, err := os.ReadFile(name)
			if err != nil || !bytes.Equal(actual, database) {
				return errors.New("independent_database_invalid")
			}
			return nil
		},
	})
	if err != nil || result.Mode != RestoreModeIndependentCopy || len(result.Artifacts) != 2 {
		t.Fatalf("RestoreBackup() result=%#v error=%v", result, err)
	}
	if actual, err := os.ReadFile(target); err != nil || !bytes.Equal(actual, database) {
		t.Fatalf("independent database = %v", err)
	}
	if actual, err := os.ReadFile(filepath.Join(attachmentTarget, "note.txt")); err != nil || string(actual) != "attachment-fixture" {
		t.Fatalf("attachment restore = %v", err)
	}
	if actual, err := os.ReadFile(planTarget); err != nil || string(actual) != "# sanitized plan\n" {
		t.Fatalf("plan restore = %v", err)
	}
	if _, err := os.Stat(source); err != nil {
		t.Fatalf("drill source was overwritten: %v", err)
	}
}

func TestRestoreRejectsImplicitReplaceAndIndependentTargetCollision(t *testing.T) {
	root := t.TempDir()
	backup := filepath.Join(root, "backups")
	if err := os.Mkdir(backup, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "fixture.sqlite.copy")
	content := p04SyntheticSQLite(0, 'S')
	if err := os.WriteFile(target, content, 0o600); err != nil {
		t.Fatal(err)
	}
	set, err := CreateBackup(context.Background(), BackupOptions{Preflight: p04PreflightReport(target, backup, content, 0), Clock: fixedClock{value: time.Date(2026, 7, 11, 7, 1, 0, 0, time.UTC)}, RunID: "reject-implicit"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := RestoreBackupForRollback(context.Background(), RestoreOptions{ManifestPath: set.ManifestPath, BackupRoot: backup, Target: target, AllowedTargetRoot: root, RunID: "implicit"}); !errors.Is(err, ErrRestoreUnsafe) {
		t.Fatalf("implicit restore error = %v", err)
	}
	if _, err := RestoreBackup(context.Background(), RestoreOptions{ManifestPath: set.ManifestPath, BackupRoot: backup, Target: target, AllowedTargetRoot: root, RunID: "collision", Mode: RestoreModeIndependentCopy}); !errors.Is(err, ErrRestoreUnsafe) {
		t.Fatalf("independent collision error = %v", err)
	}
}
