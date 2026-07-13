package chat

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	domainchat "github.com/lyming99/autoplan/backend/internal/domain/chat"
	"github.com/lyming99/autoplan/backend/internal/repository"
)

var (
	ErrQueueItemNotFound   = errors.New("chat queue item not found")
	ErrTurnNotFound        = errors.New("chat turn not found")
	ErrIdempotencyConflict = errors.New("chat idempotency key conflicts")
)

type QueueItemDTO struct {
	ID      int64  `json:"id"`
	Content string `json:"content"`
	State   string `json:"state"`
}

type QueueDTO struct {
	ProjectID      int64          `json:"project_id"`
	ProjectId      int64          `json:"projectId"`
	ConversationID int64          `json:"conversation_id"`
	ConversationId int64          `json:"conversationId"`
	Items          []QueueItemDTO `json:"items"`
	Count          int            `json:"count"`
}

type SendCommand struct {
	ProjectID      int64
	ConversationID int64
	Message        string
	RequestID      string
	CallerScope    string
	IdempotencyKey string
}

type TurnAdmission struct {
	Accepted       bool    `json:"accepted"`
	ProjectID      int64   `json:"project_id"`
	ConversationID int64   `json:"conversation_id"`
	MessageID      int64   `json:"message_id"`
	TurnID         string  `json:"turn_id"`
	OperationID    *string `json:"operation_id"`
	AdmissionID    string  `json:"-"`
	Replayed       bool    `json:"-"`
}

type QueueItemCommand struct {
	ProjectID      int64
	ConversationID int64
	MessageID      int64
	Message        string
	RequestID      string
}

type QueueMutation struct {
	Changed bool
	Queue   QueueDTO
}

type TurnClaim struct {
	Claimed        bool
	ProjectID      int64
	ConversationID int64
	MessageID      int64
	TurnID         string
	Queue          QueueDTO
}

type StopCommand struct {
	ProjectID      int64
	ConversationID int64
	MessageID      int64
	RequestID      string
}

type StopResult struct {
	Stopped        bool
	ProjectID      int64
	ConversationID int64
	MessageID      int64
	TurnID         string
	Queue          QueueDTO
}

type CreatePartialCommand struct {
	ProjectID      int64
	ConversationID int64
	Content        string
	ToolCalls      *json.RawMessage
	ToolResult     *json.RawMessage
	RequestID      string
}

type AppendPartialCommand struct {
	ProjectID      int64
	ConversationID int64
	MessageID      int64
	Content        string
	RequestID      string
}

type FinishTurnCommand struct {
	ProjectID          int64
	ConversationID     int64
	UserMessageID      int64
	AssistantMessageID int64
	Status             string
	RequestID          string
}

func (service *Service) Configured() bool {
	return service != nil && service.writer != nil && service.queueWriter != nil && service.clock != nil
}

func (service *Service) queueReady(ctx context.Context) error {
	if err := service.ready(ctx); err != nil {
		return err
	}
	if service.queueWriter == nil {
		return ErrUnavailable
	}
	return service.queueWriter.Check(ctx)
}

func (service *Service) EnsureDefaultConversation(ctx context.Context, projectID int64) (ConversationDTO, error) {
	if err := service.queueReady(ctx); err != nil {
		return ConversationDTO{}, err
	}
	if projectID <= 0 {
		return ConversationDTO{}, ErrInvalidCommand
	}
	var result domainchat.Conversation
	err := service.queueWriter.TransactChatQueue(ctx, func(transaction domainchat.QueueTransaction) error {
		value, err := transaction.EnsureDefaultConversation(ctx, projectID, service.timestamp())
		if err == nil {
			result = value
		}
		return err
	})
	if err != nil {
		return ConversationDTO{}, mapQueueError(err)
	}
	return conversationDTO(result), nil
}

