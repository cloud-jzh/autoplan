package sqlite

import (
	"context"
	"database/sql"

	"github.com/lyming99/autoplan/backend/internal/repository"
)

const projectStateSelectColumns = `project_id, running, phase, interval_seconds,
	validation_command, project_prompt, agent_cli_provider, agent_cli_command, codex_reasoning_effort,
	plan_generation_strategy, plan_generation_provider, plan_generation_command, plan_generation_model,
	plan_generation_codex_reasoning_effort, plan_generation_claude_base_url,
	plan_generation_claude_auth_token, plan_generation_claude_model, plan_generation_claude_config_id,
	plan_execution_strategy, plan_execution_provider, plan_execution_command, plan_execution_model,
	plan_execution_codex_reasoning_effort, plan_execution_claude_base_url,
	plan_execution_claude_auth_token, plan_execution_claude_model, plan_execution_claude_config_id,
	last_issue_hash, last_error, env_vars, updated_at, version`

func decodeProjectStates(rows []tableRow) (map[int64]repository.ProjectState, error) {
	states := make(map[int64]repository.ProjectState, len(rows))
	for _, row := range rows {
		if len(row.values) != 31 {
			return nil, repository.ErrSchemaDrift
		}
		state, err := decodeProjectState(row.values)
		if err != nil || state.ProjectID <= 0 {
			return nil, repository.ErrSchemaDrift
		}
		if _, duplicate := states[state.ProjectID]; duplicate {
			return nil, repository.ErrSchemaDrift
		}
		states[state.ProjectID] = state
	}
	return states, nil
}

func decodeProjectState(values []value) (repository.ProjectState, error) {
	integer := func(index int) (int64, error) {
		result, ok := integerValue(values[index])
		if !ok {
			return 0, repository.ErrSchemaDrift
		}
		return result, nil
	}
	text := func(index int) (string, error) {
		result, ok := textValue(values[index])
		if !ok {
			return "", repository.ErrSchemaDrift
		}
		return result, nil
	}
	nullable := func(index int) (*string, error) {
		result, ok := nullableTextValue(values[index])
		if !ok {
			return nil, repository.ErrSchemaDrift
		}
		return result, nil
	}

	var result repository.ProjectState
	var err error
	if result.ProjectID, err = integer(0); err != nil {
		return result, err
	}
	if result.Running, err = integer(1); err != nil {
		return result, err
	}
	if result.Phase, err = text(2); err != nil {
		return result, err
	}
	if result.IntervalSeconds, err = integer(3); err != nil {
		return result, err
	}
	if result.ValidationCommand, err = text(4); err != nil {
		return result, err
	}
	if result.ProjectPrompt, err = text(5); err != nil {
		return result, err
	}
	if result.AgentCLIProvider, err = text(6); err != nil {
		return result, err
	}
	if result.AgentCLICommand, err = text(7); err != nil {
		return result, err
	}
	if result.CodexReasoningEffort, err = nullable(8); err != nil {
		return result, err
	}
	if result.PlanGenerationStrategy, err = text(9); err != nil {
		return result, err
	}
	if result.PlanGenerationProvider, err = nullable(10); err != nil {
		return result, err
	}
	if result.PlanGenerationCommand, err = text(11); err != nil {
		return result, err
	}
	if result.PlanGenerationModel, err = text(12); err != nil {
		return result, err
	}
	if result.PlanGenerationCodexReasoningEffort, err = nullable(13); err != nil {
		return result, err
	}
	if result.PlanGenerationClaudeBaseURL, err = text(14); err != nil {
		return result, err
	}
	if result.PlanGenerationClaudeAuthToken, err = text(15); err != nil {
		return result, err
	}
	if result.PlanGenerationClaudeModel, err = text(16); err != nil {
		return result, err
	}
	if result.PlanExecutionStrategy, err = text(17); err != nil {
		return result, err
	}
	if result.PlanExecutionProvider, err = nullable(18); err != nil {
		return result, err
	}
	if result.PlanExecutionCommand, err = text(19); err != nil {
		return result, err
	}
	if result.PlanExecutionModel, err = text(20); err != nil {
		return result, err
	}
	if result.PlanExecutionCodexReasoningEffort, err = nullable(21); err != nil {
		return result, err
	}
	if result.PlanExecutionClaudeBaseURL, err = text(22); err != nil {
		return result, err
	}
	if result.PlanExecutionClaudeAuthToken, err = text(23); err != nil {
		return result, err
	}
	if result.PlanExecutionClaudeModel, err = text(24); err != nil {
		return result, err
	}
	if result.LastIssueHash, err = nullable(25); err != nil {
		return result, err
	}
	if result.LastError, err = nullable(26); err != nil {
		return result, err
	}
	if result.EnvVars, err = text(27); err != nil {
		return result, err
	}
	if result.UpdatedAt, err = text(28); err != nil {
		return result, err
	}
	if result.PlanGenerationClaudeConfigID, err = integer(29); err != nil {
		return result, err
	}
	if result.PlanExecutionClaudeConfigID, err = integer(30); err != nil {
		return result, err
	}
	return result, nil
}

