package executors

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"

	applicationoperations "github.com/lyming99/autoplan/backend/internal/application/operations"
	domainautomation "github.com/lyming99/autoplan/backend/internal/domain/automation"
	domainoperation "github.com/lyming99/autoplan/backend/internal/domain/operation"
	"github.com/lyming99/autoplan/backend/internal/repository"
	"github.com/lyming99/autoplan/backend/internal/runtime/process"
	"github.com/lyming99/autoplan/backend/internal/runtime/scheduler"
)

type runRequest struct {
	service *Service
	command RunCommand
	key     string
	digest  string
	reply   chan runReply

	operation  domainoperation.Operation
	executor   domainautomation.Executor
	context    *executionContext
	result     nodeResult
	active     bool
	submission *scheduler.Submission
}

type runReply struct {
	result Result
	err    error
}

func (service *Service) Run(ctx context.Context, command RunCommand) (Result, error) {
	if err := service.ready(ctx); err != nil {
		return Result{}, err
	}
	if !validCaller(command.Caller, command.ProjectID) || command.ExecutorID <= 0 || !validIdentity(command.RequestID, 64) || !validIdentity(command.IdempotencyKey, 128) {
		return Result{}, ErrInvalidCommand
	}
	key := executorKey{projectID: command.ProjectID, executorID: command.ExecutorID}
	operationKey := operationKey(command.ExecutorID, command.IdempotencyKey)
	digest := runDigest(OperationTypeRun, command.ProjectID, command.ExecutorID, "", command.IdempotencyKey)
	if existing, found := service.activeByIdentity(key, operationKey, digest); found {
		return Result{Operation: existing}, nil
	}
	request := &runRequest{service: service, command: command, key: operationKey, digest: digest, reply: make(chan runReply, 1)}
	submission, err := service.scheduler.Submit(context.Background(), command.ProjectID, scheduler.Command{
		Name: OperationTypeRun, Start: request.start, Work: request.work, Cancel: request.cancel, Complete: request.complete,
	})
	if err != nil {
		return Result{}, schedulerError(err)
	}
	request.submission = submission
	select {
	case reply := <-request.reply:
		return reply.result, reply.err
	case <-ctx.Done():
		submission.Cancel()
		return Result{}, ctx.Err()
	}
}

func (request *runRequest) start(ctx context.Context) error {
	if request == nil || request.service == nil {
		return ErrUnavailable
	}
	if request.service.hasActive(request.command.ProjectID, request.command.ExecutorID) {
		request.respond(Result{}, ErrBusy)
		return ErrBusy
	}
	project, executor, values, err := request.load(ctx)
	if err != nil {
		request.respond(Result{}, err)
		return err
	}
	if err := request.service.authorizeExecutor(ctx, project, executor); err != nil {
		request.respond(Result{}, err)
		return err
	}
	created, err := request.service.operations.CreateOrReuse(ctx, applicationoperations.CreateCommand{
		Caller: operationCaller(request.command), ProjectID: request.command.ProjectID, Type: OperationTypeRun,
		IdempotencyKey: request.key, RequestDigest: request.digest, RequestID: request.command.RequestID,
	})
	if err != nil {
		err = operationError(err)
		request.respond(Result{}, err)
		return err
	}
	request.operation = created.Operation
	if !created.Changed {
		request.respond(Result{Operation: created.Operation}, nil)
		return nil
	}
	claimed, err := request.service.operations.Claim(ctx, applicationoperations.ClaimCommand{
		Caller: operationCaller(request.command), ProjectID: request.command.ProjectID, OperationID: created.Operation.OperationID,
		ExpectedVersion: created.Operation.Version, RequestDigest: request.digest, RequestID: request.command.RequestID,
	})
	if err != nil {
		request.cancelCreated(ctx, created.Operation)
		err = operationError(err)
		request.respond(Result{}, err)
		return err
	}
	request.operation, request.executor, request.active = claimed.Operation, executor, true
	request.context = newExecutionContext(request.service, request.command.ProjectID, project.WorkspacePath, executor.ID, values)
	request.service.setActive(request)
	request.service.setRuntimeRunning(request.command.ProjectID, request.command.ExecutorID, executor.Type == "plugin", pluginActionForRun(executor))
	request.respond(Result{Operation: request.operation, Changed: true}, nil)
	return nil
}

