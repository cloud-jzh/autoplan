// Package migration coordinates the P04 copy-only migration workflow. It is
// deliberately not exposed through HTTP, MCP, renderer, or desktop services.
package migration

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	coremigration "github.com/lyming99/autoplan/backend/internal/migration"
	"github.com/lyming99/autoplan/backend/internal/repository/sqlite"
	"github.com/lyming99/autoplan/backend/migrations"
)

type Command string

const (
	CommandPreflight Command = "preflight"
	CommandDryRun    Command = "dry-run"
	CommandMigrate   Command = "migrate"
	CommandVerify    Command = "verify"
	CommandRestore   Command = "restore"
)

type Request struct {
	Command         Command
	Database        string
	AllowedRoot     string
	BackupDirectory string
	Manifest        string
	SanitizedCopy   bool
}

type Report struct {
	SchemaVersion   int              `json:"schema_version"`
	Command         Command          `json:"command"`
	Status          string           `json:"status"`
	Code            string           `json:"code"`
	RunID           string           `json:"run_id"`
	DatabaseID      string           `json:"database_id,omitempty"`
	FromVersion     int              `json:"from_version"`
	ToVersion       int              `json:"to_version"`
	PendingVersions []int            `json:"pending_versions,omitempty"`
	AppliedVersions []int            `json:"applied_versions,omitempty"`
	MigrationSHA256 string           `json:"migration_sha256,omitempty"`
	SourceSHA256    string           `json:"source_sha256,omitempty"`
	ResultSHA256    string           `json:"result_sha256,omitempty"`
	ManifestID      string           `json:"manifest_id,omitempty"`
	ManifestSHA256  string           `json:"manifest_sha256,omitempty"`
	RowCountsBefore map[string]int64 `json:"row_counts_before,omitempty"`
	RowCountsAfter  map[string]int64 `json:"row_counts_after,omitempty"`
	NoOp            bool             `json:"no_op"`
	WritePerformed  bool             `json:"write_performed"`
}

type OpenConnectionFunc func(context.Context, string) (*sqlite.Connection, error)
type LockFunc func(string, string) (func() error, error)

type Dependencies struct {
	RepositoryRoot string
	TemporaryRoot  string
	DriverName     string
	Clock          coremigration.Clock
	EvidenceCheck  coremigration.EvidenceCheckFunc
	AvailableBytes coremigration.AvailableBytesFunc
	OpenConnection OpenConnectionFunc
	AcquireLock    LockFunc
}

type systemClock struct{}

var errIntegrityCheck = errors.New("migration_integrity_check_failed")

func (systemClock) Now() time.Time { return time.Now().UTC() }

