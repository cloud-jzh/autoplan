package secrets

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	domainsecrets "github.com/lyming99/autoplan/backend/internal/domain/secrets"
	coremigration "github.com/lyming99/autoplan/backend/internal/migration"
	"github.com/lyming99/autoplan/backend/internal/platform/instance"
	platformsecrets "github.com/lyming99/autoplan/backend/internal/platform/secrets"
	"github.com/lyming99/autoplan/backend/internal/repository/sqlite"
)

type sourceSpec struct {
	kind      domainsecrets.Kind
	table     string
	column    string
	ownerType string
	selectSQL string
	clearSQL  string
}

type secretRef struct {
	provider  string
	reference string
	hasValue  bool
	version   int64
}

type rowValue struct {
	owner domainsecrets.Owner
	value string
	clear func(context.Context, *sql.Tx) error
}

// Execute is deliberately fail-closed. Every command validates an explicit,
// static copy before it opens SQLite; only migrate can clear plaintext and it
// requires a separate clear approval flag.
func Execute(ctx context.Context, request Request, dependencies Dependencies) (Report, error) {
	if err := checkContext(ctx); err != nil || !validCommand(request.Command) {
		return blocked(request.Command, "invalid_arguments"), ErrInvalidRequest
	}
	if request.Database == "" || request.AllowedRoot == "" || request.BackupDirectory == "" ||
		request.Authorization == "" || request.SecretStorageRoot == "" || request.KeyRoot == "" ||
		!request.SanitizedCopy || !filepath.IsAbs(request.Database) || !filepath.IsAbs(request.AllowedRoot) ||
		!filepath.IsAbs(request.BackupDirectory) || !filepath.IsAbs(request.SecretStorageRoot) || !filepath.IsAbs(request.KeyRoot) {
		return blocked(request.Command, "invalid_arguments"), ErrInvalidRequest
	}
	if request.Command == CommandMigrate && !request.ConfirmClear {
		return blocked(request.Command, "clear_approval_required"), ErrUnauthorized
	}
	if request.Command != CommandPreflight && strings.TrimSpace(dependencies.DriverName) == "" {
		return blocked(request.Command, "runtime_unavailable"), ErrInvalidRequest
	}
	if (request.Command == CommandMigrate || request.Command == CommandVerify || request.Command == CommandRestore) &&
		dependencies.Provider == nil && dependencies.ProviderFactory == nil {
		return blocked(request.Command, "runtime_unavailable"), ErrInvalidRequest
	}

	preflight, err := coremigration.Preflight(ctx, coremigration.PreflightOptions{
		Target: request.Database, AllowedRoot: request.AllowedRoot, BackupDirectory: request.BackupDirectory,
		RepositoryRoot: dependencies.RepositoryRoot, SanitizedCopy: true,
		EvidenceCheck: func(string) error { return nil },
	})
	if err != nil {
		return blocked(request.Command, "preflight_failed"), err
	}
	authorization, err := loadAuthorization(request)
	if err != nil {
		return blocked(request.Command, "authorization_invalid"), err
	}
	if !privateDirectory(request.SecretStorageRoot, request.AllowedRoot) || !privateDirectory(request.KeyRoot, request.AllowedRoot) {
		return blocked(request.Command, "authorization_invalid"), ErrUnauthorized
	}
	if err := validateAuthorizationHash(authorization, preflight.SHA256); err != nil {
		return blocked(request.Command, "authorization_invalid"), err
	}
	provider := dependencies.Provider
	if request.Command == CommandMigrate || request.Command == CommandVerify || request.Command == CommandRestore {
		if provider == nil {
			provider, err = dependencies.ProviderFactory()
			if err != nil || provider == nil {
				return blocked(request.Command, "secret_provider_unavailable"), ErrUnauthorized
			}
		}
		dependencies.Provider = provider
	}

	report := success(request.Command)
	report.DatabaseID = preflight.StableDatabaseID
	report.SourceSHA256 = preflight.SHA256
	report.ClearApproved = request.ConfirmClear

	switch request.Command {
	case CommandPreflight:
		return report, nil
	case CommandDryRun:
		return dryRun(ctx, request, dependencies, preflight, report)
	case CommandMigrate:
		return migrate(ctx, request, dependencies, preflight, report)
	case CommandVerify:
		return verify(ctx, request, dependencies, report)
	case CommandRestore:
		return restore(ctx, request, dependencies, report)
	default:
		return blocked(request.Command, "invalid_command"), ErrInvalidRequest
	}
}

