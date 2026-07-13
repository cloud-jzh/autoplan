package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"strconv"

	domainautomation "github.com/lyming99/autoplan/backend/internal/domain/automation"
	"github.com/lyming99/autoplan/backend/internal/repository"
)

const executorSelectColumns = `id, project_id, label, type, command, args_json, actions_json,
	options_json, group_kind, group_is_default, presentation_json, problem_matcher_json,
	depends_on_json, depends_order, enabled, sort_order, last_status, last_exit_code,
	last_duration_ms, last_log, last_run_at, plugin_state_json, created_at, updated_at, version`

func (transaction *writeTransaction) ListExecutors(
	ctx context.Context,
	options domainautomation.ListOptions,
) ([]domainautomation.Executor, error) {
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
	return transaction.listExecutors(ctx, options.ProjectID, boundedAutomationPage(options.Limit), options.Offset)
}

func (transaction *writeTransaction) GetExecutor(
	ctx context.Context,
	projectID, executorID int64,
) (domainautomation.Executor, bool, error) {
	if projectID <= 0 || executorID <= 0 {
		return domainautomation.Executor{}, false, nil
	}
	value, err := scanExecutor(transaction.tx.QueryRowContext(ctx,
		"SELECT "+executorSelectColumns+" FROM executors WHERE project_id = ? AND id = ?", projectID, executorID))
	if err == sql.ErrNoRows {
		return domainautomation.Executor{}, false, nil
	}
	if err != nil {
		return domainautomation.Executor{}, false, safeSQLError(ctx, err)
	}
	return value, true, nil
}

func (transaction *writeTransaction) CreateExecutor(
	ctx context.Context,
	input domainautomation.ExecutorCreate,
) (domainautomation.Executor, error) {
	if input.ProjectID <= 0 || !domainautomation.ValidUTCTimestamp(input.CreatedAt) {
		return domainautomation.Executor{}, repository.ErrInvalidAutomation
	}
	config, err := domainautomation.NormalizeExecutorInput(input.Input, nil)
	if err != nil {
		return domainautomation.Executor{}, repository.ErrInvalidAutomation
	}
	found, err := transaction.projectExists(ctx, input.ProjectID)
	if err != nil {
		return domainautomation.Executor{}, err
	}
	if !found {
		return domainautomation.Executor{}, repository.ErrNotFound
	}
	duplicate, err := transaction.executorLabelExists(ctx, input.ProjectID, config.Label, 0)
	if err != nil {
		return domainautomation.Executor{}, err
	}
	if duplicate {
		return domainautomation.Executor{}, repository.ErrDuplicate
	}
	return transaction.insertExecutor(ctx, input.ProjectID, config, input.CreatedAt, "executors:create")
}

func (transaction *writeTransaction) UpdateExecutor(
	ctx context.Context,
	input domainautomation.ExecutorUpdate,
) (domainautomation.Executor, error) {
	if input.ProjectID <= 0 || input.ExecutorID <= 0 || input.ExpectedVersion <= 0 ||
		!domainautomation.ValidUTCTimestamp(input.UpdatedAt) {
		return domainautomation.Executor{}, repository.ErrInvalidAutomation
	}
	current, found, err := transaction.GetExecutor(ctx, input.ProjectID, input.ExecutorID)
	if err != nil {
		return domainautomation.Executor{}, err
	}
	if !found {
		return domainautomation.Executor{}, repository.ErrNotFound
	}
	if current.Version != input.ExpectedVersion {
		return domainautomation.Executor{}, repository.ErrVersionConflict
	}
	config, err := domainautomation.NormalizeExecutorInput(input.Input, &current)
	if err != nil {
		return domainautomation.Executor{}, repository.ErrInvalidAutomation
	}
	duplicate, err := transaction.executorLabelExists(ctx, input.ProjectID, config.Label, input.ExecutorID)
	if err != nil {
		return domainautomation.Executor{}, err
	}
	if duplicate {
		return domainautomation.Executor{}, repository.ErrDuplicate
	}
	result, err := transaction.tx.ExecContext(ctx,
		`UPDATE executors SET label = ?, type = ?, command = ?, args_json = ?, actions_json = ?,
			options_json = ?, group_kind = ?, group_is_default = ?, presentation_json = ?,
			problem_matcher_json = ?, depends_on_json = ?, depends_order = ?, enabled = ?,
			sort_order = ?, updated_at = ?, version = version + 1
		 WHERE id = ? AND project_id = ? AND version = ?`,
		config.Label, config.Type, config.Command, string(config.ArgsJSON), optionalJSON(config.ActionsJSON),
		string(config.OptionsJSON), optionalString(config.GroupKind), boolInt(config.GroupIsDefault),
		string(config.PresentationJSON), optionalJSON(config.ProblemMatcherJSON), string(config.DependsOnJSON),
		config.DependsOrder, boolInt(config.Enabled), config.SortOrder, input.UpdatedAt,
		input.ExecutorID, input.ProjectID, input.ExpectedVersion)
	if err != nil {
		return domainautomation.Executor{}, safeSQLError(ctx, err)
	}
	if err := requireAutomationWrite(result); err != nil {
		return domainautomation.Executor{}, err
	}
	if err := transaction.wrote("executors:update"); err != nil {
		return domainautomation.Executor{}, err
	}
	updated, found, err := transaction.GetExecutor(ctx, input.ProjectID, input.ExecutorID)
	if err != nil {
		return domainautomation.Executor{}, err
	}
	if !found {
		return domainautomation.Executor{}, repository.ErrTransaction
	}
	return updated, nil
}

