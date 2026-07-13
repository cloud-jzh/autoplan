// Package chat owns persistence-neutral conversation and message invariants.
package chat

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

var (
	ErrInvalidConversation = errors.New("chat conversation is invalid")
	ErrInvalidMessage      = errors.New("chat message is invalid")
	ErrInvalidCursor       = errors.New("chat cursor is invalid")
	ErrInvalidQueue        = errors.New("chat queue is invalid")
	ErrInvalidAdmission    = errors.New("chat admission is invalid")
)

const (
	maximumTitleLength       = 200
	maximumMessageCharacters = 1000000
	maximumQueueItems        = 200

	DefaultConversationTitle = "默认对话"

	StatusStreaming   = "streaming"
	StatusQueued      = "queued"
	StatusRunning     = "running"
	StatusDone        = "done"
	StatusAborted     = "aborted"
	StatusError       = "error"
	StatusMaxRounds   = "max_rounds"
	StatusInterrupted = "interrupted"
)

type Conversation struct {
	ID         int64
	ProjectID  int64
	Title      string
	AIConfigID *int64
	PinnedAt   *string
	CreatedAt  string
	UpdatedAt  string
}

// Message intentionally carries content only within the persistence boundary.
// Public DTOs and snapshots expose presence metadata, never this data.
type Message struct {
	ID             int64
	ProjectID      int64
	ConversationID int64
	Role           string
	Content        string
	ToolCalls      *json.RawMessage
	ToolResult     *json.RawMessage
	Status         *string
	CreatedAt      string
}

type ConversationInput struct {
	Title      *string
	AIConfigID *int64
	Pinned     *bool
}

type MessageInput struct {
	ProjectID      int64
	ConversationID int64
	Role           string
	Content        string
	ToolCalls      *json.RawMessage
	ToolResult     *json.RawMessage
	Status         *string
	CreatedAt      string
}

type ConversationListOptions struct {
	ProjectID int64
	Limit     int
	Cursor    string
}

type MessageListOptions struct {
	ProjectID      int64
	ConversationID int64
	Limit          int
	Cursor         string
}

// QueueItem intentionally preserves the legacy renderer queue shape. A
// running turn is represented by the message lifecycle, while a processing
// item remains visible as the historical queue state during compatibility
// transition.
type QueueItem struct {
	ID      int64
	Content string
	State   string
}

type QueueSnapshot struct {
	ProjectID      int64
	ConversationID int64
	Items          []QueueItem
	Count          int
}

type Admission struct {
	ProjectID      int64
	ConversationID int64
	Content        string
	RequestID      string
	OccurredAt     string
	CallerScope    string
	IdempotencyKey string
	RequestHash    string
	AdmissionID    string
}

type AdmissionResult struct {
	Message     Message
	TurnID      string
	AdmissionID string
	Replayed    bool
}

type TurnTransition struct {
	ProjectID      int64
	ConversationID int64
	MessageID      int64
	From           []string
	To             string
	OccurredAt     string
}

type AssistantPartial struct {
	ProjectID      int64
	ConversationID int64
	Content        string
	ToolCalls      *json.RawMessage
	ToolResult     *json.RawMessage
	OccurredAt     string
}

type AssistantDelta struct {
	ProjectID      int64
	ConversationID int64
	MessageID      int64
	Content        string
	OccurredAt     string
}

type QueueEvent struct {
	ProjectID  int64
	Type       string
	RequestID  string
	OccurredAt string
	Payload    json.RawMessage
	AuditLabel string
}