func dryRun(ctx context.Context, request Request, dependencies Dependencies, preflight coremigration.PreflightReport, report Report) (Report, error) {
	connection, cleanup, err := openScratchCopy(ctx, preflight, dependencies.DriverName)
	if err != nil {
		return failed(request.Command, "dry_run_unavailable"), err
	}
	defer cleanup()
	if integrity, relationships, err := validateDatabase(ctx, connection); err != nil {
		return failed(request.Command, "database_invalid"), err
	} else {
		report.IntegrityOK = integrity
		report.RelationshipsOK = relationships
		report.SnapshotCompatible = integrity && relationships
	}
	summaries, err := countPlaintext(ctx, connection, "would_migrate")
	if err != nil {
		return failed(request.Command, "dry_run_failed"), err
	}
	report.DryRun = true
	report.Sources = summaries
	tables, err := tableCounts(ctx, connection)
	if err != nil {
		return failed(request.Command, "dry_run_failed"), err
	}
	report.Tables = tables
	return report, nil
}

func migrate(ctx context.Context, request Request, dependencies Dependencies, preflight coremigration.PreflightReport, report Report) (Report, error) {
	lock, err := instance.AcquireDatabaseLock(ctx, instance.DatabaseLockOptions{Target: request.Database, AllowCreate: false})
	if err != nil {
		return blocked(request.Command, "database_locked"), err
	}
	defer lock.Close(context.Background())
	backup, err := coremigration.CreateBackup(ctx, coremigration.BackupOptions{
		Preflight: preflight, Clock: migrationClock{}, RunID: runID(),
	})
	if err != nil {
		return failed(request.Command, "backup_failed"), err
	}
	report.BackupManifestID = backup.ManifestID
	report.BackupManifestSHA256 = backup.ManifestSHA256
	connection, err := sqlite.OpenConnection(ctx, sqlite.ConnectionOptions{DriverName: dependencies.DriverName, DataSourceName: request.Database})
	if err != nil {
		return failed(request.Command, "database_open_failed"), err
	}
	defer connection.Close()
	if integrity, relationships, err := validateDatabase(ctx, connection); err != nil {
		return failed(request.Command, "database_invalid"), err
	} else {
		report.IntegrityOK = integrity
		report.RelationshipsOK = relationships
		report.SnapshotCompatible = integrity && relationships
	}
	summaries, err := migratePlaintext(ctx, connection, dependencies.Provider)
	if err != nil {
		return failed(request.Command, "migration_failed"), err
	}
	report.Sources = summaries
	report.ClearPerformed = true
	if err := noPlaintextRemains(ctx, connection); err != nil {
		return failed(request.Command, "migration_verification_failed"), err
	}
	if available, err := verifySecretAvailability(ctx, connection, dependencies.Provider); err != nil || !available {
		return failed(request.Command, "secret_verification_failed"), ErrVerification
	}
	report.SecretAvailabilityOK = true
	tables, err := tableCounts(ctx, connection)
	if err != nil {
		return failed(request.Command, "migration_verification_failed"), err
	}
	report.Tables = tables
	return report, nil
}

func verify(ctx context.Context, request Request, dependencies Dependencies, report Report) (Report, error) {
	lock, err := instance.AcquireDatabaseLock(ctx, instance.DatabaseLockOptions{Target: request.Database, AllowCreate: false})
	if err != nil {
		return blocked(request.Command, "database_locked"), err
	}
	defer lock.Close(context.Background())
	connection, err := sqlite.OpenConnection(ctx, sqlite.ConnectionOptions{DriverName: dependencies.DriverName, DataSourceName: request.Database})
	if err != nil {
		return failed(request.Command, "database_open_failed"), err
	}
	defer connection.Close()
	if integrity, relationships, err := validateDatabase(ctx, connection); err != nil {
		return failed(request.Command, "database_invalid"), err
	} else {
		report.IntegrityOK = integrity
		report.RelationshipsOK = relationships
		report.SnapshotCompatible = integrity && relationships
	}
	if err := noPlaintextRemains(ctx, connection); err != nil {
		return failed(request.Command, "plaintext_remaining"), err
	}
	if available, err := verifySecretAvailability(ctx, connection, dependencies.Provider); err != nil || !available {
		return failed(request.Command, "secret_verification_failed"), ErrVerification
	}
	report.SecretAvailabilityOK = true
	summaries, err := countPlaintext(ctx, connection, "verified_no_plaintext")
	if err != nil {
		return failed(request.Command, "verification_failed"), err
	}
	report.Sources = summaries
	tables, err := tableCounts(ctx, connection)
	if err != nil {
		return failed(request.Command, "verification_failed"), err
	}
	report.Tables = tables
	return report, nil
}

