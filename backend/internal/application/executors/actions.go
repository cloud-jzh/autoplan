package executors

import (
	"context"
	"errors"

	applicationoperations "github.com/lyming99/autoplan/backend/internal/application/operations"
	domainautomation "github.com/lyming99/autoplan/backend/internal/domain/automation"
	domainoperation "github.com/lyming99/autoplan/backend/internal/domain/operation"
	"github.com/lyming99/autoplan/backend/internal/repository"
	"github.com/lyming99/autoplan/backend/internal/runtime/process"
	"github.com/lyming99/autoplan/backend/internal/runtime/scheduler"
)

type actionRequest struct {
	service *Service
	command ActionCommand
	plugin  *pluginAction
	key     string
	digest  string
	reply   chan actionReply

	operation domainoperation.Operation
	executor  domainautomation.Executor
	project   repository.Project
	result    process.Result
	noop      bool
}

type actionReply struct {
	result Result
	err    error
}

// RunAction admits only persisted Plugin lifecycle actions. It does not use a
// transport command, arguments, stdin text, environment, PID or plugin state.
func (service *Service) RunAction(ctx context.Context, command ActionCommand) (Result, error) {
	if err := service.ready(ctx); err != nil {
		return Result{}, err
	}
	if !validCaller(command.Caller, command.ProjectID) || command.ExecutorID <= 0 || !validIdentity(command.RequestID, 64) ||
		!validIdentity(command.IdempotencyKey, 128) || (command.Action != "start" && command.Action != "reload" && command.Action != "stop") {
		return Result{}, ErrInvalidCommand
	}
	if command.Action == "start" {
		executor, found, err := service.store.GetExecutor(ctx, command.ProjectID, command.ExecutorID)
		if err != nil {
			return Result{}, err
		}
		if !found || executor.ProjectID != command.ProjectID || !executor.Enabled || executor.Type != "plugin" {
			return Result{}, ErrActionInvalid
		}
		if _, err := parsePluginAction(executor, "start"); err != nil {
			return Result{}, err
		}
		return service.Run(ctx, RunCommand{
			Caller: command.Caller, ProjectID: command.ProjectID, ExecutorID: command.ExecutorID,
			RequestID: command.RequestID, IdempotencyKey: command.IdempotencyKey,
		})
	}
	key := actionKey{projectID: command.ProjectID, executorID: command.ExecutorID, action: command.Action}
	operationKey := operationKey(command.ExecutorID, command.IdempotencyKey)
	digest := runDigest(OperationTypeAction, command.ProjectID, command.ExecutorID, command.Action, command.IdempotencyKey)
	if existing, found := service.activeActionByIdentity(key, operationKey, digest); found {
		return Result{Operation: existing}, nil
	}
	request := &actionRequest{service: service, command: command, key: operationKey, digest: digest, reply: make(chan actionReply, 1)}
	submission, err := service.scheduler.Submit(context.Background(), command.ProjectID, scheduler.Command{
		Name: OperationTypeAction, Start: request.start, Work: request.work, Complete: request.complete,
	})
	if err != nil {
		return Result{}, schedulerError(err)
	}
	select {
	case reply := <-request.reply:
		return reply.result, reply.err
	case <-ctx.Done():
		submission.Cancel()
		return Result{}, ctx.Err()
	}
}

