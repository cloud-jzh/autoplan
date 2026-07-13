package tasks

import (
	"context"

	applicationloop "github.com/lyming99/autoplan/backend/internal/application/loop"
)

// RuntimeHandler validates task operations before handing them to the shared
// runtime dispatcher. Batch membership is bounded and de-duplicated by the
// common command validator.
type RuntimeHandler struct {
	dispatcher applicationloop.Dispatcher
}

func NewRuntimeHandler(dispatcher applicationloop.Dispatcher) *RuntimeHandler {
	return &RuntimeHandler{dispatcher: dispatcher}
}

func (handler *RuntimeHandler) Commands() []applicationloop.CommandKind {
	return []applicationloop.CommandKind{
		applicationloop.CommandTaskRun,
		applicationloop.CommandTaskRunBatches,
		applicationloop.CommandTaskStop,
	}
}

func (handler *RuntimeHandler) Execute(ctx context.Context, command applicationloop.Command) (applicationloop.Result, error) {
	switch command.Kind {
	case applicationloop.CommandTaskRun, applicationloop.CommandTaskStop:
		if err := applicationloop.RequireTask(command); err != nil {
			return applicationloop.Result{}, err
		}
	case applicationloop.CommandTaskRunBatches:
		if command.PlanID <= 0 || len(command.Batches) == 0 || command.TaskID != 0 {
			return applicationloop.Result{}, applicationloop.ErrInvalidCommand
		}
	default:
		return applicationloop.Result{}, applicationloop.ErrUnsupportedCommand
	}
	return applicationloop.Dispatch(ctx, handler.dispatcher, command)
}
