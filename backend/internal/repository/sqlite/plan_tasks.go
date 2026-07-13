package sqlite

import (
	"context"
	"database/sql"

	domainplan "github.com/lyming99/autoplan/backend/internal/domain/plan"
	"github.com/lyming99/autoplan/backend/internal/repository"
)

const planTaskSelectColumns = `plan_tasks.id, plans.project_id, plan_tasks.plan_id, plan_tasks.task_key,
	plan_tasks.title, plan_tasks.raw_line, plan_tasks.scope, plan_tasks.status, plan_tasks.sort_order,
	plan_tasks.started_at, plan_tasks.finished_at, plan_tasks.duration_ms, plan_tasks.updated_at,
	plan_tasks.accepted_at`

func (transaction *writeTransaction) ListPlanTasks(
	ctx context.Context,
	projectID, planID int64,
) ([]domainplan.Task, error) {
	if projectID <= 0 || planID <= 0 {
		return nil, repository.ErrInvalidTask
	}
	if _, found, err := transaction.GetPlan(ctx, projectID, planID); err != nil {
		return nil, err
	} else if !found {
		return nil, repository.ErrNotFound
	}
	rows, err := transaction.tx.QueryContext(ctx,
		"SELECT "+planTaskSelectColumns+` FROM plan_tasks JOIN plans ON plans.id = plan_tasks.plan_id
		 WHERE plans.project_id = ? AND plan_tasks.plan_id = ?
		 ORDER BY plan_tasks.sort_order ASC, plan_tasks.id ASC`, projectID, planID)
	if err != nil {
		return nil, safeSQLError(ctx, err)
	}
	defer rows.Close()
	result := make([]domainplan.Task, 0)
	for rows.Next() {
		value, scanErr := scanPlanTask(rows)
		if scanErr != nil {
			return nil, safeSQLError(ctx, scanErr)
		}
		result = append(result, value)
	}
	if err := rows.Err(); err != nil {
		return nil, safeSQLError(ctx, err)
	}
	return result, nil
}

func (transaction *writeTransaction) GetPlanTask(
	ctx context.Context,
	projectID, planID, taskID int64,
) (domainplan.Task, bool, error) {
	if projectID <= 0 || planID <= 0 || taskID <= 0 {
		return domainplan.Task{}, false, nil
	}
	value, err := scanPlanTask(transaction.tx.QueryRowContext(ctx,
		"SELECT "+planTaskSelectColumns+` FROM plan_tasks JOIN plans ON plans.id = plan_tasks.plan_id
		 WHERE plans.project_id = ? AND plan_tasks.plan_id = ? AND plan_tasks.id = ?`, projectID, planID, taskID))
	if err == sql.ErrNoRows {
		return domainplan.Task{}, false, nil
	}
	if err != nil {
		return domainplan.Task{}, false, safeSQLError(ctx, err)
	}
	return value, true, nil
}

func (transaction *writeTransaction) SetPlanTaskAcceptance(
	ctx context.Context,
	input domainplan.AcceptanceUpdate,
) (domainplan.Task, error) {
	if domainplan.ValidateAcceptanceUpdate(input) != nil {
		return domainplan.Task{}, repository.ErrInvalidTask
	}
	current, found, err := transaction.findPlanTask(ctx, input.ProjectID, input.ID)
	if err != nil {
		return domainplan.Task{}, err
	}
	if !found {
		return domainplan.Task{}, repository.ErrNotFound
	}
	if current.UpdatedAt != input.ExpectedUpdatedAt {
		return domainplan.Task{}, repository.ErrVersionConflict
	}
	if input.AcceptedAt != nil && !domainplan.IsAcceptableTask(current.Status) {
		return domainplan.Task{}, repository.ErrInvalidTask
	}
	result, err := transaction.tx.ExecContext(ctx,
		`UPDATE plan_tasks SET accepted_at = ?, updated_at = ?
		  WHERE id = ? AND plan_id = ? AND updated_at = ?
		    AND EXISTS (SELECT 1 FROM plans WHERE id = plan_tasks.plan_id AND project_id = ?)`,
		optionalString(input.AcceptedAt), input.UpdatedAt, current.ID, current.PlanID, input.ExpectedUpdatedAt, input.ProjectID)
	if err != nil {
		return domainplan.Task{}, safeSQLError(ctx, err)
	}
	if err := requireOneRow(result); err != nil {
		if err == repository.ErrNotFound {
			return domainplan.Task{}, repository.ErrVersionConflict
		}
		return domainplan.Task{}, err
	}
	if err := transaction.wrote("plan-tasks:acceptance"); err != nil {
		return domainplan.Task{}, err
	}
	updated, found, err := transaction.GetPlanTask(ctx, input.ProjectID, current.PlanID, current.ID)
	if err != nil {
		return domainplan.Task{}, err
	}
	if !found {
		return domainplan.Task{}, repository.ErrTransaction
	}
	return updated, nil
}

