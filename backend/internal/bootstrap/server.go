package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"

	"github.com/lyming99/autoplan/backend/internal/application"
	"github.com/lyming99/autoplan/backend/internal/config"
	"github.com/lyming99/autoplan/backend/internal/httpapi"
	"github.com/lyming99/autoplan/backend/internal/platform/instance"
	"github.com/lyming99/autoplan/backend/internal/platform/logging"
	"github.com/lyming99/autoplan/backend/internal/platform/prerequisite"
	"github.com/lyming99/autoplan/backend/internal/platform/repositoryroot"
	"github.com/lyming99/autoplan/backend/internal/runtime"
	"github.com/lyming99/autoplan/backend/internal/runtime/lifecycle"
	"github.com/lyming99/autoplan/backend/migrations"
)

const readinessProtocolVersion = 1

var readinessDependencies = []string{
	"configuration",
	"prerequisites",
	"migrations",
	"instance_lock",
	"application",
	"listener",
}

type readinessMessage struct {
	Version int    `json:"version"`
	Type    string `json:"type"`
	PID     int    `json:"pid"`
	Host    string `json:"host"`
	Port    int    `json:"port"`
	Ready   bool   `json:"ready"`
}

// RunServer preserves the original P001 entry for callers that do not need
// injected process streams. Production main uses RunServerCommand below.
func RunServer(ctx context.Context, args []string, output io.Writer) int {
	return RunServerCommand(ctx, args, os.Environ(), output, io.Discard)
}