func (reader *Reader) GetProjectState(ctx context.Context, projectID int64) (repository.ProjectState, bool, error) {
	reader.mu.RLock()
	defer reader.mu.RUnlock()
	if err := reader.guard(ctx); err != nil {
		return repository.ProjectState{}, false, err
	}
	if projectID <= 0 {
		return repository.ProjectState{}, false, nil
	}
	state, exists := reader.states[projectID]
	if !exists {
		return repository.ProjectState{}, false, nil
	}
	state.CodexReasoningEffort = copyString(state.CodexReasoningEffort)
	state.PlanGenerationProvider = copyString(state.PlanGenerationProvider)
	state.PlanGenerationCodexReasoningEffort = copyString(state.PlanGenerationCodexReasoningEffort)
	state.PlanExecutionProvider = copyString(state.PlanExecutionProvider)
	state.PlanExecutionCodexReasoningEffort = copyString(state.PlanExecutionCodexReasoningEffort)
	state.LastIssueHash = copyString(state.LastIssueHash)
	state.LastError = copyString(state.LastError)
	return state, true, nil
}

func copyString(source *string) *string {
	if source == nil {
		return nil
	}
	result := *source
	return &result
}

func scanSQLProjectState(row rowScanner) (repository.ProjectState, error) {
	var result repository.ProjectState
	var codexEffort, generationProvider, generationEffort sql.NullString
	var executionProvider, executionEffort, lastIssueHash, lastError sql.NullString
	if err := row.Scan(
		&result.ProjectID, &result.Running, &result.Phase, &result.IntervalSeconds,
		&result.ValidationCommand, &result.ProjectPrompt, &result.AgentCLIProvider, &result.AgentCLICommand, &codexEffort,
		&result.PlanGenerationStrategy, &generationProvider, &result.PlanGenerationCommand, &result.PlanGenerationModel,
		&generationEffort, &result.PlanGenerationClaudeBaseURL, &result.PlanGenerationClaudeAuthToken,
		&result.PlanGenerationClaudeModel, &result.PlanGenerationClaudeConfigID,
		&result.PlanExecutionStrategy, &executionProvider, &result.PlanExecutionCommand, &result.PlanExecutionModel,
		&executionEffort, &result.PlanExecutionClaudeBaseURL, &result.PlanExecutionClaudeAuthToken,
		&result.PlanExecutionClaudeModel, &result.PlanExecutionClaudeConfigID,
		&lastIssueHash, &lastError, &result.EnvVars, &result.UpdatedAt, &result.Version,
	); err != nil {
		return repository.ProjectState{}, err
	}
	result.CodexReasoningEffort = nullStringPointer(codexEffort)
	result.PlanGenerationProvider = nullStringPointer(generationProvider)
	result.PlanGenerationCodexReasoningEffort = nullStringPointer(generationEffort)
	result.PlanExecutionProvider = nullStringPointer(executionProvider)
	result.PlanExecutionCodexReasoningEffort = nullStringPointer(executionEffort)
	result.LastIssueHash = nullStringPointer(lastIssueHash)
	result.LastError = nullStringPointer(lastError)
	return result, nil
}

func nullStringPointer(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	result := value.String
	return &result
}

func (transaction *writeTransaction) GetProjectState(
	ctx context.Context,
	projectID int64,
) (repository.ProjectState, bool, error) {
	if projectID <= 0 {
		return repository.ProjectState{}, false, nil
	}
	state, err := scanSQLProjectState(transaction.tx.QueryRowContext(ctx,
		"SELECT "+projectStateSelectColumns+" FROM project_states WHERE project_id = ?", projectID))
	if err == sql.ErrNoRows {
		return repository.ProjectState{}, false, nil
	}
	if err != nil {
		return repository.ProjectState{}, false, safeSQLError(ctx, err)
	}
	return state, true, nil
}

func (transaction *writeTransaction) insertProjectState(ctx context.Context, state repository.ProjectState) error {
	_, err := transaction.tx.ExecContext(ctx,
		`INSERT INTO project_states (
		 project_id, running, phase, interval_seconds, validation_command, project_prompt,
		 agent_cli_provider, agent_cli_command, codex_reasoning_effort,
		 plan_generation_strategy, plan_generation_provider, plan_generation_command, plan_generation_model,
		 plan_generation_codex_reasoning_effort, plan_generation_claude_base_url,
		 plan_generation_claude_auth_token, plan_generation_claude_model, plan_generation_claude_config_id,
		 plan_execution_strategy, plan_execution_provider, plan_execution_command, plan_execution_model,
		 plan_execution_codex_reasoning_effort, plan_execution_claude_base_url,
		 plan_execution_claude_auth_token, plan_execution_claude_model, plan_execution_claude_config_id,
		 last_issue_hash, last_error, env_vars, updated_at, version
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		state.ProjectID, state.Running, state.Phase, state.IntervalSeconds,
		state.ValidationCommand, state.ProjectPrompt, state.AgentCLIProvider, state.AgentCLICommand, state.CodexReasoningEffort,
		state.PlanGenerationStrategy, state.PlanGenerationProvider, state.PlanGenerationCommand, state.PlanGenerationModel,
		state.PlanGenerationCodexReasoningEffort, state.PlanGenerationClaudeBaseURL,
		state.PlanGenerationClaudeAuthToken, state.PlanGenerationClaudeModel, state.PlanGenerationClaudeConfigID,
		state.PlanExecutionStrategy, state.PlanExecutionProvider, state.PlanExecutionCommand, state.PlanExecutionModel,
		state.PlanExecutionCodexReasoningEffort, state.PlanExecutionClaudeBaseURL,
		state.PlanExecutionClaudeAuthToken, state.PlanExecutionClaudeModel, state.PlanExecutionClaudeConfigID,
		state.LastIssueHash, state.LastError, state.EnvVars, state.UpdatedAt, state.Version,
	)
	if err != nil {
		return safeSQLError(ctx, err)
	}
	return transaction.wrote("project_states:create")
}
