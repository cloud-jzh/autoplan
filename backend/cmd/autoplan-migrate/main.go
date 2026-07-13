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

	applicationmigration "github.com/lyming99/autoplan/backend/internal/application/migration"
	"github.com/lyming99/autoplan/backend/internal/platform/repositoryroot"
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
	request, ok := parseArgs(args)
	if !ok {
		writeReport(output, applicationmigration.Report{
			SchemaVersion: 1, Status: "blocked", Code: "invalid_arguments",
		})
		return exitUsage
	}
	root, err := repositoryroot.Find()
	if err != nil {
		writeReport(output, applicationmigration.Report{
			SchemaVersion: 1, Command: request.Command, Status: "blocked", Code: "repository_root_unavailable",
		})
		return exitBlocked
	}
	report, err := applicationmigration.Execute(ctx, request, applicationmigration.Dependencies{
		RepositoryRoot: root, TemporaryRoot: os.TempDir(), DriverName: selectDriver(environ),
	})
	if writeReport(output, report) != nil {
		return exitFailure
	}
	if err == nil && report.Status == "ok" {
		return exitOK
	}
	if report.Code == "invalid_command" || report.Code == "invalid_arguments" {
		return exitUsage
	}
	if report.Status == "blocked" {
		return exitBlocked
	}
	return exitFailure
}

func parseArgs(args []string) (applicationmigration.Request, bool) {
	if len(args) == 0 {
		return applicationmigration.Request{}, false
	}
	command := applicationmigration.Command(args[0])
	switch command {
	case applicationmigration.CommandPreflight, applicationmigration.CommandDryRun,
		applicationmigration.CommandMigrate, applicationmigration.CommandVerify, applicationmigration.CommandRestore:
	default:
		return applicationmigration.Request{}, false
	}
	flags := flag.NewFlagSet(string(command), flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var request applicationmigration.Request
	request.Command = command
	flags.StringVar(&request.Database, "database", "", "explicit sanitized database copy")
	flags.StringVar(&request.AllowedRoot, "allow-root", "", "explicit authorized root")
	flags.StringVar(&request.BackupDirectory, "backup-dir", "", "pre-existing backup directory")
	flags.StringVar(&request.Manifest, "manifest", "", "explicit restore manifest")
	flags.BoolVar(&request.SanitizedCopy, "sanitized-copy", false, "declare an authorized sanitized copy")
	if err := flags.Parse(args[1:]); err != nil || flags.NArg() != 0 || request.Database == "" ||
		request.AllowedRoot == "" || request.BackupDirectory == "" || !request.SanitizedCopy {
		return applicationmigration.Request{}, false
	}
	if command == applicationmigration.CommandRestore && request.Manifest == "" {
		return applicationmigration.Request{}, false
	}
	if command != applicationmigration.CommandRestore && request.Manifest != "" {
		return applicationmigration.Request{}, false
	}
	return request, true
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

func writeReport(output io.Writer, report applicationmigration.Report) error {
	return json.NewEncoder(output).Encode(report)
}
