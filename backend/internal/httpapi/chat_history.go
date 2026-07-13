package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/lyming99/autoplan/backend/internal/application"
	applicationchat "github.com/lyming99/autoplan/backend/internal/application/chat"
	domainchat "github.com/lyming99/autoplan/backend/internal/domain/chat"
	"github.com/lyming99/autoplan/backend/internal/repository"
)

const (
	ProjectConversationsPath = "/api/v1/projects/{project_id}/conversations"
	ProjectConversationPath  = "/api/v1/projects/{project_id}/conversations/{conversation_id}"
	ProjectMessagesPath      = "/api/v1/projects/{project_id}/conversations/{conversation_id}/messages"
)

type ChatHistoryService interface {
	ListConversations(context.Context, domainchat.ConversationListOptions) (applicationchat.ConversationPage, error)
	GetConversation(context.Context, int64, int64) (applicationchat.ConversationDTO, error)
	CreateConversation(context.Context, applicationchat.CreateConversationCommand) (applicationchat.ConversationDTO, error)
	UpdateConversation(context.Context, applicationchat.UpdateConversationCommand) (applicationchat.ConversationDTO, error)
	DeleteConversation(context.Context, int64, int64) (int64, error)
	ListHistory(context.Context, domainchat.MessageListOptions) (applicationchat.MessagePage, error)
}

var _ ChatHistoryService = (*applicationchat.Service)(nil)

type conversationRequest struct {
	Title      *string `json:"title"`
	AIConfigID *int64  `json:"ai_config_id"`
	Pinned     *bool   `json:"pinned"`
}

type conversationPageEnvelope struct {
	Data       []applicationchat.ConversationDTO `json:"data"`
	NextCursor string                            `json:"next_cursor"`
	RequestID  string                            `json:"request_id"`
}

type messagePageEnvelope struct {
	Data       []applicationchat.MessageDTO `json:"data"`
	NextCursor string                       `json:"next_cursor"`
	RequestID  string                       `json:"request_id"`
}

func RegisterChatHistory(router *Router, security *Security, service ChatHistoryService) error {
	if router == nil || security == nil || service == nil {
		return ErrRouterDependency
	}
	conversations := security.Protect(TransportREST, conversationsEndpoint(service, router.BodyLimitBytes()))
	conversation := security.Protect(TransportREST, conversationEndpoint(service, router.BodyLimitBytes()))
	messages := security.Protect(TransportREST, messagesEndpoint(service))
	for _, route := range []struct {
		method, path string
		endpoint     Endpoint
	}{
		{http.MethodGet, ProjectConversationsPath, conversations}, {http.MethodHead, ProjectConversationsPath, conversations}, {http.MethodPost, ProjectConversationsPath, conversations},
		{http.MethodGet, ProjectConversationPath, conversation}, {http.MethodHead, ProjectConversationPath, conversation}, {http.MethodPatch, ProjectConversationPath, conversation}, {http.MethodDelete, ProjectConversationPath, conversation},
		{http.MethodGet, ProjectMessagesPath, messages}, {http.MethodHead, ProjectMessagesPath, messages},
	} {
		if err := router.HandlePattern(route.method, route.path, route.endpoint); err != nil {
			return err
		}
	}
	return nil
}