func (service *Service) GetQueue(ctx context.Context, projectID, conversationID int64) (QueueDTO, error) {
	if err := service.queueReady(ctx); err != nil {
		return QueueDTO{}, err
	}
	if projectID <= 0 || conversationID <= 0 {
		return QueueDTO{}, ErrInvalidCommand
	}
	var result QueueDTO
	err := service.queueWriter.TransactChatQueue(ctx, func(transaction domainchat.QueueTransaction) error {
		queue, err := transaction.ListQueue(ctx, projectID, conversationID)
		if err != nil {
			return err
		}
		result = queueDTO(projectID, conversationID, queue)
		return nil
	})
	if err != nil {
		return QueueDTO{}, mapQueueError(err)
	}
	return result, nil
}

// AdmitTurn persists one user message and its durable queue/audit events.
// It never invokes a provider; P003 receives the claimed turn through
// ClaimNextTurn after the admission transaction has committed.
func (service *Service) AdmitTurn(ctx context.Context, command SendCommand) (TurnAdmission, error) {
	if err := service.queueReady(ctx); err != nil {
		return TurnAdmission{}, err
	}
	if command.ProjectID <= 0 || strings.TrimSpace(command.Message) == "" || len(command.Message) > 1000000 ||
		strings.ContainsRune(command.Message, 0) || !validRequestID(command.RequestID) {
		return TurnAdmission{}, ErrInvalidCommand
	}
	occurredAt := service.timestamp()
	var result domainchat.AdmissionResult
	conversationID := command.ConversationID
	err := service.queueWriter.TransactChatQueue(ctx, func(transaction domainchat.QueueTransaction) error {
		if conversationID == 0 {
			conversation, ensureErr := transaction.EnsureDefaultConversation(ctx, command.ProjectID, occurredAt)
			if ensureErr != nil {
				return ensureErr
			}
			conversationID = conversation.ID
		}
		admission, prepareErr := prepareAdmission(command, conversationID, occurredAt)
		if prepareErr != nil {
			return prepareErr
		}
		value, admitErr := transaction.AdmitQueuedMessage(ctx, admission)
		if admitErr == nil {
			result = value
		}
		return admitErr
	})
	if err != nil {
		return TurnAdmission{}, mapQueueError(err)
	}
	admission := TurnAdmission{
		Accepted: true, ProjectID: command.ProjectID, ConversationID: conversationID, MessageID: result.Message.ID,
		TurnID: result.TurnID, AdmissionID: result.AdmissionID, Replayed: result.Replayed,
	}
	return admission, nil
}

func (service *Service) EditQueueItem(ctx context.Context, command QueueItemCommand) (QueueMutation, error) {
	if err := service.queueReady(ctx); err != nil {
		return QueueMutation{}, err
	}
	if !validQueueCommand(command, true) {
		return QueueMutation{}, ErrInvalidCommand
	}
	var result QueueMutation
	err := service.queueWriter.TransactChatQueue(ctx, func(transaction domainchat.QueueTransaction) error {
		message, changed, err := transaction.EditQueuedMessage(ctx, command.ProjectID, command.ConversationID, command.MessageID,
			command.Message, service.timestamp())
		if err != nil {
			return err
		}
		if !changed {
			if message.ID == 0 {
				return repository.ErrNotFound
			}
			return repository.ErrCapabilityDisabled
		}
		queue, err := transaction.ListQueue(ctx, command.ProjectID, command.ConversationID)
		if err != nil {
			return err
		}
		if err := transaction.AppendQueueEvent(ctx, queueEvent(command.ProjectID, command.ConversationID, message.ID,
			turnID(message.ID), domainchat.StatusQueued, len(queue), command.RequestID, service.timestamp(), "chat queue item edited")); err != nil {
			return err
		}
		result = QueueMutation{Changed: true, Queue: queueDTO(command.ProjectID, command.ConversationID, queue)}
		return nil
	})
	if err != nil {
		return QueueMutation{}, mapQueueError(err)
	}
	return result, nil
}

