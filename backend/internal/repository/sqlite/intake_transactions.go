package sqlite

import (
	"context"
	"database/sql"

	domainintake "github.com/lyming99/autoplan/backend/internal/domain/intake"
	"github.com/lyming99/autoplan/backend/internal/repository"
)

func (writer *Writer) TransactIntake(
	ctx context.Context,
	operation func(repository.IntakeWriteTransaction) error,
) error {
	if operation == nil {
		return repository.ErrTransaction
	}
	return writer.Transact(ctx, func(transaction repository.WriteTransaction) error {
		intakeTransaction, ok := transaction.(repository.IntakeWriteTransaction)
		if !ok {
			return repository.ErrTransaction
		}
		return operation(intakeTransaction)
	})
}

func (transaction *writeTransaction) DeletePlanAndSyncIntakes(
	ctx context.Context,
	projectID int64,
	planID int64,
	updatedAt string,
) (domainintake.PlanDeleteResult, error) {
	result := domainintake.PlanDeleteResult{PlanID: planID}
	if projectID <= 0 || planID <= 0 || !domainintake.ValidUTCTimestamp(updatedAt) {
		return domainintake.PlanDeleteResult{}, repository.ErrInvalidIntake
	}
	var planProjectID sql.NullInt64
	var filePath string
	err := transaction.tx.QueryRowContext(ctx,
		"SELECT project_id, file_path FROM plans WHERE id = ?", planID).Scan(&planProjectID, &filePath)
	if err == sql.ErrNoRows {
		return domainintake.PlanDeleteResult{}, repository.ErrPlanMissing
	}
	if err != nil {
		return domainintake.PlanDeleteResult{}, safeSQLError(ctx, err)
	}
	if !planProjectID.Valid || planProjectID.Int64 != projectID {
		return domainintake.PlanDeleteResult{}, repository.ErrProjectMismatch
	}
	references, err := transaction.ListIntakesForPlan(ctx, projectID, planID)
	if err != nil {
		return domainintake.PlanDeleteResult{}, err
	}
	result.LinkedIntakes = references
	if _, err := transaction.tx.ExecContext(ctx,
		"DELETE FROM intake_plan_links WHERE project_id = ? AND plan_id = ?", projectID, planID); err != nil {
		return domainintake.PlanDeleteResult{}, safeSQLError(ctx, err)
	}
	if err := transaction.wrote("plan-links:delete-plan"); err != nil {
		return domainintake.PlanDeleteResult{}, err
	}
	for _, reference := range references {
		updateResult, updateErr := transaction.tx.ExecContext(ctx,
			"UPDATE "+reference.IntakeType.Table()+` SET linked_plan_id = (
			 SELECT plan_id FROM intake_plan_links
			  WHERE project_id = ? AND intake_type = ? AND intake_id = ?
			  ORDER BY phase_index ASC, plan_id ASC LIMIT 1
			), updated_at = ? WHERE project_id = ? AND id = ? AND linked_plan_id = ?`,
			reference.ProjectID, string(reference.IntakeType), reference.IntakeID,
			updatedAt, reference.ProjectID, reference.IntakeID, planID)
		if updateErr != nil {
			return domainintake.PlanDeleteResult{}, safeSQLError(ctx, updateErr)
		}
		if _, rowsErr := updateResult.RowsAffected(); rowsErr != nil {
			return domainintake.PlanDeleteResult{}, repository.ErrTransaction
		}
		if err := transaction.wrote("plan-links:sync-legacy-after-plan-delete"); err != nil {
			return domainintake.PlanDeleteResult{}, err
		}
	}
	taskResult, err := transaction.tx.ExecContext(ctx,
		"DELETE FROM plan_tasks WHERE plan_id IN (SELECT id FROM plans WHERE id = ? AND project_id = ?)",
		planID, projectID)
	if err != nil {
		return domainintake.PlanDeleteResult{}, safeSQLError(ctx, err)
	}
	result.DeletedTaskCount, err = rowsAffected(taskResult)
	if err != nil {
		return domainintake.PlanDeleteResult{}, err
	}
	if err := transaction.wrote("plan-tasks:delete-plan"); err != nil {
		return domainintake.PlanDeleteResult{}, err
	}
	planResult, err := transaction.tx.ExecContext(ctx, "DELETE FROM plans WHERE project_id = ? AND id = ?", projectID, planID)
	if err != nil {
		return domainintake.PlanDeleteResult{}, safeSQLError(ctx, err)
	}
	if err := requireOneRow(planResult); err != nil {
		return domainintake.PlanDeleteResult{}, err
	}
	if err := transaction.wrote("plans:delete-intake-plan"); err != nil {
		return domainintake.PlanDeleteResult{}, err
	}
	scanResult, err := transaction.tx.ExecContext(ctx,
		"DELETE FROM scan_files WHERE project_id = ? AND scan_type = 'plan' AND file_path = ?",
		projectID, filePath)
	if err != nil {
		return domainintake.PlanDeleteResult{}, safeSQLError(ctx, err)
	}
	result.DeletedScanCount, err = rowsAffected(scanResult)
	if err != nil {
		return domainintake.PlanDeleteResult{}, err
	}
	if err := transaction.wrote("scan-files:delete-plan"); err != nil {
		return domainintake.PlanDeleteResult{}, err
	}
	return result, nil
}

