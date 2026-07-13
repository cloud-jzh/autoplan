package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"strconv"
	"strings"

	domainchat "github.com/lyming99/autoplan/backend/internal/domain/chat"
	"github.com/lyming99/autoplan/backend/internal/repository"
)

const chatAdmissionRoute = "chat.send"

// TransactChatQueue provides the P13A queue state machine with the same
// owner-guarded serial SQLite transaction used by every other writer. The
// domain callback receives no arbitrary SQL capability.
func (writer *Writer) TransactChatQueue(
	ctx context.Context,
	operation func(domainchat.QueueTransaction) error,
) error {
	if operation == nil {
		return repository.ErrTransaction
	}
	return writer.Transact(ctx, func(transaction repository.WriteTransaction) error {
		queue, ok := transaction.(*writeTransaction)
		if !ok {
			return repository.ErrTransaction
		}
		return operation(queue)
	})
}

func (transaction *writeTransaction) ListQueue(
	ctx context.Context,
	projectID, conversationID int64,
) ([]domainchat.QueueItem, error) {
	if _, found, err := transaction.GetConversation(ctx, projectID, conversationID); err != nil || !found {
		if err != nil {
			return nil, err
		}
		return nil, repository.ErrNotFound
	}
	rows, err := transaction.tx.QueryContext(ctx,
		"SELECT id, content, status FROM chat_messages WHERE project_id = ? AND conversation_id = ? AND role = ? AND status IN (?, ?) ORDER BY id ASC LIMIT ?",
		projectID, conversationID, "user", domainchat.StatusQueued, domainchat.StatusRunning, 201)
	if err != nil {
		return nil, safeSQLError(ctx, err)
	}
	defer rows.Close()
	items := make([]domainchat.QueueItem, 0)
	for rows.Next() {
		var item domainchat.QueueItem
		var status string
		if err := rows.Scan(&item.ID, &item.Content, &status); err != nil {
			return nil, safeSQLError(ctx, err)
		}
		if item.ID <= 0 || len(item.Content) == 0 || len(item.Content) > 1000000 {
			return nil, repository.ErrInvalidStore
		}
		switch status {
		case domainchat.StatusQueued:
			item.State = domainchat.StatusQueued
		case domainchat.StatusRunning:
			item.State = "processing"
		default:
			return nil, repository.ErrInvalidStore
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, safeSQLError(ctx, err)
	}
	if len(items) > 200 {
		return nil, repository.ErrInvalidStore
	}
	return items, nil
}

func (transaction *writeTransaction) AdmitQueuedMessage(
	ctx context.Context,
	input domainchat.Admission,
) (domainchat.AdmissionResult, error) {
	if domainchat.ValidateAdmission(input) != nil {
		return domainchat.AdmissionResult{}, repository.ErrInvalidAutomation
	}
	if _, found, err := transaction.GetConversation(ctx, input.ProjectID, input.ConversationID); err != nil || !found {
		if err != nil {
			return domainchat.AdmissionResult{}, err
		}
		return domainchat.AdmissionResult{}, repository.ErrNotFound
	}
	if input.IdempotencyKey != "" {
		replayed, found, err := transaction.findChatAdmission(ctx, input)
		if err != nil {
			return domainchat.AdmissionResult{}, err
		}
		if found {
			return replayed, nil
		}
		if err := transaction.reserveChatAdmission(ctx, input); err != nil {
			return domainchat.AdmissionResult{}, err
		}
	}
	status := domainchat.StatusQueued
	message, err := transaction.AppendChatMessage(ctx, domainchat.MessageInput{
		ProjectID: input.ProjectID, ConversationID: input.ConversationID, Role: "user",
		Content: strings.TrimSpace(input.Content), Status: &status, CreatedAt: input.OccurredAt,
	})
	if err != nil {
		return domainchat.AdmissionResult{}, err
	}
	turnID := chatTurnID(message.ID)
	queue, err := transaction.ListQueue(ctx, input.ProjectID, input.ConversationID)
	if err != nil {
		return domainchat.AdmissionResult{}, err
	}
	if err := transaction.AppendQueueEvent(ctx, newQueueEvent(
		input.ProjectID, input.ConversationID, message.ID, turnID, domainchat.StatusQueued,
		len(queue), input.RequestID, input.OccurredAt, "chat turn queued")); err != nil {
		return domainchat.AdmissionResult{}, err
	}
	result := domainchat.AdmissionResult{Message: message, TurnID: turnID, AdmissionID: input.AdmissionID}
	if input.IdempotencyKey != "" {
		if err := transaction.completeChatAdmission(ctx, input, result); err != nil {
			return domainchat.AdmissionResult{}, err
		}
	}
	return result, nil
}

func (transaction *writeTransaction) EditQueuedMessage(
	ctx context.Context,
	projectID, conversationID, messageID int64,
	content, occurredAt string,
) (domainchat.Message, bool, error) {
	content = strings.TrimSpace(content)
	if projectID <= 0 || conversationID <= 0 || messageID <= 0 || content == "" ||
		len(content) > 1000000 || containsNUL(content) || !domainchat.ValidUTCTimestamp(occurredAt) {
		return domainchat.Message{}, false, repository.ErrInvalidAutomation
	}
	current, found, err := transaction.getChatMessage(ctx, projectID, conversationID, messageID)
	if err != nil || !found {
		if err != nil {
			return domainchat.Message{}, false, err
		}
		return domainchat.Message{}, false, repository.ErrNotFound
	}
	if current.Role != "user" || current.Status == nil || *current.Status != domainchat.StatusQueued {
		return current, false, nil
	}
	result, err := transaction.tx.ExecContext(ctx,
		"UPDATE chat_messages SET content = ? WHERE id = ? AND project_id = ? AND conversation_id = ? AND status = ?",
		content, messageID, projectID, conversationID, domainchat.StatusQueued)
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
	if err := transaction.wrote("chat-queue:edit"); err != nil {
		return domainchat.Message{}, false, err
	}
	if err := transaction.touchConversationForChat(ctx, projectID, conversationID, occurredAt); err != nil {
		return domainchat.Message{}, false, err
	}
	updated, found, err := transaction.getChatMessage(ctx, projectID, conversationID, messageID)
	if err != nil || !found {
		if err != nil {
			return domainchat.Message{}, false, err
		}
		return domainchat.Message{}, false, repository.ErrTransaction
	}
	return updated, true, nil
}

func (transaction *writeTransaction) CancelQueuedMessage(
	ctx context.Context,
	projectID, conversationID, messageID int64,
	occurredAt string,
) (bool, error) {
	if projectID <= 0 || conversationID <= 0 || messageID <= 0 || !domainchat.ValidUTCTimestamp(occurredAt) {
		return false, repository.ErrInvalidAutomation
	}
	current, found, err := transaction.getChatMessage(ctx, projectID, conversationID, messageID)
	if err != nil || !found {
		if err != nil {
			return false, err
		}
		return false, repository.ErrNotFound
	}
	if current.Role != "user" || current.Status == nil || *current.Status != domainchat.StatusQueued {
		return false, nil
	}
	result, err := transaction.tx.ExecContext(ctx,
		"DELETE FROM chat_messages WHERE id = ? AND project_id = ? AND conversation_id = ? AND role = ? AND status = ?",
		messageID, projectID, conversationID, "user", domainchat.StatusQueued)
	if err != nil {
		return false, safeSQLError(ctx, err)
	}
	changed, err := rowsAffected(result)
	if err != nil {
		return false, err
	}
	if changed == 0 {
		return false, nil
	}
	if changed != 1 {
		return false, repository.ErrTransaction
	}
	if err := transaction.wrote("chat-queue:cancel"); err != nil {
		return false, err
	}
	if err := transaction.touchConversationForChat(ctx, projectID, conversationID, occurredAt); err != nil {
		return false, err
	}
	return true, nil
}

func (transaction *writeTransaction) ClearQueuedMessages(
	ctx context.Context,
	projectID, conversationID int64,
	occurredAt string,
) (int, error) {
	if projectID <= 0 || conversationID <= 0 || !domainchat.ValidUTCTimestamp(occurredAt) {
		return 0, repository.ErrInvalidAutomation
	}
	if _, found, err := transaction.GetConversation(ctx, projectID, conversationID); err != nil || !found {
		if err != nil {
			return 0, err
		}
		return 0, repository.ErrNotFound
	}
	result, err := transaction.tx.ExecContext(ctx,
		"DELETE FROM chat_messages WHERE project_id = ? AND conversation_id = ? AND role = ? AND status = ?",
		projectID, conversationID, "user", domainchat.StatusQueued)
	if err != nil {
		return 0, safeSQLError(ctx, err)
	}
	count, err := rowsAffected(result)
	if err != nil {
		return 0, err
	}
	if count > 0 {
		if err := transaction.wrote("chat-queue:clear"); err != nil {
			return 0, err
		}
		if err := transaction.touchConversationForChat(ctx, projectID, conversationID, occurredAt); err != nil {
			return 0, err
		}
	}
	return int(count), nil
}

func (transaction *writeTransaction) ClaimNextQueuedMessage(
	ctx context.Context,
	projectID, conversationID int64,
	occurredAt string,
) (domainchat.Message, bool, error) {
	if projectID <= 0 || conversationID <= 0 || !domainchat.ValidUTCTimestamp(occurredAt) {
		return domainchat.Message{}, false, repository.ErrInvalidAutomation
	}
	if _, found, err := transaction.GetConversation(ctx, projectID, conversationID); err != nil || !found {
		if err != nil {
			return domainchat.Message{}, false, err
		}
		return domainchat.Message{}, false, repository.ErrNotFound
	}
	var running int
	if err := transaction.tx.QueryRowContext(ctx,
		"SELECT COUNT(1) FROM chat_messages WHERE project_id = ? AND conversation_id = ? AND role = ? AND status = ?",
		projectID, conversationID, "user", domainchat.StatusRunning).Scan(&running); err != nil {
		return domainchat.Message{}, false, safeSQLError(ctx, err)
	}
	if running > 0 {
		return domainchat.Message{}, false, nil
	}
	var messageID int64
	err := transaction.tx.QueryRowContext(ctx,
		"SELECT id FROM chat_messages WHERE project_id = ? AND conversation_id = ? AND role = ? AND status = ? ORDER BY id ASC LIMIT 1",
		projectID, conversationID, "user", domainchat.StatusQueued).Scan(&messageID)
	if err == sql.ErrNoRows {
		return domainchat.Message{}, false, nil
	}
	if err != nil {
		return domainchat.Message{}, false, safeSQLError(ctx, err)
	}
	return transaction.transitionChatMessage(ctx, domainchat.TurnTransition{
		ProjectID: projectID, ConversationID: conversationID, MessageID: messageID,
		From: []string{domainchat.StatusQueued}, To: domainchat.StatusRunning, OccurredAt: occurredAt,
	})
}

func (transaction *writeTransaction) AbortActiveOrQueuedMessage(
	ctx context.Context,
	projectID, conversationID int64,
	occurredAt string,
) (domainchat.Message, bool, error) {
	if projectID <= 0 || conversationID <= 0 || !domainchat.ValidUTCTimestamp(occurredAt) {
		return domainchat.Message{}, false, repository.ErrInvalidAutomation
	}
	if _, found, err := transaction.GetConversation(ctx, projectID, conversationID); err != nil || !found {
		if err != nil {
			return domainchat.Message{}, false, err
		}
		return domainchat.Message{}, false, repository.ErrNotFound
	}
	var messageID int64
	var status string
	err := transaction.tx.QueryRowContext(ctx,
		"SELECT id, status FROM chat_messages WHERE project_id = ? AND conversation_id = ? AND role = ? AND status IN (?, ?) ORDER BY CASE status WHEN ? THEN 0 ELSE 1 END, id ASC LIMIT 1",
		projectID, conversationID, "user", domainchat.StatusRunning, domainchat.StatusQueued, domainchat.StatusRunning).Scan(&messageID, &status)
	if err == sql.ErrNoRows {
		return domainchat.Message{}, false, nil
	}
	if err != nil {
		return domainchat.Message{}, false, safeSQLError(ctx, err)
	}
	return transaction.transitionChatMessage(ctx, domainchat.TurnTransition{
		ProjectID: projectID, ConversationID: conversationID, MessageID: messageID,
		From: []string{status}, To: domainchat.StatusAborted, OccurredAt: occurredAt,
	})
}

func (transaction *writeTransaction) TransitionTurn(
	ctx context.Context,
	input domainchat.TurnTransition,
) (domainchat.Message, bool, error) {
	return transaction.transitionChatMessage(ctx, input)
}

func (transaction *writeTransaction) CreateAssistantPartial(
	ctx context.Context,
	input domainchat.AssistantPartial,
) (domainchat.Message, error) {
	if domainchat.ValidateAssistantPartial(input) != nil {
		return domainchat.Message{}, repository.ErrInvalidAutomation
	}
	status := domainchat.StatusStreaming
	return transaction.AppendChatMessage(ctx, domainchat.MessageInput{
		ProjectID: input.ProjectID, ConversationID: input.ConversationID, Role: "assistant",
		Content: input.Content, ToolCalls: input.ToolCalls, ToolResult: input.ToolResult,
		Status: &status, CreatedAt: input.OccurredAt,
	})
}

func (transaction *writeTransaction) AppendAssistantDelta(
	ctx context.Context,
	input domainchat.AssistantDelta,
) (domainchat.Message, bool, error) {
	return transaction.appendAssistantDelta(ctx, input)
}

func (transaction *writeTransaction) InterruptActiveMessages(
	ctx context.Context,
	occurredAt string,
) ([]domainchat.Message, error) {
	if !domainchat.ValidUTCTimestamp(occurredAt) {
		return nil, repository.ErrInvalidAutomation
	}
	rows, err := transaction.tx.QueryContext(ctx,
		"SELECT "+chatMessageSelectColumns+" FROM chat_messages WHERE status IN (?, ?) ORDER BY project_id ASC, conversation_id ASC, id ASC",
		domainchat.StatusRunning, domainchat.StatusStreaming)
	if err != nil {
		return nil, safeSQLError(ctx, err)
	}
	active := make([]domainchat.Message, 0)
	for rows.Next() {
		message, scanErr := scanChatMessage(rows)
		if scanErr != nil {
			_ = rows.Close()
			return nil, safeSQLError(ctx, scanErr)
		}
		active = append(active, message)
	}
	if err := rows.Close(); err != nil {
		return nil, safeSQLError(ctx, err)
	}
	if len(active) == 0 {
		return active, nil
	}
	for index, message := range active {
		updated, changed, transitionErr := transaction.transitionChatMessage(ctx, domainchat.TurnTransition{
			ProjectID: message.ProjectID, ConversationID: message.ConversationID, MessageID: message.ID,
			From: []string{domainchat.StatusRunning, domainchat.StatusStreaming},
			To:   domainchat.StatusInterrupted, OccurredAt: occurredAt,
		})
		if transitionErr != nil {
			return nil, transitionErr
		}
		if changed {
			active[index] = updated
		}
	}
	return active, nil
}

func (transaction *writeTransaction) ClearConversationMessages(
	ctx context.Context,
	projectID, conversationID int64,
	occurredAt string,
) (int64, error) {
	if projectID <= 0 || conversationID <= 0 || !domainchat.ValidUTCTimestamp(occurredAt) {
		return 0, repository.ErrInvalidAutomation
	}
	if _, found, err := transaction.GetConversation(ctx, projectID, conversationID); err != nil || !found {
		if err != nil {
			return 0, err
		}
		return 0, repository.ErrNotFound
	}
	var active int
	if err := transaction.tx.QueryRowContext(ctx,
		"SELECT COUNT(1) FROM chat_messages WHERE project_id = ? AND conversation_id = ? AND status IN (?, ?)",
		projectID, conversationID, domainchat.StatusRunning, domainchat.StatusStreaming).Scan(&active); err != nil {
		return 0, safeSQLError(ctx, err)
	}
	if active > 0 {
		return 0, repository.ErrCapabilityDisabled
	}
	result, err := transaction.tx.ExecContext(ctx,
		"DELETE FROM chat_messages WHERE project_id = ? AND conversation_id = ?", projectID, conversationID)
	if err != nil {
		return 0, safeSQLError(ctx, err)
	}
	count, err := rowsAffected(result)
	if err != nil {
		return 0, err
	}
	if count > 0 {
		if err := transaction.wrote("chat-messages:clear"); err != nil {
			return 0, err
		}
		if err := transaction.touchConversationForChat(ctx, projectID, conversationID, occurredAt); err != nil {
			return 0, err
		}
	}
	return count, nil
}

func (transaction *writeTransaction) AppendQueueEvent(ctx context.Context, input domainchat.QueueEvent) error {
	if domainchat.ValidateQueueEvent(input) != nil {
		return repository.ErrInvalidAutomation
	}
	if _, err := transaction.appendBusinessEvent(ctx, BusinessEvent{
		ProjectID: input.ProjectID, Type: input.Type, RequestID: input.RequestID,
		OccurredAt: input.OccurredAt, Payload: input.Payload,
	}); err != nil {
		return err
	}
	return transaction.appendRuntimeAudit(ctx, input.ProjectID, input.Type, input.AuditLabel, input.OccurredAt, input.Payload)
}

func (transaction *writeTransaction) findChatAdmission(
	ctx context.Context,
	input domainchat.Admission,
) (domainchat.AdmissionResult, bool, error) {
	scope := chatAdmissionScope(input)
	record, found, err := transaction.FindIdempotency(ctx, scope, input.IdempotencyKey)
	if err != nil || !found {
		return domainchat.AdmissionResult{}, found, err
	}
	if record.Route != chatAdmissionRoute || record.RequestHash != input.RequestHash || record.ProjectID == nil ||
		*record.ProjectID != input.ProjectID {
		return domainchat.AdmissionResult{}, false, repository.ErrIdempotencyKeyReuse
	}
	if record.Status != "succeeded" || record.ResultJSON == nil {
		return domainchat.AdmissionResult{}, false, repository.ErrDuplicate
	}
	var reference chatAdmissionReference
	if json.Unmarshal([]byte(*record.ResultJSON), &reference) != nil || reference.MessageID <= 0 ||
		reference.ConversationID != input.ConversationID || reference.TurnID != chatTurnID(reference.MessageID) {
		return domainchat.AdmissionResult{}, false, repository.ErrTransaction
	}
	message, exists, err := transaction.getChatMessage(ctx, input.ProjectID, input.ConversationID, reference.MessageID)
	if err != nil {
		return domainchat.AdmissionResult{}, false, err
	}
	if !exists {
		return domainchat.AdmissionResult{}, false, repository.ErrTransaction
	}
	return domainchat.AdmissionResult{
		Message: message, TurnID: reference.TurnID, AdmissionID: record.OperationID, Replayed: true,
	}, true, nil
}

func (transaction *writeTransaction) reserveChatAdmission(ctx context.Context, input domainchat.Admission) error {
	projectID := input.ProjectID
	return transaction.ReserveIdempotency(ctx, repository.IdempotencyRecord{
		OperationID: input.AdmissionID, ProjectID: &projectID, Route: chatAdmissionRoute,
		RequestID: input.RequestID, Scope: chatAdmissionScope(input), Key: input.IdempotencyKey,
		RequestHash: input.RequestHash, Status: "running", CreatedAt: input.OccurredAt, UpdatedAt: input.OccurredAt,
	})
}

func (transaction *writeTransaction) completeChatAdmission(
	ctx context.Context,
	input domainchat.Admission,
	result domainchat.AdmissionResult,
) error {
	reference, err := json.Marshal(chatAdmissionReference{
		MessageID: result.Message.ID, ConversationID: result.Message.ConversationID, TurnID: result.TurnID,
	})
	if err != nil {
		return repository.ErrTransaction
	}
	encoded := string(reference)
	return transaction.CompleteIdempotency(ctx, chatAdmissionScope(input), input.IdempotencyKey,
		"succeeded", &encoded, nil, input.OccurredAt)
}

func newQueueEvent(
	projectID, conversationID, messageID int64,
	turnID, status string,
	queueCount int,
	requestID, occurredAt, auditLabel string,
) domainchat.QueueEvent {
	payload, _ := json.Marshal(map[string]any{
		"conversation_id": conversationID,
		"message_id":      messageID,
		"turn_id":         turnID,
		"status":          status,
		"queue_count":     queueCount,
	})
	return domainchat.QueueEvent{
		ProjectID: projectID, Type: "business.chat_queue", RequestID: requestID,
		OccurredAt: occurredAt, Payload: payload, AuditLabel: auditLabel,
	}
}

func chatAdmissionScope(input domainchat.Admission) string {
	return "chat:" + input.CallerScope + ":" + strconv.FormatInt(input.ProjectID, 10) + ":" + strconv.FormatInt(input.ConversationID, 10)
}

func chatTurnID(messageID int64) string {
	return "chat-turn-" + strconv.FormatInt(messageID, 10)
}

type chatAdmissionReference struct {
	MessageID      int64  `json:"message_id"`
	ConversationID int64  `json:"conversation_id"`
	TurnID         string `json:"turn_id"`
}

var (
	_ domainchat.QueueTransactional = (*Writer)(nil)
	_ domainchat.QueueTransaction   = (*writeTransaction)(nil)
)