func (service *Service) CancelQueueItem(ctx context.Context, command QueueItemCommand) (QueueMutation, error) {
	if err := service.queueReady(ctx); err != nil {
		return QueueMutation{}, err
	}
	if !validQueueCommand(command, false) {
		return QueueMutation{}, ErrInvalidCommand
	}
	var result QueueMutation
	err := service.queueWriter.TransactChatQueue(ctx, func(transaction domainchat.QueueTransaction) error {
		changed, err := transaction.CancelQueuedMessage(ctx, command.ProjectID, command.ConversationID, command.MessageID, service.timestamp())
		if err != nil {
			return err
		}
		if !changed {
			return repository.ErrCapabilityDisabled
		}
		queue, err := transaction.ListQueue(ctx, command.ProjectID, command.ConversationID)
		if err != nil {
			return err
		}
		if err := transaction.AppendQueueEvent(ctx, queueEvent(command.ProjectID, command.ConversationID, command.MessageID,
			turnID(command.MessageID), domainchat.StatusAborted, len(queue), command.RequestID, service.timestamp(), "chat queue item cancelled")); err != nil {
			return err
		}
		result = QueueMutation{Changed: true, Queue: queueDTO(command.ProjectID, command.ConversationID, queue)}
		return nil
	})
	if err != nil {
		return QueueMutation{}, mapQueueError(err)
	}
	return result, nil
}

func (service *Service) ClearQueue(ctx context.Context, projectID, conversationID int64, requestID string) (QueueMutation, error) {
	if err := service.queueReady(ctx); err != nil {
		return QueueMutation{}, err
	}
	if projectID <= 0 || conversationID <= 0 || !validRequestID(requestID) {
		return QueueMutation{}, ErrInvalidCommand
	}
	var result QueueMutation
	err := service.queueWriter.TransactChatQueue(ctx, func(transaction domainchat.QueueTransaction) error {
		count, err := transaction.ClearQueuedMessages(ctx, projectID, conversationID, service.timestamp())
		if err != nil {
			return err
		}
		queue, err := transaction.ListQueue(ctx, projectID, conversationID)
		if err != nil {
			return err
		}
		if count > 0 {
			if err := transaction.AppendQueueEvent(ctx, queueEvent(projectID, conversationID, 0, "", domainchat.StatusQueued,
				len(queue), requestID, service.timestamp(), "chat queue cleared")); err != nil {
				return err
			}
		}
		result = QueueMutation{Changed: count > 0, Queue: queueDTO(projectID, conversationID, queue)}
		return nil
	})
	if err != nil {
		return QueueMutation{}, mapQueueError(err)
	}
	return result, nil
}

// ClaimNextTurn is the single persistent FIFO pump. It claims at most one
// oldest queued user message and refuses to claim while the conversation has
// a running user turn. Different conversations can be scheduled by P11/P12
// after this durable claim commits.
func (service *Service) ClaimNextTurn(ctx context.Context, projectID, conversationID int64, requestID string) (TurnClaim, error) {
	if err := service.queueReady(ctx); err != nil {
		return TurnClaim{}, err
	}
	if projectID <= 0 || conversationID <= 0 || !validRequestID(requestID) {
		return TurnClaim{}, ErrInvalidCommand
	}
	var result TurnClaim
	err := service.queueWriter.TransactChatQueue(ctx, func(transaction domainchat.QueueTransaction) error {
		message, claimed, err := transaction.ClaimNextQueuedMessage(ctx, projectID, conversationID, service.timestamp())
		if err != nil {
			return err
		}
		queue, err := transaction.ListQueue(ctx, projectID, conversationID)
		if err != nil {
			return err
		}
		result = TurnClaim{Claimed: claimed, ProjectID: projectID, ConversationID: conversationID,
			Queue: queueDTO(projectID, conversationID, queue)}
		if !claimed {
			return nil
		}
		result.MessageID = message.ID
		result.TurnID = turnID(message.ID)
		return transaction.AppendQueueEvent(ctx, queueEvent(projectID, conversationID, message.ID, result.TurnID,
			domainchat.StatusRunning, len(queue), requestID, service.timestamp(), "chat turn running"))
	})
	if err != nil {
		return TurnClaim{}, mapQueueError(err)
	}
	return result, nil
}