func (transaction *writeTransaction) DeleteIntake(
	ctx context.Context,
	projectID int64,
	intakeType domainintake.Type,
	intakeID int64,
	updatedAt string,
) (domainintake.DeleteResult, error) {
	if projectID <= 0 || intakeID <= 0 || !intakeType.Valid() || !domainintake.ValidUTCTimestamp(updatedAt) {
		return domainintake.DeleteResult{}, repository.ErrInvalidIntake
	}
	current, found, err := transaction.GetIntake(ctx, projectID, intakeType, intakeID)
	if err != nil {
		return domainintake.DeleteResult{}, err
	}
	if !found {
		return domainintake.DeleteResult{}, repository.ErrNotFound
	}
	result := domainintake.DeleteResult{Intake: current}
	links, err := transaction.ListPlanLinksForIntake(ctx, projectID, intakeType, intakeID)
	if err != nil {
		return domainintake.DeleteResult{}, err
	}
	planSet := make(map[int64]struct{}, len(links))
	result.PlanIDs = make([]int64, 0, len(links))
	for _, link := range links {
		if _, exists := planSet[link.PlanID]; exists {
			continue
		}
		planSet[link.PlanID] = struct{}{}
		result.PlanIDs = append(result.PlanIDs, link.PlanID)
	}

	ownerTypes := []string{"feedback"}
	if intakeType == domainintake.Requirement {
		ownerTypes = []string{"requirement", "requirements"}
	}
	attachmentQuery := `SELECT id FROM attachments WHERE project_id = ? AND owner_id = ? AND owner_type = ? ORDER BY id ASC`
	attachmentArguments := []any{projectID, intakeID, ownerTypes[0]}
	if len(ownerTypes) == 2 {
		attachmentQuery = `SELECT id FROM attachments WHERE project_id = ? AND owner_id = ? AND owner_type IN (?, ?) ORDER BY id ASC`
		attachmentArguments = []any{projectID, intakeID, ownerTypes[0], ownerTypes[1]}
	}
	attachmentRows, err := transaction.tx.QueryContext(ctx, attachmentQuery, attachmentArguments...)
	if err != nil {
		return domainintake.DeleteResult{}, safeSQLError(ctx, err)
	}
	for attachmentRows.Next() {
		var attachmentID int64
		if err := attachmentRows.Scan(&attachmentID); err != nil {
			_ = attachmentRows.Close()
			return domainintake.DeleteResult{}, safeSQLError(ctx, err)
		}
		result.AttachmentIDs = append(result.AttachmentIDs, attachmentID)
	}
	if closeErr := attachmentRows.Close(); closeErr != nil || attachmentRows.Err() != nil {
		return domainintake.DeleteResult{}, repository.ErrTransaction
	}

	if intakeType == domainintake.Requirement {
		detachResult, detachErr := transaction.tx.ExecContext(ctx,
			"UPDATE feedback SET requirement_id = NULL, updated_at = ? WHERE project_id = ? AND requirement_id = ?",
			updatedAt, projectID, intakeID)
		if detachErr != nil {
			return domainintake.DeleteResult{}, safeSQLError(ctx, detachErr)
		}
		result.FeedbackDetached, err = rowsAffected(detachResult)
		if err != nil {
			return domainintake.DeleteResult{}, err
		}
		if err := transaction.wrote("feedback:detach-requirement"); err != nil {
			return domainintake.DeleteResult{}, err
		}
	}

	for _, planID := range result.PlanIDs {
		deletedPlan, deleteErr := transaction.DeletePlanAndSyncIntakes(ctx, projectID, planID, updatedAt)
		if deleteErr != nil {
			return domainintake.DeleteResult{}, deleteErr
		}
		result.DeletedTaskCount += deletedPlan.DeletedTaskCount
		result.DeletedScanCount += deletedPlan.DeletedScanCount
	}
	if _, err := transaction.tx.ExecContext(ctx,
		"DELETE FROM intake_plan_links WHERE project_id = ? AND intake_type = ? AND intake_id = ?",
		projectID, string(intakeType), intakeID); err != nil {
		return domainintake.DeleteResult{}, safeSQLError(ctx, err)
	}
	if err := transaction.wrote("intake-links:delete-intake"); err != nil {
		return domainintake.DeleteResult{}, err
	}
	if _, err := transaction.tx.ExecContext(ctx,
		"DELETE FROM attachments WHERE project_id = ? AND owner_id = ? AND owner_type IN ("+placeholders(len(ownerTypes))+")",
		append([]any{projectID, intakeID}, stringsToAny(ownerTypes)...)...); err != nil {
		return domainintake.DeleteResult{}, safeSQLError(ctx, err)
	}
	if err := transaction.wrote("attachments:delete-intake-metadata"); err != nil {
		return domainintake.DeleteResult{}, err
	}
	deleteResult, err := transaction.tx.ExecContext(ctx,
		"DELETE FROM "+intakeType.Table()+" WHERE project_id = ? AND id = ?", projectID, intakeID)
	if err != nil {
		return domainintake.DeleteResult{}, safeSQLError(ctx, err)
	}
	if err := requireOneRow(deleteResult); err != nil {
		return domainintake.DeleteResult{}, err
	}
	if err := transaction.wrote("intake:delete"); err != nil {
		return domainintake.DeleteResult{}, err
	}
	return result, nil
}