func restore(ctx context.Context, request Request, dependencies Dependencies, report Report) (Report, error) {
	if request.Manifest == "" || request.RestoreTarget == "" || !filepath.IsAbs(request.Manifest) || !filepath.IsAbs(request.RestoreTarget) ||
		!withinOrEqual(request.RestoreTarget, request.AllowedRoot) || samePath(request.RestoreTarget, request.Database) {
		return blocked(request.Command, "invalid_restore_target"), ErrInvalidRequest
	}
	manifest, manifestDigest, err := coremigration.LoadAndVerifyManifest(ctx, request.Manifest, request.BackupDirectory)
	if err != nil {
		return blocked(request.Command, "backup_invalid"), err
	}
	var artifact coremigration.BackupArtifact
	for _, candidate := range manifest.Artifacts {
		if candidate.Role == "database" {
			artifact = candidate
			break
		}
	}
	if artifact.File == "" || !newRegularFileTarget(request.RestoreTarget, request.AllowedRoot) {
		return blocked(request.Command, "invalid_restore_target"), ErrInvalidRequest
	}
	if err := copyExclusive(ctx, filepath.Join(request.BackupDirectory, artifact.File), request.RestoreTarget); err != nil {
		return failed(request.Command, "restore_failed"), err
	}
	completed := false
	defer func() {
		if !completed {
			_ = os.Remove(request.RestoreTarget)
		}
	}()
	digest, size, err := hashFile(request.RestoreTarget)
	if err != nil || digest != artifact.SHA256 || size != artifact.Size {
		return failed(request.Command, "restore_verification_failed"), ErrVerification
	}
	connection, err := sqlite.OpenConnection(ctx, sqlite.ConnectionOptions{DriverName: dependencies.DriverName, DataSourceName: request.RestoreTarget})
	if err != nil {
		return failed(request.Command, "restore_open_failed"), err
	}
	defer connection.Close()
	integrity, relationships, err := validateDatabase(ctx, connection)
	if err != nil || !integrity || !relationships {
		return failed(request.Command, "restore_verification_failed"), ErrVerification
	}
	report.BackupManifestID = manifest.ManifestID
	report.BackupManifestSHA256 = manifestDigest
	report.IntegrityOK = true
	report.RelationshipsOK = true
	report.SnapshotCompatible = true
	summaries, err := countPlaintext(ctx, connection, "restored_from_immutable_backup")
	if err != nil {
		return failed(request.Command, "restore_verification_failed"), err
	}
	report.Sources = summaries
	tables, err := tableCounts(ctx, connection)
	if err != nil {
		return failed(request.Command, "restore_verification_failed"), err
	}
	report.Tables = tables
	// A rollback copy may deliberately contain plaintext and no secret_refs.
	// Availability is only claimed when the restored snapshot contains active refs.
	available, availabilityErr := verifySecretAvailability(ctx, connection, dependencies.Provider)
	if availabilityErr != nil || !available {
		return failed(request.Command, "restore_secret_verification_failed"), ErrVerification
	}
	report.SecretAvailabilityOK = true
	completed = true
	return report, nil
}

func validateDatabase(ctx context.Context, connection *sqlite.Connection) (bool, bool, error) {
	if err := sqlite.ValidateSchemaV1(ctx, connection); err != nil {
		return false, false, err
	}
	var integrity string
	if err := connection.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&integrity); err != nil || strings.ToLower(strings.TrimSpace(integrity)) != "ok" {
		return false, false, ErrVerification
	}
	rows, err := connection.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return false, false, ErrVerification
	}
	violation := rows.Next()
	closeErr := rows.Close()
	if violation || closeErr != nil || rows.Err() != nil {
		return true, false, ErrVerification
	}
	return true, true, nil
}