// RunServerCommand starts the skeleton sidecar and emits exactly one stdout
// JSON message: either a pre-listen blocked result or the post-listen readiness
// protocol message. Runtime logs are fixed-schema JSON on stderr only.
func RunServerCommand(ctx context.Context, args, environ []string, stdout, stderr io.Writer) int {
	if len(args) != 0 {
		return blockedServer(stdout, "invalid_arguments", nil, exitUsage)
	}
	runContext, stopSignals := signal.NotifyContext(ctx, lifecycle.TerminationSignals()...)
	defer stopSignals()

	root, err := repositoryroot.Find()
	if err != nil {
		return blockedServer(stdout, "repository_root_unavailable", nil, exitBlocked)
	}
	configuration, err := config.Load(config.LoadOptions{
		RepositoryRoot: root,
		TemporaryRoot:  os.TempDir(),
		Environ:        environ,
	})
	if err != nil {
		return blockedServer(stdout, "invalid_configuration", []string{config.ErrorCode(err)}, exitBlocked)
	}
	readiness, err := NewReadiness(readinessDependencies...)
	if err != nil {
		return blockedServer(stdout, "readiness_initialization_failed", nil, exitFailure)
	}
	_ = readiness.MarkReady("configuration")

	prerequisiteReport := prerequisite.Check(root)
	if !prerequisiteReport.OK {
		_ = readiness.MarkFailed("prerequisites", "gate_failed")
		return blockedServer(stdout, "prerequisite_gate_failed", prerequisiteReport.Reasons, exitBlocked)
	}
	_ = readiness.MarkReady("prerequisites")
	if err := runContext.Err(); err != nil {
		return blockedServer(stdout, "startup_cancelled", nil, exitBlocked)
	}

	migrationKind, instanceKind, ok := startupTargetKinds(configuration.Runtime.TargetKind)
	if !ok {
		_ = readiness.MarkFailed("migrations", "runtime_target_required")
		return blockedServer(stdout, "runtime_target_not_allowed", nil, exitBlocked)
	}
	registry := migrations.NewRegistry(migrations.NewCatalog())
	migrationStatus := registry.Inspect(configuration.Runtime.Directory, migrationKind)
	if !migrationStatus.AllowedForStartup() {
		_ = readiness.MarkFailed("migrations", "migration_state_blocked")
		return blockedServer(stdout, "migration_state_blocked", nil, exitBlocked)
	}
	_ = readiness.MarkReady("migrations")

	clock := runtime.SystemClock{}
	logger, err := logging.NewJSONLogger(stderr, clock)
	if err != nil {
		return blockedServer(stdout, "logger_initialization_failed", nil, exitFailure)
	}
	manager, err := lifecycle.New(readiness, configuration.HTTP.ShutdownTimeout)
	if err != nil {
		return blockedServer(stdout, "lifecycle_initialization_failed", nil, exitFailure)
	}
	instanceLock, err := instance.Acquire(instance.Options{
		Target: configuration.Runtime.Directory, Kind: instanceKind, TemporaryRoot: os.TempDir(),
	})
	if err != nil {
		_ = readiness.MarkFailed("instance_lock", instanceReason(err))
		return blockedServer(stdout, "instance_lock_failed", []string{instanceReason(err)}, exitBlocked)
	}
	if err := manager.Add(instanceLock); err != nil {
		_ = instanceLock.Close(context.Background())
		return blockedServer(stdout, "lifecycle_registration_failed", nil, exitFailure)
	}
	_ = readiness.MarkReady("instance_lock")

	applicationGate, err := readiness.Gate("application", "listener")
	if err != nil {
		return blockAfterCleanup(manager, stdout, "readiness_initialization_failed", exitFailure)
	}
	dependencies, err := AssembleDependencies(configuration, DependencyOverrides{
		Clock: clock, Readiness: applicationGate, Repository: skeletonRepository{},
		Logger: applicationLogger{logger: logger, clock: clock},
	})
	if err != nil {
		_ = readiness.MarkFailed("application", "dependency_assembly_failed")
		return blockAfterCleanup(manager, stdout, "dependency_assembly_failed", exitBlocked)
	}
	if err := manager.Add(dependencies); err != nil {
		_ = dependencies.Close(context.Background())
		return blockAfterCleanup(manager, stdout, "lifecycle_registration_failed", exitFailure)
	}
	if err := dependencies.Services.Ready(runContext); err != nil {
		_ = readiness.MarkFailed("application", "application_not_ready")
		return blockAfterCleanup(manager, stdout, "application_not_ready", exitBlocked)
	}
	_ = readiness.MarkReady("application")

	router, err := httpapi.NewRouter(httpapi.RouterOptions{
		Application:    dependencies.Application,
		Logger:         logger,
		Clock:          clock,
		BodyLimitBytes: configuration.HTTP.BodyLimitBytes,
	})
	if err != nil {
		return blockAfterCleanup(manager, stdout, "router_initialization_failed", exitFailure)
	}
	if err := httpapi.RegisterProbes(router, readiness); err != nil {
		return blockAfterCleanup(manager, stdout, "probe_registration_failed", exitFailure)
	}
	httpServer, err := configuration.HTTP.NewServer(router)
	if err != nil {
		return blockAfterCleanup(manager, stdout, "http_server_configuration_failed", exitFailure)
	}
	httpServer.ErrorLog = logging.StandardLogger(logger, clock)
	if err := manager.Add(lifecycle.CloserFunc(httpServer.Shutdown)); err != nil {
		return blockAfterCleanup(manager, stdout, "lifecycle_registration_failed", exitFailure)
	}
	if err := runContext.Err(); err != nil {
		return blockAfterCleanup(manager, stdout, "startup_cancelled", exitBlocked)
	}

	listener, err := net.Listen("tcp", configuration.HTTP.Address())
	if err != nil {
		_ = readiness.MarkFailed("listener", "listen_failed")
		return blockAfterCleanup(manager, stdout, "listen_failed", exitBlocked)
	}
	_ = readiness.MarkReady("listener")
	address, ok := listener.Addr().(*net.TCPAddr)
	if !ok || address.Port <= 0 || address.IP == nil ||
		!address.IP.Equal(net.ParseIP(config.DefaultListenHost)) || !readiness.Ready() {
		_ = listener.Close()
		_ = readiness.MarkFailed("listener", "listen_invalid")
		return blockAfterCleanup(manager, stdout, "listen_failed", exitFailure)
	}
	if err := runContext.Err(); err != nil {
		_ = listener.Close()
		return blockAfterCleanup(manager, stdout, "startup_cancelled", exitBlocked)
	}

	serveResult := make(chan error, 1)
	go func() {
		serveResult <- httpServer.Serve(listener)
	}()
	select {
	case serveErr := <-serveResult:
		_ = readiness.MarkFailed("listener", "serve_failed")
		_ = manager.Shutdown()
		if errors.Is(serveErr, http.ErrServerClosed) {
			return blockedServer(stdout, "server_closed_before_ready", nil, exitFailure)
		}
		return blockedServer(stdout, "serve_failed", nil, exitFailure)
	default:
	}
	if err := runContext.Err(); err != nil {
		_ = listener.Close()
		return blockAfterCleanup(manager, stdout, "startup_cancelled", exitBlocked)
	}

	message := readinessMessage{
		Version: readinessProtocolVersion,
		Type:    "autoplan_server_ready",
		PID:     os.Getpid(),
		Host:    configuration.HTTP.ListenHost,
		Port:    address.Port,
		Ready:   readiness.Ready(),
	}
	if err := json.NewEncoder(stdout).Encode(message); err != nil {
		_ = manager.Shutdown()
		return exitFailure
	}

	unexpectedServeFailure := false
	select {
	case <-runContext.Done():
	case <-serveResult:
		unexpectedServeFailure = true
		_ = readiness.MarkFailed("listener", "serve_failed")
	}
	shutdownErr := manager.Shutdown()
	if unexpectedServeFailure {
		safeInfrastructureLog(logger, clock, "serve_failed")
		return exitFailure
	}
	if shutdownErr != nil {
		safeInfrastructureLog(logger, clock, "shutdown_incomplete")
		return exitFailure
	}
	return exitOK
}

