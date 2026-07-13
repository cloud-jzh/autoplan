package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/lyming99/autoplan/backend/internal/application"
	applicationchat "github.com/lyming99/autoplan/backend/internal/application/chat"
	domainchat "github.com/lyming99/autoplan/backend/internal/domain/chat"
	"github.com/lyming99/autoplan/backend/internal/repository"
)

const (
	ProjectConversationQueuePath     = "/api/v1/projects/{project_id}/conversations/{conversation_id}/queue"
	ProjectConversationQueueItemPath = "/api/v1/projects/{project_id}/conversations/{conversation_id}/queue/{message_id}"
	ProjectConversationStopPath      = "/api/v1/projects/{project_id}/conversations/{conversation_id}:stop"
	ProjectConversationEventsPath    = "/api/v1/projects/{project_id}/conversations/{conversation_id}/events"
)

// ChatHTTPService is the complete P13A application boundary. It is deliberately
// narrower than a repository or provider: HTTP can admit and observe turns but
// cannot execute a model, adopt a process, or inspect transport credentials.
type ChatHTTPService interface {
	ListConversations(context.Context, domainchat.ConversationListOptions) (applicationchat.ConversationPage, error)
	GetConversation(context.Context, int64, int64) (applicationchat.ConversationDTO, error)
	CreateConversation(context.Context, applicationchat.CreateConversationCommand) (applicationchat.ConversationDTO, error)
	UpdateConversation(context.Context, applicationchat.UpdateConversationCommand) (applicationchat.ConversationDTO, error)
	DeleteConversation(context.Context, int64, int64) (int64, error)
	ListChatHistory(context.Context, domainchat.MessageListOptions) (applicationchat.ChatHistoryPage, error)
	AdmitTurn(context.Context, applicationchat.SendCommand) (applicationchat.TurnAdmission, error)
	GetQueue(context.Context, int64, int64) (applicationchat.QueueDTO, error)
	EditQueueItem(context.Context, applicationchat.QueueItemCommand) (applicationchat.QueueMutation, error)
	CancelQueueItem(context.Context, applicationchat.QueueItemCommand) (applicationchat.QueueMutation, error)
	ClearQueue(context.Context, int64, int64, string) (applicationchat.QueueMutation, error)
	RequestStop(context.Context, applicationchat.StopCommand) (applicationchat.StopResult, error)
	ClearHistory(context.Context, int64, int64, string) (int64, error)
}

var _ ChatHTTPService = (*applicationchat.Service)(nil)

type p13ConversationRequest struct {
	Title      *string         `json:"title"`
	AIConfigID p13NullableID   `json:"ai_config_id"`
	Pinned     *bool           `json:"pinned"`
	PinnedAt   json.RawMessage `json:"pinned_at"`
}

type p13NullableID struct {
	Present bool
	Value   *int64
}

func (value *p13NullableID) UnmarshalJSON(input []byte) error {
	var parsed *int64
	if err := json.Unmarshal(input, &parsed); err != nil {
		return err
	}
	value.Present, value.Value = true, parsed
	return nil
}

type p13SendRequest struct {
	Message        string `json:"message"`
	IdempotencyKey string `json:"idempotency_key"`
}

type p13QueueEditRequest struct {
	Message string `json:"message"`
}

type p13ConversationPageEnvelope struct {
	Data       []applicationchat.ConversationDTO `json:"data"`
	NextCursor string                            `json:"next_cursor"`
	RequestID  string                            `json:"request_id"`
}

type p13ChatMessagePageEnvelope struct {
	Data       []applicationchat.ChatMessageDTO `json:"data"`
	NextCursor string                           `json:"next_cursor"`
	RequestID  string                           `json:"request_id"`
}

type p13BooleanResult struct {
	OK bool `json:"ok"`
}

type p13StopResult struct {
	Stopped        bool    `json:"stopped"`
	ProjectID      int64   `json:"project_id"`
	ConversationID int64   `json:"conversation_id"`
	OperationID    *string `json:"operation_id"`
}

type p13ClearResult struct {
	Cleared        bool  `json:"cleared"`
	ProjectID      int64 `json:"project_id"`
	ConversationID int64 `json:"conversation_id"`
}

type p13ChatRoute struct {
	ProjectID      int64
	ConversationID int64
	MessageID      int64
	Action         string
}