func (transaction *writeTransaction) DeleteExecutor(
	ctx context.Context,
	input domainautomation.Delete,
) (domainautomation.Executor, error) {
	if input.ProjectID <= 0 || input.ID <= 0 || input.ExpectedVersion <= 0 {
		return domainautomation.Executor{}, repository.ErrInvalidAutomation
	}
	current, found, err := transaction.GetExecutor(ctx, input.ProjectID, input.ID)
	if err != nil {
		return domainautomation.Executor{}, err
	}
	if !found {
		return domainautomation.Executor{}, repository.ErrNotFound
	}
	if current.Version != input.ExpectedVersion {
		return domainautomation.Executor{}, repository.ErrVersionConflict
	}
	result, err := transaction.tx.ExecContext(ctx,
		"DELETE FROM executors WHERE id = ? AND project_id = ? AND version = ?",
		input.ID, input.ProjectID, input.ExpectedVersion)
	if err != nil {
		return domainautomation.Executor{}, safeSQLError(ctx, err)
	}
	if err := requireAutomationWrite(result); err != nil {
		return domainautomation.Executor{}, err
	}
	if err := transaction.wrote("executors:delete"); err != nil {
		return domainautomation.Executor{}, err
	}
	return current, nil
}

func (transaction *writeTransaction) ToggleExecutor(
	ctx context.Context,
	input domainautomation.Toggle,
) (domainautomation.Executor, error) {
	if input.ProjectID <= 0 || input.ID <= 0 || input.ExpectedVersion <= 0 ||
		!domainautomation.ValidUTCTimestamp(input.UpdatedAt) {
		return domainautomation.Executor{}, repository.ErrInvalidAutomation
	}
	current, found, err := transaction.GetExecutor(ctx, input.ProjectID, input.ID)
	if err != nil {
		return domainautomation.Executor{}, err
	}
	if !found {
		return domainautomation.Executor{}, repository.ErrNotFound
	}
	if current.Version != input.ExpectedVersion {
		return domainautomation.Executor{}, repository.ErrVersionConflict
	}
	result, err := transaction.tx.ExecContext(ctx,
		`UPDATE executors SET enabled = ?, updated_at = ?, version = version + 1
		 WHERE id = ? AND project_id = ? AND version = ?`,
		boolInt(!current.Enabled), input.UpdatedAt, input.ID, input.ProjectID, input.ExpectedVersion)
	if err != nil {
		return domainautomation.Executor{}, safeSQLError(ctx, err)
	}
	if err := requireAutomationWrite(result); err != nil {
		return domainautomation.Executor{}, err
	}
	if err := transaction.wrote("executors:toggle"); err != nil {
		return domainautomation.Executor{}, err
	}
	updated, found, err := transaction.GetExecutor(ctx, input.ProjectID, input.ID)
	if err != nil {
		return domainautomation.Executor{}, err
	}
	if !found {
		return domainautomation.Executor{}, repository.ErrTransaction
	}
	return updated, nil
}