// QueueTransaction is a narrow persistence port for P13A state changes. It
// deliberately exposes no SQL handle, process control, provider, or event bus
// publish capability. The repository appends durable outbox/audit records in
// the same transaction as the state change.
type QueueTransaction interface {
	EnsureDefaultConversation(context.Context, int64, string) (Conversation, error)
	ListQueue(context.Context, int64, int64) ([]QueueItem, error)
	AdmitQueuedMessage(context.Context, Admission) (AdmissionResult, error)
	EditQueuedMessage(context.Context, int64, int64, int64, string, string) (Message, bool, error)
	CancelQueuedMessage(context.Context, int64, int64, int64, string) (bool, error)
	ClearQueuedMessages(context.Context, int64, int64, string) (int, error)
	ClaimNextQueuedMessage(context.Context, int64, int64, string) (Message, bool, error)
	AbortActiveOrQueuedMessage(context.Context, int64, int64, string) (Message, bool, error)
	TransitionTurn(context.Context, TurnTransition) (Message, bool, error)
	CreateAssistantPartial(context.Context, AssistantPartial) (Message, error)
	AppendAssistantDelta(context.Context, AssistantDelta) (Message, bool, error)
	InterruptActiveMessages(context.Context, string) ([]Message, error)
	ClearConversationMessages(context.Context, int64, int64, string) (int64, error)
	AppendQueueEvent(context.Context, QueueEvent) error
}

type QueueTransactional interface {
	Check(context.Context) error
	TransactChatQueue(context.Context, func(QueueTransaction) error) error
}

type ConversationCursor struct {
	PinnedBucket int    `json:"p"`
	UpdatedAt    string `json:"u"`
	ID           int64  `json:"i"`
}

type MessageCursor struct {
	CreatedAt string `json:"c"`
	ID        int64  `json:"i"`
}

func NormalizeConversationInput(input ConversationInput, current *Conversation, pinnedAt string) (Conversation, error) {
	result := Conversation{}
	if current != nil {
		result = *current
		result.AIConfigID = cloneID(current.AIConfigID)
		result.PinnedAt = cloneText(current.PinnedAt)
	}
	if input.Title != nil {
		result.Title = strings.TrimSpace(*input.Title)
	}
	if input.AIConfigID != nil {
		if *input.AIConfigID <= 0 {
			result.AIConfigID = nil
		} else {
			result.AIConfigID = cloneID(input.AIConfigID)
		}
	}
	if input.Pinned != nil {
		if *input.Pinned {
			if !ValidUTCTimestamp(pinnedAt) {
				return Conversation{}, ErrInvalidConversation
			}
			result.PinnedAt = &pinnedAt
		} else {
			result.PinnedAt = nil
		}
	}
	if len(result.Title) > maximumTitleLength || strings.ContainsRune(result.Title, 0) {
		return Conversation{}, ErrInvalidConversation
	}
	return result, nil
}

func ValidateConversation(value Conversation) error {
	if value.ID <= 0 || value.ProjectID <= 0 || len(value.Title) > maximumTitleLength ||
		strings.ContainsRune(value.Title, 0) || !ValidUTCTimestamp(value.CreatedAt) ||
		!ValidUTCTimestamp(value.UpdatedAt) || !validOptionalTime(value.PinnedAt) ||
		(value.AIConfigID != nil && *value.AIConfigID <= 0) {
		return ErrInvalidConversation
	}
	created, _ := time.Parse(time.RFC3339Nano, value.CreatedAt)
	updated, _ := time.Parse(time.RFC3339Nano, value.UpdatedAt)
	if created.After(updated) {
		return ErrInvalidConversation
	}
	return nil
}

func ValidateMessage(value Message) error {
	if value.ID <= 0 || value.ProjectID <= 0 || value.ConversationID <= 0 ||
		!contains([]string{"user", "assistant", "tool", "system"}, value.Role) ||
		strings.ContainsRune(value.Content, 0) || len(value.Content) > maximumMessageCharacters ||
		!validOptionalJSON(value.ToolCalls, '[') || !validOptionalJSON(value.ToolResult, '{') ||
		!validOptionalStatus(value.Status) || !ValidUTCTimestamp(value.CreatedAt) {
		return ErrInvalidMessage
	}
	return nil
}

func ValidateMessageInput(value MessageInput) error {
	if err := ValidateMessage(Message{
		ID: 1, ProjectID: value.ProjectID, ConversationID: value.ConversationID,
		Role: strings.TrimSpace(value.Role), Content: value.Content, ToolCalls: cloneJSON(value.ToolCalls),
		ToolResult: cloneJSON(value.ToolResult), Status: cloneText(value.Status), CreatedAt: value.CreatedAt,
	}); err != nil {
		return err
	}
	if !validSafeOptionalJSON(value.ToolCalls, '[') || !validSafeOptionalJSON(value.ToolResult, '{') {
		return ErrInvalidMessage
	}
	return nil
}

