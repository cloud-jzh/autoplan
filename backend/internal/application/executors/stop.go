package executors

import (
	"context"

	applicationoperations "github.com/lyming99/autoplan/backend/internal/application/operations"
	domainautomation "github.com/lyming99/autoplan/backend/internal/domain/automation"
	domainoperation "github.com/lyming99/autoplan/backend/internal/domain/operation"
	"github.com/lyming99/autoplan/backend/internal/runtime/scheduler"
)

type stopRequest struct {
	service *Service
	command StopCommand
	reply   chan stopReply
}

type stopReply struct {
	result StopResult
	err    error
}

// Stop only acts on a current Go-owned registration for the same project and
// Executor. A legacy Node process, a terminal snapshot, a client PID, or an
// operation ID supplied by a client cannot be adopted or terminated here.
func (service *Service) Stop(ctx context.Context, command StopCommand) (StopResult, error) {
	if err := service.ready(ctx); err != nil {
		return StopResult{}, err
	}
	if !validCaller(command.Caller, command.ProjectID) || command.ExecutorID <= 0 || !validIdentity(command.RequestID, 64) {
		return StopResult{}, ErrInvalidCommand
	}
	executor, found, err := service.store.GetExecutor(ctx, command.ProjectID, command.ExecutorID)
	if err != nil {
		return StopResult{}, err
	}
	if !found || executor.ProjectID != command.ProjectID || domainautomation.ValidateExecutorRecord(executor) != nil {
		return StopResult{}, ErrNotFound
	}
	if executor.Type == "plugin" {
		if _, active := service.activeFor(command.ProjectID, command.ExecutorID, ""); !active {
			return StopResult{Stopped: false}, nil
		}
		if action, actionErr := parsePluginAction(executor, "stop"); actionErr == nil && action != nil {
			result, runErr := service.RunAction(ctx, ActionCommand{
				Caller: command.Caller, ProjectID: command.ProjectID, ExecutorID: command.ExecutorID, Action: "stop",
				RequestID: command.RequestID, IdempotencyKey: stopActionKey(command.RequestID),
			})
			if runErr != nil {
				return StopResult{}, runErr
			}
			return StopResult{Operation: result.Operation, Changed: result.Changed, Stopped: true}, nil
		}
	}
	request := &stopRequest{service: service, command: command, reply: make(chan stopReply, 1)}
	submission, err := service.scheduler.Submit(context.Background(), command.ProjectID, scheduler.Command{Name: "executor.stop", Start: request.start})
	if err != nil {
		return StopResult{}, schedulerError(err)
	}
	select {
	case reply := <-request.reply:
		return reply.result, reply.err
	case <-ctx.Done():
		submission.Cancel()
		return StopResult{}, ctx.Err()
	}
}

func (request *stopRequest) start(ctx context.Context) error {
	if request == nil || request.service == nil {
		return ErrUnavailable
	}
	active, found := request.service.activeFor(request.command.ProjectID, request.command.ExecutorID, "")
	if !found {
		request.respond(StopResult{Stopped: false}, nil)
		return nil
	}
	cancelled, err := request.service.operations.RequestCancel(ctx, applicationoperations.CancelCommand{
		Caller:    applicationoperations.Caller{ID: request.command.Caller.ID, ProjectID: request.command.ProjectID},
		ProjectID: request.command.ProjectID, OperationID: active.operation.OperationID,
		ExpectedVersion: active.operation.Version, RequestID: request.command.RequestID,
	})
	if err != nil {
		err = operationError(err)
		request.respond(StopResult{}, err)
		return err
	}
	if cancelled.Changed {
		request.service.markCancelled(request.command.ProjectID, request.command.ExecutorID, cancelled.Operation)
	}
	request.respond(StopResult{Operation: cancelled.Operation, Changed: cancelled.Changed, Stopped: cancelled.Changed}, nil)
	return nil
}

func (request *stopRequest) respond(result StopResult, err error) {
	select {
	case request.reply <- stopReply{result: result, err: err}:
	default:
	}
}

func (service *Service) cancelPluginRoot(ctx context.Context, caller Caller, projectID, executorID int64, requestID string) {
	active, found := service.activeFor(projectID, executorID, "")
	if !found || !active.plugin {
		return
	}
	cancelled, err := service.operations.RequestCancel(ctx, applicationoperations.CancelCommand{
		Caller: applicationoperations.Caller{ID: caller.ID, ProjectID: projectID}, ProjectID: projectID,
		OperationID: active.operation.OperationID, ExpectedVersion: active.operation.Version, RequestID: requestID,
	})
	if err == nil && cancelled.Changed {
		service.markCancelled(projectID, executorID, cancelled.Operation)
	}
}

func (service *Service) markCancelled(projectID, executorID int64, operation domainoperation.Operation) {
	service.mu.Lock()
	active := service.active[executorKey{projectID: projectID, executorID: executorID}]
	if active != nil && active.operation.OperationID == operation.OperationID {
		active.cancelled = true
		active.operation = operation
		if active.request != nil {
			active.request.operation = operation
			if active.request.submission != nil {
				active.request.submission.Cancel()
			}
		}
	}
	service.mu.Unlock()
}

func stopActionKey(requestID string) string { return "stop-" + requestID }
