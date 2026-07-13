package sqlite

import (
	"context"
	"database/sql"
	"strings"

	domainintake "github.com/lyming99/autoplan/backend/internal/domain/intake"
	"github.com/lyming99/autoplan/backend/internal/repository"
)

const maximumIntakePageSize = 200

func intakeSelectColumns(intakeType domainintake.Type) string {
	common := `id, project_id, %s, title, body, status,
		agent_cli_provider, agent_cli_command, codex_reasoning_effort,
		plan_generation_strategy, plan_generation_provider, plan_generation_command,
		plan_generation_model, plan_generation_codex_reasoning_effort,
		plan_generation_claude_base_url, plan_generation_claude_auth_token,
		plan_generation_claude_model, plan_generation_claude_config_id,
		COALESCE(generate_fail_count, 0), last_generate_fail_at, last_generate_error,
		last_generate_log_file, last_generate_agent_cli_provider,
		last_generate_codex_reasoning_effort, %s, %s, linked_plan_id,
		created_at, updated_at, accepted_at, %s`
	if intakeType == domainintake.Feedback {
		return formatColumns(common, "requirement_id", "NULL", "NULL", "agent_cli_session_id")
	}
	return formatColumns(common, "NULL", "source_path", "source_hash", "NULL")
}

func formatColumns(template string, values ...string) string {
	for _, value := range values {
		template = strings.Replace(template, "%s", value, 1)
	}
	return template
}