func conversationsEndpoint(service ChatHistoryService, bodyLimit int64) Endpoint {
	return func(_ application.Boundary, writer http.ResponseWriter, request *http.Request) {
		projectID, _, suffix, failure := conversationPath(request.URL.Path)
		if failure != nil || suffix != "" {
			if failure == nil {
				value := NewAPIError(CodeNotFound, nil)
				failure = &value
			}
			WriteError(writer, request, *failure)
			return
		}
		switch request.Method {
		case http.MethodGet, http.MethodHead:
			limit, cursor, failure := chatPagination(request.URL)
			if failure != nil {
				WriteError(writer, request, *failure)
				return
			}
			result, err := service.ListConversations(request.Context(), domainchat.ConversationListOptions{ProjectID: projectID, Limit: limit, Cursor: cursor})
			if err != nil {
				writeChatHistoryError(writer, request, err)
				return
			}
			WriteResponse(writer, request, http.StatusOK, conversationPageEnvelope{Data: result.Items, NextCursor: result.NextCursor, RequestID: RequestID(request.Context())})
		case http.MethodPost:
			var input conversationRequest
			if failure := DecodeJSON(writer, request, &input, bodyLimit); failure != nil {
				WriteError(writer, request, *failure)
				return
			}
			result, err := service.CreateConversation(request.Context(), applicationchat.CreateConversationCommand{ProjectID: projectID, Input: domainchat.ConversationInput{Title: input.Title, AIConfigID: input.AIConfigID, Pinned: input.Pinned}})
			if err != nil {
				writeChatHistoryError(writer, request, err)
				return
			}
			WriteResponse(writer, request, http.StatusCreated, responseEnvelope{Data: result, RequestID: RequestID(request.Context())})
		}
	}
}

func conversationEndpoint(service ChatHistoryService, bodyLimit int64) Endpoint {
	return func(_ application.Boundary, writer http.ResponseWriter, request *http.Request) {
		projectID, conversationID, suffix, failure := conversationPath(request.URL.Path)
		if failure != nil || conversationID <= 0 || suffix != "" {
			if failure == nil {
				value := NewAPIError(CodeNotFound, nil)
				failure = &value
			}
			WriteError(writer, request, *failure)
			return
		}
		switch request.Method {
		case http.MethodGet, http.MethodHead:
			result, err := service.GetConversation(request.Context(), projectID, conversationID)
			if err != nil {
				writeChatHistoryError(writer, request, err)
				return
			}
			WriteResponse(writer, request, http.StatusOK, responseEnvelope{Data: result, RequestID: RequestID(request.Context())})
		case http.MethodPatch:
			var input conversationRequest
			if failure := DecodeJSON(writer, request, &input, bodyLimit); failure != nil {
				WriteError(writer, request, *failure)
				return
			}
			result, err := service.UpdateConversation(request.Context(), applicationchat.UpdateConversationCommand{ProjectID: projectID, ConversationID: conversationID, Input: domainchat.ConversationInput{Title: input.Title, AIConfigID: input.AIConfigID, Pinned: input.Pinned}})
			if err != nil {
				writeChatHistoryError(writer, request, err)
				return
			}
			WriteResponse(writer, request, http.StatusOK, responseEnvelope{Data: result, RequestID: RequestID(request.Context())})
		case http.MethodDelete:
			deleted, err := service.DeleteConversation(request.Context(), projectID, conversationID)
			if err != nil {
				writeChatHistoryError(writer, request, err)
				return
			}
			WriteResponse(writer, request, http.StatusOK, responseEnvelope{Data: map[string]int64{"deleted_messages": deleted}, RequestID: RequestID(request.Context())})
		}
	}
}

func messagesEndpoint(service ChatHistoryService) Endpoint {
	return func(_ application.Boundary, writer http.ResponseWriter, request *http.Request) {
		projectID, conversationID, suffix, failure := conversationPath(request.URL.Path)
		if failure != nil || conversationID <= 0 || suffix != "messages" {
			if failure == nil {
				value := NewAPIError(CodeNotFound, nil)
				failure = &value
			}
			WriteError(writer, request, *failure)
			return
		}
		limit, cursor, failure := chatPagination(request.URL)
		if failure != nil {
			WriteError(writer, request, *failure)
			return
		}
		result, err := service.ListHistory(request.Context(), domainchat.MessageListOptions{ProjectID: projectID, ConversationID: conversationID, Limit: limit, Cursor: cursor})
		if err != nil {
			writeChatHistoryError(writer, request, err)
			return
		}
		WriteResponse(writer, request, http.StatusOK, messagePageEnvelope{Data: result.Items, NextCursor: result.NextCursor, RequestID: RequestID(request.Context())})
	}
}

