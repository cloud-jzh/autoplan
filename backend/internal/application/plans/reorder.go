package plans

import (
	"context"
	"fmt"

	domainplan "github.com/lyming99/autoplan/backend/internal/domain/plan"
	domainproject "github.com/lyming99/autoplan/backend/internal/domain/project"
	"github.com/lyming99/autoplan/backend/internal/repository"
)

const RouteReorder = "plans:reorder"

type ReorderCommand struct {
	ProjectID         int64
	PlanIDs           []int64
	ExpectedUpdatedAt map[int64]string
	RequestID         string
}

func (service *Service) Reorder(
	ctx context.Context,
	command ReorderCommand,
	visibility domainproject.Visibility,
) (MutationResult, error) {
	if err := service.ready(ctx); err != nil {
		return MutationResult{}, err
	}
	if command.ProjectID <= 0 || len(command.PlanIDs) == 0 || len(command.PlanIDs) != len(command.ExpectedUpdatedAt) {
		return MutationResult{}, ErrInvalidCommand
	}
	seen := make(map[int64]struct{}, len(command.PlanIDs))
	for _, id := range command.PlanIDs {
		if id <= 0 || !domainplan.ValidUTCTimestamp(command.ExpectedUpdatedAt[id]) {
			return MutationResult{}, ErrInvalidCommand
		}
		if _, exists := seen[id]; exists {
			return MutationResult{}, ErrInvalidCommand
		}
		seen[id] = struct{}{}
	}
	var updatedAt string
	err := service.writer.TransactPlans(ctx, func(transaction repository.PlanWriteTransaction) error {
		graph, err := loadPlanGraph(ctx, transaction, command.ProjectID)
		if err != nil {
			return err
		}
		if len(graph.plans) != len(command.PlanIDs) {
			return ErrInvalidCommand
		}
		for _, id := range command.PlanIDs {
			current, exists := graph.planByID[id]
			if !exists {
				return repository.ErrProjectMismatch
			}
			if current.UpdatedAt != command.ExpectedUpdatedAt[id] {
				return repository.ErrVersionConflict
			}
		}
		updatedAt = nextMutationTimestamp(service.clock.Now(), graph.planTimes())
		if _, err := transaction.ReorderPlans(ctx, domainplan.Reorder{
			ProjectID: command.ProjectID, IDs: append([]int64(nil), command.PlanIDs...),
			ExpectedUpdatedAt: copyUpdatedAt(command.ExpectedUpdatedAt), UpdatedAt: updatedAt,
		}); err != nil {
			return err
		}
		return appendPlanEvent(ctx, transaction, RouteReorder, command.RequestID, command.ProjectID,
			TargetPlan, 0, "plans.reordered", fmt.Sprintf("%d plans reordered", len(command.PlanIDs)),
			map[string]any{"plan_ids": append([]int64(nil), command.PlanIDs...)}, updatedAt)
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