func (request *actionRequest) start(ctx context.Context) error {
	if request == nil || request.service == nil {
		return ErrUnavailable
	}
	key := actionKey{projectID: request.command.ProjectID, executorID: request.command.ExecutorID, action: request.command.Action}
	if request.service.hasActiveAction(key) {
		request.respond(Result{}, ErrBusy)
		return ErrBusy
	}
	project, executor, err := request.load(ctx)
	if err != nil {
		request.respond(Result{}, err)
		return err
	}
	active, found := request.service.activeFor(request.command.ProjectID, request.command.ExecutorID, "")
	if !found || !active.plugin {
		request.respond(Result{}, ErrStateConflict)
		return ErrStateConflict
	}
	action, err := parsePluginAction(executor, request.command.Action)
	if err != nil {
		request.respond(Result{}, err)
		return err
	}
	if action.Type != "command" {
		request.respond(Result{}, ErrActionUnsupported)
		return ErrActionUnsupported
	}
	if _, err := processSpec(ctx, request.service.files, project.ID, project.WorkspacePath, executor, action); err != nil {
		request.respond(Result{}, ErrInvalidCommand)
		return ErrInvalidCommand
	}
	created, err := request.service.operations.CreateOrReuse(ctx, applicationoperations.CreateCommand{
		Caller: actionCaller(request.command), ProjectID: request.command.ProjectID, Type: OperationTypeAction,
		IdempotencyKey: request.key, RequestDigest: request.digest, RequestID: request.command.RequestID,
	})
	if err != nil {
		err = operationError(err)
		request.respond(Result{}, err)
		return err
	}
	request.operation = created.Operation
	if !created.Changed {
		request.noop = true
		request.respond(Result{Operation: created.Operation}, nil)
		return nil
	}
	claimed, err := request.service.operations.Claim(ctx, applicationoperations.ClaimCommand{
		Caller: actionCaller(request.command), ProjectID: request.command.ProjectID, OperationID: created.Operation.OperationID,
		ExpectedVersion: created.Operation.Version, RequestDigest: request.digest, RequestID: request.command.RequestID,
	})
	if err != nil {
		request.cancelCreated(ctx, created.Operation)
		err = operationError(err)
		request.respond(Result{}, err)
		return err
	}
	request.operation, request.executor, request.project, request.plugin = claimed.Operation, executor, project, action
	request.service.setActiveAction(request)
	request.service.setRuntimeRunning(request.command.ProjectID, request.command.ExecutorID, true, request.command.Action)
	request.respond(Result{Operation: request.operation, Changed: true}, nil)
	return nil
}

func (request *actionRequest) load(ctx context.Context) (repository.Project, domainautomation.Executor, error) {
	project, found, err := request.service.store.GetProject(ctx, request.command.ProjectID)
	if err != nil {
		return repository.Project{}, domainautomation.Executor{}, err
	}
	if !found || project.ID != request.command.ProjectID {
		return repository.Project{}, domainautomation.Executor{}, ErrNotFound
	}
	executor, found, err := request.service.store.GetExecutor(ctx, request.command.ProjectID, request.command.ExecutorID)
	if err != nil {
		return repository.Project{}, domainautomation.Executor{}, err
	}
	if !found || executor.ProjectID != request.command.ProjectID || !executor.Enabled || executor.Type != "plugin" || domainautomation.ValidateExecutorRecord(executor) != nil {
		return repository.Project{}, domainautomation.Executor{}, ErrNotFound
	}
	return project, executor, nil
}

func (request *actionRequest) work(ctx context.Context) error {
	if request != nil && request.noop {
		return nil
	}
	if request == nil || request.plugin == nil {
		return ErrActionInvalid
	}
	spec, err := processSpec(ctx, request.service.files, request.command.ProjectID, request.project.WorkspacePath, request.executor, request.plugin)
	if err != nil {
		return err
	}
	result, runErr := request.service.runner.Run(ctx, spec)
	request.result = result
	if runErr != nil {
		return runErr
	}
	if result.ExitCode != 0 {
		return errNodeFailed
	}
	return nil
}

