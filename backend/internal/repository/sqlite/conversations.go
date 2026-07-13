package sqlite

import (
	"context"
	"database/sql"

	domainchat "github.com/lyming99/autoplan/backend/internal/domain/chat"
	"github.com/lyming99/autoplan/backend/internal/repository"
)

const conversationSelectColumns = "id, project_id, title, ai_config_id, pinned_at, created_at, updated_at"

func (transaction *writeTransaction) ListConversations(
	ctx context.Context,
	options domainchat.ConversationListOptions,
) ([]domainchat.Conversation, string, error) {
	if options.ProjectID <= 0 {
		return nil, "", repository.ErrInvalidAutomation
	}
	found, err := transaction.projectExists(ctx, options.ProjectID)
	if err != nil {
		return nil, "", err
	}
	if !found {
		return nil, "", repository.ErrNotFound
	}
	cursor, err := domainchat.DecodeConversationCursor(options.Cursor)
	if err != nil {
		return nil, "", repository.ErrInvalidAutomation
	}
	limit := boundedChatPage(options.Limit)
	query := "SELECT " + conversationSelectColumns + " FROM conversations WHERE project_id = ?"
	args := []any{options.ProjectID}
	if cursor != nil {
		query += ` AND (
            CASE WHEN pinned_at IS NULL OR pinned_at = '' THEN 1 ELSE 0 END > ?
            OR (CASE WHEN pinned_at IS NULL OR pinned_at = '' THEN 1 ELSE 0 END = ?
                AND (updated_at < ? OR (updated_at = ? AND id < ?)))
        )`
		args = append(args, cursor.PinnedBucket, cursor.PinnedBucket, cursor.UpdatedAt, cursor.UpdatedAt, cursor.ID)
	}
	query += " ORDER BY CASE WHEN pinned_at IS NULL OR pinned_at = '' THEN 1 ELSE 0 END ASC, updated_at DESC, id DESC LIMIT ?"
	args = append(args, limit+1)
	rows, err := transaction.tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, "", safeSQLError(ctx, err)
	}
	defer rows.Close()
	result := make([]domainchat.Conversation, 0, limit)
	for rows.Next() {
		value, scanErr := scanConversation(rows)
		if scanErr != nil {
			return nil, "", safeSQLError(ctx, scanErr)
		}
		result = append(result, value)
	}
	if err := rows.Err(); err != nil {
		return nil, "", safeSQLError(ctx, err)
	}
	next := ""
	if len(result) > limit {
		result = result[:limit]
		last := result[len(result)-1]
		bucket := 1
		if last.PinnedAt != nil && *last.PinnedAt != "" {
			bucket = 0
		}
		next, err = domainchat.EncodeConversationCursor(domainchat.ConversationCursor{PinnedBucket: bucket, UpdatedAt: last.UpdatedAt, ID: last.ID})
		if err != nil {
			return nil, "", repository.ErrTransaction
		}
	}
	return result, next, nil
}

func (transaction *writeTransaction) GetConversation(ctx context.Context, projectID, conversationID int64) (domainchat.Conversation, bool, error) {
	if projectID <= 0 || conversationID <= 0 {
		return domainchat.Conversation{}, false, nil
	}
	value, err := scanConversation(transaction.tx.QueryRowContext(ctx,
		"SELECT "+conversationSelectColumns+" FROM conversations WHERE project_id = ? AND id = ?", projectID, conversationID))
	if err == sql.ErrNoRows {
		return domainchat.Conversation{}, false, nil
	}
	if err != nil {
		return domainchat.Conversation{}, false, safeSQLError(ctx, err)
	}
	return value, true, nil
}