func (transaction *writeTransaction) ReorderExecutors(
	ctx context.Context,
	input domainautomation.Reorder,
) ([]domainautomation.Executor, error) {
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
	current, err := transaction.listExecutors(ctx, input.ProjectID, 0, 0)
	if err != nil {
		return nil, err
	}
	if len(current) != len(input.IDs) {
		return nil, repository.ErrAutomationConflict
	}
	byID := make(map[int64]domainautomation.Executor, len(current))
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
			`UPDATE executors SET sort_order = ?, updated_at = ?, version = version + 1
			 WHERE id = ? AND project_id = ? AND version = ?`,
			int64(index+1), input.UpdatedAt, id, input.ProjectID, input.ExpectedVersion[id])
		if updateErr != nil {
			return nil, safeSQLError(ctx, updateErr)
		}
		if err := requireAutomationWrite(result); err != nil {
			return nil, err
		}
		if err := transaction.wrote("executors:reorder"); err != nil {
			return nil, err
		}
	}
	return transaction.listExecutors(ctx, input.ProjectID, 0, 0)
}

func (transaction *writeTransaction) ImportExecutors(
	ctx context.Context,
	input domainautomation.Import,
) ([]domainautomation.Executor, error) {
	if input.ProjectID <= 0 || len(input.Items) == 0 || !domainautomation.ValidUTCTimestamp(input.UpdatedAt) {
		return nil, repository.ErrInvalidAutomation
	}
	found, err := transaction.projectExists(ctx, input.ProjectID)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, repository.ErrNotFound
	}
	existing, err := transaction.listExecutors(ctx, input.ProjectID, 0, 0)
	if err != nil {
		return nil, err
	}
	labels := make(map[string]struct{}, len(existing)+len(input.Items))
	maxSort := int64(0)
	for _, item := range existing {
		labels[item.Label] = struct{}{}
		if item.SortOrder > maxSort {
			maxSort = item.SortOrder
		}
	}
	configs := make([]domainautomation.ExecutorConfig, 0, len(input.Items))
	dedupeLabels := input.DedupeLabels == nil || *input.DedupeLabels
	for index, item := range input.Items {
		config, normalizeErr := domainautomation.NormalizeExecutorInput(item, nil)
		if normalizeErr != nil {
			return nil, repository.ErrInvalidAutomation
		}
		if _, exists := labels[config.Label]; exists {
			if !dedupeLabels {
				return nil, repository.ErrDuplicate
			}
			config.Label = uniqueExecutorLabel(config.Label, labels)
		}
		labels[config.Label] = struct{}{}
		if item.SortOrder == nil {
			config.SortOrder = maxSort + int64(index) + 1
		}
		configs = append(configs, config)
	}
	result := make([]domainautomation.Executor, 0, len(configs))
	for _, config := range configs {
		created, createErr := transaction.insertExecutor(ctx, input.ProjectID, config, input.UpdatedAt, "executors:import")
		if createErr != nil {
			return nil, createErr
		}
		result = append(result, created)
	}
	return result, nil
}

func (transaction *writeTransaction) insertExecutor(
	ctx context.Context,
	projectID int64,
	config domainautomation.ExecutorConfig,
	timestamp, writeLabel string,
) (domainautomation.Executor, error) {
	result, err := transaction.tx.ExecContext(ctx,
		`INSERT INTO executors (
			project_id, label, type, command, args_json, actions_json, options_json, group_kind,
			group_is_default, presentation_json, problem_matcher_json, depends_on_json,
			depends_order, enabled, sort_order, created_at, updated_at, version
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1)`,
		projectID, config.Label, config.Type, config.Command, string(config.ArgsJSON), optionalJSON(config.ActionsJSON),
		string(config.OptionsJSON), optionalString(config.GroupKind), boolInt(config.GroupIsDefault),
		string(config.PresentationJSON), optionalJSON(config.ProblemMatcherJSON), string(config.DependsOnJSON),
		config.DependsOrder, boolInt(config.Enabled), config.SortOrder, timestamp, timestamp)
	if err != nil {
		return domainautomation.Executor{}, safeSQLError(ctx, err)
	}
	id, err := result.LastInsertId()
	if err != nil || id <= 0 {
		return domainautomation.Executor{}, repository.ErrTransaction
	}
	if err := transaction.wrote(writeLabel); err != nil {
		return domainautomation.Executor{}, err
	}
	created, found, err := transaction.GetExecutor(ctx, projectID, id)
	if err != nil {
		return domainautomation.Executor{}, err
	}
	if !found {
		return domainautomation.Executor{}, repository.ErrTransaction
	}
	return created, nil
}