func (transaction *writeTransaction) RedoPlanTask(
	ctx context.Context,
	input domainplan.TaskRedo,
) (domainplan.Task, error) {
	if domainplan.ValidateTaskRedo(input) != nil {
		return domainplan.Task{}, repository.ErrInvalidTask
	}
	plan, found, err := transaction.GetPlan(ctx, input.ProjectID, input.PlanID)
	if err != nil {
		return domainplan.Task{}, err
	}
	if !found {
		return domainplan.Task{}, repository.ErrNotFound
	}
	task, found, err := transaction.GetPlanTask(ctx, input.ProjectID, input.PlanID, input.TaskID)
	if err != nil {
		return domainplan.Task{}, err
	}
	if !found {
		return domainplan.Task{}, repository.ErrNotFound
	}
	if plan.UpdatedAt != input.ExpectedPlanUpdatedAt || task.UpdatedAt != input.ExpectedTaskUpdatedAt {
		return domainplan.Task{}, repository.ErrVersionConflict
	}
	if plan.Status == domainplan.StatusRunning || task.Status == domainplan.TaskRunning ||
		(!domainplan.IsAcceptableTask(task.Status) && task.AcceptedAt == nil) {
		return domainplan.Task{}, repository.ErrInvalidTask
	}
	taskResult, err := transaction.tx.ExecContext(ctx,
		`UPDATE plan_tasks SET status = ?, accepted_at = NULL, updated_at = ?
		  WHERE id = ? AND plan_id = ? AND updated_at = ?
		    AND EXISTS (SELECT 1 FROM plans WHERE id = plan_tasks.plan_id AND project_id = ?)`,
		string(domainplan.TaskPending), input.UpdatedAt, task.ID, input.PlanID, input.ExpectedTaskUpdatedAt, input.ProjectID)
	if err != nil {
		return domainplan.Task{}, safeSQLError(ctx, err)
	}
	if err := requireOneRow(taskResult); err != nil {
		if err == repository.ErrNotFound {
			return domainplan.Task{}, repository.ErrVersionConflict
		}
		return domainplan.Task{}, err
	}
	if err := transaction.wrote("plan-tasks:redo"); err != nil {
		return domainplan.Task{}, err
	}
	planResult, err := transaction.tx.ExecContext(ctx,
		`UPDATE plans SET status = ?, validation_passed = 0, accepted_at = NULL, updated_at = ?
		  WHERE id = ? AND project_id = ? AND updated_at = ?`,
		string(domainplan.StatusPending), input.UpdatedAt, input.PlanID, input.ProjectID, input.ExpectedPlanUpdatedAt)
	if err != nil {
		return domainplan.Task{}, safeSQLError(ctx, err)
	}
	if err := requireOneRow(planResult); err != nil {
		if err == repository.ErrNotFound {
			return domainplan.Task{}, repository.ErrVersionConflict
		}
		return domainplan.Task{}, err
	}
	if err := transaction.wrote("plans:redo-from-task"); err != nil {
		return domainplan.Task{}, err
	}
	updated, found, err := transaction.GetPlanTask(ctx, input.ProjectID, input.PlanID, input.TaskID)
	if err != nil {
		return domainplan.Task{}, err
	}
	if !found {
		return domainplan.Task{}, repository.ErrTransaction
	}
	return updated, nil
}

func (transaction *writeTransaction) findPlanTask(
	ctx context.Context,
	projectID, taskID int64,
) (domainplan.Task, bool, error) {
	if projectID <= 0 || taskID <= 0 {
		return domainplan.Task{}, false, nil
	}
	value, err := scanPlanTask(transaction.tx.QueryRowContext(ctx,
		"SELECT "+planTaskSelectColumns+` FROM plan_tasks JOIN plans ON plans.id = plan_tasks.plan_id
		 WHERE plans.project_id = ? AND plan_tasks.id = ?`, projectID, taskID))
	if err == sql.ErrNoRows {
		return domainplan.Task{}, false, nil
	}
	if err != nil {
		return domainplan.Task{}, false, safeSQLError(ctx, err)
	}
	return value, true, nil
}

func scanPlanTask(row rowScanner) (domainplan.Task, error) {
	var value domainplan.Task
	var startedAt, finishedAt, acceptedAt sql.NullString
	if err := row.Scan(
		&value.ID, &value.ProjectID, &value.PlanID, &value.Key, &value.Title, &value.RawLine,
		&value.Scope, &value.Status, &value.SortOrder, &startedAt, &finishedAt, &value.DurationMS,
		&value.UpdatedAt, &acceptedAt,
	); err != nil {
		return domainplan.Task{}, err
	}
	value.StartedAt = nullStringPointer(startedAt)
	value.FinishedAt = nullStringPointer(finishedAt)
	value.AcceptedAt = nullStringPointer(acceptedAt)
	if domainplan.ValidateTaskRecord(value) != nil {
		return domainplan.Task{}, repository.ErrInvalidStore
	}
	return value, nil
}
