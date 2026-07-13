package plans

import (
	"context"

	applicationloop "github.com/lyming99/autoplan/backend/internal/application/loop"
)

// RuntimeHandler owns the plan-generation, parsing, validation and execution
// command family. It never receives a repository or a process implementation;
// the supervised runtime dispatcher owns those side effects.
type RuntimeHandler struct {
	dispatcher applicationloop.Dispatcher
}

func NewRuntimeHandler(dispatcher applicationloop.Dispatcher) *RuntimeHandler {
	return &RuntimeHandler{dispatcher: dispatcher}
}

func (handler *RuntimeHandler) Commands() []applicationloop.CommandKind {
	return []applicationloop.CommandKind{
		applicationloop.CommandPlanGenerate,
		applicationloop.CommandPlanParse,
		applicationloop.CommandPlanRun,
		applicationloop.CommandPlanStop,
		applicationloop.CommandPlanResume,
		applicationloop.CommandPlanReexecute,
		applicationloop.CommandPlanRecreate,
		applicationloop.CommandPlanValidate,
	}
}

func (handler *RuntimeHandler) Execute(ctx context.Context, command applicationloop.Command) (applicationloop.Result, error) {
	switch command.Kind {
	case applicationloop.CommandPlanGenerate:
		if command.IntakeID <= 0 {
			return applicationloop.Result{}, applicationloop.ErrInvalidCommand
		}
	case applicationloop.CommandPlanParse, applicationloop.CommandPlanRun, applicationloop.CommandPlanStop,
		applicationloop.CommandPlanResume, applicationloop.CommandPlanReexecute, applicationloop.CommandPlanRecreate,
		applicationloop.CommandPlanValidate:
		if err := applicationloop.RequirePlan(command); err != nil {
			return applicationloop.Result{}, err
		}
	default:
		return applicationloop.Result{}, applicationloop.ErrUnsupportedCommand
	}
	return applicationloop.Dispatch(ctx, handler.dispatcher, command)
}
