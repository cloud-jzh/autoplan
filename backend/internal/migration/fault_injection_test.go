package migration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/lyming99/autoplan/backend/migrations"
)

type injectedDatabase struct {
	*fakeDatabase
	fault string
}

func (database *injectedDatabase) Begin(ctx context.Context) (Transaction, error) {
	transaction, err := database.fakeDatabase.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return &injectedTransaction{Transaction: transaction, fault: database.fault}, nil
}

type injectedTransaction struct {
	Transaction
	fault string
}

func (transaction *injectedTransaction) ExecContext(ctx context.Context, query string, arguments ...any) (sql.Result, error) {
	inject, after := injectedFailure(transaction.fault, query)
	if inject && !after {
		return nil, errors.New("synthetic_process_interruption")
	}
	result, err := transaction.Transaction.ExecContext(ctx, query, arguments...)
	if err != nil {
		return nil, err
	}
	if inject {
		return nil, errors.New("synthetic_process_interruption")
	}
	return result, nil
}

func p04SyntheticSQLite(version int, marker byte) []byte {
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

func p04PreflightReport(target, backup string, content []byte, version int) PreflightReport {
	digest := sha256.Sum256(content)
	encoded := hex.EncodeToString(digest[:])
	info, _ := os.Stat(target)
	return PreflightReport{
		Target: target, BackupDirectory: backup, Size: int64(len(content)), SHA256: encoded,
		UserVersion: version, StableDatabaseID: encoded[:16], SourceInfo: info,
	}
}

type shortWriter struct{}

func (shortWriter) Write(content []byte) (int, error) {
	if len(content) == 0 {
		return 0, nil
	}
	return len(content) - 1, nil
}

type errorWriter struct{ err error }

func (writer errorWriter) Write([]byte) (int, error) { return 0, writer.err }

func TestFaultStageInventoryPinsEveryMigrationAndRestoreBoundary(t *testing.T) {
	content, err := os.ReadFile(filepath.Join(p04RepositoryRoot(t), "fixtures", "migration", "p04", "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var recipe struct {
		FaultStages []string `json:"fault_stages"`
		FaultModes  []string `json:"fault_modes"`
	}
	if err := json.Unmarshal(content, &recipe); err != nil {
		t.Fatal(err)
	}
	expected := []string{
		"before-backup", "after-backup", "migration-transaction", "migration-sql", "before-ledger-record",
		"after-ledger-record", "wal-checkpoint", "post-migration-audit",
		"restore-staging", "restore-atomic-replace",
	}
	if !equalStrings(recipe.FaultStages, expected) {
		t.Fatalf("fault stages = %v, want %v", recipe.FaultStages, expected)
	}
	expectedModes := []string{
		"cancellation", "panic", "process-interruption", "short-write",
		"enospc", "permission-denied", "checksum-damage",
	}
	if !equalStrings(recipe.FaultModes, expectedModes) {
		t.Fatalf("fault modes = %v, want %v", recipe.FaultModes, expectedModes)
	}
}

func TestCopyFaultsNeverReportSuccessfulBackupWrites(t *testing.T) {
	payload := bytes.Repeat([]byte("fixture"), 4096)
	cases := []struct {
		name   string
		writer io.Writer
	}{
		{name: "short-write", writer: shortWriter{}},
		{name: "enospc", writer: errorWriter{err: syscall.Errno(28)}},
		{name: "permission", writer: errorWriter{err: os.ErrPermission}},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			written, err := copyWithContext(context.Background(), item.writer, bytes.NewReader(payload))
			if !errors.Is(err, ErrBackupFailed) || written >= int64(len(payload)) {
				t.Fatalf("copyWithContext() = %d, %v", written, err)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if written, err := copyWithContext(ctx, io.Discard, bytes.NewReader(payload)); !errors.Is(err, context.Canceled) || written != 0 {
		t.Fatalf("cancelled copyWithContext() = %d, %v", written, err)
	}
}

func TestBackupBoundaryCancellationAndPostBackupFailurePreserveSource(t *testing.T) {
	root := t.TempDir()
	backup := filepath.Join(root, "backups")
	if err := os.Mkdir(backup, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "fixture.sqlite.copy")
	source := p04SyntheticSQLite(0, 'A')
	if err := os.WriteFile(target, source, 0o600); err != nil {
		t.Fatal(err)
	}
	options := BackupOptions{
		Preflight: p04PreflightReport(target, backup, source, 0),
		Clock:     fixedClock{value: time.Date(2026, 7, 11, 5, 0, 0, 0, time.UTC)},
		RunID:     "fault-boundary",
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := CreateBackup(ctx, options); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled CreateBackup() error = %v", err)
	}
	if entries, _ := os.ReadDir(backup); len(entries) != 0 {
		t.Fatalf("before-backup cancellation left %d artifacts", len(entries))
	}
	set, err := CreateBackup(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadAndVerifyManifest(context.Background(), set.ManifestPath, backup); err != nil {
		t.Fatalf("post-backup injected failure would not leave a valid recovery point: %v", err)
	}
	actual, err := os.ReadFile(target)
	if err != nil || !bytes.Equal(actual, source) {
		t.Fatalf("backup boundaries changed source: %v", err)
	}
}

func TestRunnerFaultInjectionRollsBackTransactionLedgerAndVersion(t *testing.T) {
	cases := []struct {
		name     string
		fault    string
		verifier VerifyFunc
		expected error
	}{
		{name: "migration-sql", fault: "migration-sql", expected: ErrMigrationFailed},
		{name: "before-ledger-record", fault: "before-ledger-record", expected: ErrMigrationFailed},
		{name: "after-ledger-record", fault: "after-ledger-record", expected: ErrMigrationFailed},
		{name: "process-interruption", fault: "after-user-version", expected: ErrMigrationFailed},
		{name: "post-migration-audit", verifier: func(context.Context, Queryer) error {
			return errors.New("synthetic_post_audit_failure")
		}, expected: ErrSchemaVerification},
		{name: "panic", verifier: func(context.Context, Queryer) error {
			panic("synthetic_post_audit_panic")
		}, expected: ErrMigrationPanic},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			base := newFakeDatabase()
			database := &injectedDatabase{fakeDatabase: base, fault: item.fault}
			options := []Option{WithClock(fixedClock{value: time.Date(2026, 7, 11, 5, 1, 0, 0, time.UTC)})}
			if item.verifier != nil {
				options = append(options, WithVerifier(item.verifier))
			}
			_, err := NewRunner(database, migrations.NewRegistry(migrations.NewCatalog()), options...).Run(context.Background())
			if !errors.Is(err, item.expected) {
				t.Fatalf("Run() error = %v, want %v", err, item.expected)
			}
			if base.userVersion != 0 || base.ledger || len(base.history) != 0 ||
				base.lastTx == nil || !base.lastTx.rolledBack || base.lastTx.committed {
				t.Fatalf("fault left a completed or partial migration: %#v", base)
			}
		})
	}
}

func TestRunnerCancellationAfterLedgerAndVersionStillRollsBack(t *testing.T) {
	base := newFakeDatabase()
	ctx, cancel := context.WithCancel(context.Background())
	runner := NewRunner(
		base,
		migrations.NewRegistry(migrations.NewCatalog()),
		WithClock(fixedClock{value: time.Date(2026, 7, 11, 5, 2, 0, 0, time.UTC)}),
		WithVerifier(func(context.Context, Queryer) error {
			cancel()
			return nil
		}),
	)
	_, err := runner.Run(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v", err)
	}
	if base.userVersion != 0 || base.ledger || len(base.history) != 0 ||
		base.lastTx == nil || !base.lastTx.rolledBack || base.lastTx.committed {
		t.Fatalf("cancelled transaction leaked migration state: %#v", base)
	}
}

func TestWALCheckpointFaultIsBlockedByPreflightWithoutChangingSource(t *testing.T) {
	root := t.TempDir()
	backup := filepath.Join(root, "backups")
	if err := os.Mkdir(backup, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "fixture.sqlite.copy")
	source := p04SyntheticSQLite(0, 'W')
	if err := os.WriteFile(target, source, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target+"-wal", []byte("synthetic-active-wal"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Preflight(context.Background(), PreflightOptions{
		Target: target, AllowedRoot: root, BackupDirectory: backup, SanitizedCopy: true,
		EvidenceCheck:  func(string) error { return nil },
		AvailableBytes: func(string) (int64, error) { return 1 << 30, nil },
	})
	if !errors.Is(err, ErrPreflightSidecarActive) {
		t.Fatalf("Preflight() error = %v", err)
	}
	actual, readErr := os.ReadFile(target)
	if readErr != nil || !bytes.Equal(actual, source) {
		t.Fatalf("WAL/checkpoint rejection changed source: %v", readErr)
	}
}

func injectedFailure(fault, query string) (bool, bool) {
	switch {
	case fault == "migration-sql" && strings.Contains(query, "CREATE TABLE"):
		return true, false
	case fault == "before-ledger-record" && strings.HasPrefix(query, "INSERT INTO schema_migrations"):
		return true, false
	case fault == "after-ledger-record" && strings.HasPrefix(query, "INSERT INTO schema_migrations"):
		return true, true
	case fault == "after-user-version" && strings.HasPrefix(query, "PRAGMA user_version = "):
		return true, true
	default:
		return false, false
	}
}
