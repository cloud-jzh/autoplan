package intake

import (
	"context"
	"fmt"

	applicationidempotency "github.com/lyming99/autoplan/backend/internal/application/idempotency"
	domainintake "github.com/lyming99/autoplan/backend/internal/domain/intake"
	domainproject "github.com/lyming99/autoplan/backend/internal/domain/project"
	"github.com/lyming99/autoplan/backend/internal/repository"
)

const RouteDelete = "intake:delete"

type DeleteCommand struct {
	ProjectID int64
	Type      domainintake.Type
	ID        int64
	Metadata  MutationMetadata
}

func (service *Service) Delete(
	ctx context.Context,
	command DeleteCommand,
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
	prepared, err := service.idempotency.Prepare(applicationidempotency.Request{
		Scope: command.Metadata.CallerScope, Key: command.Metadata.IdempotencyKey,
		RequestID: command.Metadata.RequestID, Route: RouteDelete, ProjectID: &projectID,
		Payload: struct {
			ProjectID int64
			Type      domainintake.Type
			ID        int64
		}{command.ProjectID, command.Type, command.ID}, OccurredAt: occurredAt,
	})
	if err != nil {
		return MutationResult{}, err
	}
	prepared = mutationPrepared(prepared, command.Metadata)
	operationID := prepared.OperationID
	if operationID == "" {
		operationID, _ = eventIdentity(RouteDelete, command.Metadata.RequestID, projectID, command.ID, occurredAt)
	}
	deletion := AttachmentDeletion{
		OperationID: operationID, ProjectID: projectID, IntakeType: command.Type, IntakeID: command.ID,
	}
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
		if _, found, getErr := transaction.GetIntake(ctx, projectID, command.Type, command.ID); getErr != nil {
			return getErr
		} else if !found {
			return repository.ErrNotFound
		}
		preparedDeletion, prepareErr := service.attachments.PrepareIntakeDeletion(
			ctx, projectID, command.Type, command.ID, operationID,
		)
		if prepareErr != nil {
			return prepareErr
		}
		if preparedDeletion.OperationID != operationID || preparedDeletion.ProjectID != projectID ||
			preparedDeletion.IntakeType != command.Type || preparedDeletion.IntakeID != command.ID {
			return repository.ErrTransaction
		}
		deletion = preparedDeletion
		stoppedPlanIDs, stopErr := service.runtime.StopIntakePlans(ctx, projectID, command.Type, command.ID)
		if stopErr != nil {
			return stopErr
		}
		deleted, deleteErr := transaction.DeleteIntake(ctx, projectID, command.Type, command.ID, occurredAt)
		if deleteErr != nil {
			return deleteErr
		}
		deletion.AttachmentIDs = append([]int64(nil), deleted.AttachmentIDs...)
		cleanupStatus := "not_required"
		if len(deletion.AttachmentIDs) != 0 {
			cleanupStatus = "pending"
		}
		if eventErr := appendEvent(ctx, transaction, prepared, RouteDelete,
			"intake.deleted", projectID, command.ID, command.Type,
			fmt.Sprintf("%s #%d deleted", command.Type, command.ID),
			map[string]any{
				"intake_type": command.Type, "intake_id": command.ID,
				"plan_ids": deleted.PlanIDs, "attachment_count": len(deleted.AttachmentIDs),
				"feedback_detached": deleted.FeedbackDetached, "attachment_cleanup_status": cleanupStatus,
				"stopped_plan_ids": stoppedPlanIDs,
			}, occurredAt); eventErr != nil {
			return eventErr
		}
		return service.idempotency.Complete(ctx, transaction, prepared, reference, occurredAt)
	})
	if err != nil {
		return MutationResult{}, err
	}
	cleanup := finalizeCleanup(ctx, service.attachments, deletion)
	return service.snapshotResult(ctx, reference, visibility, &cleanup)
}

func finalizeCleanup(ctx context.Context, workflow AttachmentWorkflow, deletion AttachmentDeletion) CleanupDTO {
	result, err := workflow.FinalizeIntakeDeletion(ctx, deletion)
	status := "complete"
	code := result.Code
	if len(deletion.AttachmentIDs) == 0 && result.Total == 0 && result.Pending == 0 && err == nil {
		status = "not_required"
	} else if err != nil || result.Pending > 0 {
		status = "recovery_required"
		if code == "" {
			code = "attachment_cleanup_pending"
		}
	}
	return CleanupDTO{
		Status: status, Total: result.Total, Deleted: result.Deleted,
		Missing: result.Missing, Pending: result.Pending, Code: code,
	}
}