func (request *runRequest) load(ctx context.Context) (repository.Project, domainautomation.Executor, []domainautomation.Executor, error) {
	project, found, err := request.service.store.GetProject(ctx, request.command.ProjectID)
	if err != nil {
		return repository.Project{}, domainautomation.Executor{}, nil, err
	}
	if !found || project.ID != request.command.ProjectID || strings.TrimSpace(project.WorkspacePath) == "" {
		return repository.Project{}, domainautomation.Executor{}, nil, ErrNotFound
	}
	executor, found, err := request.service.store.GetExecutor(ctx, request.command.ProjectID, request.command.ExecutorID)
	if err != nil {
		return repository.Project{}, domainautomation.Executor{}, nil, err
	}
	if !found || executor.ProjectID != request.command.ProjectID || domainautomation.ValidateExecutorRecord(executor) != nil {
		return repository.Project{}, domainautomation.Executor{}, nil, ErrNotFound
	}
	if !executor.Enabled {
		return repository.Project{}, domainautomation.Executor{}, nil, ErrDisabled
	}
	values, err := request.service.listAllExecutors(ctx, request.command.ProjectID)
	if err != nil {
		return repository.Project{}, domainautomation.Executor{}, nil, err
	}
	return project, executor, values, nil
}

func (service *Service) authorizeExecutor(ctx context.Context, project repository.Project, executor domainautomation.Executor) error {
	if _, err := processSpec(ctx, service.files, project.ID, project.WorkspacePath, executor, nil); err != nil {
		return ErrInvalidCommand
	}
	if executor.Type == "plugin" {
		if _, err := parsePluginAction(executor, "start"); err != nil {
			return err
		}
	}
	return nil
}

func (request *runRequest) work(ctx context.Context) error {
	if request == nil || !request.active || request.context == nil {
		return nil
	}
	if request.executor.Type == "plugin" {
		action, err := parsePluginAction(request.executor, "start")
		if err != nil {
			request.result = nodeResult{ExecutorID: request.executor.ID, Label: request.executor.Label, Status: "bad", ExitCode: -1, Code: "ACTION_INVALID"}
			return err
		}
		request.result = request.runPlugin(ctx, action)
	} else {
		request.result = request.context.run(ctx, request.executor.ID, nil)
	}
	if request.result.ok() {
		return nil
	}
	if request.result.cancelled() {
		return context.Canceled
	}
	return errNodeFailed
}

func (request *runRequest) runPlugin(ctx context.Context, action *pluginAction) nodeResult {
	started := request.service.clock.Now()
	spec, err := processSpec(ctx, request.service.files, request.command.ProjectID, request.context.workspace, request.executor, action)
	if err != nil {
		return nodeResult{ExecutorID: request.executor.ID, Label: request.executor.Label, Status: "bad", ExitCode: -1, Code: "PROCESS_SPEC_INVALID"}
	}
	value, runErr := request.service.runner.Run(ctx, spec)
	duration := elapsedMilliseconds(started, value.EndedAt, request.service.clock.Now())
	if errors.Is(runErr, process.ErrCancelled) || errors.Is(runErr, context.Canceled) {
		request.service.setRuntimeTerminal(request.command.ProjectID, request.executor.ID, "stopped", value.ExitCode, duration, "stop")
		return nodeResult{ExecutorID: request.executor.ID, Label: request.executor.Label, Status: "stopped", ExitCode: value.ExitCode, DurationMS: duration, Code: "OPERATION_CANCELLED"}
	}
	if runErr != nil || value.ExitCode != 0 {
		code := "EXECUTOR_EXIT_NONZERO"
		if runErr != nil {
			code = string(process.ErrorCode(runErr))
		}
		request.service.setRuntimeTerminal(request.command.ProjectID, request.executor.ID, "bad", value.ExitCode, duration, "start")
		return nodeResult{ExecutorID: request.executor.ID, Label: request.executor.Label, Status: "bad", ExitCode: value.ExitCode, DurationMS: duration, Code: code}
	}
	request.service.setRuntimeTerminal(request.command.ProjectID, request.executor.ID, "ok", value.ExitCode, duration, "start")
	return nodeResult{ExecutorID: request.executor.ID, Label: request.executor.Label, Status: "ok", ExitCode: value.ExitCode, DurationMS: duration}
}

func (request *runRequest) cancel(context.Context) {
	// The Stop service records cancellation before scheduler cancellation reaches
	// this work. P11 runner cancellation owns process-tree termination.
}

func (request *runRequest) complete(ctx context.Context, work scheduler.WorkResult) error {
	if request == nil || request.service == nil || !request.active {
		return nil
	}
	active, found := request.service.activeFor(request.command.ProjectID, request.command.ExecutorID, request.operation.OperationID)
	if !found {
		return nil
	}
	defer request.service.clearActive(request.command.ProjectID, request.command.ExecutorID, request.operation.OperationID)
	if active.cancelled || work.Cancelled || errors.Is(work.Err, context.Canceled) || errors.Is(work.Err, process.ErrCancelled) {
		completed, err := request.service.operations.ConfirmCancel(ctx, applicationoperations.CancelCommand{
			Caller: operationCaller(request.command), ProjectID: request.command.ProjectID, OperationID: active.operation.OperationID,
			ExpectedVersion: active.operation.Version, RequestID: request.command.RequestID,
		})
		if err != nil {
			return operationError(err)
		}
		request.operation = completed.Operation
		return nil
	}
	if work.Err != nil {
		failed, err := request.service.operations.Fail(ctx, applicationoperations.FailCommand{
			Caller: operationCaller(request.command), ProjectID: request.command.ProjectID, OperationID: active.operation.OperationID,
			ExpectedVersion: active.operation.Version, RequestID: request.command.RequestID,
			Code: request.result.failureCode(), Summary: "Executor execution failed.",
		})
		if err != nil {
			return operationError(err)
		}
		request.operation = failed.Operation
		return work.Err
	}
	completed, err := request.service.operations.Succeed(ctx, applicationoperations.CompleteCommand{
		Caller: operationCaller(request.command), ProjectID: request.command.ProjectID, OperationID: active.operation.OperationID,
		ExpectedVersion: active.operation.Version, RequestID: request.command.RequestID,
	})
	if err != nil {
		return operationError(err)
	}
	request.operation = completed.Operation
	return nil
}

