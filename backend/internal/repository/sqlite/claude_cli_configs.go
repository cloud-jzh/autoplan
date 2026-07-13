package sqlite

import (
	"context"
	"database/sql"
	"strings"

	domainconfig "github.com/lyming99/autoplan/backend/internal/domain/config"
	"github.com/lyming99/autoplan/backend/internal/repository"
)

const claudeCLIConfigSelectColumns = "id, project_id, name, base_url, auth_token, model, is_default, created_at, updated_at, version"

func (transaction *writeTransaction) ListClaudeCLIConfigs(ctx context.Context) ([]domainconfig.ClaudeCLIConfig, error) {
	rows, err := transaction.tx.QueryContext(ctx, "SELECT "+claudeCLIConfigSelectColumns+" FROM claude_cli_configs WHERE project_id IS NULL ORDER BY id ASC")
	if err != nil {
		return nil, safeSQLError(ctx, err)
	}
	defer rows.Close()
	result := make([]domainconfig.ClaudeCLIConfig, 0)
	for rows.Next() {
		value, scanErr := scanClaudeCLIConfig(rows)
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

func (transaction *writeTransaction) GetClaudeCLIConfig(ctx context.Context, id int64) (domainconfig.ClaudeCLIConfig, bool, error) {
	if id <= 0 {
		return domainconfig.ClaudeCLIConfig{}, false, nil
	}
	value, err := scanClaudeCLIConfig(transaction.tx.QueryRowContext(ctx,
		"SELECT "+claudeCLIConfigSelectColumns+" FROM claude_cli_configs WHERE id = ? AND project_id IS NULL", id))
	if err == sql.ErrNoRows {
		return domainconfig.ClaudeCLIConfig{}, false, nil
	}
	if err != nil {
		return domainconfig.ClaudeCLIConfig{}, false, safeSQLError(ctx, err)
	}
	return value, true, nil
}

func (transaction *writeTransaction) CreateClaudeCLIConfig(ctx context.Context, input domainconfig.ClaudeCLIConfigInput, createdAt string) (domainconfig.ClaudeCLIConfig, error) {
	if !domainconfig.ValidUTCTimestamp(createdAt) {
		return domainconfig.ClaudeCLIConfig{}, repository.ErrInvalidAutomation
	}
	config, err := domainconfig.NormalizeClaudeCLIConfig(input, nil)
	if err != nil {
		return domainconfig.ClaudeCLIConfig{}, repository.ErrInvalidAutomation
	}
	token := ""
	if input.AuthToken != nil {
		token = strings.TrimSpace(*input.AuthToken)
	}
	result, err := transaction.tx.ExecContext(ctx,
		`INSERT INTO claude_cli_configs (project_id, name, base_url, auth_token, model, is_default, created_at, updated_at, version)
		 VALUES (NULL, ?, ?, ?, ?, 0, ?, ?, 1)`, config.Name, config.BaseURL, token, config.Model, createdAt, createdAt)
	if err != nil {
		return domainconfig.ClaudeCLIConfig{}, safeSQLError(ctx, err)
	}
	id, err := result.LastInsertId()
	if err != nil || id <= 0 {
		return domainconfig.ClaudeCLIConfig{}, repository.ErrTransaction
	}
	if err := transaction.wrote("claude-cli-configs:create"); err != nil {
		return domainconfig.ClaudeCLIConfig{}, err
	}
	created, found, err := transaction.GetClaudeCLIConfig(ctx, id)
	if err != nil || !found {
		if err != nil {
			return domainconfig.ClaudeCLIConfig{}, err
		}
		return domainconfig.ClaudeCLIConfig{}, repository.ErrTransaction
	}
	return created, nil
}

func (transaction *writeTransaction) UpdateClaudeCLIConfig(ctx context.Context, id, expectedVersion int64, input domainconfig.ClaudeCLIConfigInput, updatedAt string) (domainconfig.ClaudeCLIConfig, error) {
	if id <= 0 || expectedVersion <= 0 || !domainconfig.ValidUTCTimestamp(updatedAt) {
		return domainconfig.ClaudeCLIConfig{}, repository.ErrInvalidAutomation
	}
	current, found, err := transaction.GetClaudeCLIConfig(ctx, id)
	if err != nil {
		return domainconfig.ClaudeCLIConfig{}, err
	}
	if !found {
		return domainconfig.ClaudeCLIConfig{}, repository.ErrNotFound
	}
	if current.Version != expectedVersion {
		return domainconfig.ClaudeCLIConfig{}, repository.ErrVersionConflict
	}
	next, err := domainconfig.NormalizeClaudeCLIConfig(input, &current)
	if err != nil {
		return domainconfig.ClaudeCLIConfig{}, repository.ErrInvalidAutomation
	}
	tokenPresent, token := 0, ""
	if input.AuthToken != nil {
		tokenPresent, token = 1, strings.TrimSpace(*input.AuthToken)
	}
	result, err := transaction.tx.ExecContext(ctx,
		`UPDATE claude_cli_configs SET name = ?, base_url = ?,
			auth_token = CASE WHEN ? = 1 THEN ? ELSE auth_token END, model = ?, updated_at = ?, version = version + 1
		 WHERE id = ? AND project_id IS NULL AND version = ?`, next.Name, next.BaseURL, tokenPresent, token, next.Model, updatedAt, id, expectedVersion)
	if err != nil {
		return domainconfig.ClaudeCLIConfig{}, safeSQLError(ctx, err)
	}
	if err := requireAutomationWrite(result); err != nil {
		return domainconfig.ClaudeCLIConfig{}, err
	}
	if err := transaction.wrote("claude-cli-configs:update"); err != nil {
		return domainconfig.ClaudeCLIConfig{}, err
	}
	updated, found, err := transaction.GetClaudeCLIConfig(ctx, id)
	if err != nil || !found {
		if err != nil {
			return domainconfig.ClaudeCLIConfig{}, err
		}
		return domainconfig.ClaudeCLIConfig{}, repository.ErrTransaction
	}
	return updated, nil
}

func (transaction *writeTransaction) DeleteClaudeCLIConfig(ctx context.Context, id, expectedVersion int64, updatedAt string) error {
	if id <= 0 || expectedVersion <= 0 || !domainconfig.ValidUTCTimestamp(updatedAt) {
		return repository.ErrInvalidAutomation
	}
	current, found, err := transaction.GetClaudeCLIConfig(ctx, id)
	if err != nil {
		return err
	}
	if !found {
		return repository.ErrNotFound
	}
	if current.Version != expectedVersion {
		return repository.ErrVersionConflict
	}
	result, err := transaction.tx.ExecContext(ctx, "DELETE FROM claude_cli_configs WHERE id = ? AND project_id IS NULL AND version = ?", id, expectedVersion)
	if err != nil {
		return safeSQLError(ctx, err)
	}
	if err := requireAutomationWrite(result); err != nil {
		return err
	}
	if err := transaction.wrote("claude-cli-configs:delete"); err != nil {
		return err
	}
	if !current.IsDefault {
		return nil
	}
	var replacementID int64
	err = transaction.tx.QueryRowContext(ctx,
		"SELECT id FROM claude_cli_configs WHERE project_id IS NULL ORDER BY id ASC LIMIT 1").Scan(&replacementID)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return safeSQLError(ctx, err)
	}
	result, err = transaction.tx.ExecContext(ctx,
		"UPDATE claude_cli_configs SET is_default = 1, updated_at = ?, version = version + 1 WHERE id = ? AND project_id IS NULL", updatedAt, replacementID)
	if err != nil {
		return safeSQLError(ctx, err)
	}
	if err := requireOneRow(result); err != nil {
		return err
	}
	return transaction.wrote("claude-cli-configs:promote-default")
}

func (transaction *writeTransaction) SetDefaultClaudeCLIConfig(ctx context.Context, id, expectedVersion int64, updatedAt string) (domainconfig.ClaudeCLIConfig, error) {
	if id <= 0 || expectedVersion <= 0 || !domainconfig.ValidUTCTimestamp(updatedAt) {
		return domainconfig.ClaudeCLIConfig{}, repository.ErrInvalidAutomation
	}
	current, found, err := transaction.GetClaudeCLIConfig(ctx, id)
	if err != nil {
		return domainconfig.ClaudeCLIConfig{}, err
	}
	if !found {
		return domainconfig.ClaudeCLIConfig{}, repository.ErrNotFound
	}
	if current.Version != expectedVersion {
		return domainconfig.ClaudeCLIConfig{}, repository.ErrVersionConflict
	}
	if _, err := transaction.tx.ExecContext(ctx,
		"UPDATE claude_cli_configs SET is_default = 0, updated_at = ?, version = version + 1 WHERE project_id IS NULL AND id != ? AND is_default != 0", updatedAt, id); err != nil {
		return domainconfig.ClaudeCLIConfig{}, safeSQLError(ctx, err)
	}
	if err := transaction.wrote("claude-cli-configs:clear-default"); err != nil {
		return domainconfig.ClaudeCLIConfig{}, err
	}
	result, err := transaction.tx.ExecContext(ctx,
		"UPDATE claude_cli_configs SET is_default = 1, updated_at = ?, version = version + 1 WHERE id = ? AND project_id IS NULL AND version = ?", updatedAt, id, expectedVersion)
	if err != nil {
		return domainconfig.ClaudeCLIConfig{}, safeSQLError(ctx, err)
	}
	if err := requireAutomationWrite(result); err != nil {
		return domainconfig.ClaudeCLIConfig{}, err
	}
	if err := transaction.wrote("claude-cli-configs:set-default"); err != nil {
		return domainconfig.ClaudeCLIConfig{}, err
	}
	return transaction.mustClaudeCLIConfig(ctx, id)
}

func (transaction *writeTransaction) mustClaudeCLIConfig(ctx context.Context, id int64) (domainconfig.ClaudeCLIConfig, error) {
	value, found, err := transaction.GetClaudeCLIConfig(ctx, id)
	if err != nil {
		return domainconfig.ClaudeCLIConfig{}, err
	}
	if !found {
		return domainconfig.ClaudeCLIConfig{}, repository.ErrTransaction
	}
	return value, nil
}

func scanClaudeCLIConfig(row rowScanner) (domainconfig.ClaudeCLIConfig, error) {
	var value domainconfig.ClaudeCLIConfig
	var projectID sql.NullInt64
	var token string
	var isDefault int64
	if err := row.Scan(&value.ID, &projectID, &value.Name, &value.BaseURL, &token, &value.Model, &isDefault, &value.CreatedAt, &value.UpdatedAt, &value.Version); err != nil {
		return domainconfig.ClaudeCLIConfig{}, err
	}
	if projectID.Valid || (isDefault != 0 && isDefault != 1) {
		return domainconfig.ClaudeCLIConfig{}, repository.ErrInvalidStore
	}
	value.HasAuthToken = strings.TrimSpace(token) != ""
	value.MaskedAuthToken = domainconfig.MaskSecret(token)
	value.IsDefault = isDefault == 1
	normalized, err := domainconfig.NormalizeClaudeCLIConfig(domainconfig.ClaudeCLIConfigInput{}, &value)
	if err != nil || value.ID <= 0 || value.Version <= 0 ||
		!domainconfig.ValidUTCTimestamp(value.CreatedAt) || !domainconfig.ValidUTCTimestamp(value.UpdatedAt) {
		return domainconfig.ClaudeCLIConfig{}, repository.ErrInvalidStore
	}
	return normalized, nil
}