func Execute(ctx context.Context, request Request, dependencies Dependencies) (Report, error) {
	switch request.Command {
	case CommandPreflight, CommandDryRun, CommandMigrate, CommandVerify, CommandRestore:
	default:
		return blocked(request.Command, "invalid_command", ""), errors.New("migration_command_invalid")
	}
	runID, err := newRunID()
	if err != nil {
		return blocked(request.Command, "run_id_unavailable", ""), err
	}
	report := blocked(request.Command, "preflight_failed", runID)
	if dependencies.Clock == nil {
		dependencies.Clock = systemClock{}
	}
	if dependencies.TemporaryRoot == "" {
		dependencies.TemporaryRoot = os.TempDir()
	}
	if dependencies.AcquireLock == nil {
		dependencies.AcquireLock = acquireFileLock
	}
	preflight, err := coremigration.Preflight(ctx, coremigration.PreflightOptions{
		Target: request.Database, AllowedRoot: request.AllowedRoot,
		BackupDirectory: request.BackupDirectory, RepositoryRoot: dependencies.RepositoryRoot,
		SanitizedCopy: request.SanitizedCopy, AvailableBytes: dependencies.AvailableBytes,
		EvidenceCheck: dependencies.EvidenceCheck, RestoreMode: request.Command == CommandRestore,
	})
	if err != nil {
		report.Code = errorCode(err)
		return report, err
	}
	report.DatabaseID = preflight.StableDatabaseID
	report.SourceSHA256 = preflight.SHA256
	report.FromVersion = preflight.UserVersion
	report.ToVersion = preflight.UserVersion
	report.MigrationSHA256 = migrations.SchemaV1Checksum
	if preflight.UserVersion < migrations.SchemaV1UserVersion {
		report.PendingVersions = []int{migrations.SchemaV1Version}
	}
	if request.Command == CommandPreflight {
		report.Status, report.Code = "ok", "preflight_ok"
		return report, nil
	}
	if dependencies.OpenConnection == nil {
		if strings.TrimSpace(dependencies.DriverName) == "" {
			report.Code = "sqlite_driver_unavailable"
			return report, sqlite.ErrConnectionUnavailable
		}
		dependencies.OpenConnection = func(ctx context.Context, target string) (*sqlite.Connection, error) {
			return sqlite.OpenConnection(ctx, sqlite.ConnectionOptions{
				DriverName: dependencies.DriverName, DataSourceName: target,
			})
		}
	}
	if request.Command == CommandVerify && preflight.UserVersion != migrations.SchemaV1UserVersion {
		report.Code = "migration_pending"
		return report, coremigration.ErrDirtyHistory
	}
	release, err := dependencies.AcquireLock(preflight.Target, runID)
	if err != nil {
		report.Code = "migration_locked"
		return report, err
	}
	var result Report
	var commandErr error
	switch request.Command {
	case CommandDryRun:
		result, commandErr = executeDryRun(ctx, report, preflight, dependencies)
	case CommandMigrate:
		result, commandErr = executeMigrate(ctx, report, preflight, dependencies)
	case CommandVerify:
		result, commandErr = executeVerify(ctx, report, preflight, dependencies)
	case CommandRestore:
		result, commandErr = executeRestore(ctx, report, request, preflight, dependencies)
	}
	if releaseErr := release(); releaseErr != nil && commandErr == nil {
		result.Status, result.Code = "blocked", "lock_release_failed"
		return result, releaseErr
	}
	return result, commandErr
}

func executeDryRun(ctx context.Context, report Report, preflight coremigration.PreflightReport, dependencies Dependencies) (Report, error) {
	directory, err := os.MkdirTemp(dependencies.TemporaryRoot, "autoplan-p04-dry-")
	if err != nil {
		report.Code = "dry_run_temporary_failed"
		return report, err
	}
	cleaned := false
	defer func() {
		if !cleaned {
			_ = os.RemoveAll(directory)
		}
	}()
	target := filepath.Join(directory, "database.sqlite.copy")
	artifact, err := coremigrationCopy(ctx, preflight.Target, target)
	if err != nil || artifact.SHA256 != preflight.SHA256 {
		report.Code = "dry_run_copy_failed"
		return report, err
	}
	beforeCounts, err := collectTableCounts(ctx, target, dependencies.OpenConnection)
	if err != nil {
		report.Code = "dry_run_read_failed"
		return report, err
	}
	result, resultSHA, err := migrateAndVerify(ctx, target, dependencies)
	if err != nil {
		report.Code = errorCode(err)
		return report, err
	}
	afterCounts, err := collectTableCounts(ctx, target, dependencies.OpenConnection)
	if err != nil {
		report.Code = "dry_run_verify_failed"
		return report, err
	}
	unchanged, _, err := coremigrationHash(ctx, preflight.Target)
	after, statErr := os.Stat(preflight.Target)
	if err != nil || statErr != nil || unchanged != preflight.SHA256 ||
		!os.SameFile(preflight.SourceInfo, after) || preflight.SourceInfo.Size() != after.Size() ||
		preflight.SourceInfo.ModTime() != after.ModTime() {
		report.Code = "source_changed"
		return report, coremigration.ErrPreflightSourceChanged
	}
	report.Status, report.Code = "ok", "dry_run_ok"
	report.ToVersion = result.ToVersion
	report.AppliedVersions = appliedVersions(result)
	report.ResultSHA256 = resultSHA
	report.RowCountsBefore = beforeCounts
	report.RowCountsAfter = afterCounts
	report.NoOp = result.NoOp
	report.WritePerformed = false
	if err := os.RemoveAll(directory); err != nil {
		report.Status, report.Code = "blocked", "dry_run_cleanup_failed"
		return report, err
	}
	cleaned = true
	return report, nil
}

