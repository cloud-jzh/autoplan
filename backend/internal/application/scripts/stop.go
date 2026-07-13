package scripts

import (
	"context"

	applicationoperations "github.com/lyming99/autoplan/backend/internal/application/operations"
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

// Stop only addresses the in-memory registration created by this Go service.
// It cannot discover, adopt or signal a PID, a client-supplied operation ID,
// a snapshot entry, or a legacy Node runtime process.
func (service *Service) Stop(ctx context.Context, command StopCommand) (StopResult, error) {
	if err := service.ready(ctx); err != nil {
		return StopResult{}, err
	}
	if !validCaller(command.Caller, command.ProjectID) || command.ScriptID <= 0 || !validIdentity(command.RequestID, 64) {
		return StopResult{}, ErrInvalidCommand
	}
	request := &stopRequest{service: service, command: command, reply: make(chan stopReply, 1)}
	submission, err := service.scheduler.Submit(context.Background(), command.ProjectID, scheduler.Command{
		Name: "script.stop", Start: request.start,
	})
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
	active, found := request.service.activeFor(request.command.ProjectID, request.command.ScriptID, "")
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
		request.respond(StopResult{}, operationError(err))
		return operationError(err)
	}
	if cancelled.Changed {
		request.service.markCancelled(request.command.ProjectID, request.command.ScriptID, cancelled.Operation)
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

func (service *Service) markCancelled(projectID, scriptID int64, operation domainoperation.Operation) {
	service.mu.Lock()
	active := service.active[scriptKey{projectID: projectID, scriptID: scriptID}]
	if active != nil && active.operation.OperationID == operation.OperationID {
		active.cancelled = true
		active.operation = operation
		if active.request != nil {
			active.request.operation = operation
		}
		if active.request != nil && active.request.submission != nil {
			active.request.submission.Cancel()
		}
	}
	service.mu.Unlock()
}