func p13ConversationsEndpoint(service ChatHTTPService, bodyLimit int64) Endpoint {
	return func(_ application.Boundary, writer http.ResponseWriter, request *http.Request) {
		target, failure := p13ChatTarget(request.URL.Path)
		if failure != nil || target.Action != "conversations" {
			p13WriteRouteError(writer, request, failure)
			return
		}
		switch request.Method {
		case http.MethodGet:
			limit, cursor, failure := chatPagination(request.URL)
			if failure != nil {
				WriteError(writer, request, *failure)
				return
			}
			result, err := service.ListConversations(request.Context(), domainchat.ConversationListOptions{
				ProjectID: target.ProjectID, Limit: limit, Cursor: cursor,
			})
			if err != nil {
				writeP13ChatError(writer, request, err)
				return
			}
			WriteResponse(writer, request, http.StatusOK, p13ConversationPageEnvelope{
				Data: result.Items, NextCursor: result.NextCursor, RequestID: RequestID(request.Context()),
			})
		case http.MethodPost:
			if _, failure := mutationRequestContext(request); failure != nil {
				WriteError(writer, request, *failure)
				return
			}
			var input p13ConversationRequest
			if failure := DecodeJSON(writer, request, &input, bodyLimit); failure != nil {
				WriteError(writer, request, *failure)
				return
			}
			conversationInput, failure := input.toConversationInput(false)
			if failure != nil {
				WriteError(writer, request, *failure)
				return
			}
			result, err := service.CreateConversation(request.Context(), applicationchat.CreateConversationCommand{
				ProjectID: target.ProjectID, Input: conversationInput,
			})
			if err != nil {
				writeP13ChatError(writer, request, err)
				return
			}
			WriteResponse(writer, request, http.StatusCreated, responseEnvelope{Data: result, RequestID: RequestID(request.Context())})
		}
	}
}

func p13ConversationEndpoint(service ChatHTTPService, bodyLimit int64) Endpoint {
	return func(_ application.Boundary, writer http.ResponseWriter, request *http.Request) {
		target, failure := p13ChatTarget(request.URL.Path)
		if failure != nil || target.Action != "conversation" {
			p13WriteRouteError(writer, request, failure)
			return
		}
		switch request.Method {
		case http.MethodGet:
			result, err := service.GetConversation(request.Context(), target.ProjectID, target.ConversationID)
			if err != nil {
				writeP13ChatError(writer, request, err)
				return
			}
			WriteResponse(writer, request, http.StatusOK, responseEnvelope{Data: result, RequestID: RequestID(request.Context())})
		case http.MethodPatch:
			if _, failure := mutationRequestContext(request); failure != nil {
				WriteError(writer, request, *failure)
				return
			}
			var input p13ConversationRequest
			if failure := DecodeJSON(writer, request, &input, bodyLimit); failure != nil {
				WriteError(writer, request, *failure)
				return
			}
			conversationInput, failure := input.toConversationInput(true)
			if failure != nil {
				WriteError(writer, request, *failure)
				return
			}
			result, err := service.UpdateConversation(request.Context(), applicationchat.UpdateConversationCommand{
				ProjectID: target.ProjectID, ConversationID: target.ConversationID, Input: conversationInput,
			})
			if err != nil {
				writeP13ChatError(writer, request, err)
				return
			}
			WriteResponse(writer, request, http.StatusOK, responseEnvelope{Data: result, RequestID: RequestID(request.Context())})
		case http.MethodDelete:
			if _, failure := mutationRequestContext(request); failure != nil {
				WriteError(writer, request, *failure)
				return
			}
			deleted, err := service.DeleteConversation(request.Context(), target.ProjectID, target.ConversationID)
			if err != nil {
				writeP13ChatError(writer, request, err)
				return
			}
			WriteResponse(writer, request, http.StatusOK, responseEnvelope{Data: map[string]int64{"deleted_messages": deleted}, RequestID: RequestID(request.Context())})
		}
	}
}

