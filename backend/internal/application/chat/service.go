// Package chat exposes static conversation history operations and a separate
// runtime command handler; direct runtime methods remain non-operational.
package chat

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	domainchat "github.com/lyming99/autoplan/backend/internal/domain/chat"
	"github.com/lyming99/autoplan/backend/internal/repository"
)

var (
	ErrUnavailable     = errors.New("chat application service unavailable")
	ErrInvalidCommand  = errors.New("chat command is invalid")
	ErrStateConflict   = errors.New("chat state conflicts")
	ErrRuntimeDisabled = errors.New("chat runtime capability is disabled")
)

type Clock interface{ Now() time.Time }

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now().UTC() }

type Dependencies struct {
	Writer repository.ChatTransactional
	Queue  domainchat.QueueTransactional
	Clock  Clock
}

type Service struct {
	writer      repository.ChatTransactional
	queueWriter domainchat.QueueTransactional
	clock       Clock
}

func NewService(dependencies Dependencies) *Service {
	clock := dependencies.Clock
	if clock == nil {
		clock = systemClock{}
	}
	queue := dependencies.Queue
	if queue == nil {
		queue, _ = dependencies.Writer.(domainchat.QueueTransactional)
	}
	return &Service{writer: dependencies.Writer, queueWriter: queue, clock: clock}
}

func (service *Service) ready(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if service == nil || service.writer == nil || service.clock == nil {
		return ErrUnavailable
	}
	return service.writer.Check(ctx)
}

func (service *Service) timestamp(after ...string) string {
	next := service.clock.Now().UTC().Truncate(time.Millisecond)
	for _, value := range after {
		parsed, err := time.Parse(time.RFC3339Nano, value)
		if err == nil && !next.After(parsed) {
			next = parsed.Add(time.Millisecond)
		}
	}
	return next.UTC().Format("2006-01-02T15:04:05.000Z")
}

type ConversationDTO struct {
	ID            int64   `json:"id"`
	ProjectID     int64   `json:"project_id"`
	ProjectId     int64   `json:"projectId"`
	Title         string  `json:"title"`
	AIConfigID    *int64  `json:"ai_config_id"`
	AIConfigId    *int64  `json:"aiConfigId"`
	PinnedAt      *string `json:"pinned_at"`
	PinnedAtCamel *string `json:"pinnedAt"`
	Pinned        bool    `json:"pinned"`
	CreatedAt     string  `json:"created_at"`
	CreatedAtCam  string  `json:"createdAt"`
	UpdatedAt     string  `json:"updated_at"`
	UpdatedAtCam  string  `json:"updatedAt"`
}

type MessageDTO struct {
	ID             int64   `json:"id"`
	ProjectID      int64   `json:"project_id"`
	ConversationID int64   `json:"conversation_id"`
	Role           string  `json:"role"`
	Status         *string `json:"status"`
	CreatedAt      string  `json:"created_at"`
	HasContent     bool    `json:"has_content"`
	HasToolCalls   bool    `json:"has_tool_calls"`
	HasToolResult  bool    `json:"has_tool_result"`
}

type ConversationPage struct {
	Items      []ConversationDTO `json:"items"`
	NextCursor string            `json:"next_cursor"`
}

type MessagePage struct {
	Items      []MessageDTO `json:"items"`
	NextCursor string       `json:"next_cursor"`
}

// ChatMessageDTO is the complete P13 history projection. MessageDTO remains
// the old metadata-only shape consumed by static compatibility callers.
type ChatMessageDTO struct {
	ID             int64            `json:"id"`
	ProjectID      int64            `json:"project_id"`
	ProjectId      int64            `json:"projectId"`
	ConversationID int64            `json:"conversation_id"`
	ConversationId int64            `json:"conversationId"`
	Role           string           `json:"role"`
	Content        string           `json:"content"`
	ToolCallsRaw   *string          `json:"tool_calls"`
	ToolCalls      *json.RawMessage `json:"toolCalls"`
	ToolResultRaw  *string          `json:"tool_result"`
	ToolResult     *json.RawMessage `json:"toolResult"`
	Status         string           `json:"status"`
	CreatedAt      string           `json:"created_at"`
	CreatedAtCam   string           `json:"createdAt"`
}

type ChatHistoryPage struct {
	Items      []ChatMessageDTO `json:"items"`
	NextCursor string           `json:"next_cursor"`
}

type CreateConversationCommand struct {
	ProjectID int64
	Input     domainchat.ConversationInput
}

type UpdateConversationCommand struct {
	ProjectID      int64
	ConversationID int64
	Input          domainchat.ConversationInput
}

