package bootstrap

import (
	"context"
	"flag"
	"io"

	"github.com/lyming99/autoplan/backend/internal/platform/prerequisite"
	"github.com/lyming99/autoplan/backend/internal/platform/repositoryroot"
	backendruntime "github.com/lyming99/autoplan/backend/internal/runtime"
	"github.com/lyming99/autoplan/backend/migrations"
)

// RunMigrate exposes status and dry-run only. It never opens the target and the
// P001 catalog contains no executable or production migration.
func RunMigrate(_ context.Context, args []string, output io.Writer) int {
	options, ok := parseMigrate(args)
	if !ok {
		return resultExit(output, commandResult{
			SchemaVersion: 1, Command: "autoplan-migrate", Status: "blocked",
			Code: "invalid_arguments", WritePerformed: false,
		}, exitUsage)
	}
	root, err := repositoryroot.Find()
	if err != nil {
		return resultExit(output, commandResult{
			SchemaVersion: 1, Command: "autoplan-migrate", Status: "blocked",
			Code: "repository_root_unavailable", WritePerformed: false,
		}, exitBlocked)
	}
	report := prerequisite.Check(root)
	if !report.OK {
		return resultExit(output, commandResult{
			SchemaVersion: 1, Command: "autoplan-migrate", Status: "blocked",
			Code: "prerequisite_gate_failed", Reasons: report.Reasons, WritePerformed: false,
		}, exitBlocked)
	}
	target := backendruntime.Target{Path: options.target, Kind: backendruntime.TargetKind(options.kind)}
	if err := backendruntime.ValidateTarget(target, root); err != nil {
		return resultExit(output, commandResult{
			SchemaVersion: 1, Command: "autoplan-migrate", Status: "blocked",
			Code: "unsafe_target", WritePerformed: false,
		}, exitBlocked)
	}
	catalog := migrations.NewCatalog()
	return resultExit(output, commandResult{
		SchemaVersion: 1, Command: "autoplan-migrate", Status: "ok",
		Code: "no_migrations_registered", Mode: options.mode(), TargetKind: options.kind,
		RegisteredMigrations: len(catalog.Entries()), WritePerformed: false,
	}, exitOK)
}

type migrateOptions struct {
	target string
	kind   string
	dryRun bool
	status bool
}

func (options migrateOptions) mode() string {
	if options.dryRun {
		return "dry_run"
	}
	return "status"
}

func parseMigrate(args []string) (migrateOptions, bool) {
	var options migrateOptions
	flags := flag.NewFlagSet("autoplan-migrate", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&options.target, "target", "", "explicit sanitized fixture, temporary target, or database copy")
	flags.StringVar(&options.kind, "target-kind", "", "fixture, temporary, or database-copy")
	flags.BoolVar(&options.dryRun, "dry-run", false, "show the registered migration plan without writing")
	flags.BoolVar(&options.status, "status", false, "show migration registration status without writing")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return migrateOptions{}, false
	}
	if options.target == "" || options.kind == "" || options.dryRun == options.status {
		return migrateOptions{}, false
	}
	return options, true
}