// SafeToolCalls and SafeToolResult identify values that may leave the
// persistence boundary. Historical records may be readable even when they
// predate P13 redaction rules; callers must omit their raw tool projection
// unless these checks pass.
func SafeToolCalls(value *json.RawMessage) bool {
	return validSafeOptionalJSON(value, '[')
}

func SafeToolResult(value *json.RawMessage) bool {
	return validSafeOptionalJSON(value, '{')
}

func ValidateAdmission(value Admission) error {
	if value.ProjectID <= 0 || value.ConversationID <= 0 || strings.TrimSpace(value.Content) == "" ||
		len(value.Content) > maximumMessageCharacters || strings.ContainsRune(value.Content, 0) ||
		!ValidUTCTimestamp(value.OccurredAt) || !validOpaque(value.RequestID, 64) {
		return ErrInvalidAdmission
	}
	if value.IdempotencyKey == "" {
		return nil
	}
	if !validOpaque(value.CallerScope, 256) || !validOpaque(value.IdempotencyKey, 256) ||
		!validOpaque(value.RequestHash, 64) || !validOpaque(value.AdmissionID, 128) {
		return ErrInvalidAdmission
	}
	return nil
}

func ValidateTurnTransition(value TurnTransition) error {
	if value.ProjectID <= 0 || value.ConversationID <= 0 || value.MessageID <= 0 ||
		!ValidUTCTimestamp(value.OccurredAt) || !isMessageStatus(value.To) || len(value.From) == 0 {
		return ErrInvalidQueue
	}
	for _, status := range value.From {
		if !isMessageStatus(status) {
			return ErrInvalidQueue
		}
	}
	return nil
}

func ValidateAssistantPartial(value AssistantPartial) error {
	if value.ProjectID <= 0 || value.ConversationID <= 0 || len(value.Content) > maximumMessageCharacters ||
		strings.ContainsRune(value.Content, 0) || !ValidUTCTimestamp(value.OccurredAt) ||
		!validSafeOptionalJSON(value.ToolCalls, '[') || !validSafeOptionalJSON(value.ToolResult, '{') {
		return ErrInvalidQueue
	}
	return nil
}

func ValidateAssistantDelta(value AssistantDelta) error {
	if value.ProjectID <= 0 || value.ConversationID <= 0 || value.MessageID <= 0 ||
		value.Content == "" || len(value.Content) > maximumMessageCharacters ||
		strings.ContainsRune(value.Content, 0) || !ValidUTCTimestamp(value.OccurredAt) {
		return ErrInvalidQueue
	}
	return nil
}

func ValidateQueueEvent(value QueueEvent) error {
	if value.ProjectID <= 0 || !validOpaque(value.Type, 128) || !validOpaque(value.RequestID, 64) ||
		!ValidUTCTimestamp(value.OccurredAt) || len(value.Payload) == 0 || len(value.Payload) > 8192 ||
		!json.Valid(value.Payload) || strings.TrimSpace(value.AuditLabel) == "" || len(value.AuditLabel) > 200 {
		return ErrInvalidQueue
	}
	var payload map[string]any
	if json.Unmarshal(value.Payload, &payload) != nil || !safeToolJSON(payload) {
		return ErrInvalidQueue
	}
	return nil
}

func EncodeConversationCursor(value ConversationCursor) (string, error) {
	if (value.PinnedBucket != 0 && value.PinnedBucket != 1) || value.ID <= 0 || !ValidUTCTimestamp(value.UpdatedAt) {
		return "", ErrInvalidCursor
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", ErrInvalidCursor
	}
	return base64.RawURLEncoding.EncodeToString(encoded), nil
}