func conversationPath(path string) (int64, int64, string, *APIError) {
	segments := strings.Split(strings.TrimPrefix(path, "/api/v1/projects/"), "/")
	if len(segments) < 2 || len(segments) > 4 || segments[1] != "conversations" {
		failure := NewAPIError(CodeNotFound, nil)
		return 0, 0, "", &failure
	}
	projectID, valid := parseCanonicalPositiveID(segments[0])
	if !valid {
		failure := NewAPIError(CodeInvalidProjectID, &ErrorDetails{Field: "project_id"})
		return 0, 0, "", &failure
	}
	if len(segments) == 2 {
		return projectID, 0, "", nil
	}
	conversationID, valid := parseCanonicalPositiveID(segments[2])
	if !valid {
		failure := NewAPIError(CodeInvalidConversation, &ErrorDetails{Field: "conversation_id"})
		return 0, 0, "", &failure
	}
	if len(segments) == 3 {
		return projectID, conversationID, "", nil
	}
	if segments[3] != "messages" {
		failure := NewAPIError(CodeNotFound, nil)
		return 0, 0, "", &failure
	}
	return projectID, conversationID, "messages", nil
}

func chatPagination(location *url.URL) (int, string, *APIError) {
	if location == nil {
		failure := NewAPIError(CodeInvalidPagination, nil)
		return 0, "", &failure
	}
	limit, cursor := 50, ""
	for name, entries := range location.Query() {
		if (name != "limit" && name != "cursor") || len(entries) != 1 {
			failure := NewAPIError(CodeInvalidPagination, &ErrorDetails{Field: name})
			return 0, "", &failure
		}
	}
	if value, exists := location.Query()["limit"]; exists {
		parsed, valid := parsePositiveInt(value[0], 200)
		if !valid {
			failure := NewAPIError(CodeInvalidPagination, &ErrorDetails{Field: "limit"})
			return 0, "", &failure
		}
		limit = parsed
	}
	if value, exists := location.Query()["cursor"]; exists {
		if value[0] == "" || len(value[0]) > 512 {
			failure := NewAPIError(CodeInvalidCursor, &ErrorDetails{Field: "cursor"})
			return 0, "", &failure
		}
		cursor = value[0]
	}
	return limit, cursor, nil
}

func writeChatHistoryError(writer http.ResponseWriter, request *http.Request, err error) {
	code := CodeInternal
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		code = CodeRequestTimeout
	case errors.Is(err, applicationchat.ErrInvalidCommand), errors.Is(err, domainchat.ErrInvalidConversation), errors.Is(err, domainchat.ErrInvalidMessage):
		code = CodeInvalidConversation
	case errors.Is(err, domainchat.ErrInvalidCursor):
		code = CodeInvalidCursor
	case errors.Is(err, applicationchat.ErrStateConflict), errors.Is(err, repository.ErrVersionConflict):
		code = CodePreconditionFailed
	case errors.Is(err, repository.ErrNotFound), errors.Is(err, repository.ErrProjectMismatch):
		code = CodeConversationNotFound
	case errors.Is(err, repository.ErrTransaction), errors.Is(err, repository.ErrCommit), errors.Is(err, repository.ErrRollback):
		code = CodeRepositoryBusy
	case errors.Is(err, repository.ErrSchemaDrift):
		code = CodeRepositorySchemaDrift
	case errors.Is(err, applicationchat.ErrUnavailable), errors.Is(err, repository.ErrNotConfigured), errors.Is(err, repository.ErrClosed), errors.Is(err, repository.ErrWriterUnauthorized):
		code = CodeRepositoryUnavailable
	}
	WriteError(writer, request, NewAPIError(code, nil))
}
