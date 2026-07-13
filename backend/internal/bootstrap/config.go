package bootstrap

import (
	"context"
	"io"
	"os"

	"github.com/lyming99/autoplan/backend/internal/config"
	"github.com/lyming99/autoplan/backend/internal/platform/prerequisite"
	"github.com/lyming99/autoplan/backend/internal/platform/repositoryroot"
)

// RunConfiguredServer validates configuration and prerequisite evidence before
// assembling any process-local dependency. P002 still refuses to start an HTTP
// runtime because that lifecycle belongs to later tasks.
func RunConfiguredServer(ctx context.Context, args, environ []string, output io.Writer) int {
	if len(args) != 0 {
		return resultExit(output, commandResult{
			SchemaVersion: 1, Command: "autoplan-server", Status: "blocked",
			Code: "invalid_arguments", WritePerformed: false,
		}, exitUsage)
	}
	root, err := repositoryroot.Find()
	if err != nil {
		return resultExit(output, commandResult{
			SchemaVersion: 1, Command: "autoplan-server", Status: "blocked",
			Code: "repository_root_unavailable", WritePerformed: false,
		}, exitBlocked)
	}
	configuration, err := config.Load(config.LoadOptions{
		RepositoryRoot: root,
		TemporaryRoot:  os.TempDir(),
		Environ:        environ,
	})
	if err != nil {
		return resultExit(output, commandResult{
			SchemaVersion: 1, Command: "autoplan-server", Status: "blocked",
			Code: "invalid_configuration", Reasons: []string{config.ErrorCode(err)}, WritePerformed: false,
		}, exitBlocked)
	}
	report := prerequisite.Check(root)
	if !report.OK {
		return resultExit(output, commandResult{
			SchemaVersion: 1, Command: "autoplan-server", Status: "blocked",
			Code: "prerequisite_gate_failed", Reasons: report.Reasons, WritePerformed: false,
		}, exitBlocked)
	}
	dependencies, err := AssembleDependencies(configuration, DependencyOverrides{})
	if err != nil {
		return resultExit(output, commandResult{
			SchemaVersion: 1, Command: "autoplan-server", Status: "blocked",
			Code: "dependency_assembly_failed", WritePerformed: false,
		}, exitBlocked)
	}
	defer dependencies.Close(ctx)
	return resultExit(output, commandResult{
		SchemaVersion: 1, Command: "autoplan-server", Status: "blocked",
		Code: "server_runtime_not_implemented", WritePerformed: false,
	}, exitBlocked)
}
