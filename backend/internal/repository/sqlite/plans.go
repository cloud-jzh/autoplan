package sqlite

import (
	"context"
	"database/sql"

	domainplan "github.com/lyming99/autoplan/backend/internal/domain/plan"
	"github.com/lyming99/autoplan/backend/internal/repository"
)

const maximumPlanPageSize = 200

const planSelectColumns = `id, project_id, issue_hash, file_path, hash, status, sort_order,
	total_tasks, completed_tasks, validation_passed,
	agent_cli_provider, agent_cli_command, codex_reasoning_effort,
	plan_generation_strategy, plan_generation_provider, plan_generation_command,
	plan_generation_model, plan_generation_codex_reasoning_effort, plan_generation_claude_config_id,
	plan_execution_strategy, plan_execution_provider, plan_execution_command,
	plan_execution_model, plan_execution_codex_reasoning_effort, plan_execution_claude_config_id,
	plan_generation_duration_ms, created_at, updated_at, accepted_at`

func (transaction *writeTransaction) ListPlans(
	ctx context.Context,
	options domainplan.ListOptions,
) ([]domainplan.Plan, error) {
	if options.ProjectID <= 0 || options.Offset < 0 {
		return nil, repository.ErrInvalidPlan
	}
	found, err := transaction.projectExists(ctx, options.ProjectID)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, repository.ErrNotFound
	}
	limit := boundedPlanPage(options.Limit)
	rows, err := transaction.tx.QueryContext(ctx,
		"SELECT "+planSelectColumns+` FROM plans
		 WHERE project_id = ?
		 ORDER BY sort_order ASC, created_at ASC, id ASC
		 LIMIT ? OFFSET ?`, options.ProjectID, limit, options.Offset)
	if err != nil {
		return nil, safeSQLError(ctx, err)
	}
	defer rows.Close()
	result := make([]domainplan.Plan, 0)
	for rows.Next() {
		value, scanErr := scanPlan(rows)
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

func (transaction *writeTransaction) GetPlan(
	ctx context.Context,
	projectID, planID int64,
) (domainplan.Plan, bool, error) {
	if projectID <= 0 || planID <= 0 {
		return domainplan.Plan{}, false, nil
	}
	value, err := scanPlan(transaction.tx.QueryRowContext(ctx,
		"SELECT "+planSelectColumns+" FROM plans WHERE project_id = ? AND id = ?", projectID, planID))
	if err == sql.ErrNoRows {
		return domainplan.Plan{}, false, nil
	}
	if err != nil {
		return domainplan.Plan{}, false, safeSQLError(ctx, err)
	}
	return value, true, nil
}

func (transaction *writeTransaction) ReorderPlans(
	ctx context.Context,
	input domainplan.Reorder,
) ([]domainplan.Plan, error) {
	if domainplan.ValidateReorder(input) != nil {
		return nil, repository.ErrPlanOrderConflict
	}
	found, err := transaction.projectExists(ctx, input.ProjectID)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, repository.ErrNotFound
	}
	rows, err := transaction.tx.QueryContext(ctx,
		`SELECT id, sort_order, updated_at FROM plans
		  WHERE project_id = ? ORDER BY sort_order ASC, created_at ASC, id ASC`, input.ProjectID)
	if err != nil {
		return nil, safeSQLError(ctx, err)
	}
	type currentPlan struct {
		id        int64
		sortOrder int64
		updatedAt string
	}
	current := make([]currentPlan, 0)
	for rows.Next() {
		var item currentPlan
		if err := rows.Scan(&item.id, &item.sortOrder, &item.updatedAt); err != nil {
			_ = rows.Close()
			return nil, safeSQLError(ctx, err)
		}
		current = append(current, item)
	}
	if closeErr := rows.Close(); closeErr != nil || rows.Err() != nil {
		return nil, repository.ErrTransaction
	}
	if len(current) != len(input.IDs) {
		return nil, repository.ErrPlanOrderConflict
	}
	currentByID := make(map[int64]currentPlan, len(current))
	for _, item := range current {
		currentByID[item.id] = item
	}
	for _, id := range input.IDs {
		item, exists := currentByID[id]
		if !exists {
			return nil, repository.ErrProjectMismatch
		}
		if item.updatedAt != input.ExpectedUpdatedAt[id] {
			return nil, repository.ErrVersionConflict
		}
	}
	for index, id := range input.IDs {
		item := currentByID[id]
		order := int64(index + 1)
		if item.sortOrder == order {
			continue
		}
		result, updateErr := transaction.tx.ExecContext(ctx,
			`UPDATE plans SET sort_order = ?, updated_at = ?
			  WHERE id = ? AND project_id = ? AND updated_at = ?`,
			order, input.UpdatedAt, id, input.ProjectID, item.updatedAt)
		if updateErr != nil {
			return nil, safeSQLError(ctx, updateErr)
		}
		if err := requireOneRow(result); err != nil {
			if err == repository.ErrNotFound {
				return nil, repository.ErrVersionConflict
			}
			return nil, err
		}
		if err := transaction.wrote("plans:reorder"); err != nil {
			return nil, err
		}
	}
	return transaction.ListPlans(ctx, domainplan.ListOptions{ProjectID: input.ProjectID, Limit: len(input.IDs)})
}

