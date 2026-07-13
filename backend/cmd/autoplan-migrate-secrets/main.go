// autoplan-migrate-secrets only accepts a caller-prepared database copy. Its
// JSON output is aggregate-only and safe to retain as migration evidence.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"io"
	"os"
	"os/signal"
	"strings"

	secretmigration "github.com/lyming99/autoplan/backend/internal/migration/secrets"
	"github.com/lyming99/autoplan/backend/internal/platform/repositoryroot"
	platformsecrets "github.com/lyming99/autoplan/backend/internal/platform/secrets"
	"github.com/lyming99/autoplan/backend/internal/platform/secrets/encryptedfile"
	"github.com/lyming99/autoplan/backend/internal/platform/secrets/keyring"
	"github.com/lyming99/autoplan/backend/internal/runtime/lifecycle"
)

const (
	exitOK      = 0
	exitFailure = 1
	exitUsage   = 2
	exitBlocked = 3
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), lifecycle.TerminationSignals()...)
	defer cancel()
	os.Exit(run(ctx, os.Args[1:], os.Environ(), os.Stdout))
}

func run(ctx context.Context, args, environ []string, output io.Writer) int {
	request, allowFallback, ok := parseArgs(args)
	if !ok {
		_ = json.NewEncoder(output).Encode(secretmigration.Report{SchemaVersion: 1, Status: "blocked", Code: "invalid_arguments"})
		return exitUsage
	}
	root, err := repositoryroot.Find()
	if err != nil {
		_ = json.NewEncoder(output).Encode(secretmigration.Report{SchemaVersion: 1, Command: request.Command, Status: "blocked", Code: "repository_root_unavailable"})
		return exitBlocked
	}
	var providerFactory func() (platformsecrets.Provider, error)
	if request.Command == secretmigration.CommandMigrate || request.Command == secretmigration.CommandVerify || request.Command == secretmigration.CommandRestore {
		providerFactory = func() (platformsecrets.Provider, error) { return newProvider(request, allowFallback) }
	}
	report, err := secretmigration.Execute(ctx, request, secretmigration.Dependencies{
		RepositoryRoot: root, DriverName: selectDriver(environ), ProviderFactory: providerFactory,
	})
	if encodeErr := json.NewEncoder(output).Encode(report); encodeErr != nil {
		return exitFailure
	}
	if err == nil && report.Status == "ok" {
		return exitOK
	}
	if report.Code == "invalid_arguments" || report.Code == "invalid_command" || report.Code == "invalid_restore_target" {
		return exitUsage
	}
	if report.Status == "blocked" {
		return exitBlocked
	}
	return exitFailure
}

func parseArgs(args []string) (secretmigration.Request, bool, bool) {
	if len(args) == 0 {
		return secretmigration.Request{}, false, false
	}
	request := secretmigration.Request{Command: secretmigration.Command(args[0])}
	switch request.Command {
	case secretmigration.CommandPreflight, secretmigration.CommandDryRun, secretmigration.CommandMigrate,
		secretmigration.CommandVerify, secretmigration.CommandRestore:
	default:
		return secretmigration.Request{}, false, false
	}
	flags := flag.NewFlagSet(string(request.Command), flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var allowFallback bool
	flags.StringVar(&request.Database, "database", "", "authorized database copy")
	flags.StringVar(&request.AllowedRoot, "allow-root", "", "authorized root")
	flags.StringVar(&request.BackupDirectory, "backup-dir", "", "immutable backup directory")
	flags.StringVar(&request.Authorization, "authorization", "", "prepared-copy authorization manifest")
	flags.StringVar(&request.SecretStorageRoot, "secret-store", "", "explicit secret storage root")
	flags.StringVar(&request.KeyRoot, "key-root", "", "explicit encrypted-store key root")
	flags.StringVar(&request.Manifest, "manifest", "", "immutable backup manifest for restore")
	flags.StringVar(&request.RestoreTarget, "restore-target", "", "new restore drill target")
	flags.BoolVar(&request.SanitizedCopy, "sanitized-copy", false, "declare caller-owned isolated copy")
	flags.BoolVar(&request.ConfirmClear, "confirm-clear", false, "separate approval for irreversible plaintext clear")
	flags.BoolVar(&allowFallback, "allow-fallback", false, "allow encrypted fallback after keyring unavailability")
	if err := flags.Parse(args[1:]); err != nil || flags.NArg() != 0 || request.Database == "" || request.AllowedRoot == "" ||
		request.BackupDirectory == "" || request.Authorization == "" || request.SecretStorageRoot == "" || request.KeyRoot == "" || !request.SanitizedCopy {
		return secretmigration.Request{}, false, false
	}
	if request.Command == secretmigration.CommandMigrate && !request.ConfirmClear {
		return secretmigration.Request{}, false, false
	}
	if request.Command == secretmigration.CommandRestore && (request.Manifest == "" || request.RestoreTarget == "") {
		return secretmigration.Request{}, false, false
	}
	if request.Command != secretmigration.CommandRestore && (request.Manifest != "" || request.RestoreTarget != "") {
		return secretmigration.Request{}, false, false
	}
	return request, allowFallback, true
}

func newProvider(request secretmigration.Request, allowFallback bool) (platformsecrets.Provider, error) {
	primary, err := keyring.New(keyring.Options{Service: "autoplan-p08-secrets"})
	if err != nil {
		return nil, err
	}
	var fallback platformsecrets.Provider
	if allowFallback {
		fallback, err = encryptedfile.New(encryptedfile.Options{Root: request.SecretStorageRoot, KeyRoot: request.KeyRoot})
		if err != nil {
			return nil, err
		}
	}
	return platformsecrets.NewPreferred(platformsecrets.PreferenceOptions{
		Primary: primary, Fallback: fallback, AllowFallback: allowFallback,
	})
}

func selectDriver(environ []string) string {
	for _, entry := range environ {
		name, value, found := strings.Cut(entry, "=")
		if found && name == "AUTOPLAN_SQLITE_DRIVER" {
			return strings.TrimSpace(value)
		}
	}
	drivers := sql.Drivers()
	if len(drivers) == 1 {
		return drivers[0]
	}
	for _, driver := range drivers {
		if driver == "sqlite" || driver == "sqlite3" {
			return driver
		}
	}
	return ""
}