// EnsureDefaultConversation preserves the Node compatibility rule: a send
// without a conversation targets the project-local "默认对话" row, creating it
// once when absent. Writer serialization makes the lookup/create sequence
// atomic without relying on a second database owner.
func (transaction *writeTransaction) EnsureDefaultConversation(
	ctx context.Context,
	projectID int64,
	createdAt string,
) (domainchat.Conversation, error) {
	if projectID <= 0 || !domainchat.ValidUTCTimestamp(createdAt) {
		return domainchat.Conversation{}, repository.ErrInvalidAutomation
	}
	found, err := transaction.projectExists(ctx, projectID)
	if err != nil {
		return domainchat.Conversation{}, err
	}
	if !found {
		return domainchat.Conversation{}, repository.ErrNotFound
	}
	value, err := scanConversation(transaction.tx.QueryRowContext(ctx,
		"SELECT "+conversationSelectColumns+" FROM conversations WHERE project_id = ? AND title = ? ORDER BY id ASC LIMIT 1",
		projectID, domainchat.DefaultConversationTitle))
	if err == nil {
		return value, nil
	}
	if err != sql.ErrNoRows {
		return domainchat.Conversation{}, safeSQLError(ctx, err)
	}
	title := domainchat.DefaultConversationTitle
	return transaction.CreateConversation(ctx, projectID, domainchat.ConversationInput{Title: &title}, createdAt)
}

func (transaction *writeTransaction) CreateConversation(
	ctx context.Context,
	projectID int64,
	input domainchat.ConversationInput,
	createdAt string,
) (domainchat.Conversation, error) {
	if projectID <= 0 || !domainchat.ValidUTCTimestamp(createdAt) {
		return domainchat.Conversation{}, repository.ErrInvalidAutomation
	}
	found, err := transaction.projectExists(ctx, projectID)
	if err != nil {
		return domainchat.Conversation{}, err
	}
	if !found {
		return domainchat.Conversation{}, repository.ErrNotFound
	}
	config, err := domainchat.NormalizeConversationInput(input, nil, createdAt)
	if err != nil {
		return domainchat.Conversation{}, repository.ErrInvalidAutomation
	}
	if err := transaction.requireGlobalAIConfig(ctx, config.AIConfigID); err != nil {
		return domainchat.Conversation{}, err
	}
	result, err := transaction.tx.ExecContext(ctx,
		`INSERT INTO conversations (project_id, title, ai_config_id, pinned_at, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?)`, projectID, config.Title, optionalInt64(config.AIConfigID), optionalString(config.PinnedAt), createdAt, createdAt)
	if err != nil {
		return domainchat.Conversation{}, safeSQLError(ctx, err)
	}
	id, err := result.LastInsertId()
	if err != nil || id <= 0 {
		return domainchat.Conversation{}, repository.ErrTransaction
	}
	if err := transaction.wrote("conversations:create"); err != nil {
		return domainchat.Conversation{}, err
	}
	created, found, err := transaction.GetConversation(ctx, projectID, id)
	if err != nil || !found {
		if err != nil {
			return domainchat.Conversation{}, err
		}
		return domainchat.Conversation{}, repository.ErrTransaction
	}
	return created, nil
}

func (transaction *writeTransaction) UpdateConversation(
	ctx context.Context,
	projectID, conversationID int64,
	input domainchat.ConversationInput,
	updatedAt string,
) (domainchat.Conversation, error) {
	if projectID <= 0 || conversationID <= 0 || !domainchat.ValidUTCTimestamp(updatedAt) {
		return domainchat.Conversation{}, repository.ErrInvalidAutomation
	}
	current, found, err := transaction.GetConversation(ctx, projectID, conversationID)
	if err != nil {
		return domainchat.Conversation{}, err
	}
	if !found {
		return domainchat.Conversation{}, repository.ErrNotFound
	}
	next, err := domainchat.NormalizeConversationInput(input, &current, updatedAt)
	if err != nil {
		return domainchat.Conversation{}, repository.ErrInvalidAutomation
	}
	if err := transaction.requireGlobalAIConfig(ctx, next.AIConfigID); err != nil {
		return domainchat.Conversation{}, err
	}
	result, err := transaction.tx.ExecContext(ctx,
		`UPDATE conversations SET title = ?, ai_config_id = ?, pinned_at = ?, updated_at = ?
		 WHERE id = ? AND project_id = ?`, next.Title, optionalInt64(next.AIConfigID), optionalString(next.PinnedAt), updatedAt, conversationID, projectID)
	if err != nil {
		return domainchat.Conversation{}, safeSQLError(ctx, err)
	}
	if err := requireOneRow(result); err != nil {
		return domainchat.Conversation{}, err
	}
	if err := transaction.wrote("conversations:update"); err != nil {
		return domainchat.Conversation{}, err
	}
	updated, found, err := transaction.GetConversation(ctx, projectID, conversationID)
	if err != nil || !found {
		if err != nil {
			return domainchat.Conversation{}, err
		}
		return domainchat.Conversation{}, repository.ErrTransaction
	}
	return updated, nil
}

