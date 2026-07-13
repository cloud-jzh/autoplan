package sqlite

import (
	"context"
	"database/sql"

	domainautomation "github.com/lyming99/autoplan/backend/internal/domain/automation"
	"github.com/lyming99/autoplan/backend/internal/repository"
)

const maximumAutomationPageSize = 200

const scriptSelectColumns = `id, project_id, name, path, runtime, body, description,
	trigger_mode, hook_stage, schedule_cron, enabled, work_dir, timeout_seconds,
	fail_aborts, context_inject, sort_order, last_status, last_exit_code,
	last_duration_ms, last_log, last_run_at, created_at, updated_at, source_type, version`

func (transaction *writeTransaction) ListScripts(
	ctx context.Context,
	options domainautomation.ListOptions,
) ([]domainautomation.Script, error) {
	if options.ProjectID <= 0 || options.Offset < 0 {
		return nil, repository.ErrInvalidAutomation
	}
	found, err := transaction.projectExists(ctx, options.ProjectID)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, repository.ErrNotFound
	}
	return transaction.listScripts(ctx, options.ProjectID, boundedAutomationPage(options.Limit), options.Offset)
}

func (transaction *writeTransaction) listScripts(
	ctx context.Context,
	projectID int64,
	limit, offset int,
) ([]domainautomation.Script, error) {
	query := "SELECT " + scriptSelectColumns + " FROM scripts WHERE project_id = ? ORDER BY sort_order ASC, id ASC"
	args := []any{projectID}
	if limit > 0 {
		query += " LIMIT ? OFFSET ?"
		args = append(args, limit, offset)
	}
	rows, err := transaction.tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, safeSQLError(ctx, err)
	}
	defer rows.Close()
	result := make([]domainautomation.Script, 0)
	for rows.Next() {
		value, scanErr := scanScript(rows)
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

func (transaction *writeTransaction) GetScript(
	ctx context.Context,
	projectID, scriptID int64,
) (domainautomation.Script, bool, error) {
	if projectID <= 0 || scriptID <= 0 {
		return domainautomation.Script{}, false, nil
	}
	value, err := scanScript(transaction.tx.QueryRowContext(ctx,
		"SELECT "+scriptSelectColumns+" FROM scripts WHERE project_id = ? AND id = ?", projectID, scriptID))
	if err == sql.ErrNoRows {
		return domainautomation.Script{}, false, nil
	}
	if err != nil {
		return domainautomation.Script{}, false, safeSQLError(ctx, err)
	}
	return value, true, nil
}

func (transaction *writeTransaction) CreateScript(
	ctx context.Context,
	input domainautomation.ScriptCreate,
) (domainautomation.Script, error) {
	if input.ProjectID <= 0 || !domainautomation.ValidUTCTimestamp(input.CreatedAt) {
		return domainautomation.Script{}, repository.ErrInvalidAutomation
	}
	config, err := domainautomation.NormalizeScriptInput(input.Input, nil)
	if err != nil {
		return domainautomation.Script{}, repository.ErrInvalidAutomation
	}
	found, err := transaction.projectExists(ctx, input.ProjectID)
	if err != nil {
		return domainautomation.Script{}, err
	}
	if !found {
		return domainautomation.Script{}, repository.ErrNotFound
	}
	result, err := transaction.tx.ExecContext(ctx,
		`INSERT INTO scripts (
			project_id, name, path, runtime, body, description, trigger_mode, hook_stage,
			schedule_cron, enabled, work_dir, timeout_seconds, fail_aborts, context_inject,
			sort_order, created_at, updated_at, source_type, version
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1)`,
		input.ProjectID, config.Name, config.Path, config.Runtime, config.Body, config.Description,
		config.TriggerMode, optionalString(config.HookStage), optionalString(config.ScheduleCron), boolInt(config.Enabled),
		config.WorkDir, config.TimeoutSeconds, boolInt(config.FailAborts), config.ContextInject, config.SortOrder,
		input.CreatedAt, input.CreatedAt, config.SourceType)
	if err != nil {
		return domainautomation.Script{}, safeSQLError(ctx, err)
	}
	id, err := result.LastInsertId()
	if err != nil || id <= 0 {
		return domainautomation.Script{}, repository.ErrTransaction
	}
	if err := transaction.wrote("scripts:create"); err != nil {
		return domainautomation.Script{}, err
	}
	created, found, err := transaction.GetScript(ctx, input.ProjectID, id)
	if err != nil {
		return domainautomation.Script{}, err
	}
	if !found {
		return domainautomation.Script{}, repository.ErrTransaction
	}
	return created, nil
}

func (transaction *writeTransaction) UpdateScript(
	ctx context.Context,
	input domainautomation.ScriptUpdate,
) (domainautomation.Script, error) {
	if input.ProjectID <= 0 || input.ScriptID <= 0 || input.ExpectedVersion <= 0 ||
		!domainautomation.ValidUTCTimestamp(input.UpdatedAt) {
		return domainautomation.Script{}, repository.ErrInvalidAutomation
	}
	current, found, err := transaction.GetScript(ctx, input.ProjectID, input.ScriptID)
	if err != nil {
		return domainautomation.Script{}, err
	}
	if !found {
		return domainautomation.Script{}, repository.ErrNotFound
	}
	if current.Version != input.ExpectedVersion {
		return domainautomation.Script{}, repository.ErrVersionConflict
	}
	config, err := domainautomation.NormalizeScriptInput(input.Input, &current)
	if err != nil {
		return domainautomation.Script{}, repository.ErrInvalidAutomation
	}
	result, err := transaction.tx.ExecContext(ctx,
		`UPDATE scripts SET name = ?, path = ?, runtime = ?, body = ?, description = ?,
			trigger_mode = ?, hook_stage = ?, schedule_cron = ?, enabled = ?, work_dir = ?,
			timeout_seconds = ?, fail_aborts = ?, context_inject = ?, sort_order = ?,
			source_type = ?, updated_at = ?, version = version + 1
		 WHERE id = ? AND project_id = ? AND version = ?`,
		config.Name, config.Path, config.Runtime, config.Body, config.Description, config.TriggerMode,
		optionalString(config.HookStage), optionalString(config.ScheduleCron), boolInt(config.Enabled), config.WorkDir,
		config.TimeoutSeconds, boolInt(config.FailAborts), config.ContextInject, config.SortOrder, config.SourceType,
		input.UpdatedAt, input.ScriptID, input.ProjectID, input.ExpectedVersion)
	if err != nil {
		return domainautomation.Script{}, safeSQLError(ctx, err)
	}
	if err := requireAutomationWrite(result); err != nil {
		return domainautomation.Script{}, err
	}
	if err := transaction.wrote("scripts:update"); err != nil {
		return domainautomation.Script{}, err
	}
	updated, found, err := transaction.GetScript(ctx, input.ProjectID, input.ScriptID)
	if err != nil {
		return domainautomation.Script{}, err
	}
	if !found {
		return domainautomation.Script{}, repository.ErrTransaction
	}
	return updated, nil
}

func (transaction *writeTransaction) DeleteScript(
	ctx context.Context,
	input domainautomation.Delete,
) (domainautomation.Script, error) {
	if input.ProjectID <= 0 || input.ID <= 0 || input.ExpectedVersion <= 0 {
		return domainautomation.Script{}, repository.ErrInvalidAutomation
	}
	current, found, err := transaction.GetScript(ctx, input.ProjectID, input.ID)
	if err != nil {
		return domainautomation.Script{}, err
	}
	if !found {
		return domainautomation.Script{}, repository.ErrNotFound
	}
	if current.Version != input.ExpectedVersion {
		return domainautomation.Script{}, repository.ErrVersionConflict
	}
	result, err := transaction.tx.ExecContext(ctx,
		"DELETE FROM scripts WHERE id = ? AND project_id = ? AND version = ?",
		input.ID, input.ProjectID, input.ExpectedVersion)
	if err != nil {
		return domainautomation.Script{}, safeSQLError(ctx, err)
	}
	if err := requireAutomationWrite(result); err != nil {
		return domainautomation.Script{}, err
	}
	if err := transaction.wrote("scripts:delete"); err != nil {
		return domainautomation.Script{}, err
	}
	return current, nil
}

func (transaction *writeTransaction) ToggleScript(
	ctx context.Context,
	input domainautomation.Toggle,
) (domainautomation.Script, error) {
	if input.ProjectID <= 0 || input.ID <= 0 || input.ExpectedVersion <= 0 ||
		!domainautomation.ValidUTCTimestamp(input.UpdatedAt) {
		return domainautomation.Script{}, repository.ErrInvalidAutomation
	}
	current, found, err := transaction.GetScript(ctx, input.ProjectID, input.ID)
	if err != nil {
		return domainautomation.Script{}, err
	}
	if !found {
		return domainautomation.Script{}, repository.ErrNotFound
	}
	if current.Version != input.ExpectedVersion {
		return domainautomation.Script{}, repository.ErrVersionConflict
	}
	result, err := transaction.tx.ExecContext(ctx,
		`UPDATE scripts SET enabled = ?, updated_at = ?, version = version + 1
		 WHERE id = ? AND project_id = ? AND version = ?`,
		boolInt(!current.Enabled), input.UpdatedAt, input.ID, input.ProjectID, input.ExpectedVersion)
	if err != nil {
		return domainautomation.Script{}, safeSQLError(ctx, err)
	}
	if err := requireAutomationWrite(result); err != nil {
		return domainautomation.Script{}, err
	}
	if err := transaction.wrote("scripts:toggle"); err != nil {
		return domainautomation.Script{}, err
	}
	updated, found, err := transaction.GetScript(ctx, input.ProjectID, input.ID)
	if err != nil {
		return domainautomation.Script{}, err
	}
	if !found {
		return domainautomation.Script{}, repository.ErrTransaction
	}
	return updated, nil
}

func (transaction *writeTransaction) ReorderScripts(
	ctx context.Context,
	input domainautomation.Reorder,
) ([]domainautomation.Script, error) {
	if domainautomation.ValidateReorder(input) != nil {
		return nil, repository.ErrAutomationConflict
	}
	found, err := transaction.projectExists(ctx, input.ProjectID)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, repository.ErrNotFound
	}
	current, err := transaction.listScripts(ctx, input.ProjectID, 0, 0)
	if err != nil {
		return nil, err
	}
	if len(current) != len(input.IDs) {
		return nil, repository.ErrAutomationConflict
	}
	byID := make(map[int64]domainautomation.Script, len(current))
	for _, item := range current {
		byID[item.ID] = item
	}
	for _, id := range input.IDs {
		item, exists := byID[id]
		if !exists {
			return nil, repository.ErrNotFound
		}
		if item.Version != input.ExpectedVersion[id] {
			return nil, repository.ErrVersionConflict
		}
	}
	for index, id := range input.IDs {
		result, updateErr := transaction.tx.ExecContext(ctx,
			`UPDATE scripts SET sort_order = ?, updated_at = ?, version = version + 1
			 WHERE id = ? AND project_id = ? AND version = ?`,
			int64(index+1), input.UpdatedAt, id, input.ProjectID, input.ExpectedVersion[id])
		if updateErr != nil {
			return nil, safeSQLError(ctx, updateErr)
		}
		if err := requireAutomationWrite(result); err != nil {
			return nil, err
		}
		if err := transaction.wrote("scripts:reorder"); err != nil {
			return nil, err
		}
	}
	return transaction.listScripts(ctx, input.ProjectID, 0, 0)
}