func (service *Service) ListConversations(ctx context.Context, options domainchat.ConversationListOptions) (ConversationPage, error) {
	if err := service.ready(ctx); err != nil {
		return ConversationPage{}, err
	}
	if options.ProjectID <= 0 {
		return ConversationPage{}, ErrInvalidCommand
	}
	var result ConversationPage
	err := service.writer.TransactChat(ctx, func(transaction repository.ChatWriteTransaction) error {
		records, cursor, err := transaction.ListConversations(ctx, options)
		if err != nil {
			return err
		}
		result.Items = make([]ConversationDTO, 0, len(records))
		for _, record := range records {
			result.Items = append(result.Items, conversationDTO(record))
		}
		result.NextCursor = cursor
		return nil
	})
	return result, mapError(err)
}

func (service *Service) GetConversation(ctx context.Context, projectID, conversationID int64) (ConversationDTO, error) {
	if err := service.ready(ctx); err != nil {
		return ConversationDTO{}, err
	}
	if projectID <= 0 || conversationID <= 0 {
		return ConversationDTO{}, ErrInvalidCommand
	}
	var result ConversationDTO
	err := service.writer.TransactChat(ctx, func(transaction repository.ChatWriteTransaction) error {
		record, found, err := transaction.GetConversation(ctx, projectID, conversationID)
		if err != nil {
			return err
		}
		if !found {
			return repository.ErrNotFound
		}
		result = conversationDTO(record)
		return nil
	})
	return result, mapError(err)
}

func (service *Service) CreateConversation(ctx context.Context, command CreateConversationCommand) (ConversationDTO, error) {
	if err := service.ready(ctx); err != nil {
		return ConversationDTO{}, err
	}
	if command.ProjectID <= 0 {
		return ConversationDTO{}, ErrInvalidCommand
	}
	var result ConversationDTO
	err := service.writer.TransactChat(ctx, func(transaction repository.ChatWriteTransaction) error {
		record, err := transaction.CreateConversation(ctx, command.ProjectID, command.Input, service.timestamp())
		if err == nil {
			result = conversationDTO(record)
		}
		return err
	})
	return result, mapError(err)
}

func (service *Service) UpdateConversation(ctx context.Context, command UpdateConversationCommand) (ConversationDTO, error) {
	if err := service.ready(ctx); err != nil {
		return ConversationDTO{}, err
	}
	if command.ProjectID <= 0 || command.ConversationID <= 0 {
		return ConversationDTO{}, ErrInvalidCommand
	}
	var result ConversationDTO
	err := service.writer.TransactChat(ctx, func(transaction repository.ChatWriteTransaction) error {
		current, found, err := transaction.GetConversation(ctx, command.ProjectID, command.ConversationID)
		if err != nil || !found {
			if err != nil {
				return err
			}
			return repository.ErrNotFound
		}
		record, err := transaction.UpdateConversation(ctx, command.ProjectID, command.ConversationID, command.Input, service.timestamp(current.UpdatedAt))
		if err == nil {
			result = conversationDTO(record)
		}
		return err
	})
	return result, mapError(err)
}

func (service *Service) DeleteConversation(ctx context.Context, projectID, conversationID int64) (int64, error) {
	if err := service.ready(ctx); err != nil {
		return 0, err
	}
	if projectID <= 0 || conversationID <= 0 {
		return 0, ErrInvalidCommand
	}
	var deletedMessages int64
	err := service.writer.TransactChat(ctx, func(transaction repository.ChatWriteTransaction) error {
		count, err := transaction.DeleteConversation(ctx, projectID, conversationID)
		deletedMessages = count
		return err
	})
	return deletedMessages, mapError(err)
}

func (service *Service) ListHistory(ctx context.Context, options domainchat.MessageListOptions) (MessagePage, error) {
	if err := service.ready(ctx); err != nil {
		return MessagePage{}, err
	}
	if options.ProjectID <= 0 || options.ConversationID <= 0 {
		return MessagePage{}, ErrInvalidCommand
	}
	var result MessagePage
	err := service.writer.TransactChat(ctx, func(transaction repository.ChatWriteTransaction) error {
		records, cursor, err := transaction.ListChatMessages(ctx, options)
		if err != nil {
			return err
		}
		result.Items = make([]MessageDTO, 0, len(records))
		for _, record := range records {
			result.Items = append(result.Items, messageDTO(record))
		}
		result.NextCursor = cursor
		return nil
	})
	return result, mapError(err)
}

// ListChatHistory is the full, transport-neutral P13 history use case.
// It retains stable database ordering and scope validation while suppressing
// unsafe legacy tool records from the public projection.
func (service *Service) ListChatHistory(ctx context.Context, options domainchat.MessageListOptions) (ChatHistoryPage, error) {
	if err := service.ready(ctx); err != nil {
		return ChatHistoryPage{}, err
	}
	if options.ProjectID <= 0 || options.ConversationID <= 0 {
		return ChatHistoryPage{}, ErrInvalidCommand
	}
	var result ChatHistoryPage
	err := service.writer.TransactChat(ctx, func(transaction repository.ChatWriteTransaction) error {
		records, cursor, err := transaction.ListChatMessages(ctx, options)
		if err != nil {
			return err
		}
		result.Items = make([]ChatMessageDTO, 0, len(records))
		for _, record := range records {
			result.Items = append(result.Items, chatMessageDTO(record))
		}
		result.NextCursor = cursor
		return nil
	})
	return result, mapError(err)
}