func p13MessagesEndpoint(service ChatHTTPService, bodyLimit int64) Endpoint {
	return func(_ application.Boundary, writer http.ResponseWriter, request *http.Request) {
		target, failure := p13ChatTarget(request.URL.Path)
		if failure != nil || target.Action != "messages" {
			p13WriteRouteError(writer, request, failure)
			return
		}
		if !p13RequireChatConversation(writer, request, service, target) {
			return
		}
		switch request.Method {
		case http.MethodGet:
			limit, cursor, failure := chatPagination(request.URL)
			if failure != nil {
				WriteError(writer, request, *failure)
				return
			}
			result, err := service.ListChatHistory(request.Context(), domainchat.MessageListOptions{
				ProjectID: target.ProjectID, ConversationID: target.ConversationID, Limit: limit, Cursor: cursor,
			})
			if err != nil {
				writeP13ChatError(writer, request, err)
				return
			}
			WriteResponse(writer, request, http.StatusOK, p13ChatMessagePageEnvelope{
				Data: result.Items, NextCursor: result.NextCursor, RequestID: RequestID(request.Context()),
			})
		case http.MethodPost:
			mutation, failure := mutationRequestContext(request)
			if failure != nil {
				WriteError(writer, request, *failure)
				return
			}
			var input p13SendRequest
			if failure := DecodeJSON(writer, request, &input, bodyLimit); failure != nil {
				WriteError(writer, request, *failure)
				return
			}
			if !validP13IdempotencyKey(input.IdempotencyKey) ||
				(mutation.IdempotencyKey != "" && input.IdempotencyKey != "" && mutation.IdempotencyKey != input.IdempotencyKey) {
				WriteError(writer, request, NewAPIError(CodeInvalidIdempotencyKey, &ErrorDetails{Field: "idempotency_key"}))
				return
			}
			if mutation.IdempotencyKey == "" {
				mutation.IdempotencyKey = input.IdempotencyKey
			}
			result, err := service.AdmitTurn(request.Context(), applicationchat.SendCommand{
				ProjectID: target.ProjectID, ConversationID: target.ConversationID, Message: input.Message,
				RequestID: mutation.RequestID, CallerScope: mutation.CallerScope, IdempotencyKey: mutation.IdempotencyKey,
			})
			if err != nil {
				writeP13ChatError(writer, request, err)
				return
			}
			WriteResponse(writer, request, http.StatusAccepted, responseEnvelope{Data: result, RequestID: RequestID(request.Context())})
		case http.MethodDelete:
			mutation, failure := mutationRequestContext(request)
			if failure != nil {
				WriteError(writer, request, *failure)
				return
			}
			cleared, err := service.ClearHistory(request.Context(), target.ProjectID, target.ConversationID, mutation.RequestID)
			if err != nil {
				writeP13ChatError(writer, request, err)
				return
			}
			WriteResponse(writer, request, http.StatusOK, responseEnvelope{Data: p13ClearResult{
				Cleared: cleared > 0, ProjectID: target.ProjectID, ConversationID: target.ConversationID,
			}, RequestID: RequestID(request.Context())})
		}
	}
}

func p13QueueEndpoint(service ChatHTTPService) Endpoint {
	return func(_ application.Boundary, writer http.ResponseWriter, request *http.Request) {
		target, failure := p13ChatTarget(request.URL.Path)
		if failure != nil || target.Action != "queue" {
			p13WriteRouteError(writer, request, failure)
			return
		}
		if !p13RequireChatConversation(writer, request, service, target) {
			return
		}
		switch request.Method {
		case http.MethodGet:
			if !emptyQuery(request.URL.RawQuery) {
				WriteError(writer, request, NewAPIError(CodeInvalidPagination, &ErrorDetails{Field: "query"}))
				return
			}
			result, err := service.GetQueue(request.Context(), target.ProjectID, target.ConversationID)
			if err != nil {
				writeP13ChatError(writer, request, err)
				return
			}
			WriteResponse(writer, request, http.StatusOK, responseEnvelope{Data: result, RequestID: RequestID(request.Context())})
		case http.MethodDelete:
			mutation, failure := mutationRequestContext(request)
			if failure != nil {
				WriteError(writer, request, *failure)
				return
			}
			result, err := service.ClearQueue(request.Context(), target.ProjectID, target.ConversationID, mutation.RequestID)
			if err != nil {
				writeP13ChatError(writer, request, err)
				return
			}
			WriteResponse(writer, request, http.StatusOK, responseEnvelope{Data: p13BooleanResult{OK: result.Changed}, RequestID: RequestID(request.Context())})
		}
	}
}

