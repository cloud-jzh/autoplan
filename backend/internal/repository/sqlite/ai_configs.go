package sqlite

import (
	"context"
	"database/sql"
	"strings"

	domainconfig "github.com/lyming99/autoplan/backend/internal/domain/config"
	"github.com/lyming99/autoplan/backend/internal/repository"
)

const aiConfigSelectColumns = "id, project_id, name, provider, base_url, api_key, model, temperature, thinking_depth, thinking_budget_tokens, created_at, updated_at, version"

func (transaction *writeTransaction) ListAIConfigs(ctx context.Context) ([]domainconfig.AIConfig, error) {
	rows, err := transaction.tx.QueryContext(ctx, "SELECT "+aiConfigSelectColumns+" FROM ai_configs WHERE project_id IS NULL ORDER BY id ASC")
	if err != nil {
		return nil, safeSQLError(ctx, err)
	}
	defer rows.Close()
	result := make([]domainconfig.AIConfig, 0)
	for rows.Next() {
		value, scanErr := scanAIConfig(rows)
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

func (transaction *writeTransaction) GetAIConfig(ctx context.Context, id int64) (domainconfig.AIConfig, bool, error) {
	if id <= 0 {
		return domainconfig.AIConfig{}, false, nil
	}
	value, err := scanAIConfig(transaction.tx.QueryRowContext(ctx,
		"SELECT "+aiConfigSelectColumns+" FROM ai_configs WHERE id = ? AND project_id IS NULL", id))
	if err == sql.ErrNoRows {
		return domainconfig.AIConfig{}, false, nil
	}
	if err != nil {
		return domainconfig.AIConfig{}, false, safeSQLError(ctx, err)
	}
	return value, true, nil
}

func (transaction *writeTransaction) CreateAIConfig(ctx context.Context, input domainconfig.AIConfigInput, createdAt string) (domainconfig.AIConfig, error) {
	if !domainconfig.ValidUTCTimestamp(createdAt) {
		return domainconfig.AIConfig{}, repository.ErrInvalidAutomation
	}
	config, err := domainconfig.NormalizeAIConfig(input, nil)
	if err != nil {
		return domainconfig.AIConfig{}, repository.ErrInvalidAutomation
	}
	apiKey := ""
	if input.APIKey != nil {
		apiKey = strings.TrimSpace(*input.APIKey)
	}
	result, err := transaction.tx.ExecContext(ctx,
		`INSERT INTO ai_configs (project_id, name, provider, base_url, api_key, model, temperature,
			thinking_depth, thinking_budget_tokens, created_at, updated_at, version)
		 VALUES (NULL, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1)`, config.Name, config.Provider, config.BaseURL, apiKey,
		config.Model, config.Temperature, optionalString(config.ThinkingDepth), optionalInt64(config.ThinkingBudgetTokens), createdAt, createdAt)
	if err != nil {
		return domainconfig.AIConfig{}, safeSQLError(ctx, err)
	}
	id, err := result.LastInsertId()
	if err != nil || id <= 0 {
		return domainconfig.AIConfig{}, repository.ErrTransaction
	}
	if err := transaction.wrote("ai-configs:create"); err != nil {
		return domainconfig.AIConfig{}, err
	}
	created, found, err := transaction.GetAIConfig(ctx, id)
	if err != nil || !found {
		if err != nil {
			return domainconfig.AIConfig{}, err
		}
		return domainconfig.AIConfig{}, repository.ErrTransaction
	}
	return created, nil
}

func (transaction *writeTransaction) UpdateAIConfig(ctx context.Context, id, expectedVersion int64, input domainconfig.AIConfigInput, updatedAt string) (domainconfig.AIConfig, error) {
	if id <= 0 || expectedVersion <= 0 || !domainconfig.ValidUTCTimestamp(updatedAt) {
		return domainconfig.AIConfig{}, repository.ErrInvalidAutomation
	}
	current, found, err := transaction.GetAIConfig(ctx, id)
	if err != nil {
		return domainconfig.AIConfig{}, err
	}
	if !found {
		return domainconfig.AIConfig{}, repository.ErrNotFound
	}
	if current.Version != expectedVersion {
		return domainconfig.AIConfig{}, repository.ErrVersionConflict
	}
	next, err := domainconfig.NormalizeAIConfig(input, &current)
	if err != nil {
		return domainconfig.AIConfig{}, repository.ErrInvalidAutomation
	}
	apiKeyPresent, apiKey := 0, ""
	if input.APIKey != nil {
		apiKeyPresent, apiKey = 1, strings.TrimSpace(*input.APIKey)
	}
	result, err := transaction.tx.ExecContext(ctx,
		`UPDATE ai_configs SET name = ?, provider = ?, base_url = ?,
			api_key = CASE WHEN ? = 1 THEN ? ELSE api_key END, model = ?, temperature = ?,
			thinking_depth = ?, thinking_budget_tokens = ?, updated_at = ?, version = version + 1
		 WHERE id = ? AND project_id IS NULL AND version = ?`, next.Name, next.Provider, next.BaseURL,
		apiKeyPresent, apiKey, next.Model, next.Temperature, optionalString(next.ThinkingDepth), optionalInt64(next.ThinkingBudgetTokens),
		updatedAt, id, expectedVersion)
	if err != nil {
		return domainconfig.AIConfig{}, safeSQLError(ctx, err)
	}
	if err := requireAutomationWrite(result); err != nil {
		return domainconfig.AIConfig{}, err
	}
	if err := transaction.wrote("ai-configs:update"); err != nil {
		return domainconfig.AIConfig{}, err
	}
	updated, found, err := transaction.GetAIConfig(ctx, id)
	if err != nil || !found {
		if err != nil {
			return domainconfig.AIConfig{}, err
		}
		return domainconfig.AIConfig{}, repository.ErrTransaction
	}
	return updated, nil
}

func (transaction *writeTransaction) DeleteAIConfig(ctx context.Context, id, expectedVersion int64, updatedAt string) error {
	if id <= 0 || expectedVersion <= 0 || !domainconfig.ValidUTCTimestamp(updatedAt) {
		return repository.ErrInvalidAutomation
	}
	current, found, err := transaction.GetAIConfig(ctx, id)
	if err != nil {
		return err
	}
	if !found {
		return repository.ErrNotFound
	}
	if current.Version != expectedVersion {
		return repository.ErrVersionConflict
	}
	result, err := transaction.tx.ExecContext(ctx,
		"UPDATE conversations SET ai_config_id = NULL, updated_at = ? WHERE ai_config_id = ?", updatedAt, id)
	if err != nil {
		return safeSQLError(ctx, err)
	}
	if _, err := rowsAffected(result); err != nil {
		return err
	}
	if err := transaction.wrote("conversations:unlink-ai-config"); err != nil {
		return err
	}
	result, err = transaction.tx.ExecContext(ctx, "DELETE FROM ai_configs WHERE id = ? AND project_id IS NULL AND version = ?", id, expectedVersion)
	if err != nil {
		return safeSQLError(ctx, err)
	}
	if err := requireAutomationWrite(result); err != nil {
		return err
	}
	return transaction.wrote("ai-configs:delete")
}

func scanAIConfig(row rowScanner) (domainconfig.AIConfig, error) {
	var value domainconfig.AIConfig
	var projectID sql.NullInt64
	var apiKey string
	var thinkingDepth sql.NullString
	var thinkingBudget sql.NullInt64
	if err := row.Scan(&value.ID, &projectID, &value.Name, &value.Provider, &value.BaseURL, &apiKey, &value.Model,
		&value.Temperature, &thinkingDepth, &thinkingBudget, &value.CreatedAt, &value.UpdatedAt, &value.Version); err != nil {
		return domainconfig.AIConfig{}, err
	}
	if projectID.Valid {
		return domainconfig.AIConfig{}, repository.ErrInvalidStore
	}
	value.ProjectID = nil
	value.HasAPIKey = strings.TrimSpace(apiKey) != ""
	value.MaskedAPIKey = domainconfig.MaskSecret(apiKey)
	value.ThinkingDepth = nullStringPointer(thinkingDepth)
	value.ThinkingBudgetTokens = nullInt64Pointer(thinkingBudget)
	normalized, err := domainconfig.NormalizeAIConfig(domainconfig.AIConfigInput{}, &value)
	if err != nil || value.ID <= 0 || value.Version <= 0 ||
		!domainconfig.ValidUTCTimestamp(value.CreatedAt) || !domainconfig.ValidUTCTimestamp(value.UpdatedAt) {
		return domainconfig.AIConfig{}, repository.ErrInvalidStore
	}
	return normalized, nil
}
