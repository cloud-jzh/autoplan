// Package tasks retains the legacy disabled PlanTask action boundary. P002
// adds RuntimeHandler for the shared runtime command bridge.
package tasks

import (
	"context"

	"github.com/lyming99/autoplan/backend/internal/application/capabilities"
)

// ActionTarget identifies a task by its project and plan ownership scope. It
// intentionally carries no executable command, workspace reference, or
// process/session identifier.
type ActionTarget struct {
	ProjectID int64 `json:"project_id"`
	PlanID    int64 `json:"plan_id"`
	TaskID    int64 `json:"task_id"`
}

type ActionPreconditions struct {
	ExpectedTaskUpdatedAt string `json:"expected_task_updated_at"`
	ExpectedPlanUpdatedAt string `json:"expected_plan_updated_at"`
}

type ActionRequest struct {
	Version        string              `json:"version"`
	Target         ActionTarget        `json:"target"`
	Preconditions  ActionPreconditions `json:"preconditions"`
	IdempotencyKey string              `json:"-"`
}

type RunRequest ActionRequest
type StopRequest ActionRequest

type Batch struct {
	TaskIDs []int64 `json:"task_ids"`
}

type BatchActionTarget struct {
	ProjectID int64 `json:"project_id"`
	PlanID    int64 `json:"plan_id"`
}

type BatchActionPreconditions struct {
	ExpectedPlanUpdatedAt string `json:"expected_plan_updated_at"`
}

// RunBatchesRequest preserves the existing batch grouping contract while
// keeping the target and optimistic preconditions versioned.
type RunBatchesRequest struct {
	Version        string                   `json:"version"`
	Target         BatchActionTarget        `json:"target"`
	Preconditions  BatchActionPreconditions `json:"preconditions"`
	Batches        []Batch                  `json:"batches"`
	IdempotencyKey string                   `json:"-"`
}

type ActionAccepted struct {
	Operation capabilities.OperationReference `json:"operation"`
}

type ActionService struct{}

func NewActionService() *ActionService { return &ActionService{} }

func (service *ActionService) Run(context.Context, RunRequest) (ActionAccepted, error) {
	return ActionAccepted{}, capabilities.NewService().DisabledAction(capabilities.TasksRun)
}

func (service *ActionService) RunBatches(context.Context, RunBatchesRequest) (ActionAccepted, error) {
	return ActionAccepted{}, capabilities.NewService().DisabledAction(capabilities.TasksRunBatches)
}

func (service *ActionService) Stop(context.Context, StopRequest) (ActionAccepted, error) {
	return ActionAccepted{}, capabilities.NewService().DisabledAction(capabilities.TasksStop)
}