func p13QueueItemEndpoint(service ChatHTTPService, bodyLimit int64) Endpoint {
	return func(_ application.Boundary, writer http.ResponseWriter, request *http.Request) {
		target, failure := p13ChatTarget(request.URL.Path)
		if failure != nil || target.Action != "queue_item" {
			p13WriteRouteError(writer, request, failure)
			return
		}
		if !p13RequireChatConversation(writer, request, service, target) {
			return
		}
		mutation, failure := mutationRequestContext(request)
		if failure != nil {
			WriteError(writer, request, *failure)
			return
		}
		command := applicationchat.QueueItemCommand{
			ProjectID: target.ProjectID, ConversationID: target.ConversationID, MessageID: target.MessageID, RequestID: mutation.RequestID,
		}
		var err error
		switch request.Method {
		case http.MethodPatch:
			var input p13QueueEditRequest
			if failure := DecodeJSON(writer, request, &input, bodyLimit); failure != nil {
				WriteError(writer, request, *failure)
				return
			}
			command.Message = input.Message
			_, err = service.EditQueueItem(request.Context(), command)
		case http.MethodDelete:
			_, err = service.CancelQueueItem(request.Context(), command)
		}
		if err != nil {
			writeP13ChatError(writer, request, err)
			return
		}
		WriteResponse(writer, request, http.StatusOK, responseEnvelope{Data: p13BooleanResult{OK: true}, RequestID: RequestID(request.Context())})
	}
}

func p13StopEndpoint(service ChatHTTPService) Endpoint {
	return func(_ application.Boundary, writer http.ResponseWriter, request *http.Request) {
		target, failure := p13ChatTarget(request.URL.Path)
		if failure != nil || target.Action != "stop" {
			p13WriteRouteError(writer, request, failure)
			return
		}
		if !p13RequireChatConversation(writer, request, service, target) {
			return
		}
		mutation, failure := mutationRequestContext(request)
		if failure != nil {
			WriteError(writer, request, *failure)
			return
		}
		result, err := service.RequestStop(request.Context(), applicationchat.StopCommand{
			ProjectID: target.ProjectID, ConversationID: target.ConversationID, RequestID: mutation.RequestID,
		})
		if err != nil {
			writeP13ChatError(writer, request, err)
			return
		}
		WriteResponse(writer, request, http.StatusOK, responseEnvelope{Data: p13StopResult{
			Stopped: result.Stopped, ProjectID: result.ProjectID, ConversationID: result.ConversationID,
		}, RequestID: RequestID(request.Context())})
	}
}

func (input p13ConversationRequest) toConversationInput(requireMutation bool) (domainchat.ConversationInput, *APIError) {
	result := domainchat.ConversationInput{Title: input.Title, Pinned: input.Pinned}
	if input.AIConfigID.Present {
		if input.AIConfigID.Value == nil {
			clear := int64(0)
			result.AIConfigID = &clear
		} else if *input.AIConfigID.Value > 0 {
			result.AIConfigID = input.AIConfigID.Value
		} else {
			failure := NewAPIError(CodeInvalidConversation, &ErrorDetails{Field: "ai_config_id"})
			return domainchat.ConversationInput{}, &failure
		}
	}
	if len(input.PinnedAt) > 0 {
		var timestamp *string
		if json.Unmarshal(input.PinnedAt, &timestamp) != nil {
			failure := NewAPIError(CodeInvalidConversation, &ErrorDetails{Field: "pinned_at"})
			return domainchat.ConversationInput{}, &failure
		}
		pinned := timestamp != nil
		if timestamp != nil {
			parsed, err := time.Parse(time.RFC3339Nano, *timestamp)
			if err != nil || parsed.Location() != time.UTC || !strings.HasSuffix(*timestamp, "Z") {
				failure := NewAPIError(CodeInvalidConversation, &ErrorDetails{Field: "pinned_at"})
				return domainchat.ConversationInput{}, &failure
			}
		}
		if result.Pinned != nil && *result.Pinned != pinned {
			failure := NewAPIError(CodeInvalidConversation, &ErrorDetails{Field: "pinned"})
			return domainchat.ConversationInput{}, &failure
		}
		result.Pinned = &pinned
	}
	if requireMutation && result.Title == nil && result.AIConfigID == nil && result.Pinned == nil {
		failure := NewAPIError(CodeInvalidConversation, &ErrorDetails{Field: "conversation"})
		return domainchat.ConversationInput{}, &failure
	}
	return result, nil
}

func validP13IdempotencyKey(value string) bool {
	return value == "" || (len(value) <= MaximumIdempotencyLength && idempotencyPattern.MatchString(value))
}