func startupTargetKinds(kind config.RuntimeTargetKind) (migrations.TargetKind, instance.TargetKind, bool) {
	switch kind {
	case config.RuntimeTargetTemporary:
		return migrations.TargetTemporary, instance.TargetTemporary, true
	case config.RuntimeTargetDatabaseCopy:
		return migrations.TargetDatabaseCopy, instance.TargetDatabaseCopy, true
	default:
		return "", "", false
	}
}

func instanceReason(err error) string {
	if errors.Is(err, instance.ErrAlreadyRunning) {
		return "instance_lock_conflict"
	}
	if errors.Is(err, instance.ErrUnsafeTarget) {
		return "instance_target_unsafe"
	}
	return "instance_lock_unavailable"
}

func blockAfterCleanup(manager *lifecycle.Manager, output io.Writer, code string, desired int) int {
	reasons := []string(nil)
	if manager.Shutdown() != nil {
		reasons = append(reasons, "cleanup_incomplete")
		desired = exitFailure
	}
	return blockedServer(output, code, reasons, desired)
}

func blockedServer(output io.Writer, code string, reasons []string, desired int) int {
	return resultExit(output, commandResult{
		SchemaVersion:  1,
		Command:        "autoplan-server",
		Status:         "blocked",
		Code:           code,
		Reasons:        reasons,
		WritePerformed: false,
	}, desired)
}

func safeInfrastructureLog(logger logging.Logger, clock logging.Clock, code string) {
	defer func() { _ = recover() }()
	_ = logger.Log(logging.Event{
		OccurredAt: clock.Now(), Level: "error", Code: code, Retryable: false,
	})
}

type skeletonRepository struct{}

func (skeletonRepository) Check(ctx context.Context) error { return ctx.Err() }

type applicationLogger struct {
	logger logging.Logger
	clock  logging.Clock
}

func (adapter applicationLogger) Log(ctx context.Context, entry application.LogEntry) {
	defer func() { _ = recover() }()
	_ = adapter.logger.Log(logging.Event{
		OccurredAt: adapter.clock.Now(), Level: entry.Level, Code: entry.Code,
		RequestID: httpapi.RequestID(ctx), Retryable: false,
	})
}

func resultExit(output io.Writer, result commandResult, desired int) int {
	if writeResult(output, result) != exitOK {
		return exitFailure
	}
	return desired
}
