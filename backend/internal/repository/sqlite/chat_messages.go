package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"

	domainchat "github.com/lyming99/autoplan/backend/internal/domain/chat"
	"github.com/lyming99/autoplan/backend/internal/repository"
)

const chatMessageSelectColumns = "id, project_id, conversation_id, role, content, tool_calls, tool_result, status, created_at"

func (transaction *writeTransaction) ListChatMessages(
	ctx context.Context,
	options domainchat.MessageListOptions,
) ([]domainchat.Message, string, error) {
	if options.ProjectID <= 0 || options.ConversationID <= 0 {
		return nil, "", repository.ErrInvalidAutomation
	}
	if _, found, err := transaction.GetConversation(ctx, options.ProjectID, options.ConversationID); err != nil || !found {
		if err != nil {
			return nil, "", err
		}
		return nil, "", repository.ErrNotFound
	}
	cursor, err := domainchat.DecodeMessageCursor(options.Cursor)
	if err != nil {
		return nil, "", repository.ErrInvalidAutomation
	}
	query := "SELECT " + chatMessageSelectColumns + " FROM chat_messages WHERE project_id = ? AND conversation_id = ?"
	args := []any{options.ProjectID, options.ConversationID}
	if cursor != nil {
		query += " AND (created_at > ? OR (created_at = ? AND id > ?))"
		args = append(args, cursor.CreatedAt, cursor.CreatedAt, cursor.ID)
	}
	limit := boundedChatPage(options.Limit)
	query += " ORDER BY created_at ASC, id ASC LIMIT ?"
	args = append(args, limit+1)
	rows, err := transaction.tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, "", safeSQLError(ctx, err)
	}
	defer rows.Close()
	result := make([]domainchat.Message, 0, limit)
	for rows.Next() {
		value, scanErr := scanChatMessage(rows)
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
		next, err = domainchat.EncodeMessageCursor(domainchat.MessageCursor{CreatedAt: last.CreatedAt, ID: last.ID})
		if err != nil {
			return nil, "", repository.ErrTransaction
		}
	}
	return result, next, nil
}