func (transaction *writeTransaction) executorLabelExists(
	ctx context.Context,
	projectID int64,
	label string,
	exceptID int64,
) (bool, error) {
	var exists int
	err := transaction.tx.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM executors WHERE project_id = ? AND label = ? AND id != ?)`,
		projectID, label, exceptID).Scan(&exists)
	if err != nil {
		return false, safeSQLError(ctx, err)
	}
	return exists == 1, nil
}

func (transaction *writeTransaction) listExecutors(
	ctx context.Context,
	projectID int64,
	limit, offset int,
) ([]domainautomation.Executor, error) {
	query := "SELECT " + executorSelectColumns + " FROM executors WHERE project_id = ? ORDER BY sort_order ASC, id ASC"
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
	result := make([]domainautomation.Executor, 0)
	for rows.Next() {
		value, scanErr := scanExecutor(rows)
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

func scanExecutor(row rowScanner) (domainautomation.Executor, error) {
	var value domainautomation.Executor
	var argsJSON, optionsJSON, presentationJSON, dependsOnJSON string
	var actionsJSON, groupKind, problemMatcherJSON, lastStatus, lastLog, lastRunAt, pluginStateJSON sql.NullString
	var groupDefault, enabled int64
	var lastExitCode, lastDurationMS sql.NullInt64
	if err := row.Scan(&value.ID, &value.ProjectID, &value.Label, &value.Type, &value.Command,
		&argsJSON, &actionsJSON, &optionsJSON, &groupKind, &groupDefault, &presentationJSON,
		&problemMatcherJSON, &dependsOnJSON, &value.DependsOrder, &enabled, &value.SortOrder,
		&lastStatus, &lastExitCode, &lastDurationMS, &lastLog, &lastRunAt, &pluginStateJSON,
		&value.CreatedAt, &value.UpdatedAt, &value.Version); err != nil {
		return domainautomation.Executor{}, err
	}
	if groupDefault != 0 && groupDefault != 1 || enabled != 0 && enabled != 1 {
		return domainautomation.Executor{}, repository.ErrInvalidStore
	}
	value.ArgsJSON = json.RawMessage(argsJSON)
	value.ActionsJSON = nullJSONPointer(actionsJSON)
	value.OptionsJSON = json.RawMessage(optionsJSON)
	value.GroupKind = nullStringPointer(groupKind)
	value.GroupIsDefault = groupDefault == 1
	value.PresentationJSON = json.RawMessage(presentationJSON)
	value.ProblemMatcherJSON = nullJSONPointer(problemMatcherJSON)
	value.DependsOnJSON = json.RawMessage(dependsOnJSON)
	value.Enabled = enabled == 1
	value.LastStatus = nullStringPointer(lastStatus)
	value.LastExitCode = nullInt64Pointer(lastExitCode)
	value.LastDurationMS = nullInt64Pointer(lastDurationMS)
	value.LastLog = nullStringPointer(lastLog)
	value.LastRunAt = nullStringPointer(lastRunAt)
	value.PluginStateJSON = nullJSONPointer(pluginStateJSON)
	if domainautomation.ValidateExecutorRecord(value) != nil {
		return domainautomation.Executor{}, repository.ErrInvalidStore
	}
	return value, nil
}

func optionalJSON(value *json.RawMessage) any {
	if value == nil {
		return nil
	}
	return string(*value)
}

func nullJSONPointer(value sql.NullString) *json.RawMessage {
	if !value.Valid {
		return nil
	}
	result := json.RawMessage(value.String)
	return &result
}

func uniqueExecutorLabel(value string, used map[string]struct{}) string {
	if _, exists := used[value]; !exists {
		return value
	}
	for index := 2; ; index++ {
		candidate := value + " (" + decimalString(index) + ")"
		if _, exists := used[candidate]; !exists {
			return candidate
		}
	}
}

func decimalString(value int) string {
	return strconv.Itoa(value)
}
