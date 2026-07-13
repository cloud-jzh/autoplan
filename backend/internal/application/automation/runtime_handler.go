package automation

import (
	"context"
	"strings"

	applicationloop "github.com/lyming99/autoplan/backend/internal/application/loop"
)

// RuntimeHandler is separate from static automation persistence. The only
// runtime inputs are an owned Script/Executor identifier and a bounded action
// name; executable details remain in the owning runtime and never cross this
// command boundary.
type RuntimeHandler struct {
	dispatcher applicationloop.Dispatcher
}

func NewRuntimeHandler(dispatcher applicationloop.Dispatcher) *RuntimeHandler {
	return &RuntimeHandler{dispatcher: dispatcher}
}

func (handler *RuntimeHandler) Commands() []applicationloop.CommandKind {
	return []applicationloop.CommandKind{
		applicationloop.CommandScriptRun,
		applicationloop.CommandScriptStop,
		applicationloop.CommandExecutorRun,
		applicationloop.CommandExecutorStop,
		applicationloop.CommandExecutorAction,
	}
}

func (handler *RuntimeHandler) Execute(ctx context.Context, command applicationloop.Command) (applicationloop.Result, error) {
	switch command.Kind {
	case applicationloop.CommandScriptRun, applicationloop.CommandScriptStop:
		if err := applicationloop.RequireScript(command); err != nil {
			return applicationloop.Result{}, err
		}
	case applicationloop.CommandExecutorRun, applicationloop.CommandExecutorStop:
		if err := applicationloop.RequireExecutor(command); err != nil {
			return applicationloop.Result{}, err
		}
	case applicationloop.CommandExecutorAction:
		if err := applicationloop.RequireExecutor(command); err != nil || strings.TrimSpace(command.Action) == "" {
			return applicationloop.Result{}, applicationloop.ErrInvalidCommand
		}
	default:
		return applicationloop.Result{}, applicationloop.ErrUnsupportedCommand
	}
	return applicationloop.Dispatch(ctx, handler.dispatcher, command)
}
