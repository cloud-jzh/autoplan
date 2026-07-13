package intake

import (
	"context"
	"fmt"

	applicationidempotency "github.com/lyming99/autoplan/backend/internal/application/idempotency"
	domainintake "github.com/lyming99/autoplan/backend/internal/domain/intake"
	domainproject "github.com/lyming99/autoplan/backend/internal/domain/project"
	"github.com/lyming99/autoplan/backend/internal/repository"
)

const (
	RouteCreate   = "intake:create"
	RouteUpdate   = "intake:update"
	RouteAccept   = "intake:accept"
	RouteUnaccept = "intake:unaccept"
)

type MutationMetadata struct {
	CallerScope    string
	IdempotencyKey string
	RequestID      string
}

type CreateCommand struct {
	ProjectID      int64
	Type           domainintake.Type
	RequirementID  *int64
	Title          string
	Body           string
	Status         domainintake.Status
	AgentCLI       domainintake.AgentCLIConfig
	PlanGeneration domainintake.PlanGenerationConfig
	Metadata       MutationMetadata
}

type NullableInt64 struct {
	Set   bool
	Value *int64
}

type UpdateCommand struct {
	ProjectID         int64
	Type              domainintake.Type
	ID                int64
	ExpectedUpdatedAt string
	RequirementID     NullableInt64
	Title             *string
	Body              *string
	Status            *domainintake.Status
	AgentCLI          *domainintake.AgentCLIConfig
	PlanGeneration    *domainintake.PlanGenerationConfig
	Metadata          MutationMetadata
}

type AcceptanceCommand struct {
	ProjectID int64
	Type      domainintake.Type
	ID        int64
	Accept    bool
	Metadata  MutationMetadata
}

func (service *Service) Create(
	ctx context.Context,
	command CreateCommand,
	visibility domainproject.Visibility,
) (MutationResult, error) {
	if err := service.ready(ctx); err != nil {
		return MutationResult{}, err
	}
	now := service.now()
	occurredAt := formatTimestamp(now)
	input := domainintake.NormalizeCreate(domainintake.Create{
		ProjectID: command.ProjectID, Type: command.Type, RequirementID: copyInt64(command.RequirementID),
		Title: command.Title, Body: command.Body, Status: command.Status,
		AgentCLI: command.AgentCLI, PlanGeneration: command.PlanGeneration,
		CreatedAt: occurredAt, UpdatedAt: occurredAt,
	})
	if domainintake.ValidateCreate(input) != nil {
		return MutationResult{}, ErrInvalidCommand
	}
	projectID := input.ProjectID
	prepared, err := service.idempotency.Prepare(applicationidempotency.Request{
		Scope: command.Metadata.CallerScope, Key: command.Metadata.IdempotencyKey,
		RequestID: command.Metadata.RequestID, Route: RouteCreate, ProjectID: &projectID,
		Payload: struct {
			ProjectID      int64
			Type           domainintake.Type
			RequirementID  *int64
			Title          string
			Body           string
			Status         domainintake.Status
			AgentCLI       domainintake.AgentCLIConfig
			PlanGeneration domainintake.PlanGenerationConfig
		}{
			input.ProjectID, input.Type, input.RequirementID, input.Title, input.Body,
			input.Status, input.AgentCLI, input.PlanGeneration,
		}, OccurredAt: occurredAt,
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
		existing, duplicate, duplicateErr := transaction.FindDuplicateIntake(ctx, domainintake.DuplicateQuery{
			ProjectID: input.ProjectID, Type: input.Type, RequirementID: input.RequirementID,
			Title: input.Title, Body: input.Body,
		})
		if duplicateErr != nil {
			return duplicateErr
		}
		if duplicate {
			links, linkErr := transaction.ListPlanLinksForIntake(ctx, existing.ProjectID, existing.Type, existing.ID)
			if linkErr != nil {
				return linkErr
			}
			return DuplicateError{IntakeType: existing.Type, Existing: intakeDTO(existing, links)}
		}
		created, createErr := transaction.CreateIntake(ctx, input)
		if createErr != nil {
			return createErr
		}
		if eventErr := appendEvent(ctx, transaction, prepared, RouteCreate,
			string(created.Type)+".created", projectID, created.ID, created.Type,
			fmt.Sprintf("%s #%d created", created.Type, created.ID),
			map[string]any{"intake_type": created.Type, "intake_id": created.ID, "status": created.Status},
			occurredAt); eventErr != nil {
			return eventErr
		}
		return service.idempotency.Complete(ctx, transaction, prepared, reference, occurredAt)
	})
	if err != nil {
		return MutationResult{}, err
	}
	return service.snapshotResult(ctx, reference, visibility, nil)
}

