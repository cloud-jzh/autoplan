// Package secrets migrates plaintext compatibility fields from an explicitly
// authorized database copy into the platform secret provider. It never opens
// an application default database and never returns secret material.
package secrets

import (
	"context"
	"errors"

	platformsecrets "github.com/lyming99/autoplan/backend/internal/platform/secrets"
)

const reportSchemaVersion = 1

var (
	ErrInvalidRequest = errors.New("secret_migration_invalid_request")
	ErrUnauthorized   = errors.New("secret_migration_unauthorized")
	ErrMigration      = errors.New("secret_migration_failed")
	ErrVerification   = errors.New("secret_migration_verification_failed")
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
	Command           Command
	Database          string
	AllowedRoot       string
	BackupDirectory   string
	Authorization     string
	SecretStorageRoot string
	KeyRoot           string
	Manifest          string
	RestoreTarget     string
	SanitizedCopy     bool
	ConfirmClear      bool
}

type Dependencies struct {
	RepositoryRoot  string
	DriverName      string
	Provider        platformsecrets.Provider
	ProviderFactory func() (platformsecrets.Provider, error)
}

// Report is intentionally aggregate-only. It does not contain database paths,
// owner identifiers, provider locators, secret values, or error details.
type Report struct {
	SchemaVersion        int             `json:"schema_version"`
	Command              Command         `json:"command,omitempty"`
	Status               string          `json:"status"`
	Code                 string          `json:"code"`
	DatabaseID           string          `json:"database_id,omitempty"`
	SourceSHA256         string          `json:"source_sha256,omitempty"`
	BackupManifestID     string          `json:"backup_manifest_id,omitempty"`
	BackupManifestSHA256 string          `json:"backup_manifest_sha256,omitempty"`
	DryRun               bool            `json:"dry_run"`
	ClearApproved        bool            `json:"clear_approved"`
	ClearPerformed       bool            `json:"clear_performed"`
	IntegrityOK          bool            `json:"integrity_ok"`
	RelationshipsOK      bool            `json:"relationships_ok"`
	SnapshotCompatible   bool            `json:"snapshot_compatible"`
	SecretAvailabilityOK bool            `json:"secret_availability_ok"`
	Sources              []SourceSummary `json:"sources"`
	Tables               []TableSummary  `json:"tables"`
}

type SourceSummary struct {
	Kind   string `json:"kind"`
	Table  string `json:"table"`
	Column string `json:"column"`
	Count  int64  `json:"count"`
	Action string `json:"action"`
}

type TableSummary struct {
	Table string `json:"table"`
	Rows  int64  `json:"rows"`
}

func blocked(command Command, code string) Report {
	return Report{SchemaVersion: reportSchemaVersion, Command: command, Status: "blocked", Code: code, Sources: []SourceSummary{}, Tables: []TableSummary{}}
}

func failed(command Command, code string) Report {
	return Report{SchemaVersion: reportSchemaVersion, Command: command, Status: "failed", Code: code, Sources: []SourceSummary{}, Tables: []TableSummary{}}
}

func success(command Command) Report {
	return Report{SchemaVersion: reportSchemaVersion, Command: command, Status: "ok", Code: "ok", Sources: []SourceSummary{}, Tables: []TableSummary{}}
}

func validCommand(command Command) bool {
	switch command {
	case CommandPreflight, CommandDryRun, CommandMigrate, CommandVerify, CommandRestore:
		return true
	default:
		return false
	}
}

func checkContext(ctx context.Context) error {
	if ctx == nil {
		return ErrInvalidRequest
	}
	return ctx.Err()
}