func scanScript(row rowScanner) (domainautomation.Script, error) {
	var value domainautomation.Script
	var projectID sql.NullInt64
	var hookStage, scheduleCron, lastStatus, lastLog, lastRunAt sql.NullString
	var lastExitCode, lastDurationMS sql.NullInt64
	var enabled, failAborts int64
	if err := row.Scan(&value.ID, &projectID, &value.Name, &value.Path, &value.Runtime, &value.Body,
		&value.Description, &value.TriggerMode, &hookStage, &scheduleCron, &enabled, &value.WorkDir,
		&value.TimeoutSeconds, &failAborts, &value.ContextInject, &value.SortOrder, &lastStatus,
		&lastExitCode, &lastDurationMS, &lastLog, &lastRunAt, &value.CreatedAt, &value.UpdatedAt,
		&value.SourceType, &value.Version); err != nil {
		return domainautomation.Script{}, err
	}
	if !projectID.Valid || projectID.Int64 <= 0 {
		return domainautomation.Script{}, repository.ErrInvalidStore
	}
	if enabled != 0 && enabled != 1 || failAborts != 0 && failAborts != 1 {
		return domainautomation.Script{}, repository.ErrInvalidStore
	}
	value.ProjectID = int64Pointer(projectID.Int64)
	value.HookStage = nullStringPointer(hookStage)
	value.ScheduleCron = nullStringPointer(scheduleCron)
	value.Enabled = enabled == 1
	value.FailAborts = failAborts == 1
	value.LastStatus = nullStringPointer(lastStatus)
	value.LastExitCode = nullInt64Pointer(lastExitCode)
	value.LastDurationMS = nullInt64Pointer(lastDurationMS)
	value.LastLog = nullStringPointer(lastLog)
	value.LastRunAt = nullStringPointer(lastRunAt)
	if domainautomation.ValidateScriptRecord(value) != nil {
		return domainautomation.Script{}, repository.ErrInvalidStore
	}
	return value, nil
}

func boundedAutomationPage(limit int) int {
	if limit <= 0 {
		return 100
	}
	if limit > maximumAutomationPageSize {
		return maximumAutomationPageSize
	}
	return limit
}

func boolInt(value bool) int64 {
	if value {
		return 1
	}
	return 0
}

func int64Pointer(value int64) *int64 {
	result := value
	return &result
}

func nullInt64Pointer(value sql.NullInt64) *int64 {
	if !value.Valid {
		return nil
	}
	return int64Pointer(value.Int64)
}

func requireAutomationWrite(result sql.Result) error {
	err := requireOneRow(result)
	if err == repository.ErrNotFound {
		return repository.ErrVersionConflict
	}
	return err
}