func (request *runRequest) cancelCreated(ctx context.Context, operation domainoperation.Operation) {
	_, _ = request.service.operations.ConfirmCancel(ctx, applicationoperations.CancelCommand{
		Caller: operationCaller(request.command), ProjectID: request.command.ProjectID, OperationID: operation.OperationID,
		ExpectedVersion: operation.Version, RequestID: request.command.RequestID,
	})
}

func (request *runRequest) respond(result Result, err error) {
	select {
	case request.reply <- runReply{result: result, err: err}:
	default:
	}
}

func (result nodeResult) failureCode() string {
	if result.Code != "" {
		return result.Code
	}
	return "EXECUTOR_RUN_FAILED"
}

var errNodeFailed = errors.New("executor node failed")

func operationKey(executorID int64, idempotencyKey string) string {
	sum := sha256.Sum256([]byte(strconv.FormatInt(executorID, 10) + "\x00" + idempotencyKey))
	return "executor-" + strconv.FormatInt(executorID, 10) + "-" + hex.EncodeToString(sum[:16])
}

func runDigest(operationType string, projectID, executorID int64, action, idempotencyKey string) string {
	sum := sha256.Sum256([]byte(operationType + "\x00" + strconv.FormatInt(projectID, 10) + "\x00" + strconv.FormatInt(executorID, 10) + "\x00" + action + "\x00" + idempotencyKey))
	return hex.EncodeToString(sum[:])
}

func pluginActionForRun(executor domainautomation.Executor) string {
	if executor.Type == "plugin" {
		return "start"
	}
	return ""
}

func (service *Service) hasActive(projectID, executorID int64) bool {
	service.mu.Lock()
	defer service.mu.Unlock()
	return service.active[executorKey{projectID: projectID, executorID: executorID}] != nil
}

func (service *Service) activeByIdentity(key executorKey, idempotencyKey, digest string) (domainoperation.Operation, bool) {
	service.mu.Lock()
	defer service.mu.Unlock()
	active := service.active[key]
	if active == nil || active.idempotencyKey != idempotencyKey || active.digest != digest {
		return domainoperation.Operation{}, false
	}
	return active.operation, true
}

func (service *Service) setActive(request *runRequest) {
	key := executorKey{projectID: request.command.ProjectID, executorID: request.command.ExecutorID}
	service.mu.Lock()
	service.active[key] = &activeRun{
		operation: request.operation, executor: request.executor, request: request,
		idempotencyKey: request.key, digest: request.digest, plugin: request.executor.Type == "plugin",
	}
	service.mu.Unlock()
}

func (service *Service) activeFor(projectID, executorID int64, operationID string) (*activeRun, bool) {
	service.mu.Lock()
	defer service.mu.Unlock()
	active := service.active[executorKey{projectID: projectID, executorID: executorID}]
	if active == nil || (operationID != "" && active.operation.OperationID != operationID) {
		return nil, false
	}
	copy := *active
	return &copy, true
}

func (service *Service) clearActive(projectID, executorID int64, operationID string) {
	service.mu.Lock()
	key := executorKey{projectID: projectID, executorID: executorID}
	if active := service.active[key]; active != nil && active.operation.OperationID == operationID {
		delete(service.active, key)
	}
	service.mu.Unlock()
}

func (service *Service) setRuntimeRunning(projectID, executorID int64, plugin bool, action string) {
	service.mu.Lock()
	if plugin && action == "" {
		action = "start"
	}
	service.last[executorKey{projectID: projectID, executorID: executorID}] = runtimeLast{
		status: "running", ranAt: service.clock.Now().UTC().Format(time.RFC3339Nano), pluginAction: action,
	}
	service.mu.Unlock()
}

func (service *Service) setRuntimeTerminal(projectID, executorID int64, status string, exitCode int, duration int64, action string) {
	service.mu.Lock()
	service.last[executorKey{projectID: projectID, executorID: executorID}] = runtimeLast{
		status: status, exitCode: int64(exitCode), durationMS: duration, hasMetrics: true,
		ranAt: service.clock.Now().UTC().Format(time.RFC3339Nano), pluginAction: action,
	}
	service.mu.Unlock()
}
