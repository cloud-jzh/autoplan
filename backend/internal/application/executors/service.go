// Package executors owns the project-scoped Executor runtime boundary. It
// reads persisted definitions, resolves the in-project dependency graph and
// invokes only the shared, policy-constrained process runner.
package executors

import (
	"context"
	"errors"
	"sync"
	"time"

	filesapp "github.com/lyming99/autoplan/backend/internal/application/files"
	applicationoperations "github.com/lyming99/autoplan/backend/internal/application/operations"
	domainautomation "github.com/lyming99/autoplan/backend/internal/domain/automation"
	domainfiles "github.com/lyming99/autoplan/backend/internal/domain/files"
	domainoperation "github.com/lyming99/autoplan/backend/internal/domain/operation"
	"github.com/lyming99/autoplan/backend/internal/repository"
	"github.com/lyming99/autoplan/backend/internal/runtime/process"
	"github.com/lyming99/autoplan/backend/internal/runtime/scheduler"
)

const (
	OperationTypeRun    = "executor.run"
	OperationTypeAction = "executor.action"
)

var (
	ErrUnavailable       = errors.New("executor application service unavailable")
	ErrInvalidCommand    = errors.New("executor command is invalid")
	ErrUnauthorized      = errors.New("executor caller is not authorized")
	ErrNotFound          = errors.New("executor was not found")
	ErrDisabled          = errors.New("executor is disabled")
	ErrBusy              = errors.New("executor is already running")
	ErrQueueFull         = errors.New("executor runtime queue is full")
	ErrStateConflict     = errors.New("executor runtime state conflicts")
	ErrActionInvalid     = errors.New("executor action is invalid")
	ErrActionUnsupported = errors.New("executor action is not supported by the shared runner")
	ErrDependencyMissing = errors.New("executor dependency is missing")
	ErrDependencyCycle   = errors.New("executor dependency contains a cycle")
	ErrDependencyFailed  = errors.New("executor dependency failed")
)

// Store permits only project-scoped Executor and Project reads. Durable run
// records, audit and outbox atomics remain a P005 persistence responsibility.
type Store interface {
	Check(context.Context) error
	GetProject(context.Context, int64) (repository.Project, bool, error)
	GetExecutor(context.Context, int64, int64) (domainautomation.Executor, bool, error)
	ListExecutors(context.Context, domainautomation.ListOptions) ([]domainautomation.Executor, error)
}

type Runner interface {
	Run(context.Context, process.Spec) (process.Result, error)
}

type FilePolicy interface {
	AuthorizeWorkingDirectory(context.Context, string, string) (domainfiles.Decision, error)
}

var _ FilePolicy = (*filesapp.Service)(nil)

type Clock interface{ Now() time.Time }

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now().UTC() }

type Dependencies struct {
	Store      Store
	Operations *applicationoperations.Service
	Scheduler  *scheduler.Manager
	Runner     Runner
	Files      FilePolicy
	Clock      Clock
}

type Caller struct {
	ID        string
	ProjectID int64
}

type RunCommand struct {
	Caller         Caller
	ProjectID      int64
	ExecutorID     int64
	RequestID      string
	IdempotencyKey string
}

type ActionCommand struct {
	Caller         Caller
	ProjectID      int64
	ExecutorID     int64
	Action         string
	RequestID      string
	IdempotencyKey string
}

type StopCommand struct {
	Caller     Caller
	ProjectID  int64
	ExecutorID int64
	RequestID  string
}

type Result struct {
	Operation domainoperation.Operation
	Changed   bool
}

type StopResult struct {
	Operation domainoperation.Operation
	Changed   bool
	Stopped   bool
}

// RuntimeSnapshot is the redacted in-memory overlay used only for the
// project/executor pair that this Go service registered itself.
type RuntimeSnapshot struct {
	Running         bool
	OperationID     string
	OperationStatus string
	LastStatus      *string
	LastExitCode    *int64
	LastDurationMS  *int64
	LastRunAt       *string
	PluginRunning   bool
	PluginAction    string
}

