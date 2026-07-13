package intake

import (
	"context"
	"fmt"
	"sort"
	"strings"

	applicationidempotency "github.com/lyming99/autoplan/backend/internal/application/idempotency"
	domainintake "github.com/lyming99/autoplan/backend/internal/domain/intake"
	domainproject "github.com/lyming99/autoplan/backend/internal/domain/project"
	"github.com/lyming99/autoplan/backend/internal/repository"
)

const RouteReplaceLinks = "intake:replace-plan-links"

type ReplaceLinksCommand struct {
	ProjectID int64
	Type      domainintake.Type
	ID        int64
	Links     []domainintake.PlanLinkInput
	Metadata  MutationMetadata
}

func (service *Service) Links(
	ctx context.Context,
	projectID int64,
	intakeType domainintake.Type,
	intakeID int64,
) ([]LinkedPlanDTO, error) {
	if err := service.ready(ctx); err != nil {
		return nil, err
	}
	if projectID <= 0 || intakeID <= 0 || !intakeType.Valid() {
		return nil, ErrInvalidCommand
	}
	var result []LinkedPlanDTO
	err := service.writer.TransactIntake(ctx, func(transaction repository.IntakeWriteTransaction) error {
		if _, found, err := transaction.GetIntake(ctx, projectID, intakeType, intakeID); err != nil {
			return err
		} else if !found {
			return repository.ErrNotFound
		}
		links, err := transaction.ListPlanLinksForIntake(ctx, projectID, intakeType, intakeID)
		if err != nil {
			return err
		}
		result = intakeDTO(domainintake.Intake{ID: intakeID}, links).LinkedPlans
		return nil
	})
	return result, err
}

func (service *Service) ReplaceLinks(
	ctx context.Context,
	command ReplaceLinksCommand,
	visibility domainproject.Visibility,
) (MutationResult, error) {
	if err := service.ready(ctx); err != nil {
		return MutationResult{}, err
	}
	links, normalizeErr := normalizePlanLinks(command.ProjectID, command.Type, command.ID, command.Links)
	if normalizeErr != nil {
		return MutationResult{}, ErrInvalidCommand
	}
	command.Links = links
	now := service.now()
	occurredAt := formatTimestamp(now)
	projectID := command.ProjectID
	prepared, err := service.idempotency.Prepare(applicationidempotency.Request{
		Scope: command.Metadata.CallerScope, Key: command.Metadata.IdempotencyKey,
		RequestID: command.Metadata.RequestID, Route: RouteReplaceLinks, ProjectID: &projectID,
		Payload: struct {
			ProjectID int64
			Type      domainintake.Type
			ID        int64
			Links     []domainintake.PlanLinkInput
		}{command.ProjectID, command.Type, command.ID, command.Links}, OccurredAt: occurredAt,
	})
	if err != nil {
		return MutationResult{}, err
	}
	prepared = mutationPrepared(prepared, command.Metadata)
	reference := activeProjectReference(projectID)
	err = service.writer.TransactIntake(ctx, func(transaction repository.IntakeWriteTransaction) error {
		decision, beginErr := service.idempotency.Begin(ctx, transaction, prepared)
		if beginErr != nil {
			return beginErr
		}
		if decision.Replay {
			reference = decision.Reference
			return nil
		}
		current, found, getErr := transaction.GetIntake(ctx, projectID, command.Type, command.ID)
		if getErr != nil {
			return getErr
		}
		if !found {
			return repository.ErrNotFound
		}
		existing, listErr := transaction.ListPlanLinksForIntake(ctx, projectID, command.Type, command.ID)
		if listErr != nil {
			return listErr
		}
		if sameNormalizedLinks(existing, command.Links) {
			return service.idempotency.Complete(ctx, transaction, prepared, reference, occurredAt)
		}
		mutationAt := nextTimestamp(now, current.UpdatedAt)
		links, replaceErr := transaction.ReplacePlanLinks(ctx, projectID, command.Type, command.ID, command.Links, mutationAt)
		if replaceErr != nil {
			return replaceErr
		}
		planIDs := make([]int64, 0, len(links))
		for _, link := range links {
			planIDs = append(planIDs, link.PlanID)
		}
		if eventErr := appendEvent(ctx, transaction, prepared, RouteReplaceLinks,
			"intake.plan_links.replaced", projectID, command.ID, command.Type,
			fmt.Sprintf("%s #%d plan links replaced", command.Type, command.ID),
			map[string]any{"intake_type": command.Type, "intake_id": command.ID, "plan_ids": planIDs},
			mutationAt); eventErr != nil {
			return eventErr
		}
		return service.idempotency.Complete(ctx, transaction, prepared, reference, mutationAt)
	})
	if err != nil {
		return MutationResult{}, err
	}
	return service.snapshotResult(ctx, reference, visibility, nil)
}

func sameNormalizedLinks(existing []domainintake.PlanLink, requested []domainintake.PlanLinkInput) bool {
	if len(existing) != len(requested) {
		return false
	}
	for index := range requested {
		if existing[index].PlanID != requested[index].PlanID ||
			existing[index].PhaseIndex != requested[index].PhaseIndex ||
			existing[index].PhaseTitle != requested[index].PhaseTitle {
			return false
		}
	}
	return true
}

func normalizePlanLinks(
	projectID int64,
	intakeType domainintake.Type,
	intakeID int64,
	inputs []domainintake.PlanLinkInput,
) ([]domainintake.PlanLinkInput, error) {
	result := make([]domainintake.PlanLinkInput, 0, len(inputs))
	seenPlans := make(map[int64]struct{}, len(inputs))
	for index, input := range inputs {
		if input.PlanID <= 0 || input.PhaseIndex < 0 {
			return nil, ErrInvalidCommand
		}
		if _, duplicate := seenPlans[input.PlanID]; duplicate {
			continue
		}
		seenPlans[input.PlanID] = struct{}{}
		if input.PhaseIndex == 0 {
			input.PhaseIndex = int64(index + 1)
		}
		input.PhaseTitle = strings.TrimSpace(input.PhaseTitle)
		result = append(result, input)
	}
	sort.SliceStable(result, func(left, right int) bool {
		if result[left].PhaseIndex == result[right].PhaseIndex {
			return result[left].PlanID < result[right].PlanID
		}
		return result[left].PhaseIndex < result[right].PhaseIndex
	})
	if domainintake.ValidatePlanLinks(projectID, intakeID, intakeType, result) != nil {
		return nil, ErrInvalidCommand
	}
	return result, nil
}