func (transaction *writeTransaction) SetPlanAcceptance(
	ctx context.Context,
	input domainplan.AcceptanceUpdate,
) (domainplan.Plan, error) {
	if domainplan.ValidateAcceptanceUpdate(input) != nil {
		return domainplan.Plan{}, repository.ErrInvalidPlan
	}
	current, found, err := transaction.GetPlan(ctx, input.ProjectID, input.ID)
	if err != nil {
		return domainplan.Plan{}, err
	}
	if !found {
		return domainplan.Plan{}, repository.ErrNotFound
	}
	if current.UpdatedAt != input.ExpectedUpdatedAt {
		return domainplan.Plan{}, repository.ErrVersionConflict
	}
	if input.AcceptedAt != nil && !domainplan.IsAcceptablePlan(current.Status) {
		return domainplan.Plan{}, repository.ErrInvalidPlan
	}
	result, err := transaction.tx.ExecContext(ctx,
		`UPDATE plans SET accepted_at = ?, updated_at = ?
		  WHERE id = ? AND project_id = ? AND updated_at = ?`,
		optionalString(input.AcceptedAt), input.UpdatedAt, input.ID, input.ProjectID, input.ExpectedUpdatedAt)
	if err != nil {
		return domainplan.Plan{}, safeSQLError(ctx, err)
	}
	if err := requireOneRow(result); err != nil {
		if err == repository.ErrNotFound {
			return domainplan.Plan{}, repository.ErrVersionConflict
		}
		return domainplan.Plan{}, err
	}
	if err := transaction.wrote("plans:acceptance"); err != nil {
		return domainplan.Plan{}, err
	}
	updated, found, err := transaction.GetPlan(ctx, input.ProjectID, input.ID)
	if err != nil {
		return domainplan.Plan{}, err
	}
	if !found {
		return domainplan.Plan{}, repository.ErrTransaction
	}
	return updated, nil
}

func boundedPlanPage(limit int) int {
	if limit <= 0 {
		return 100
	}
	if limit > maximumPlanPageSize {
		return maximumPlanPageSize
	}
	return limit
}

func scanPlan(row rowScanner) (domainplan.Plan, error) {
	var value domainplan.Plan
	var validationPassed int64
	var provider, reasoning sql.NullString
	var generationProvider, generationReasoning sql.NullString
	var executionProvider, executionReasoning sql.NullString
	var acceptedAt sql.NullString
	if err := row.Scan(
		&value.ID, &value.ProjectID, &value.IssueHash, &value.SourceRef, &value.Digest, &value.Status, &value.SortOrder,
		&value.TotalTasks, &value.CompletedTasks, &validationPassed,
		&provider, &value.AgentCLI.Command, &reasoning,
		&value.PlanGeneration.Strategy, &generationProvider, &value.PlanGeneration.Command,
		&value.PlanGeneration.Model, &generationReasoning, &value.PlanGeneration.ClaudeConfigID,
		&value.PlanExecution.Strategy, &executionProvider, &value.PlanExecution.Command,
		&value.PlanExecution.Model, &executionReasoning, &value.PlanExecution.ClaudeConfigID,
		&value.GenerationMillis, &value.CreatedAt, &value.UpdatedAt, &acceptedAt,
	); err != nil {
		return domainplan.Plan{}, err
	}
	if validationPassed != 0 && validationPassed != 1 {
		return domainplan.Plan{}, repository.ErrInvalidStore
	}
	value.ValidationPassed = validationPassed == 1
	value.AgentCLI.Provider = nullStringPointer(provider)
	value.AgentCLI.CodexReasoningEffort = nullStringPointer(reasoning)
	value.PlanGeneration.Provider = nullStringPointer(generationProvider)
	value.PlanGeneration.CodexReasoningEffort = nullStringPointer(generationReasoning)
	value.PlanExecution.Provider = nullStringPointer(executionProvider)
	value.PlanExecution.CodexReasoningEffort = nullStringPointer(executionReasoning)
	value.AcceptedAt = nullStringPointer(acceptedAt)
	if domainplan.ValidateRecord(value) != nil {
		return domainplan.Plan{}, repository.ErrInvalidStore
	}
	return value, nil
}