func DecodeConversationCursor(value string) (*ConversationCursor, error) {
	if value == "" {
		return nil, nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(decoded) > 512 {
		return nil, ErrInvalidCursor
	}
	var cursor ConversationCursor
	if json.Unmarshal(decoded, &cursor) != nil {
		return nil, ErrInvalidCursor
	}
	if _, err := EncodeConversationCursor(cursor); err != nil {
		return nil, err
	}
	return &cursor, nil
}

func EncodeMessageCursor(value MessageCursor) (string, error) {
	if value.ID <= 0 || !ValidUTCTimestamp(value.CreatedAt) {
		return "", ErrInvalidCursor
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", ErrInvalidCursor
	}
	return base64.RawURLEncoding.EncodeToString(encoded), nil
}

func DecodeMessageCursor(value string) (*MessageCursor, error) {
	if value == "" {
		return nil, nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(decoded) > 512 {
		return nil, ErrInvalidCursor
	}
	var cursor MessageCursor
	if json.Unmarshal(decoded, &cursor) != nil {
		return nil, ErrInvalidCursor
	}
	if _, err := EncodeMessageCursor(cursor); err != nil {
		return nil, err
	}
	return &cursor, nil
}

func ValidUTCTimestamp(value string) bool {
	if !strings.HasSuffix(value, "Z") {
		return false
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	return err == nil && parsed.Location() == time.UTC
}

func cloneText(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneID(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneJSON(value *json.RawMessage) *json.RawMessage {
	if value == nil {
		return nil
	}
	copy := append(json.RawMessage(nil), (*value)...)
	return &copy
}

func validOptionalTime(value *string) bool { return value == nil || ValidUTCTimestamp(*value) }

func validOptionalJSON(value *json.RawMessage, prefix byte) bool {
	if value == nil {
		return true
	}
	trimmed := strings.TrimSpace(string(*value))
	return len(trimmed) != 0 && trimmed[0] == prefix && json.Valid([]byte(trimmed))
}

func validSafeOptionalJSON(value *json.RawMessage, prefix byte) bool {
	if value == nil {
		return true
	}
	trimmed := strings.TrimSpace(string(*value))
	if len(trimmed) == 0 || len(trimmed) > 65536 || trimmed[0] != prefix || !json.Valid([]byte(trimmed)) {
		return false
	}
	var decoded any
	if json.Unmarshal([]byte(trimmed), &decoded) != nil {
		return false
	}
	return safeToolJSON(decoded)
}

func validOptionalStatus(value *string) bool {
	return value == nil || isMessageStatus(*value)
}

func isMessageStatus(value string) bool {
	return contains([]string{
		StatusStreaming, StatusQueued, StatusRunning, StatusDone, StatusAborted,
		StatusError, StatusMaxRounds, StatusInterrupted,
	}, value)
}

func validOpaque(value string, maximum int) bool {
	if value == "" || len(value) > maximum || strings.TrimSpace(value) != value || strings.ContainsRune(value, 0) {
		return false
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}

func safeToolJSON(value any) bool {
	return safeToolJSONAtDepth(value, 0)
}

func safeToolJSONAtDepth(value any, depth int) bool {
	if depth > 16 {
		return false
	}
	switch typed := value.(type) {
	case map[string]any:
		if len(typed) > 64 {
			return false
		}
		for key, child := range typed {
			lower := strings.ToLower(strings.ReplaceAll(key, "-", "_"))
			for _, forbidden := range []string{"token", "secret", "password", "api_key", "apikey", "authorization", "cookie", "env", "workspace_path", "stored_path"} {
				if strings.Contains(lower, forbidden) {
					return false
				}
			}
			if !safeToolJSONAtDepth(child, depth+1) {
				return false
			}
		}
	case []any:
		if len(typed) > 128 {
			return false
		}
		for _, child := range typed {
			if !safeToolJSONAtDepth(child, depth+1) {
				return false
			}
		}
	case string:
		trimmed := strings.TrimSpace(typed)
		if len(typed) > 2048 || strings.ContainsRune(typed, 0) || strings.HasPrefix(trimmed, "/") ||
			strings.HasPrefix(strings.ToLower(trimmed), "file:") ||
			(len(trimmed) >= 3 && trimmed[1] == ':' && (trimmed[2] == '\\' || trimmed[2] == '/')) {
			return false
		}
	}
	return true
}

func contains(values []string, candidate string) bool {
	for _, value := range values {
		if value == candidate {
			return true
		}
	}
	return false
}