func (service *Service) RequestStop(ctx context.Context, command StopCommand) (StopResult, error) {
	if err := service.queueReady(ctx); err != nil {
		return StopResult{}, err
	}
	if command.ProjectID <= 0 || command.ConversationID <= 0 || !validRequestID(command.RequestID) {
		return StopResult{}, ErrInvalidCommand
	}
	var result StopResult
	err := service.queueWriter.TransactChatQueue(ctx, func(transaction domainchat.QueueTransaction) error {
		var (
			message domainchat.Message
			changed bool
			err     error
		)
		if command.MessageID > 0 {
			message, changed, err = transaction.TransitionTurn(ctx, domainchat.TurnTransition{
				ProjectID: command.ProjectID, ConversationID: command.ConversationID, MessageID: command.MessageID,
				From: []string{domainchat.StatusQueued, domainchat.StatusRunning},
				To:   domainchat.StatusAborted, OccurredAt: service.timestamp(),
			})
		} else {
			message, changed, err = transaction.AbortActiveOrQueuedMessage(ctx, command.ProjectID, command.ConversationID, service.timestamp())
		}
		if err != nil {
			return err
		}
		queue, queueErr := transaction.ListQueue(ctx, command.ProjectID, command.ConversationID)
		if queueErr != nil {
			return queueErr
		}
		result = StopResult{Stopped: changed, ProjectID: command.ProjectID, ConversationID: command.ConversationID,
			Queue: queueDTO(command.ProjectID, command.ConversationID, queue)}
		if !changed {
			return nil
		}
		result.MessageID, result.TurnID = message.ID, turnID(message.ID)
		return transaction.AppendQueueEvent(ctx, queueEvent(command.ProjectID, command.ConversationID, message.ID,
			result.TurnID, domainchat.StatusAborted, len(queue), command.RequestID, service.timestamp(), "chat turn cancellation requested"))
	})
	if err != nil {
		return StopResult{}, mapTurnError(err)
	}
	return result, nil
}

func (service *Service) CreateAssistantPartial(ctx context.Context, command CreatePartialCommand) (int64, error) {
	if err := service.queueReady(ctx); err != nil {
		return 0, err
	}
	if command.ProjectID <= 0 || command.ConversationID <= 0 || !validRequestID(command.RequestID) {
		return 0, ErrInvalidCommand
	}
	var messageID int64
	err := service.queueWriter.TransactChatQueue(ctx, func(transaction domainchat.QueueTransaction) error {
		message, err := transaction.CreateAssistantPartial(ctx, domainchat.AssistantPartial{
			ProjectID: command.ProjectID, ConversationID: command.ConversationID, Content: command.Content,
			ToolCalls: command.ToolCalls, ToolResult: command.ToolResult, OccurredAt: service.timestamp(),
		})
		if err != nil {
			return err
		}
		messageID = message.ID
		return transaction.AppendQueueEvent(ctx, partialEvent(command.ProjectID, command.ConversationID, message.ID,
			0, command.RequestID, service.timestamp(), "chat assistant partial created"))
	})
	if err != nil {
		return 0, mapQueueError(err)
	}
	return messageID, nil
}

func (service *Service) AppendAssistantPartial(ctx context.Context, command AppendPartialCommand) error {
	if err := service.queueReady(ctx); err != nil {
		return err
	}
	if command.ProjectID <= 0 || command.ConversationID <= 0 || command.MessageID <= 0 ||
		command.Content == "" || !validRequestID(command.RequestID) {
		return ErrInvalidCommand
	}
	err := service.queueWriter.TransactChatQueue(ctx, func(transaction domainchat.QueueTransaction) error {
		message, changed, err := transaction.AppendAssistantDelta(ctx, domainchat.AssistantDelta{
			ProjectID: command.ProjectID, ConversationID: command.ConversationID, MessageID: command.MessageID,
			Content: command.Content, OccurredAt: service.timestamp(),
		})
		if err != nil {
			return err
		}
		if !changed {
			if message.ID == 0 {
				return repository.ErrNotFound
			}
			return repository.ErrCapabilityDisabled
		}
		return transaction.AppendQueueEvent(ctx, partialEvent(command.ProjectID, command.ConversationID, message.ID,
			int64(len(command.Content)), command.RequestID, service.timestamp(), "chat assistant partial appended"))
	})
	return mapTurnError(err)
}