func executeMigrate(ctx context.Context, report Report, preflight coremigration.PreflightReport, dependencies Dependencies) (Report, error) {
	if preflight.UserVersion == migrations.SchemaV1UserVersion {
		verified, err := executeVerify(ctx, report, preflight, dependencies)
		if err == nil {
			verified.Code = "migration_noop"
		}
		return verified, err
	}
	backup, err := coremigration.CreateBackup(ctx, coremigration.BackupOptions{
		Preflight: preflight, Clock: dependencies.Clock, RunID: report.RunID,
	})
	if err != nil {
		report.Code = errorCode(err)
		return report, err
	}
	report.ManifestID, report.ManifestSHA256 = backup.ManifestID, backup.ManifestSHA256
	currentSHA, currentSize, hashErr := coremigrationHash(ctx, preflight.Target)
	if hashErr != nil || currentSHA != preflight.SHA256 || currentSize != preflight.Size {
		report.Code = "source_changed"
		return report, coremigration.ErrPreflightSourceChanged
	}
	result, resultSHA, err := migrateAndVerify(ctx, preflight.Target, dependencies)
	if err != nil {
		report.Code = errorCode(err)
		return report, err
	}
	report.Status, report.Code = "ok", "migration_ok"
	report.ToVersion = result.ToVersion
	report.AppliedVersions = appliedVersions(result)
	report.ResultSHA256 = resultSHA
	report.NoOp = result.NoOp
	report.WritePerformed = !result.NoOp
	return report, nil
}

func executeVerify(ctx context.Context, report Report, preflight coremigration.PreflightReport, dependencies Dependencies) (Report, error) {
	result, resultSHA, err := migrateAndVerify(ctx, preflight.Target, dependencies)
	if err != nil {
		report.Code = errorCode(err)
		return report, err
	}
	if !result.NoOp {
		report.Code = "verify_would_write"
		return report, coremigration.ErrDirtyHistory
	}
	report.Status, report.Code = "ok", "verified"
	report.ToVersion = result.ToVersion
	report.ResultSHA256 = resultSHA
	report.NoOp = true
	return report, nil
}

func executeRestore(ctx context.Context, report Report, request Request, preflight coremigration.PreflightReport, dependencies Dependencies) (Report, error) {
	if request.Manifest == "" || !filepath.IsAbs(request.Manifest) {
		report.Code = "manifest_invalid"
		return report, coremigration.ErrManifestInvalid
	}
	result, err := coremigration.RestoreBackup(ctx, coremigration.RestoreOptions{
		ManifestPath: request.Manifest, BackupRoot: preflight.BackupDirectory,
		Target: preflight.Target, AllowedTargetRoot: request.AllowedRoot, RunID: report.RunID,
		VerifyDatabase: func(ctx context.Context, target string) error {
			return verifyIntegrity(ctx, target, dependencies.OpenConnection)
		},
	})
	if err != nil {
		report.Code = errorCode(err)
		return report, err
	}
	report.Status, report.Code = "ok", "restored"
	report.ManifestID = result.ManifestID
	report.ResultSHA256 = result.SHA256
	report.ToVersion = result.UserVersion
	report.WritePerformed = true
	return report, nil
}

