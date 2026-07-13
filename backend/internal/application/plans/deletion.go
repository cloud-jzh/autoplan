package plans

import (
	"context"
	"fmt"

	domainplan "github.com/lyming99/autoplan/backend/internal/domain/plan"
	domainproject "github.com/lyming99/autoplan/backend/internal/domain/project"
	"github.com/lyming99/autoplan/backend/internal/repository"
)

const RouteDelete = "plans:delete"

type DeleteCommand struct {
	ProjectID         int64
	PlanID            int64
	ExpectedUpdatedAt string
	RequestID         string
}

// Delete removes only persistence metadata. It intentionally never resolves
// SourceRef as a local path, stops a process, or deletes a workspace file.
func (service *Service) Delete(
	ctx context.Context,
	command DeleteCommand,
	visibility domainproject.Visibility,
) (MutationResult, error) {
	if err := service.ready(ctx); err != nil {
		return MutationResult{}, err
	}
	if command.ProjectID <= 0 || command.PlanID <= 0 || !domainplan.ValidUTCTimestamp(command.ExpectedUpdatedAt) {
		return MutationResult{}, ErrInvalidCommand
	}
	var deleted domainplan.DeleteResult
	var updatedAt string
	err := service.writer.TransactPlans(ctx, func(transaction repository.PlanWriteTransaction) error {
		current, found, err := transaction.GetPlan(ctx, command.ProjectID, command.PlanID)
		if err != nil {
			return err
		}
		if !found {
			return repository.ErrNotFound
		}
		if current.UpdatedAt != command.ExpectedUpdatedAt {
			return repository.ErrVersionConflict
		}
		updatedAt = nextMutationTimestamp(service.clock.Now(), []string{current.UpdatedAt})
		deleted, err = transaction.DeletePlanAggregate(ctx, domainplan.Delete{
			ProjectID: command.ProjectID, PlanID: command.PlanID,
			ExpectedUpdatedAt: current.UpdatedAt, UpdatedAt: updatedAt,
		})
		if err != nil {
			return err
		}
		return appendPlanEvent(ctx, transaction, RouteDelete, command.RequestID, command.ProjectID,
			TargetPlan, command.PlanID, "plan.deleted", fmt.Sprintf("plan #%d deleted", command.PlanID),
			map[string]any{
				"plan_id": command.PlanID, "deleted_tasks": deleted.DeletedTaskCount,
				"deleted_scans": deleted.DeletedScanCount, "linked_intake_count": len(deleted.LinkedIntakes),
				"keep_intakes": true,
			}, updatedAt)
	})
	if err != nil {
		return MutationResult{}, mapMutationError(err)
	}
	snapshot, err := service.snapshot(ctx, command.ProjectID, visibility)
	if err != nil {
		return MutationResult{}, err
	}
	return MutationResult{Snapshot: snapshot}, nil
}