func (request *actionRequest) complete(ctx context.Context, work scheduler.WorkResult) error {
	if request == nil || request.service == nil || request.noop {
		return nil
	}
	defer request.service.clearActiveAction(actionKey{projectID: request.command.ProjectID, executorID: request.command.ExecutorID, action: request.command.Action}, request.operation.OperationID)
	duration := elapsedMilliseconds(request.result.StartedAt, request.result.EndedAt, request.service.clock.Now())
	if work.Cancelled || errors.Is(work.Err, context.Canceled) || errors.Is(work.Err, process.ErrCancelled) {
		completed, err := request.service.operations.ConfirmCancel(ctx, applicationoperations.CancelCommand{
			Caller: actionCaller(request.command), ProjectID: request.command.ProjectID, OperationID: request.operation.OperationID,
			ExpectedVersion: request.operation.Version, RequestID: request.command.RequestID,
		})
		if err != nil {
			return operationError(err)
		}
		request.service.setRuntimeTerminal(request.command.ProjectID, request.command.ExecutorID, "stopped", request.result.ExitCode, duration, request.command.Action)
		request.operation = completed.Operation
		if request.command.Action == "stop" {
			request.service.cancelPluginRoot(ctx, request.command.Caller, request.command.ProjectID, request.command.ExecutorID, request.command.RequestID)
		}
		return nil
	}
	if work.Err != nil {
		code := "EXECUTOR_ACTION_FAILED"
		if !errors.Is(work.Err, errNodeFailed) {
			code = string(process.ErrorCode(work.Err))
		}
		failed, err := request.service.operations.Fail(ctx, applicationoperations.FailCommand{
			Caller: actionCaller(request.command), ProjectID: request.command.ProjectID, OperationID: request.operation.OperationID,
			ExpectedVersion: request.operation.Version, RequestID: request.command.RequestID,
			Code: code, Summary: "Executor action failed.",
		})
		if err != nil {
			return operationError(err)
		}
		request.service.setRuntimeTerminal(request.command.ProjectID, request.command.ExecutorID, "bad", request.result.ExitCode, duration, request.command.Action)
		request.operation = failed.Operation
		if request.command.Action == "stop" {
			request.service.cancelPluginRoot(ctx, request.command.Caller, request.command.ProjectID, request.command.ExecutorID, request.command.RequestID)
		}
		return work.Err
	}
	completed, err := request.service.operations.Succeed(ctx, applicationoperations.CompleteCommand{
		Caller: actionCaller(request.command), ProjectID: request.command.ProjectID, OperationID: request.operation.OperationID,
		ExpectedVersion: request.operation.Version, RequestID: request.command.RequestID,
	})
	if err != nil {
		return operationError(err)
	}
	request.service.setRuntimeTerminal(request.command.ProjectID, request.command.ExecutorID, "running", request.result.ExitCode, duration, request.command.Action)
	request.operation = completed.Operation
	if request.command.Action == "stop" {
		request.service.cancelPluginRoot(ctx, request.command.Caller, request.command.ProjectID, request.command.ExecutorID, request.command.RequestID)
	}
	return nil
}

func (request *actionRequest) cancelCreated(ctx context.Context, operation domainoperation.Operation) {
	_, _ = request.service.operations.ConfirmCancel(ctx, applicationoperations.CancelCommand{
		Caller: actionCaller(request.command), ProjectID: request.command.ProjectID, OperationID: operation.OperationID,
		ExpectedVersion: operation.Version, RequestID: request.command.RequestID,
	})
}

func (request *actionRequest) respond(result Result, err error) {
	select {
	case request.reply <- actionReply{result: result, err: err}:
	default:
	}
}

func (service *Service) hasActiveAction(key actionKey) bool {
	service.mu.Lock()
	defer service.mu.Unlock()
	return service.actions[key] != nil
}

func (service *Service) activeActionByIdentity(key actionKey, idempotencyKey, digest string) (domainoperation.Operation, bool) {
	service.mu.Lock()
	defer service.mu.Unlock()
	request := service.actions[key]
	if request == nil || request.key != idempotencyKey || request.digest != digest {
		return domainoperation.Operation{}, false
	}
	return request.operation, true
}

func (service *Service) setActiveAction(request *actionRequest) {
	service.mu.Lock()
	service.actions[actionKey{projectID: request.command.ProjectID, executorID: request.command.ExecutorID, action: request.command.Action}] = request
	service.mu.Unlock()
}

func (service *Service) clearActiveAction(key actionKey, operationID string) {
	service.mu.Lock()
	if request := service.actions[key]; request != nil && request.operation.OperationID == operationID {
		delete(service.actions, key)
	}
	service.mu.Unlock()
}