func plaintextSpecs() []sourceSpec {
	return []sourceSpec{
		{domainsecrets.KindAIConfigAPIKey, "ai_configs", "api_key", "ai_config", "SELECT id, api_key FROM ai_configs WHERE api_key <> ''", "UPDATE ai_configs SET api_key = '', updated_at = ?, version = version + 1 WHERE id = ? AND api_key = ?"},
		{domainsecrets.KindClaudeCLIAuthToken, "claude_cli_configs", "auth_token", "claude_cli_config", "SELECT id, auth_token FROM claude_cli_configs WHERE auth_token <> ''", "UPDATE claude_cli_configs SET auth_token = '', updated_at = ?, version = version + 1 WHERE id = ? AND auth_token = ?"},
		{domainsecrets.KindAIConfigAPIKey, "settings", "value:chat.apiKey", "settings", "SELECT 'chat.api-key', value FROM settings WHERE key = 'chat.apiKey' AND value <> ''", "UPDATE settings SET value = '', version = version + 1 WHERE key = 'chat.apiKey' AND value = ?"},
		{domainsecrets.KindMCPAuthToken, "settings", "value:mcp.authToken", "settings", "SELECT 'mcp.auth-token', value FROM settings WHERE key = 'mcp.authToken' AND value <> ''", "UPDATE settings SET value = '', version = version + 1 WHERE key = 'mcp.authToken' AND value = ?"},
		{domainsecrets.KindPlanGenerationClaudeAuthToken, "project_states", "plan_generation_claude_auth_token", "project_state", "SELECT project_id, plan_generation_claude_auth_token FROM project_states WHERE plan_generation_claude_auth_token <> ''", "UPDATE project_states SET plan_generation_claude_auth_token = '', updated_at = ?, version = version + 1 WHERE project_id = ? AND plan_generation_claude_auth_token = ?"},
		{domainsecrets.KindPlanExecutionClaudeAuthToken, "project_states", "plan_execution_claude_auth_token", "project_state", "SELECT project_id, plan_execution_claude_auth_token FROM project_states WHERE plan_execution_claude_auth_token <> ''", "UPDATE project_states SET plan_execution_claude_auth_token = '', updated_at = ?, version = version + 1 WHERE project_id = ? AND plan_execution_claude_auth_token = ?"},
		{domainsecrets.Kind("env_vars"), "project_states", "env_vars", "project_state", "SELECT project_id, env_vars FROM project_states WHERE env_vars <> ''", "UPDATE project_states SET env_vars = '', updated_at = ?, version = version + 1 WHERE project_id = ? AND env_vars = ?"},
		{domainsecrets.KindPlanGenerationClaudeAuthToken, "requirements", "plan_generation_claude_auth_token", "requirement", "SELECT id, plan_generation_claude_auth_token FROM requirements WHERE plan_generation_claude_auth_token <> ''", "UPDATE requirements SET plan_generation_claude_auth_token = '', updated_at = ? WHERE id = ? AND plan_generation_claude_auth_token = ?"},
		{domainsecrets.KindPlanGenerationClaudeAuthToken, "feedback", "plan_generation_claude_auth_token", "feedback", "SELECT id, plan_generation_claude_auth_token FROM feedback WHERE plan_generation_claude_auth_token <> ''", "UPDATE feedback SET plan_generation_claude_auth_token = '', updated_at = ? WHERE id = ? AND plan_generation_claude_auth_token = ?"},
		{domainsecrets.KindPlanGenerationClaudeAuthToken, "plans", "plan_generation_claude_auth_token", "plan", "SELECT id, plan_generation_claude_auth_token FROM plans WHERE plan_generation_claude_auth_token <> ''", "UPDATE plans SET plan_generation_claude_auth_token = '', updated_at = ? WHERE id = ? AND plan_generation_claude_auth_token = ?"},
		{domainsecrets.KindPlanExecutionClaudeAuthToken, "plans", "plan_execution_claude_auth_token", "plan", "SELECT id, plan_execution_claude_auth_token FROM plans WHERE plan_execution_claude_auth_token <> ''", "UPDATE plans SET plan_execution_claude_auth_token = '', updated_at = ? WHERE id = ? AND plan_execution_claude_auth_token = ?"},
	}
}