func (transaction *writeTransaction) AppendIntakeEvent(
	ctx context.Context,
	event domainintake.PendingEvent,
) error {
	if domainintake.ValidatePendingEvent(event) != nil {
		return repository.ErrInvalidIntake
	}
	found, err := transaction.projectExists(ctx, event.ProjectID)
	if err != nil {
		return err
	}
	if !found {
		return repository.ErrNotFound
	}
	if event.OperationID != nil {
		var operationProjectID sql.NullInt64
		err := transaction.tx.QueryRowContext(ctx,
			"SELECT project_id FROM operations WHERE operation_id = ?", *event.OperationID).Scan(&operationProjectID)
		if err == sql.ErrNoRows {
			return repository.ErrNotFound
		}
		if err != nil {
			return safeSQLError(ctx, err)
		}
		if !operationProjectID.Valid || operationProjectID.Int64 != event.ProjectID {
			return repository.ErrProjectMismatch
		}
	}
	if _, err := transaction.tx.ExecContext(ctx,
		"INSERT INTO events (project_id, type, message, meta, created_at) VALUES (?, ?, ?, ?, ?)",
		event.ProjectID, event.Type, event.Message, event.DataJSON, event.OccurredAt); err != nil {
		return safeSQLError(ctx, err)
	}
	if err := transaction.wrote("events:append-intake"); err != nil {
		return err
	}
	if _, err := transaction.tx.ExecContext(ctx,
		`INSERT INTO event_outbox
		 (event_id, schema_version, stream_key, sequence, type, request_id, operation_id,
		  project_id, occurred_at, data_json, created_at)
		 VALUES (?, 1, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		event.EventID, event.StreamKey, event.Sequence, event.Type, event.RequestID,
		optionalString(event.OperationID), event.ProjectID, event.OccurredAt, event.DataJSON, event.CreatedAt); err != nil {
		return safeSQLError(ctx, err)
	}
	return transaction.wrote("event-outbox:append-intake")
}

func rowsAffected(result sql.Result) (int64, error) {
	count, err := result.RowsAffected()
	if err != nil || count < 0 {
		return 0, repository.ErrTransaction
	}
	return count, nil
}

func placeholders(count int) string {
	if count <= 0 {
		return "NULL"
	}
	result := "?"
	for index := 1; index < count; index++ {
		result += ", ?"
	}
	return result
}

func stringsToAny(values []string) []any {
	result := make([]any, len(values))
	for index, value := range values {
		result[index] = value
	}
	return result
}

var _ repository.IntakeTransactional = (*Writer)(nil)
var _ repository.IntakeWriteTransaction = (*writeTransaction)(nil)