func migrateAndVerify(ctx context.Context, target string, dependencies Dependencies) (coremigration.Result, string, error) {
	connection, err := dependencies.OpenConnection(ctx, target)
	if err != nil {
		return coremigration.Result{}, "", err
	}
	registry := migrations.NewRegistry(migrations.NewCatalog())
	runner := coremigration.NewRunner(coremigration.WrapSQL(connection), registry,
		coremigration.WithClock(dependencies.Clock))
	result, runErr := runner.Run(ctx)
	if runErr == nil {
		runErr = sqlite.ValidateSchemaV1(ctx, connection)
	}
	closeErr := connection.Close()
	if runErr != nil {
		return coremigration.Result{}, "", runErr
	}
	if closeErr != nil {
		return coremigration.Result{}, "", closeErr
	}
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if _, err := os.Lstat(target + suffix); err == nil || !os.IsNotExist(err) {
			return coremigration.Result{}, "", errors.New("sqlite_sidecar_remained")
		}
	}
	if err := verifyIntegrity(ctx, target, dependencies.OpenConnection); err != nil {
		return coremigration.Result{}, "", err
	}
	digest, _, err := coremigrationHash(ctx, target)
	return result, digest, err
}

func verifyIntegrity(ctx context.Context, target string, open OpenConnectionFunc) error {
	identifier, err := newRunID()
	if err != nil {
		return err
	}
	verificationCopy := target + "." + identifier + ".integrity.sqlite.copy"
	if _, err := copyForService(ctx, target, verificationCopy); err != nil {
		return err
	}
	defer func() {
		for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
			_ = os.Remove(verificationCopy + suffix)
		}
	}()
	connection, err := open(ctx, verificationCopy)
	if err != nil {
		return err
	}
	defer connection.Close()
	rows, err := connection.QueryContext(ctx, "PRAGMA integrity_check")
	if err != nil {
		return err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var result string
		if err := rows.Scan(&result); err != nil || result != "ok" {
			return errIntegrityCheck
		}
		count++
	}
	if err := rows.Err(); err != nil || count != 1 {
		return errIntegrityCheck
	}
	return nil
}

func collectTableCounts(ctx context.Context, target string, open OpenConnectionFunc) (map[string]int64, error) {
	connection, err := open(ctx, target)
	if err != nil {
		return nil, err
	}
	defer connection.Close()
	rows, err := connection.QueryContext(ctx,
		"SELECT name FROM sqlite_schema WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name")
	if err != nil {
		return nil, err
	}
	names := make([]string, 0)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil || !safeSQLIdentifier(name) {
			_ = rows.Close()
			return nil, errors.New("database_table_invalid")
		}
		names = append(names, name)
	}
	if err := rows.Close(); err != nil || rows.Err() != nil {
		return nil, errors.New("database_table_read_failed")
	}
	counts := make(map[string]int64, len(names))
	for _, name := range names {
		var count int64
		if err := connection.QueryRowContext(ctx, fmt.Sprintf("SELECT COUNT(*) FROM \"%s\"", name)).Scan(&count); err != nil {
			return nil, errors.New("database_count_failed")
		}
		counts[name] = count
	}
	return counts, nil
}

func safeSQLIdentifier(value string) bool {
	if value == "" {
		return false
	}
	for index, char := range value {
		if !((char >= 'a' && char <= 'z') || char == '_' || (index > 0 && char >= '0' && char <= '9')) {
			return false
		}
	}
	return true
}

func acquireFileLock(target, runID string) (func() error, error) {
	path := target + ".p04-migrate.lock"
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, errors.New("migration_lock_unavailable")
	}
	if _, err := file.WriteString(runID + "\n"); err != nil || file.Sync() != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return nil, errors.New("migration_lock_unavailable")
	}
	return func() error {
		owned, statErr := file.Stat()
		current, pathErr := os.Lstat(path)
		closeErr := file.Close()
		if statErr != nil || pathErr != nil || closeErr != nil || !os.SameFile(owned, current) {
			return errors.New("migration_lock_release_failed")
		}
		return os.Remove(path)
	}, nil
}

func newRunID() (string, error) {
	bytes := make([]byte, 8)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return "p04-" + hex.EncodeToString(bytes), nil
}