func (service *Service) Update(
	ctx context.Context,
	command UpdateCommand,
	visibility domainproject.Visibility,
) (MutationResult, error) {
	if err := service.ready(ctx); err != nil {
		return MutationResult{}, err
	}
	if command.ProjectID <= 0 || command.ID <= 0 || !command.Type.Valid() ||
		(command.Type == domainintake.Requirement && command.RequirementID.Set) ||
		(command.ExpectedUpdatedAt != "" && !domainintake.ValidUTCTimestamp(command.ExpectedUpdatedAt)) {
		return MutationResult{}, ErrInvalidCommand
	}
	command = normalizeUpdateCommand(command)
	now := service.now()
	occurredAt := formatTimestamp(now)
	projectID := command.ProjectID
	prepared, err := service.idempotency.Prepare(applicationidempotency.Request{
		Scope: command.Metadata.CallerScope, Key: command.Metadata.IdempotencyKey,
		RequestID: command.Metadata.RequestID, Route: RouteUpdate, ProjectID: &projectID,
		Payload: struct {
			ProjectID         int64
			Type              domainintake.Type
			ID                int64
			ExpectedUpdatedAt string
			RequirementID     NullableInt64
			Title             *string
			Body              *string
			Status            *domainintake.Status
			AgentCLI          *domainintake.AgentCLIConfig
			PlanGeneration    *domainintake.PlanGenerationConfig
		}{
			command.ProjectID, command.Type, command.ID, command.ExpectedUpdatedAt,
			command.RequirementID, command.Title, command.Body, command.Status,
			command.AgentCLI, command.PlanGeneration,
		}, OccurredAt: occurredAt,
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
		if command.ExpectedUpdatedAt != "" && current.UpdatedAt != command.ExpectedUpdatedAt {
			return ErrStateConflict
		}
		mutationAt := nextTimestamp(now, current.UpdatedAt)
		update, updateErr := buildUpdate(current, command, mutationAt)
		if updateErr != nil {
			return updateErr
		}
		updated, updateErr := transaction.UpdateIntake(ctx, projectID, command.Type, command.ID, update)
		if updateErr != nil {
			return updateErr
		}
		if eventErr := appendEvent(ctx, transaction, prepared, RouteUpdate,
			string(updated.Type)+".updated", projectID, updated.ID, updated.Type,
			fmt.Sprintf("%s #%d updated", updated.Type, updated.ID),
			map[string]any{"intake_type": updated.Type, "intake_id": updated.ID, "status": updated.Status},
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

func (service *Service) SetAcceptance(
	ctx context.Context,
	command AcceptanceCommand,
	visibility domainproject.Visibility,
) (MutationResult, error) {
	if err := service.ready(ctx); err != nil {
		return MutationResult{}, err
	}
	if command.ProjectID <= 0 || command.ID <= 0 || !command.Type.Valid() {
		return MutationResult{}, ErrInvalidCommand
	}
	now := service.now()
	occurredAt := formatTimestamp(now)
	projectID := command.ProjectID
	route := RouteUnaccept
	if command.Accept {
		route = RouteAccept
	}
	prepared, err := service.idempotency.Prepare(applicationidempotency.Request{
		Scope: command.Metadata.CallerScope, Key: command.Metadata.IdempotencyKey,
		RequestID: command.Metadata.RequestID, Route: route, ProjectID: &projectID,
		Payload: struct {
			ProjectID int64
			Type      domainintake.Type
			ID        int64
			Accept    bool
		}{command.ProjectID, command.Type, command.ID, command.Accept}, OccurredAt: occurredAt,
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
		mutationAt := nextTimestamp(now, current.UpdatedAt)
		var acceptedAt *string
		if command.Accept {
			acceptedAt = &mutationAt
		}
		updated, updateErr := transaction.SetIntakeAcceptance(ctx, projectID, command.Type, command.ID, acceptedAt, mutationAt)
		if updateErr != nil {
			return updateErr
		}
		eventType := string(updated.Type) + ".unaccepted"
		if command.Accept {
			eventType = string(updated.Type) + ".accepted"
		}
		if eventErr := appendEvent(ctx, transaction, prepared, route, eventType,
			projectID, updated.ID, updated.Type, fmt.Sprintf("%s #%d acceptance updated", updated.Type, updated.ID),
			map[string]any{
				"intake_type": updated.Type, "intake_id": updated.ID, "accepted_at": acceptedAt,
				"previous_accepted_at": current.AcceptedAt,
			},
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

func normalizeUpdateCommand(command UpdateCommand) UpdateCommand {
	if !command.RequirementID.Set {
		command.RequirementID.Value = nil
	}
	if command.AgentCLI != nil {
		value := domainintake.NormalizeUpdate(domainintake.Update{AgentCLI: *command.AgentCLI})
		normalized := value.AgentCLI
		command.AgentCLI = &normalized
	}
	if command.PlanGeneration != nil {
		value := domainintake.NormalizeUpdate(domainintake.Update{PlanGeneration: *command.PlanGeneration})
		normalized := value.PlanGeneration
		command.PlanGeneration = &normalized
	}
	return command
}

func buildUpdate(current domainintake.Intake, command UpdateCommand, updatedAt string) (domainintake.Update, error) {
	body := current.Body
	if command.Body != nil {
		body = *command.Body
	}
	title := domainintake.DefaultTitle(body, defaultTitle(current.Type))
	if command.Title != nil {
		title = *command.Title
	}
	status := current.Status
	if command.Status != nil {
		status = *command.Status
	}
	if !validStatusTransition(current.Status, status) {
		return domainintake.Update{}, ErrInvalidTransition
	}
	requirementID := copyInt64(current.RequirementID)
	if current.Type == domainintake.Feedback && command.RequirementID.Set {
		requirementID = copyInt64(command.RequirementID.Value)
	}
	agentCLI := current.AgentCLI
	if command.AgentCLI != nil {
		agentCLI = *command.AgentCLI
	}
	planGeneration := current.PlanGeneration
	if command.PlanGeneration != nil {
		planGeneration = *command.PlanGeneration
	}
	update := domainintake.NormalizeUpdate(domainintake.Update{
		RequirementID: requirementID, Title: title, Body: body, Status: status,
		AgentCLI: agentCLI, PlanGeneration: planGeneration, Failure: current.Failure,
		AcceptedAt: copyString(current.AcceptedAt), SessionID: copyString(current.SessionID), UpdatedAt: updatedAt,
	})
	if domainintake.ValidateUpdate(current.Type, update) != nil {
		return domainintake.Update{}, ErrInvalidCommand
	}
	return update, nil
}

func validStatusTransition(current, next domainintake.Status) bool {
	if !current.Valid() || !next.Valid() {
		return false
	}
	if current == next {
		return true
	}
	switch current {
	case domainintake.StatusDraft:
		return next == domainintake.StatusOpen || next == domainintake.StatusClosed
	case domainintake.StatusOpen:
		return next == domainintake.StatusCompleted || next == domainintake.StatusClosed
	case domainintake.StatusCompleted:
		return next == domainintake.StatusOpen || next == domainintake.StatusClosed
	case domainintake.StatusClosed:
		return next == domainintake.StatusOpen
	default:
		return false
	}
}

func defaultTitle(intakeType domainintake.Type) string {
	if intakeType == domainintake.Feedback {
		return "未命名反馈"
	}
	return "未命名需求"
}

func (service *Service) snapshotResult(
	ctx context.Context,
	reference applicationidempotency.Reference,
	visibility domainproject.Visibility,
	cleanup *CleanupDTO,
) (MutationResult, error) {
	if reference.Kind != "active-project" || reference.ProjectID == nil {
		return MutationResult{}, repository.ErrTransaction
	}
	snapshot, err := service.assembler.Assemble(ctx, reference.ProjectID, visibility)
	if err != nil {
		return MutationResult{}, err
	}
	return MutationResult{Snapshot: snapshot, Cleanup: cleanup}, nil
}