func (service *Service) FinishTurn(ctx context.Context, command FinishTurnCommand) error {
	if err := service.queueReady(ctx); err != nil {
		return err
	}
	if command.ProjectID <= 0 || command.ConversationID <= 0 || command.UserMessageID <= 0 ||
		!terminalChatStatus(command.Status) || !validRequestID(command.RequestID) {
		return ErrInvalidCommand
	}
	err := service.queueWriter.TransactChatQueue(ctx, func(transaction domainchat.QueueTransaction) error {
		user, changed, err := transaction.TransitionTurn(ctx, domainchat.TurnTransition{
			ProjectID: command.ProjectID, ConversationID: command.ConversationID, MessageID: command.UserMessageID,
			From: []string{domainchat.StatusRunning}, To: command.Status, OccurredAt: service.timestamp(),
		})
		if err != nil {
			return err
		}
		if !changed {
			if user.ID == 0 {
				return repository.ErrNotFound
			}
			return repository.ErrCapabilityDisabled
		}
		if command.AssistantMessageID > 0 {
			assistant, completed, finishErr := transaction.TransitionTurn(ctx, domainchat.TurnTransition{
				ProjectID: command.ProjectID, ConversationID: command.ConversationID, MessageID: command.AssistantMessageID,
				From: []string{domainchat.StatusStreaming}, To: command.Status, OccurredAt: service.timestamp(),
			})
			if finishErr != nil {
				return finishErr
			}
			if !completed || assistant.Role != "assistant" {
				return repository.ErrCapabilityDisabled
			}
		}
		queue, err := transaction.ListQueue(ctx, command.ProjectID, command.ConversationID)
		if err != nil {
			return err
		}
		return transaction.AppendQueueEvent(ctx, queueEvent(command.ProjectID, command.ConversationID, user.ID,
			turnID(user.ID), command.Status, len(queue), command.RequestID, service.timestamp(), "chat turn terminal"))
	})
	return mapTurnError(err)
}

func (service *Service) ClearHistory(ctx context.Context, projectID, conversationID int64, requestID string) (int64, error) {
	if err := service.queueReady(ctx); err != nil {
		return 0, err
	}
	if projectID <= 0 || conversationID <= 0 || !validRequestID(requestID) {
		return 0, ErrInvalidCommand
	}
	var cleared int64
	err := service.queueWriter.TransactChatQueue(ctx, func(transaction domainchat.QueueTransaction) error {
		count, err := transaction.ClearConversationMessages(ctx, projectID, conversationID, service.timestamp())
		if err != nil {
			return err
		}
		cleared = count
		if count == 0 {
			return nil
		}
		return transaction.AppendQueueEvent(ctx, queueEvent(projectID, conversationID, 0, "", "cleared", 0,
			requestID, service.timestamp(), "chat history cleared"))
	})
	if err != nil {
		return 0, mapQueueError(err)
	}
	return cleared, nil
}

func queueDTO(projectID, conversationID int64, items []domainchat.QueueItem) QueueDTO {
	result := QueueDTO{
		ProjectID: projectID, ProjectId: projectID,
		ConversationID: conversationID, ConversationId: conversationID,
		Items: make([]QueueItemDTO, 0, len(items)), Count: len(items),
	}
	for _, item := range items {
		result.Items = append(result.Items, QueueItemDTO{ID: item.ID, Content: item.Content, State: item.State})
	}
	return result
}