type Service struct {
	store      Store
	operations *applicationoperations.Service
	scheduler  *scheduler.Manager
	runner     Runner
	files      FilePolicy
	clock      Clock

	mu      sync.Mutex
	active  map[executorKey]*activeRun
	actions map[actionKey]*actionRequest
	last    map[executorKey]runtimeLast
	closed  bool
}

type executorKey struct {
	projectID  int64
	executorID int64
}

type activeRun struct {
	operation      domainoperation.Operation
	executor       domainautomation.Executor
	request        *runRequest
	cancelled      bool
	idempotencyKey string
	digest         string
	plugin         bool
}

type actionKey struct {
	projectID  int64
	executorID int64
	action     string
}

type runtimeLast struct {
	status       string
	exitCode     int64
	durationMS   int64
	ranAt        string
	hasMetrics   bool
	pluginAction string
}

func NewService(dependencies Dependencies) *Service {
	clock := dependencies.Clock
	if clock == nil {
		clock = systemClock{}
	}
	return &Service{
		store: dependencies.Store, operations: dependencies.Operations, scheduler: dependencies.Scheduler,
		runner: dependencies.Runner, files: dependencies.Files, clock: clock,
		active: make(map[executorKey]*activeRun), actions: make(map[actionKey]*actionRequest), last: make(map[executorKey]runtimeLast),
	}
}

func (service *Service) Configured() bool {
	if service == nil {
		return false
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	return !service.closed && service.store != nil && service.operations != nil && service.operations.Configured() &&
		service.scheduler != nil && service.runner != nil && service.files != nil && service.clock != nil
}

func (service *Service) BindOperations(operations *applicationoperations.Service) {
	if service == nil {
		return
	}
	service.mu.Lock()
	service.operations = operations
	service.mu.Unlock()
}

func (service *Service) ready(ctx context.Context) error {
	if ctx == nil {
		return ErrInvalidCommand
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !service.Configured() {
		return ErrUnavailable
	}
	return service.store.Check(ctx)
}

func (service *Service) Close() {
	if service == nil {
		return
	}
	service.mu.Lock()
	service.closed = true
	service.mu.Unlock()
}

func (service *Service) ExecutorRuntime(projectID, executorID int64) (RuntimeSnapshot, bool) {
	if service == nil || projectID <= 0 || executorID <= 0 {
		return RuntimeSnapshot{}, false
	}
	key := executorKey{projectID: projectID, executorID: executorID}
	service.mu.Lock()
	defer service.mu.Unlock()
	active := service.active[key]
	last, hasLast := service.last[key]
	if active == nil && !hasLast {
		return RuntimeSnapshot{}, false
	}
	result := RuntimeSnapshot{}
	if active != nil {
		result.Running, result.OperationID, result.OperationStatus = true, active.operation.OperationID, string(active.operation.Status)
		result.PluginRunning = active.plugin
	}
	if hasLast {
		status, ranAt := last.status, last.ranAt
		result.LastStatus, result.LastRunAt, result.PluginAction = &status, &ranAt, last.pluginAction
		if last.hasMetrics {
			exitCode, duration := last.exitCode, last.durationMS
			result.LastExitCode, result.LastDurationMS = &exitCode, &duration
		}
	}
	return result, true
}

// OperationExecutor establishes ownership for executor.run and
// executor.action. Restart recovery deliberately refuses to launch either:
// only a live registration can prove ownership of the process tree.
type OperationExecutor struct {
	service       *Service
	operationType string
}

func NewOperationExecutor(service *Service, operationType string) *OperationExecutor {
	return &OperationExecutor{service: service, operationType: operationType}
}

func (executor *OperationExecutor) Type() string { return executor.operationType }

func (executor *OperationExecutor) CanRecover(ctx context.Context, operation domainoperation.Operation) (bool, error) {
	if executor == nil || executor.service == nil || operation.Type != executor.operationType {
		return false, nil
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	return false, nil
}

func (executor *OperationExecutor) ExecuteRecovered(context.Context, domainoperation.Operation) error {
	return ErrUnavailable
}

var _ applicationoperations.Executor = (*OperationExecutor)(nil)