func countPlaintext(ctx context.Context, connection *sqlite.Connection, action string) ([]SourceSummary, error) {
	summaries := make([]SourceSummary, 0, len(plaintextSpecs())+1)
	for _, spec := range plaintextSpecs() {
		count, err := countRows(ctx, connection, "SELECT COUNT(*) FROM ("+spec.selectSQL+")")
		if err != nil {
			return nil, err
		}
		summaries = append(summaries, SourceSummary{Kind: string(spec.kind), Table: spec.table, Column: spec.column, Count: count, Action: action})
	}
	executorCount, err := countExecutorEnvironment(ctx, connection)
	if err != nil {
		return nil, err
	}
	summaries = append(summaries, SourceSummary{Kind: "env_vars", Table: "executors", Column: "options_json.env", Count: executorCount, Action: action})
	return summaries, nil
}

func noPlaintextRemains(ctx context.Context, connection *sqlite.Connection) error {
	summaries, err := countPlaintext(ctx, connection, "")
	if err != nil {
		return err
	}
	for _, summary := range summaries {
		if summary.Count != 0 {
			return ErrVerification
		}
	}
	return nil
}

func countRows(ctx context.Context, connection *sqlite.Connection, query string) (int64, error) {
	var count int64
	if err := connection.QueryRowContext(ctx, query).Scan(&count); err != nil || count < 0 {
		return 0, ErrMigration
	}
	return count, nil
}

func countExecutorEnvironment(ctx context.Context, connection *sqlite.Connection) (int64, error) {
	rows, err := connection.QueryContext(ctx, "SELECT options_json FROM executors WHERE options_json <> ''")
	if err != nil {
		return 0, ErrMigration
	}
	defer rows.Close()
	var count int64
	for rows.Next() {
		var options string
		if err := rows.Scan(&options); err != nil {
			return 0, ErrMigration
		}
		_, present, err := executorEnvironment(options)
		if err != nil {
			return 0, err
		}
		if present {
			count++
		}
	}
	if err := rows.Err(); err != nil {
		return 0, ErrMigration
	}
	return count, nil
}

func migratePlaintext(ctx context.Context, connection *sqlite.Connection, provider platformsecrets.Provider) ([]SourceSummary, error) {
	summaries := make([]SourceSummary, 0, len(plaintextSpecs())+1)
	for _, spec := range plaintextSpecs() {
		rows, err := rowsForSpec(ctx, connection, spec)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			if err := migrateRow(ctx, connection, provider, spec.kind, row); err != nil {
				return nil, err
			}
		}
		summaries = append(summaries, SourceSummary{Kind: string(spec.kind), Table: spec.table, Column: spec.column, Count: int64(len(rows)), Action: "migrated_and_cleared"})
	}
	executorRows, err := executorRows(ctx, connection)
	if err != nil {
		return nil, err
	}
	for _, row := range executorRows {
		if err := migrateRow(ctx, connection, provider, domainsecrets.Kind("env_vars"), row); err != nil {
			return nil, err
		}
	}
	summaries = append(summaries, SourceSummary{Kind: "env_vars", Table: "executors", Column: "options_json.env", Count: int64(len(executorRows)), Action: "migrated_and_cleared"})
	return summaries, nil
}

func rowsForSpec(ctx context.Context, connection *sqlite.Connection, spec sourceSpec) ([]rowValue, error) {
	rows, err := connection.QueryContext(ctx, spec.selectSQL)
	if err != nil {
		return nil, ErrMigration
	}
	defer rows.Close()
	result := []rowValue{}
	for rows.Next() {
		var id, value string
		if err := rows.Scan(&id, &value); err != nil || value == "" {
			return nil, ErrMigration
		}
		owner := domainsecrets.Owner{Type: spec.ownerType, ID: id}
		if domainsecrets.ValidateScope(spec.kind, owner) != nil {
			return nil, ErrMigration
		}
		oldValue := value
		ownerID := id
		result = append(result, rowValue{owner: owner, value: value, clear: func(callContext context.Context, transaction *sql.Tx) error {
			args := []any{time.Now().UTC().Format(time.RFC3339Nano), ownerID, oldValue}
			if spec.table == "settings" {
				args = []any{oldValue}
			}
			result, err := transaction.ExecContext(callContext, spec.clearSQL, args...)
			if err != nil {
				return ErrMigration
			}
			changed, err := result.RowsAffected()
			if err != nil || changed != 1 {
				return ErrMigration
			}
			return nil
		}})
	}
	if err := rows.Err(); err != nil {
		return nil, ErrMigration
	}
	return result, nil
}