func (transaction *writeTransaction) ListIntakes(
	ctx context.Context,
	options domainintake.ListOptions,
) ([]domainintake.Intake, error) {
	if options.ProjectID <= 0 || !options.Type.Valid() || options.Offset < 0 ||
		(options.Status != nil && !options.Status.Valid()) {
		return nil, repository.ErrInvalidIntake
	}
	limit := options.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > maximumIntakePageSize {
		limit = maximumIntakePageSize
	}
	found, err := transaction.projectExists(ctx, options.ProjectID)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, repository.ErrNotFound
	}
	query := "SELECT " + intakeSelectColumns(options.Type) + " FROM " + options.Type.Table() + " WHERE project_id = ?"
	arguments := []any{options.ProjectID}
	if options.Status != nil {
		query += " AND status = ?"
		arguments = append(arguments, string(*options.Status))
	}
	query += " ORDER BY updated_at DESC, id DESC LIMIT ? OFFSET ?"
	arguments = append(arguments, limit, options.Offset)
	rows, err := transaction.tx.QueryContext(ctx, query, arguments...)
	if err != nil {
		return nil, safeSQLError(ctx, err)
	}
	defer rows.Close()
	result := make([]domainintake.Intake, 0)
	for rows.Next() {
		value, scanErr := scanIntake(rows, options.Type)
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

func (transaction *writeTransaction) GetIntake(
	ctx context.Context,
	projectID int64,
	intakeType domainintake.Type,
	intakeID int64,
) (domainintake.Intake, bool, error) {
	if projectID <= 0 || intakeID <= 0 || !intakeType.Valid() {
		return domainintake.Intake{}, false, nil
	}
	value, err := scanIntake(transaction.tx.QueryRowContext(ctx,
		"SELECT "+intakeSelectColumns(intakeType)+" FROM "+intakeType.Table()+" WHERE project_id = ? AND id = ?",
		projectID, intakeID), intakeType)
	if err == sql.ErrNoRows {
		return domainintake.Intake{}, false, nil
	}
	if err != nil {
		return domainintake.Intake{}, false, safeSQLError(ctx, err)
	}
	return value, true, nil
}

func (transaction *writeTransaction) FindDuplicateIntake(
	ctx context.Context,
	query domainintake.DuplicateQuery,
) (domainintake.Intake, bool, error) {
	if query.ProjectID <= 0 || !query.Type.Valid() ||
		(query.Type == domainintake.Requirement && query.RequirementID != nil) ||
		(query.RequirementID != nil && *query.RequirementID <= 0) {
		return domainintake.Intake{}, false, repository.ErrInvalidIntake
	}
	statement := "SELECT " + intakeSelectColumns(query.Type) + " FROM " + query.Type.Table() +
		" WHERE project_id = ? AND COALESCE(status, 'open') <> 'closed'"
	arguments := []any{query.ProjectID}
	if query.Type == domainintake.Feedback {
		if query.RequirementID == nil {
			statement += " AND requirement_id IS NULL"
		} else {
			statement += " AND requirement_id = ?"
			arguments = append(arguments, *query.RequirementID)
		}
	}
	statement += " ORDER BY id ASC"
	rows, err := transaction.tx.QueryContext(ctx, statement, arguments...)
	if err != nil {
		return domainintake.Intake{}, false, safeSQLError(ctx, err)
	}
	defer rows.Close()
	for rows.Next() {
		candidate, scanErr := scanIntake(rows, query.Type)
		if scanErr != nil {
			return domainintake.Intake{}, false, safeSQLError(ctx, scanErr)
		}
		if domainintake.DuplicateEquivalent(candidate.Title, candidate.Body, query.Title, query.Body) {
			return candidate, true, nil
		}
	}
	if err := rows.Err(); err != nil {
		return domainintake.Intake{}, false, safeSQLError(ctx, err)
	}
	return domainintake.Intake{}, false, nil
}

func (transaction *writeTransaction) CreateIntake(
	ctx context.Context,
	input domainintake.Create,
) (domainintake.Intake, error) {
	input = domainintake.NormalizeCreate(input)
	if domainintake.ValidateCreate(input) != nil {
		return domainintake.Intake{}, repository.ErrInvalidIntake
	}
	projectFound, err := transaction.projectExists(ctx, input.ProjectID)
	if err != nil {
		return domainintake.Intake{}, err
	}
	if !projectFound {
		return domainintake.Intake{}, repository.ErrNotFound
	}
	if input.Type == domainintake.Feedback && input.RequirementID != nil {
		if err := transaction.validateRequirementProject(ctx, input.ProjectID, *input.RequirementID); err != nil {
			return domainintake.Intake{}, err
		}
	}
	if _, duplicate, err := transaction.FindDuplicateIntake(ctx, domainintake.DuplicateQuery{
		ProjectID: input.ProjectID, Type: input.Type, RequirementID: input.RequirementID,
		Title: input.Title, Body: input.Body,
	}); err != nil {
		return domainintake.Intake{}, err
	} else if duplicate {
		return domainintake.Intake{}, repository.ErrDuplicate
	}

	columns := "project_id, title, body, status"
	values := "?, ?, ?, ?"
	arguments := []any{input.ProjectID, input.Title, input.Body, string(input.Status)}
	if input.Type == domainintake.Feedback {
		columns = "project_id, requirement_id, title, body, status"
		values = "?, ?, ?, ?, ?"
		arguments = []any{input.ProjectID, optionalInt64(input.RequirementID), input.Title, input.Body, string(input.Status)}
	}
	columns += `, agent_cli_provider, agent_cli_command, codex_reasoning_effort,
		plan_generation_strategy, plan_generation_provider, plan_generation_command,
		plan_generation_model, plan_generation_codex_reasoning_effort,
		plan_generation_claude_base_url, plan_generation_claude_auth_token,
		plan_generation_claude_model, plan_generation_claude_config_id, created_at, updated_at`
	values += ", ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?"
	arguments = append(arguments,
		optionalString(input.AgentCLI.Provider), input.AgentCLI.Command, optionalString(input.AgentCLI.CodexReasoningEffort),
		optionalString(input.PlanGeneration.Strategy), optionalString(input.PlanGeneration.Provider), input.PlanGeneration.Command,
		input.PlanGeneration.Model, optionalString(input.PlanGeneration.CodexReasoningEffort),
		input.PlanGeneration.ClaudeBaseURL, input.PlanGeneration.ClaudeAuthToken,
		input.PlanGeneration.ClaudeModel, input.PlanGeneration.ClaudeConfigID, input.CreatedAt, input.UpdatedAt,
	)
	result, err := transaction.tx.ExecContext(ctx,
		"INSERT INTO "+input.Type.Table()+" ("+columns+") VALUES ("+values+")", arguments...)
	if err != nil {
		return domainintake.Intake{}, safeSQLError(ctx, err)
	}
	intakeID, err := result.LastInsertId()
	if err != nil || intakeID <= 0 {
		return domainintake.Intake{}, repository.ErrTransaction
	}
	if err := transaction.wrote("intake:create"); err != nil {
		return domainintake.Intake{}, err
	}
	created, found, err := transaction.GetIntake(ctx, input.ProjectID, input.Type, intakeID)
	if err != nil {
		return domainintake.Intake{}, err
	}
	if !found {
		return domainintake.Intake{}, repository.ErrTransaction
	}
	return created, nil
}

func (transaction *writeTransaction) UpdateIntake(
	ctx context.Context,
	projectID int64,
	intakeType domainintake.Type,
	intakeID int64,
	update domainintake.Update,
) (domainintake.Intake, error) {
	update = domainintake.NormalizeUpdate(update)
	if projectID <= 0 || intakeID <= 0 || domainintake.ValidateUpdate(intakeType, update) != nil {
		return domainintake.Intake{}, repository.ErrInvalidIntake
	}
	if _, found, err := transaction.GetIntake(ctx, projectID, intakeType, intakeID); err != nil {
		return domainintake.Intake{}, err
	} else if !found {
		return domainintake.Intake{}, repository.ErrNotFound
	}
	if intakeType == domainintake.Feedback && update.RequirementID != nil {
		if err := transaction.validateRequirementProject(ctx, projectID, *update.RequirementID); err != nil {
			return domainintake.Intake{}, err
		}
	}
	setClause := `title = ?, body = ?, status = ?, agent_cli_provider = ?, agent_cli_command = ?,
		codex_reasoning_effort = ?, plan_generation_strategy = ?, plan_generation_provider = ?,
		plan_generation_command = ?, plan_generation_model = ?, plan_generation_codex_reasoning_effort = ?,
		plan_generation_claude_base_url = ?, plan_generation_claude_auth_token = ?,
		plan_generation_claude_model = ?, plan_generation_claude_config_id = ?,
		generate_fail_count = ?, last_generate_fail_at = ?, last_generate_error = ?,
		last_generate_log_file = ?, last_generate_agent_cli_provider = ?,
		last_generate_codex_reasoning_effort = ?, accepted_at = ?, updated_at = ?`
	arguments := []any{
		update.Title, update.Body, string(update.Status), optionalString(update.AgentCLI.Provider), update.AgentCLI.Command,
		optionalString(update.AgentCLI.CodexReasoningEffort), optionalString(update.PlanGeneration.Strategy),
		optionalString(update.PlanGeneration.Provider), update.PlanGeneration.Command, update.PlanGeneration.Model,
		optionalString(update.PlanGeneration.CodexReasoningEffort), update.PlanGeneration.ClaudeBaseURL,
		update.PlanGeneration.ClaudeAuthToken, update.PlanGeneration.ClaudeModel, update.PlanGeneration.ClaudeConfigID,
		update.Failure.Count, optionalString(update.Failure.LastFailedAt), optionalString(update.Failure.LastError),
		optionalString(update.Failure.LastLogRef), optionalString(update.Failure.LastAgentCLIProvider),
		optionalString(update.Failure.LastCodexEffort), optionalString(update.AcceptedAt), update.UpdatedAt,
	}
	if intakeType == domainintake.Feedback {
		setClause = "requirement_id = ?, " + setClause + ", agent_cli_session_id = ?"
		arguments = append([]any{optionalInt64(update.RequirementID)}, arguments...)
		arguments = append(arguments, optionalString(update.SessionID))
	}
	arguments = append(arguments, projectID, intakeID)
	result, err := transaction.tx.ExecContext(ctx,
		"UPDATE "+intakeType.Table()+" SET "+setClause+" WHERE project_id = ? AND id = ?", arguments...)
	if err != nil {
		return domainintake.Intake{}, safeSQLError(ctx, err)
	}
	if err := requireOneRow(result); err != nil {
		return domainintake.Intake{}, err
	}
	if err := transaction.wrote("intake:update"); err != nil {
		return domainintake.Intake{}, err
	}
	updated, found, err := transaction.GetIntake(ctx, projectID, intakeType, intakeID)
	if err != nil {
		return domainintake.Intake{}, err
	}
	if !found {
		return domainintake.Intake{}, repository.ErrTransaction
	}
	return updated, nil
}

func (transaction *writeTransaction) SetIntakeAcceptance(
	ctx context.Context,
	projectID int64,
	intakeType domainintake.Type,
	intakeID int64,
	acceptedAt *string,
	updatedAt string,
) (domainintake.Intake, error) {
	if projectID <= 0 || intakeID <= 0 || !intakeType.Valid() || !domainintake.ValidUTCTimestamp(updatedAt) ||
		(acceptedAt != nil && !domainintake.ValidUTCTimestamp(*acceptedAt)) {
		return domainintake.Intake{}, repository.ErrInvalidIntake
	}
	result, err := transaction.tx.ExecContext(ctx,
		"UPDATE "+intakeType.Table()+" SET accepted_at = ?, updated_at = ? WHERE project_id = ? AND id = ?",
		optionalString(acceptedAt), updatedAt, projectID, intakeID)
	if err != nil {
		return domainintake.Intake{}, safeSQLError(ctx, err)
	}
	if err := requireOneRow(result); err != nil {
		return domainintake.Intake{}, err
	}
	if err := transaction.wrote("intake:acceptance"); err != nil {
		return domainintake.Intake{}, err
	}
	updated, found, err := transaction.GetIntake(ctx, projectID, intakeType, intakeID)
	if err != nil {
		return domainintake.Intake{}, err
	}
	if !found {
		return domainintake.Intake{}, repository.ErrTransaction
	}
	return updated, nil
}

func scanIntake(row rowScanner, intakeType domainintake.Type) (domainintake.Intake, error) {
	var result domainintake.Intake
	var requirementID, linkedPlanID, claudeConfigID sql.NullInt64
	var agentProvider, effort, strategy, planProvider, planEffort sql.NullString
	var failedAt, lastError, lastLog, lastProvider, lastEffort sql.NullString
	var sourceRef, sourceDigest, acceptedAt, sessionID sql.NullString
	var status string
	if err := row.Scan(
		&result.ID, &result.ProjectID, &requirementID, &result.Title, &result.Body, &status,
		&agentProvider, &result.AgentCLI.Command, &effort,
		&strategy, &planProvider, &result.PlanGeneration.Command, &result.PlanGeneration.Model, &planEffort,
		&result.PlanGeneration.ClaudeBaseURL, &result.PlanGeneration.ClaudeAuthToken,
		&result.PlanGeneration.ClaudeModel, &claudeConfigID,
		&result.Failure.Count, &failedAt, &lastError, &lastLog, &lastProvider, &lastEffort,
		&sourceRef, &sourceDigest, &linkedPlanID, &result.CreatedAt, &result.UpdatedAt, &acceptedAt, &sessionID,
	); err != nil {
		return domainintake.Intake{}, err
	}
	result.Type = intakeType
	result.Status = domainintake.Status(status)
	result.RequirementID = nullInt64Pointer(requirementID)
	result.LinkedPlanID = nullInt64Pointer(linkedPlanID)
	result.AgentCLI.Provider = nullStringPointer(agentProvider)
	result.AgentCLI.CodexReasoningEffort = nullStringPointer(effort)
	result.PlanGeneration.Strategy = nullStringPointer(strategy)
	result.PlanGeneration.Provider = nullStringPointer(planProvider)
	result.PlanGeneration.CodexReasoningEffort = nullStringPointer(planEffort)
	if claudeConfigID.Valid {
		result.PlanGeneration.ClaudeConfigID = claudeConfigID.Int64
	}
	result.Failure.LastFailedAt = nullStringPointer(failedAt)
	result.Failure.LastError = nullStringPointer(lastError)
	result.Failure.LastLogRef = nullStringPointer(lastLog)
	result.Failure.LastAgentCLIProvider = nullStringPointer(lastProvider)
	result.Failure.LastCodexEffort = nullStringPointer(lastEffort)
	result.SourceRef = nullStringPointer(sourceRef)
	result.SourceDigest = nullStringPointer(sourceDigest)
	result.AcceptedAt = nullStringPointer(acceptedAt)
	result.SessionID = nullStringPointer(sessionID)
	if domainintake.ValidateRecord(result) != nil {
		return domainintake.Intake{}, repository.ErrInvalidStore
	}
	return result, nil
}

func (transaction *writeTransaction) projectExists(ctx context.Context, projectID int64) (bool, error) {
	var value int
	err := transaction.tx.QueryRowContext(ctx, "SELECT 1 FROM projects WHERE id = ?", projectID).Scan(&value)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, safeSQLError(ctx, err)
	}
	return true, nil
}

func (transaction *writeTransaction) validateRequirementProject(ctx context.Context, projectID, requirementID int64) error {
	var ownerProjectID sql.NullInt64
	err := transaction.tx.QueryRowContext(ctx, "SELECT project_id FROM requirements WHERE id = ?", requirementID).Scan(&ownerProjectID)
	if err == sql.ErrNoRows {
		return repository.ErrRequirementMissing
	}
	if err != nil {
		return safeSQLError(ctx, err)
	}
	if !ownerProjectID.Valid || ownerProjectID.Int64 != projectID {
		return repository.ErrProjectMismatch
	}
	return nil
}

func optionalString(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}

func optionalInt64(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}