func (transaction *writeTransaction) AppendChatMessage(ctx context.Context, input domainchat.MessageInput) (domainchat.Message, error) {
	if domainchat.ValidateMessageInput(input) != nil {
		return domainchat.Message{}, repository.ErrInvalidAutomation
	}
	if _, found, err := transaction.GetConversation(ctx, input.ProjectID, input.ConversationID); err != nil || !found {
		if err != nil {
			return domainchat.Message{}, err
		}
		return domainchat.Message{}, repository.ErrNotFound
	}
	result, err := transaction.tx.ExecContext(ctx,
		`INSERT INTO chat_messages (project_id, conversation_id, role, content, tool_calls, tool_result, status, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, input.ProjectID, input.ConversationID, input.Role, input.Content,
		optionalRawJSON(input.ToolCalls), optionalRawJSON(input.ToolResult), optionalString(input.Status), input.CreatedAt)
	if err != nil {
		return domainchat.Message{}, safeSQLError(ctx, err)
	}
	id, err := result.LastInsertId()
	if err != nil || id <= 0 {
		return domainchat.Message{}, repository.ErrTransaction
	}
	if err := transaction.wrote("chat-messages:create"); err != nil {
		return domainchat.Message{}, err
	}
	updated, err := transaction.tx.ExecContext(ctx,
		"UPDATE conversations SET updated_at = ? WHERE id = ? AND project_id = ?", input.CreatedAt, input.ConversationID, input.ProjectID)
	if err != nil {
		return domainchat.Message{}, safeSQLError(ctx, err)
	}
	if err := requireOneRow(updated); err != nil {
		return domainchat.Message{}, err
	}
	if err := transaction.wrote("conversations:touch-message"); err != nil {
		return domainchat.Message{}, err
	}
	value, err := scanChatMessage(transaction.tx.QueryRowContext(ctx,
		"SELECT "+chatMessageSelectColumns+" FROM chat_messages WHERE id = ? AND project_id = ? AND conversation_id = ?", id, input.ProjectID, input.ConversationID))
	if err != nil {
		return domainchat.Message{}, safeSQLError(ctx, err)
	}
	return value, nil
}

func (transaction *writeTransaction) getChatMessage(
	ctx context.Context,
	projectID, conversationID, messageID int64,
) (domainchat.Message, bool, error) {
	if projectID <= 0 || conversationID <= 0 || messageID <= 0 {
		return domainchat.Message{}, false, repository.ErrInvalidAutomation
	}
	value, err := scanChatMessage(transaction.tx.QueryRowContext(ctx,
		"SELECT "+chatMessageSelectColumns+" FROM chat_messages WHERE id = ? AND project_id = ? AND conversation_id = ?",
		messageID, projectID, conversationID))
	if err == sql.ErrNoRows {
		return domainchat.Message{}, false, nil
	}
	if err != nil {
		return domainchat.Message{}, false, safeSQLError(ctx, err)
	}
	return value, true, nil
}

func (transaction *writeTransaction) transitionChatMessage(
	ctx context.Context,
	input domainchat.TurnTransition,
) (domainchat.Message, bool, error) {
	if domainchat.ValidateTurnTransition(input) != nil {
		return domainchat.Message{}, false, repository.ErrInvalidAutomation
	}
	current, found, err := transaction.getChatMessage(ctx, input.ProjectID, input.ConversationID, input.MessageID)
	if err != nil || !found {
		if err != nil {
			return domainchat.Message{}, false, err
		}
		return domainchat.Message{}, false, repository.ErrNotFound
	}
	currentStatus := ""
	if current.Status != nil {
		currentStatus = *current.Status
	}
	if !containsChatStatus(input.From, currentStatus) {
		return current, false, nil
	}
	result, err := transaction.tx.ExecContext(ctx,
		"UPDATE chat_messages SET status = ? WHERE id = ? AND project_id = ? AND conversation_id = ? AND status = ?",
		input.To, input.MessageID, input.ProjectID, input.ConversationID, currentStatus)
	if err != nil {
		return domainchat.Message{}, false, safeSQLError(ctx, err)
	}
	changed, err := rowsAffected(result)
	if err != nil {
		return domainchat.Message{}, false, err
	}
	if changed == 0 {
		return current, false, nil
	}
	if changed != 1 {
		return domainchat.Message{}, false, repository.ErrTransaction
	}
	if err := transaction.wrote("chat-messages:transition"); err != nil {
		return domainchat.Message{}, false, err
	}
	if err := transaction.touchConversationForChat(ctx, input.ProjectID, input.ConversationID, input.OccurredAt); err != nil {
		return domainchat.Message{}, false, err
	}
	updated, found, err := transaction.getChatMessage(ctx, input.ProjectID, input.ConversationID, input.MessageID)
	if err != nil || !found {
		if err != nil {
			return domainchat.Message{}, false, err
		}
		return domainchat.Message{}, false, repository.ErrTransaction
	}
	return updated, true, nil
}

func (transaction *writeTransaction) appendAssistantDelta(
	ctx context.Context,
	input domainchat.AssistantDelta,
) (domainchat.Message, bool, error) {
	if domainchat.ValidateAssistantDelta(input) != nil {
		return domainchat.Message{}, false, repository.ErrInvalidAutomation
	}
	current, found, err := transaction.getChatMessage(ctx, input.ProjectID, input.ConversationID, input.MessageID)
	if err != nil || !found {
		if err != nil {
			return domainchat.Message{}, false, err
		}
		return domainchat.Message{}, false, repository.ErrNotFound
	}
	if current.Role != "assistant" || current.Status == nil || *current.Status != domainchat.StatusStreaming {
		return current, false, nil
	}
	next := current.Content + input.Content
	if len(next) > 1000000 || containsNUL(next) {
		return domainchat.Message{}, false, repository.ErrInvalidAutomation
	}
	result, err := transaction.tx.ExecContext(ctx,
		"UPDATE chat_messages SET content = ? WHERE id = ? AND project_id = ? AND conversation_id = ? AND status = ?",
		next, input.MessageID, input.ProjectID, input.ConversationID, domainchat.StatusStreaming)
	if err != nil {
		return domainchat.Message{}, false, safeSQLError(ctx, err)
	}
	changed, err := rowsAffected(result)
	if err != nil {
		return domainchat.Message{}, false, err
	}
	if changed == 0 {
		return current, false, nil
	}
	if changed != 1 {
		return domainchat.Message{}, false, repository.ErrTransaction
	}
	if err := transaction.wrote("chat-messages:append-partial"); err != nil {
		return domainchat.Message{}, false, err
	}
	if err := transaction.touchConversationForChat(ctx, input.ProjectID, input.ConversationID, input.OccurredAt); err != nil {
		return domainchat.Message{}, false, err
	}
	updated, found, err := transaction.getChatMessage(ctx, input.ProjectID, input.ConversationID, input.MessageID)
	if err != nil || !found {
		if err != nil {
			return domainchat.Message{}, false, err
		}
		return domainchat.Message{}, false, repository.ErrTransaction
	}
	return updated, true, nil
}

func (transaction *writeTransaction) touchConversationForChat(
	ctx context.Context,
	projectID, conversationID int64,
	updatedAt string,
) error {
	if !domainchat.ValidUTCTimestamp(updatedAt) {
		return repository.ErrInvalidAutomation
	}
	updated, err := transaction.tx.ExecContext(ctx,
		"UPDATE conversations SET updated_at = ? WHERE id = ? AND project_id = ?",
		updatedAt, conversationID, projectID)
	if err != nil {
		return safeSQLError(ctx, err)
	}
	if err := requireOneRow(updated); err != nil {
		return err
	}
	return transaction.wrote("conversations:touch-chat")
}

func scanChatMessage(row rowScanner) (domainchat.Message, error) {
	var value domainchat.Message
	var toolCalls, toolResult, status sql.NullString
	if err := row.Scan(&value.ID, &value.ProjectID, &value.ConversationID, &value.Role, &value.Content, &toolCalls, &toolResult, &status, &value.CreatedAt); err != nil {
		return domainchat.Message{}, err
	}
	value.ToolCalls = nullRawJSON(toolCalls)
	value.ToolResult = nullRawJSON(toolResult)
	value.Status = nullStringPointer(status)
	if domainchat.ValidateMessage(value) != nil {
		return domainchat.Message{}, repository.ErrInvalidStore
	}
	return value, nil
}

func optionalRawJSON(value *json.RawMessage) any {
	if value == nil {
		return nil
	}
	return string(*value)
}

func nullRawJSON(value sql.NullString) *json.RawMessage {
	if !value.Valid {
		return nil
	}
	copy := json.RawMessage(value.String)
	return &copy
}

func containsChatStatus(values []string, candidate string) bool {
	for _, value := range values {
		if value == candidate {
			return true
		}
	}
	return false
}

func containsNUL(value string) bool {
	for _, character := range value {
		if character == 0 {
			return true
		}
	}
	return false
}
