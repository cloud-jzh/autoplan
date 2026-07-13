package sqlite

import (
	"context"

	domainconfig "github.com/lyming99/autoplan/backend/internal/domain/config"
	"github.com/lyming99/autoplan/backend/internal/repository"
)

func (transaction *writeTransaction) PutLoopConfig(
	ctx context.Context,
	projectID int64,
	expectedVersion int64,
	value repository.LoopConfig,
	updatedAt string,
) (repository.ProjectState, bool, error) {
	if expectedVersion <= 0 {
		return repository.ProjectState{}, false, repository.ErrVersionRequired
	}
	if !domainconfig.ValidUTCTimestamp(updatedAt) {
		return repository.ProjectState{}, false, repository.ErrTransaction
	}
	current, found, err := transaction.GetProjectState(ctx, projectID)
	if err != nil {
		return repository.ProjectState{}, false, err
	}
	if !found {
		return repository.ProjectState{}, false, repository.ErrNotFound
	}
	if current.Version != expectedVersion {
		return repository.ProjectState{}, false, repository.ErrVersionConflict
	}
	normalized, err := domainconfig.NormalizeLoopConfig(value)
	if err != nil {
		return repository.ProjectState{}, false, repository.ErrTransaction
	}
	if domainconfig.Equal(domainconfig.LoopConfigFromState(current), normalized) {
		return current, false, nil
	}
	result, err := transaction.tx.ExecContext(ctx,
		`UPDATE project_states SET
		 interval_seconds = ?, validation_command = ?, project_prompt = ?,
		 agent_cli_provider = ?, agent_cli_command = ?, codex_reasoning_effort = ?,
		 plan_generation_strategy = ?, plan_generation_provider = ?, plan_generation_command = ?,
		 plan_generation_model = ?, plan_generation_codex_reasoning_effort = ?,
		 plan_generation_claude_base_url = ?, plan_generation_claude_auth_token = ?,
		 plan_generation_claude_model = ?, plan_generation_claude_config_id = ?,
		 plan_execution_strategy = ?, plan_execution_provider = ?, plan_execution_command = ?,
		 plan_execution_model = ?, plan_execution_codex_reasoning_effort = ?,
		 plan_execution_claude_base_url = ?, plan_execution_claude_auth_token = ?,
		 plan_execution_claude_model = ?, plan_execution_claude_config_id = ?,
		 env_vars = ?, updated_at = ?, version = version + 1
		 WHERE project_id = ? AND version = ?`,
		normalized.IntervalSeconds, normalized.ValidationCommand, normalized.ProjectPrompt,
		normalized.AgentCLIProvider, normalized.AgentCLICommand, normalized.CodexReasoningEffort,
		normalized.PlanGenerationStrategy, normalized.PlanGenerationProvider, normalized.PlanGenerationCommand,
		normalized.PlanGenerationModel, normalized.PlanGenerationCodexReasoningEffort,
		normalized.PlanGenerationClaudeBaseURL, normalized.PlanGenerationClaudeAuthToken,
		normalized.PlanGenerationClaudeModel, normalized.PlanGenerationClaudeConfigID,
		normalized.PlanExecutionStrategy, normalized.PlanExecutionProvider, normalized.PlanExecutionCommand,
		normalized.PlanExecutionModel, normalized.PlanExecutionCodexReasoningEffort,
		normalized.PlanExecutionClaudeBaseURL, normalized.PlanExecutionClaudeAuthToken,
		normalized.PlanExecutionClaudeModel, normalized.PlanExecutionClaudeConfigID,
		normalized.EnvVars, updatedAt, projectID, expectedVersion,
	)
	if err != nil {
		return repository.ProjectState{}, false, safeSQLError(ctx, err)
	}
	if err := requireOneRow(result); err != nil {
		if err == repository.ErrNotFound {
			return repository.ProjectState{}, false, repository.ErrVersionConflict
		}
		return repository.ProjectState{}, false, err
	}
	if err := transaction.wrote("project_states:configure"); err != nil {
		return repository.ProjectState{}, false, err
	}
	projectResult, err := transaction.tx.ExecContext(ctx,
		"UPDATE projects SET updated_at = ? WHERE id = ?", updatedAt, projectID)
	if err != nil {
		return repository.ProjectState{}, false, safeSQLError(ctx, err)
	}
	if err := requireOneRow(projectResult); err != nil {
		return repository.ProjectState{}, false, err
	}
	if err := transaction.wrote("projects:configure"); err != nil {
		return repository.ProjectState{}, false, err
	}
	next := stateWithLoopConfig(current, normalized)
	next.UpdatedAt = updatedAt
	next.Version++
	return next, true, nil
}

func (transaction *writeTransaction) ResetLoopConfig(
	ctx context.Context,
	projectID int64,
	expectedVersion int64,
	updatedAt string,
) (repository.ProjectState, bool, error) {
	return transaction.PutLoopConfig(ctx, projectID, expectedVersion, domainconfig.DefaultLoopConfig(), updatedAt)
}

func stateWithLoopConfig(state repository.ProjectState, value repository.LoopConfig) repository.ProjectState {
	state.IntervalSeconds = value.IntervalSeconds
	state.ValidationCommand = value.ValidationCommand
	state.ProjectPrompt = value.ProjectPrompt
	state.AgentCLIProvider = value.AgentCLIProvider
	state.AgentCLICommand = value.AgentCLICommand
	state.CodexReasoningEffort = copyString(value.CodexReasoningEffort)
	state.PlanGenerationStrategy = value.PlanGenerationStrategy
	state.PlanGenerationProvider = copyString(value.PlanGenerationProvider)
	state.PlanGenerationCommand = value.PlanGenerationCommand
	state.PlanGenerationModel = value.PlanGenerationModel
	state.PlanGenerationCodexReasoningEffort = copyString(value.PlanGenerationCodexReasoningEffort)
	state.PlanGenerationClaudeBaseURL = value.PlanGenerationClaudeBaseURL
	state.PlanGenerationClaudeAuthToken = value.PlanGenerationClaudeAuthToken
	state.PlanGenerationClaudeModel = value.PlanGenerationClaudeModel
	state.PlanGenerationClaudeConfigID = value.PlanGenerationClaudeConfigID
	state.PlanExecutionStrategy = value.PlanExecutionStrategy
	state.PlanExecutionProvider = copyString(value.PlanExecutionProvider)
	state.PlanExecutionCommand = value.PlanExecutionCommand
	state.PlanExecutionModel = value.PlanExecutionModel
	state.PlanExecutionCodexReasoningEffort = copyString(value.PlanExecutionCodexReasoningEffort)
	state.PlanExecutionClaudeBaseURL = value.PlanExecutionClaudeBaseURL
	state.PlanExecutionClaudeAuthToken = value.PlanExecutionClaudeAuthToken
	state.PlanExecutionClaudeModel = value.PlanExecutionClaudeModel
	state.PlanExecutionClaudeConfigID = value.PlanExecutionClaudeConfigID
	state.EnvVars = value.EnvVars
	return state
}