// AppendStaticMessage is limited to pre-authorized static persistence flows.
// It never starts a model, queue, tool, stream, or title-generation process.
func (service *Service) AppendStaticMessage(ctx context.Context, input domainchat.MessageInput) (MessageDTO, error) {
	if err := service.ready(ctx); err != nil {
		return MessageDTO{}, err
	}
	if input.ProjectID <= 0 || input.ConversationID <= 0 {
		return MessageDTO{}, ErrInvalidCommand
	}
	if input.CreatedAt == "" {
		input.CreatedAt = service.timestamp()
	}
	var result MessageDTO
	err := service.writer.TransactChat(ctx, func(transaction repository.ChatWriteTransaction) error {
		record, err := transaction.AppendChatMessage(ctx, input)
		if err == nil {
			result = messageDTO(record)
		}
		return err
	})
	return result, mapError(err)
}

// Direct runtime methods remain disabled for compatibility. P002 runtime
// commands are validated by RuntimeHandler and dispatched through the shared
// application bridge rather than this static persistence service.
func (service *Service) Send(context.Context, int64, int64, string) error  { return ErrRuntimeDisabled }
func (service *Service) Stop(context.Context, int64, int64) error          { return ErrRuntimeDisabled }
func (service *Service) Pump(context.Context, int64, int64) error          { return ErrRuntimeDisabled }
func (service *Service) GenerateTitle(context.Context, int64, int64) error { return ErrRuntimeDisabled }

func conversationDTO(value domainchat.Conversation) ConversationDTO {
	return ConversationDTO{ID: value.ID, ProjectID: value.ProjectID, ProjectId: value.ProjectID, Title: value.Title,
		AIConfigID: copyID(value.AIConfigID), AIConfigId: copyID(value.AIConfigID), PinnedAt: copyText(value.PinnedAt),
		PinnedAtCamel: copyText(value.PinnedAt), Pinned: value.PinnedAt != nil && *value.PinnedAt != "",
		CreatedAt: value.CreatedAt, CreatedAtCam: value.CreatedAt, UpdatedAt: value.UpdatedAt, UpdatedAtCam: value.UpdatedAt}
}

func messageDTO(value domainchat.Message) MessageDTO {
	return MessageDTO{ID: value.ID, ProjectID: value.ProjectID, ConversationID: value.ConversationID, Role: value.Role,
		Status: copyText(value.Status), CreatedAt: value.CreatedAt, HasContent: value.Content != "",
		HasToolCalls: value.ToolCalls != nil, HasToolResult: value.ToolResult != nil}
}

func chatMessageDTO(value domainchat.Message) ChatMessageDTO {
	result := ChatMessageDTO{
		ID: value.ID, ProjectID: value.ProjectID, ProjectId: value.ProjectID,
		ConversationID: value.ConversationID, ConversationId: value.ConversationID,
		Role: value.Role, Content: value.Content, Status: publicMessageStatus(value.Status),
		CreatedAt: value.CreatedAt, CreatedAtCam: value.CreatedAt,
	}
	if domainchat.SafeToolCalls(value.ToolCalls) {
		result.ToolCallsRaw = rawJSONText(value.ToolCalls)
		result.ToolCalls = copyRawJSON(value.ToolCalls)
	}
	if domainchat.SafeToolResult(value.ToolResult) {
		result.ToolResultRaw = rawJSONText(value.ToolResult)
		result.ToolResult = copyRawJSON(value.ToolResult)
	}
	return result
}

func publicMessageStatus(value *string) string {
	if value == nil || *value == "" {
		return domainchat.StatusDone
	}
	if *value == domainchat.StatusRunning {
		return domainchat.StatusQueued
	}
	return *value
}

func copyRawJSON(value *json.RawMessage) *json.RawMessage {
	if value == nil {
		return nil
	}
	copy := append(json.RawMessage(nil), (*value)...)
	return &copy
}

func rawJSONText(value *json.RawMessage) *string {
	if value == nil {
		return nil
	}
	text := string(*value)
	return &text
}

func copyText(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func copyID(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func mapError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, repository.ErrVersionConflict) || errors.Is(err, repository.ErrDuplicate) {
		return ErrStateConflict
	}
	if errors.Is(err, repository.ErrInvalidAutomation) || errors.Is(err, domainchat.ErrInvalidConversation) ||
		errors.Is(err, domainchat.ErrInvalidMessage) || errors.Is(err, domainchat.ErrInvalidCursor) {
		return ErrInvalidCommand
	}
	return err
}