func executorRows(ctx context.Context, connection *sqlite.Connection) ([]rowValue, error) {
	rows, err := connection.QueryContext(ctx, "SELECT id, options_json FROM executors WHERE options_json <> ''")
	if err != nil {
		return nil, ErrMigration
	}
	defer rows.Close()
	result := []rowValue{}
	for rows.Next() {
		var id int64
		var options string
		if err := rows.Scan(&id, &options); err != nil {
			return nil, ErrMigration
		}
		value, present, err := executorEnvironment(options)
		if err != nil {
			return nil, err
		}
		if !present {
			continue
		}
		owner := domainsecrets.Owner{Type: "executor", ID: strconv.FormatInt(id, 10)}
		if domainsecrets.ValidateScope(domainsecrets.Kind("env_vars"), owner) != nil {
			return nil, ErrMigration
		}
		cleared, err := executorWithoutEnvironment(options)
		if err != nil {
			return nil, err
		}
		oldOptions := options
		ownerID := id
		result = append(result, rowValue{owner: owner, value: value, clear: func(callContext context.Context, transaction *sql.Tx) error {
			updated, err := transaction.ExecContext(callContext,
				"UPDATE executors SET options_json = ?, updated_at = ?, version = version + 1 WHERE id = ? AND options_json = ?",
				cleared, time.Now().UTC().Format(time.RFC3339Nano), ownerID, oldOptions)
			if err != nil {
				return ErrMigration
			}
			changed, err := updated.RowsAffected()
			if err != nil || changed != 1 {
				return ErrMigration
			}
			return nil
		}})
	}
	if err := rows.Err(); err != nil {
		return nil, ErrMigration
	}
	return result, nil
}

func executorEnvironment(options string) (string, bool, error) {
	var decoded map[string]json.RawMessage
	if json.Unmarshal([]byte(options), &decoded) != nil || decoded == nil {
		return "", false, ErrMigration
	}
	value, found := decoded["env"]
	if !found || len(value) == 0 || string(value) == "null" || string(value) == `""` || string(value) == "{}" {
		return "", false, nil
	}
	var validate any
	if json.Unmarshal(value, &validate) != nil {
		return "", false, ErrMigration
	}
	return string(value), true, nil
}

func executorWithoutEnvironment(options string) (string, error) {
	var decoded map[string]json.RawMessage
	if json.Unmarshal([]byte(options), &decoded) != nil || decoded == nil {
		return "", ErrMigration
	}
	delete(decoded, "env")
	encoded, err := json.Marshal(decoded)
	if err != nil {
		return "", ErrMigration
	}
	return string(encoded), nil
}

func migrateRow(ctx context.Context, connection *sqlite.Connection, provider platformsecrets.Provider, kind domainsecrets.Kind, row rowValue) error {
	existing, found, err := findSecretRef(ctx, connection, kind, row.owner)
	if err != nil {
		return err
	}
	if found {
		if !existing.hasValue {
			return ErrMigration
		}
		binding := domainsecrets.Binding{Kind: kind, Owner: row.owner, Version: existing.version}
		available, err := providerExists(ctx, provider, existing.provider, binding, existing.reference)
		if err != nil || !available {
			return ErrMigration
		}
		return clearExisting(ctx, connection, row.clear)
	}
	binding := domainsecrets.Binding{Kind: kind, Owner: row.owner, Version: 1}
	secret := []byte(row.value)
	defer clearBytes(secret)
	if domainsecrets.ValidateSecret(secret) != nil {
		return ErrMigration
	}
	providerName, reference, cleanupProvider, err := putSecret(ctx, provider, binding, secret)
	if err != nil {
		return ErrMigration
	}
	committed := false
	defer func() {
		if !committed {
			cleanupSecret(binding, reference, cleanupProvider)
		}
	}()
	available, err := cleanupProvider.Exists(ctx, binding, reference)
	if err != nil || !available {
		return ErrMigration
	}
	transaction, err := connection.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return ErrMigration
	}
	defer transaction.Rollback()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := transaction.ExecContext(ctx,
		"INSERT INTO secret_refs (owner_type, owner_id, field_name, provider, reference, has_value, created_at, updated_at, version) VALUES (?, ?, ?, ?, ?, 1, ?, ?, 1)",
		row.owner.Type, row.owner.ID, string(kind), providerName, reference, now, now); err != nil {
		return ErrMigration
	}
	if err := row.clear(ctx, transaction); err != nil {
		return err
	}
	if err := transaction.Commit(); err != nil {
		return ErrMigration
	}
	committed = true
	return nil
}

