package plans

import (
	"context"

	"github.com/lyming99/autoplan/backend/internal/application/capabilities"
)

// ActionTarget identifies a plan without carrying file, workspace, command,
// session, or provider data.
type ActionTarget struct {
	ProjectID int64 `json:"project_id"`
	PlanID    int64 `json:"plan_id"`
}

// ActionPreconditions are part of the v1 request contract. They are not read
// while an action is disabled, preventing resource-state disclosure.
type ActionPreconditions struct {
	ExpectedUpdatedAt string `json:"expected_updated_at"`
}

// ActionRequest is the common versioned Plan action contract. IdempotencyKey
// is supplied by the transport header and therefore is intentionally omitted
// from the JSON body.
type ActionRequest struct {
	Version        string              `json:"version"`
	Target         ActionTarget        `json:"target"`
	Preconditions  ActionPreconditions `json:"preconditions"`
	IdempotencyKey string              `json:"-"`
}

type RunRequest ActionRequest
type StopRequest ActionRequest
type ResumeRequest ActionRequest
type ReexecuteRequest ActionRequest
type RecreateRequest ActionRequest

// ActionAccepted is the only future success payload. P003 always returns it
// empty together with DisabledActionError and no caller may serialize it as a
// successful response.
type ActionAccepted struct {
	Operation capabilities.OperationReference `json:"operation"`
}

// ActionService is deliberately dependency-free. A later implementation must
// replace this boundary behind an explicit capability and migration gate; it
// cannot obtain a runtime or repository from this placeholder.
type ActionService struct{}

func NewActionService() *ActionService { return &ActionService{} }

func (service *ActionService) Run(context.Context, RunRequest) (ActionAccepted, error) {
	return ActionAccepted{}, capabilities.NewService().DisabledAction(capabilities.PlansRun)
}

func (service *ActionService) Stop(context.Context, StopRequest) (ActionAccepted, error) {
	return ActionAccepted{}, capabilities.NewService().DisabledAction(capabilities.PlansStop)
}

func (service *ActionService) Resume(context.Context, ResumeRequest) (ActionAccepted, error) {
	return ActionAccepted{}, capabilities.NewService().DisabledAction(capabilities.PlansResume)
}

func (service *ActionService) Reexecute(context.Context, ReexecuteRequest) (ActionAccepted, error) {
	return ActionAccepted{}, capabilities.NewService().DisabledAction(capabilities.PlansReexecute)
}

func (service *ActionService) Recreate(context.Context, RecreateRequest) (ActionAccepted, error) {
	return ActionAccepted{}, capabilities.NewService().DisabledAction(capabilities.PlansRecreate)
}