func (transaction *writeTransaction) UnlinkConversationAIConfig(
	ctx context.Context,
	projectID, conversationID int64,
	updatedAt string,
) (domainchat.Conversation, error) {
	return transaction.UpdateConversation(ctx, projectID, conversationID, domainchat.ConversationInput{AIConfigID: int64Pointer(0)}, updatedAt)
}

func (transaction *writeTransaction) DeleteConversation(ctx context.Context, projectID, conversationID int64) (int64, error) {
	if projectID <= 0 || conversationID <= 0 {
		return 0, repository.ErrInvalidAutomation
	}
	_, found, err := transaction.GetConversation(ctx, projectID, conversationID)
	if err != nil {
		return 0, err
	}
	if !found {
		return 0, repository.ErrNotFound
	}
	messageResult, err := transaction.tx.ExecContext(ctx,
		"DELETE FROM chat_messages WHERE conversation_id = ? AND project_id = ?", conversationID, projectID)
	if err != nil {
		return 0, safeSQLError(ctx, err)
	}
	count, err := rowsAffected(messageResult)
	if err != nil {
		return 0, err
	}
	if err := transaction.wrote("chat-messages:delete-conversation"); err != nil {
		return 0, err
	}
	result, err := transaction.tx.ExecContext(ctx, "DELETE FROM conversations WHERE id = ? AND project_id = ?", conversationID, projectID)
	if err != nil {
		return 0, safeSQLError(ctx, err)
	}
	if err := requireOneRow(result); err != nil {
		return 0, err
	}
	if err := transaction.wrote("conversations:delete"); err != nil {
		return 0, err
	}
	return count, nil
}

func (transaction *writeTransaction) requireGlobalAIConfig(ctx context.Context, value *int64) error {
	if value == nil {
		return nil
	}
	var exists int
	if err := transaction.tx.QueryRowContext(ctx,
		"SELECT EXISTS(SELECT 1 FROM ai_configs WHERE id = ? AND project_id IS NULL)", *value).Scan(&exists); err != nil {
		return safeSQLError(ctx, err)
	}
	if exists != 1 {
		return repository.ErrNotFound
	}
	return nil
}

func scanConversation(row rowScanner) (domainchat.Conversation, error) {
	var value domainchat.Conversation
	var aiConfigID sql.NullInt64
	var pinnedAt sql.NullString
	if err := row.Scan(&value.ID, &value.ProjectID, &value.Title, &aiConfigID, &pinnedAt, &value.CreatedAt, &value.UpdatedAt); err != nil {
		return domainchat.Conversation{}, err
	}
	value.AIConfigID = nullInt64Pointer(aiConfigID)
	value.PinnedAt = nullStringPointer(pinnedAt)
	if domainchat.ValidateConversation(value) != nil {
		return domainchat.Conversation{}, repository.ErrInvalidStore
	}
	return value, nil
}

func boundedChatPage(value int) int {
	if value <= 0 {
		return 100
	}
	if value > 200 {
		return 200
	}
	return value
}