func clearExisting(ctx context.Context, connection *sqlite.Connection, clear func(context.Context, *sql.Tx) error) error {
	transaction, err := connection.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return ErrMigration
	}
	defer transaction.Rollback()
	if err := clear(ctx, transaction); err != nil {
		return err
	}
	if err := transaction.Commit(); err != nil {
		return ErrMigration
	}
	return nil
}

func findSecretRef(ctx context.Context, connection *sqlite.Connection, kind domainsecrets.Kind, owner domainsecrets.Owner) (secretRef, bool, error) {
	var result secretRef
	var hasValue int
	err := connection.QueryRowContext(ctx,
		"SELECT provider, reference, has_value, version FROM secret_refs WHERE owner_type = ? AND owner_id = ? AND field_name = ?",
		owner.Type, owner.ID, string(kind)).Scan(&result.provider, &result.reference, &hasValue, &result.version)
	if errors.Is(err, sql.ErrNoRows) {
		return secretRef{}, false, nil
	}
	if err != nil || (hasValue != 0 && hasValue != 1) || result.version <= 0 ||
		domainsecrets.ValidateScope(kind, owner) != nil || platformsecrets.ValidateReference(result.reference) != nil {
		return secretRef{}, false, ErrMigration
	}
	result.hasValue = hasValue == 1
	return result, true, nil
}

func putSecret(ctx context.Context, provider platformsecrets.Provider, binding domainsecrets.Binding, value []byte) (string, string, platformsecrets.Provider, error) {
	if detailed, ok := provider.(platformsecrets.DetailedPutter); ok {
		result, err := detailed.PutWithProvider(ctx, binding, value)
		if err != nil || platformsecrets.ValidateReference(result.Reference) != nil {
			return "", "", nil, ErrMigration
		}
		selected, ok := resolveProvider(provider, result.Provider)
		if !ok {
			return "", "", nil, ErrMigration
		}
		return result.Provider, result.Reference, selected, nil
	}
	reference, err := provider.Put(ctx, binding, value)
	if err != nil || platformsecrets.ValidateReference(reference) != nil {
		return "", "", nil, ErrMigration
	}
	return provider.Name(), reference, provider, nil
}

func providerExists(ctx context.Context, provider platformsecrets.Provider, providerName string, binding domainsecrets.Binding, reference string) (bool, error) {
	selected, ok := resolveProvider(provider, providerName)
	if !ok {
		return false, ErrVerification
	}
	exists, err := selected.Exists(ctx, binding, reference)
	if err != nil {
		return false, ErrVerification
	}
	return exists, nil
}

func resolveProvider(provider platformsecrets.Provider, name string) (platformsecrets.Provider, bool) {
	if provider == nil || name == "" {
		return nil, false
	}
	if resolver, ok := provider.(platformsecrets.Resolver); ok {
		return resolver.ProviderFor(name)
	}
	return provider, provider.Name() == name
}

func cleanupSecret(binding domainsecrets.Binding, reference string, provider platformsecrets.Provider) {
	if provider == nil || reference == "" {
		return
	}
	cleanupContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = provider.Delete(cleanupContext, binding, reference)
}

func verifySecretAvailability(ctx context.Context, connection *sqlite.Connection, provider platformsecrets.Provider) (bool, error) {
	rows, err := connection.QueryContext(ctx, "SELECT owner_type, owner_id, field_name, provider, reference, has_value, version FROM secret_refs WHERE has_value = 1")
	if err != nil {
		return false, ErrVerification
	}
	defer rows.Close()
	for rows.Next() {
		var owner domainsecrets.Owner
		var kind domainsecrets.Kind
		var providerName, reference string
		var hasValue int
		var version int64
		if err := rows.Scan(&owner.Type, &owner.ID, &kind, &providerName, &reference, &hasValue, &version); err != nil || hasValue != 1 {
			return false, ErrVerification
		}
		binding := domainsecrets.Binding{Kind: kind, Owner: owner, Version: version}
		if !binding.Valid() {
			return false, ErrVerification
		}
		exists, err := providerExists(ctx, provider, providerName, binding, reference)
		if err != nil || !exists {
			return false, ErrVerification
		}
	}
	if err := rows.Err(); err != nil {
		return false, ErrVerification
	}
	return true, nil
}