func prepareAdmission(command SendCommand, conversationID int64, occurredAt string) (domainchat.Admission, error) {
	input := domainchat.Admission{
		ProjectID: command.ProjectID, ConversationID: conversationID, Content: strings.TrimSpace(command.Message),
		RequestID: command.RequestID, OccurredAt: occurredAt, CallerScope: command.CallerScope,
		IdempotencyKey: strings.TrimSpace(command.IdempotencyKey),
	}
	if input.IdempotencyKey == "" {
		return input, domainchat.ValidateAdmission(input)
	}
	if strings.TrimSpace(input.CallerScope) == "" {
		return domainchat.Admission{}, ErrInvalidCommand
	}
	fingerprint := sha256.Sum256([]byte(strconv.FormatInt(input.ProjectID, 10) + "\x00" +
		strconv.FormatInt(input.ConversationID, 10) + "\x00" + input.Content))
	input.RequestHash = hex.EncodeToString(fingerprint[:])
	operationHash := sha256.Sum256([]byte(input.CallerScope + "\x00" + input.IdempotencyKey + "\x00" + input.RequestHash))
	input.AdmissionID = "chat-admit-" + hex.EncodeToString(operationHash[:16])
	return input, domainchat.ValidateAdmission(input)
}

func queueEvent(projectID, conversationID, messageID int64, turnID, status string, count int, requestID, occurredAt, label string) domainchat.QueueEvent {
	payload, _ := json.Marshal(map[string]any{
		"conversation_id": conversationID,
		"message_id":      messageID,
		"turn_id":         turnID,
		"status":          status,
		"queue_count":     count,
	})
	return domainchat.QueueEvent{
		ProjectID: projectID, Type: "business.chat_queue", RequestID: requestID,
		OccurredAt: occurredAt, Payload: payload, AuditLabel: label,
	}
}

func partialEvent(projectID, conversationID, messageID, deltaBytes int64, requestID, occurredAt, label string) domainchat.QueueEvent {
	payload, _ := json.Marshal(map[string]any{
		"conversation_id": conversationID,
		"message_id":      messageID,
		"delta_bytes":     deltaBytes,
		"status":          domainchat.StatusStreaming,
	})
	return domainchat.QueueEvent{
		ProjectID: projectID, Type: "business.chat_partial", RequestID: requestID,
		OccurredAt: occurredAt, Payload: payload, AuditLabel: label,
	}
}

func validQueueCommand(command QueueItemCommand, requireMessage bool) bool {
	if command.ProjectID <= 0 || command.ConversationID <= 0 || command.MessageID <= 0 || !validRequestID(command.RequestID) {
		return false
	}
	return !requireMessage || (strings.TrimSpace(command.Message) != "" && len(command.Message) <= 1000000 && !strings.ContainsRune(command.Message, 0))
}

func terminalChatStatus(status string) bool {
	switch status {
	case domainchat.StatusDone, domainchat.StatusAborted, domainchat.StatusError, domainchat.StatusMaxRounds, domainchat.StatusInterrupted:
		return true
	default:
		return false
	}
}

func turnID(messageID int64) string {
	return "chat-turn-" + strconv.FormatInt(messageID, 10)
}

func validRequestID(value string) bool {
	if value == "" || len(value) > 64 || strings.TrimSpace(value) != value || strings.ContainsRune(value, 0) {
		return false
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}

func mapQueueError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, repository.ErrIdempotencyKeyReuse):
		return ErrIdempotencyConflict
	case errors.Is(err, repository.ErrNotFound):
		return ErrQueueItemNotFound
	case errors.Is(err, repository.ErrCapabilityDisabled), errors.Is(err, repository.ErrDuplicate), errors.Is(err, repository.ErrVersionConflict):
		return ErrStateConflict
	default:
		return mapError(err)
	}
}

func mapTurnError(err error) error {
	if errors.Is(err, repository.ErrNotFound) {
		return ErrTurnNotFound
	}
	return mapQueueError(err)
}