func p13ChatTarget(path string) (p13ChatRoute, *APIError) {
	const prefix = "/api/v1/projects/"
	if !strings.HasPrefix(path, prefix) {
		failure := NewAPIError(CodeNotFound, nil)
		return p13ChatRoute{}, &failure
	}
	segments := strings.Split(strings.TrimPrefix(path, prefix), "/")
	if len(segments) < 2 || segments[1] != "conversations" {
		failure := NewAPIError(CodeNotFound, nil)
		return p13ChatRoute{}, &failure
	}
	projectID, valid := parseCanonicalPositiveID(segments[0])
	if !valid {
		failure := NewAPIError(CodeInvalidProjectID, &ErrorDetails{Field: "project_id"})
		return p13ChatRoute{}, &failure
	}
	target := p13ChatRoute{ProjectID: projectID}
	if len(segments) == 2 {
		target.Action = "conversations"
		return target, nil
	}
	conversationText := segments[2]
	if strings.HasSuffix(conversationText, ":stop") && len(segments) == 3 {
		conversationText = strings.TrimSuffix(conversationText, ":stop")
		target.Action = "stop"
	}
	conversationID, valid := parseCanonicalPositiveID(conversationText)
	if !valid {
		failure := NewAPIError(CodeInvalidConversation, &ErrorDetails{Field: "conversation_id"})
		return p13ChatRoute{}, &failure
	}
	target.ConversationID = conversationID
	if target.Action == "stop" {
		return target, nil
	}
	if len(segments) == 3 {
		target.Action = "conversation"
		return target, nil
	}
	switch {
	case len(segments) == 4 && segments[3] == "messages":
		target.Action = "messages"
	case len(segments) == 4 && segments[3] == "queue":
		target.Action = "queue"
	case len(segments) == 4 && segments[3] == "events":
		target.Action = "events"
	case len(segments) == 5 && segments[3] == "queue":
		messageID, valid := parseCanonicalPositiveID(segments[4])
		if !valid {
			failure := NewAPIError(CodeInvalidConversation, &ErrorDetails{Field: "message_id"})
			return p13ChatRoute{}, &failure
		}
		target.MessageID, target.Action = messageID, "queue_item"
	default:
		failure := NewAPIError(CodeNotFound, nil)
		return p13ChatRoute{}, &failure
	}
	return target, nil
}

func p13WriteRouteError(writer http.ResponseWriter, request *http.Request, failure *APIError) {
	if failure == nil {
		value := NewAPIError(CodeNotFound, nil)
		failure = &value
	}
	WriteError(writer, request, *failure)
}

func p13RequireChatConversation(writer http.ResponseWriter, request *http.Request, service ChatHTTPService, target p13ChatRoute) bool {
	if _, err := service.GetConversation(request.Context(), target.ProjectID, target.ConversationID); err != nil {
		writeP13ChatError(writer, request, err)
		return false
	}
	return true
}

func writeP13ChatError(writer http.ResponseWriter, request *http.Request, err error) {
	code := CodeInternal
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		code = CodeRequestTimeout
	case errors.Is(err, applicationchat.ErrInvalidCommand), errors.Is(err, domainchat.ErrInvalidConversation),
		errors.Is(err, domainchat.ErrInvalidMessage), errors.Is(err, domainchat.ErrInvalidQueue):
		code = CodeInvalidConversation
	case errors.Is(err, domainchat.ErrInvalidCursor):
		code = CodeInvalidCursor
	case errors.Is(err, applicationchat.ErrQueueItemNotFound):
		code = CodeChatQueueItemNotFound
	case errors.Is(err, applicationchat.ErrTurnNotFound):
		code = CodeChatTurnNotFound
	case errors.Is(err, applicationchat.ErrIdempotencyConflict), errors.Is(err, repository.ErrIdempotencyKeyReuse):
		code = CodeChatIdempotencyConflict
	case errors.Is(err, applicationchat.ErrStateConflict), errors.Is(err, repository.ErrVersionConflict),
		errors.Is(err, repository.ErrCapabilityDisabled):
		code = CodeChatTurnStateConflict
	case errors.Is(err, repository.ErrNotFound), errors.Is(err, repository.ErrProjectMismatch):
		code = CodeConversationNotFound
	case errors.Is(err, applicationchat.ErrUnavailable), errors.Is(err, applicationchat.ErrRuntimeDisabled),
		errors.Is(err, repository.ErrNotConfigured), errors.Is(err, repository.ErrClosed), errors.Is(err, repository.ErrWriterUnauthorized):
		code = CodeChatRuntimeUnavailable
	case errors.Is(err, repository.ErrTransaction), errors.Is(err, repository.ErrCommit), errors.Is(err, repository.ErrRollback):
		code = CodeRepositoryBusy
	case errors.Is(err, repository.ErrSchemaDrift):
		code = CodeRepositorySchemaDrift
	}
	WriteError(writer, request, NewAPIError(code, nil))
}