func tableCounts(ctx context.Context, connection *sqlite.Connection) ([]TableSummary, error) {
	tables := []string{"ai_configs", "claude_cli_configs", "settings", "project_states", "requirements", "feedback", "plans", "executors", "secret_refs"}
	result := make([]TableSummary, 0, len(tables))
	for _, table := range tables {
		count, err := countRows(ctx, connection, "SELECT COUNT(*) FROM "+table)
		if err != nil {
			return nil, err
		}
		result = append(result, TableSummary{Table: table, Rows: count})
	}
	return result, nil
}

func openScratchCopy(ctx context.Context, preflight coremigration.PreflightReport, driverName string) (*sqlite.Connection, func(), error) {
	name := filepath.Join(preflight.BackupDirectory, ".p08-dry-run-"+preflight.StableDatabaseID+"-"+runID()+".sqlite.copy")
	if err := copyExclusive(ctx, preflight.Target, name); err != nil {
		return nil, func() {}, err
	}
	connection, err := sqlite.OpenConnection(ctx, sqlite.ConnectionOptions{DriverName: driverName, DataSourceName: name})
	cleanup := func() {
		if connection != nil {
			_ = connection.Close()
		}
		for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
			_ = os.Remove(name + suffix)
		}
	}
	if err != nil {
		cleanup()
		return nil, func() {}, err
	}
	return connection, cleanup, nil
}

func copyExclusive(ctx context.Context, source, destination string) error {
	input, err := os.Open(source)
	if err != nil {
		return ErrMigration
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return ErrMigration
	}
	completed := false
	defer func() {
		_ = output.Close()
		if !completed {
			_ = os.Remove(destination)
		}
	}()
	buffer := make([]byte, 128*1024)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		count, readErr := input.Read(buffer)
		if count > 0 {
			if _, err := output.Write(buffer[:count]); err != nil {
				return ErrMigration
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return ErrMigration
		}
	}
	if err := output.Sync(); err != nil || output.Close() != nil {
		return ErrMigration
	}
	completed = true
	return nil
}

func privateDirectory(value, root string) bool {
	if !filepath.IsAbs(value) || !withinOrEqual(value, root) {
		return false
	}
	info, err := os.Lstat(value)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return false
	}
	resolvedRoot, rootErr := filepath.EvalSymlinks(root)
	resolvedValue, valueErr := filepath.EvalSymlinks(value)
	return rootErr == nil && valueErr == nil && samePath(value, resolvedValue) && withinOrEqual(resolvedValue, resolvedRoot)
}

func newRegularFileTarget(value, root string) bool {
	if filepath.Base(value) == "autoplan.sqlite" || !withinOrEqual(value, root) {
		return false
	}
	if _, err := os.Lstat(value); !os.IsNotExist(err) {
		return false
	}
	parent := filepath.Dir(value)
	info, err := os.Lstat(parent)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return false
	}
	resolvedRoot, rootErr := filepath.EvalSymlinks(root)
	resolvedParent, parentErr := filepath.EvalSymlinks(parent)
	return rootErr == nil && parentErr == nil && samePath(parent, resolvedParent) && withinOrEqual(resolvedParent, resolvedRoot)
}

func samePath(left, right string) bool {
	return strings.EqualFold(filepath.Clean(left), filepath.Clean(right))
}

func clearBytes(value []byte) {
	for index := range value {
		value[index] = 0
	}
}

type migrationClock struct{}

func (migrationClock) Now() time.Time { return time.Now().UTC() }

func runID() string {
	value := make([]byte, 8)
	if _, err := rand.Read(value); err == nil {
		return "p08-" + hex.EncodeToString(value)
	}
	return fmt.Sprintf("p08-%d", time.Now().UTC().UnixNano())
}