func appliedVersions(result coremigration.Result) []int {
	versions := make([]int, 0, len(result.Applied))
	for _, item := range result.Applied {
		versions = append(versions, item.Version)
	}
	return versions
}

func blocked(command Command, code, runID string) Report {
	return Report{SchemaVersion: 1, Command: command, Status: "blocked", Code: code, RunID: runID}
}

func errorCode(err error) string {
	switch {
	case errors.Is(err, coremigration.ErrPreflightUnsafeTarget):
		return "unsafe_target"
	case errors.Is(err, coremigration.ErrPreflightSourceInvalid):
		return "source_invalid"
	case errors.Is(err, coremigration.ErrPreflightBackupInvalid):
		return "backup_directory_invalid"
	case errors.Is(err, coremigration.ErrPreflightSourceChanged):
		return "source_changed"
	case errors.Is(err, coremigration.ErrPreflightPrerequisite):
		return "prerequisite_failed"
	case errors.Is(err, coremigration.ErrPreflightInsufficientSpace):
		return "insufficient_space"
	case errors.Is(err, coremigration.ErrPreflightSidecarActive):
		return "database_in_use"
	case errors.Is(err, coremigration.ErrBackupExists):
		return "backup_exists"
	case errors.Is(err, coremigration.ErrBackupFailed):
		return "backup_failed"
	case errors.Is(err, coremigration.ErrBackupVerification):
		return "backup_verification_failed"
	case errors.Is(err, coremigration.ErrManifestInvalid):
		return "manifest_invalid"
	case errors.Is(err, coremigration.ErrRestoreVerification):
		return "restore_verification_failed"
	case errors.Is(err, coremigration.ErrRestoreUnsafe):
		return "restore_unsafe"
	case errors.Is(err, coremigration.ErrRestoreFailed):
		return "restore_failed"
	case errors.Is(err, coremigration.ErrChecksumDrift):
		return "migration_checksum_drift"
	case errors.Is(err, coremigration.ErrFutureVersion), errors.Is(err, coremigration.ErrPreflightFutureVersion):
		return "future_schema_version"
	case errors.Is(err, sqlite.ErrConnectionPolicy):
		return "sqlite_policy_failed"
	case errors.Is(err, sqlite.ErrConnectionUnavailable):
		return "sqlite_unavailable"
	case errors.Is(err, coremigration.ErrSchemaVerification):
		return "migration_verification_failed"
	case errors.Is(err, errIntegrityCheck):
		return "integrity_check_failed"
	default:
		return "migration_failed"
	}
}

func coremigrationCopy(ctx context.Context, source, destination string) (coremigration.BackupArtifact, error) {
	return copyForService(ctx, source, destination)
}

func copyForService(ctx context.Context, source, destination string) (coremigration.BackupArtifact, error) {
	input, err := os.Open(source)
	if err != nil {
		return coremigration.BackupArtifact{}, err
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return coremigration.BackupArtifact{}, err
	}
	hasher := sha256.New()
	written, err := io.Copy(io.MultiWriter(output, hasher), input)
	syncErr := output.Sync()
	closeErr := output.Close()
	if err != nil || syncErr != nil || closeErr != nil {
		_ = os.Remove(destination)
		return coremigration.BackupArtifact{}, errors.New("dry_run_copy_failed")
	}
	return coremigration.BackupArtifact{Role: "dry-run", File: filepath.Base(destination), Size: written, SHA256: hex.EncodeToString(hasher.Sum(nil))}, nil
}

func coremigrationHash(ctx context.Context, target string) (string, int64, error) {
	file, err := os.Open(target)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()
	hasher := sha256.New()
	buffer := make([]byte, 128*1024)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return "", 0, err
		}
		count, readErr := file.Read(buffer)
		if count > 0 {
			_, _ = hasher.Write(buffer[:count])
			total += int64(count)
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return "", 0, readErr
		}
	}
	return hex.EncodeToString(hasher.Sum(nil)), total, nil
}
