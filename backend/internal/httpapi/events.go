package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/lyming99/autoplan/backend/internal/application"
	applicationoperations "github.com/lyming99/autoplan/backend/internal/application/operations"
	domainproject "github.com/lyming99/autoplan/backend/internal/domain/project"
	"github.com/lyming99/autoplan/backend/internal/runtime/eventbus"
)

const (
	ProjectEventsPath   = "/api/v1/projects/{project_id}/events"
	OperationEventsPath = "/api/v1/operations/{operation_id}/events"
)

// RegisterEvents exposes only the P10 eventbus subscription surface. Project
// and Operation checks happen before Subscribe, so a cursor or opaque
// operation_id cannot enumerate another project stream.
func RegisterEvents(
	router *Router,
	security *Security,
	projects ProjectService,
	operations OperationService,
	bus *eventbus.Bus,
) error {
	if router == nil || security == nil || projects == nil || operations == nil || bus == nil {
		return ErrRouterDependency
	}
	projectEvents := security.Protect(TransportSSE, projectEventsEndpoint(projects, bus))
	operationEvents := security.Protect(TransportSSE, operationEventsEndpoint(projects, operations, bus))
	for _, route := range []struct {
		path     string
		endpoint Endpoint
	}{
		{ProjectEventsPath, projectEvents},
		{OperationEventsPath, operationEvents},
	} {
		if err := router.HandlePattern(http.MethodGet, route.path, route.endpoint); err != nil {
			return err
		}
	}
	return nil
}

func projectEventsEndpoint(projects ProjectService, bus *eventbus.Bus) Endpoint {
	return func(_ application.Boundary, writer http.ResponseWriter, request *http.Request) {
		if !acceptsEventStream(request.Header.Values("Accept")) {
			WriteError(writer, request, NewAPIError(CodeUnsupportedMediaType, nil))
			return
		}
		projectID, failure := projectEventIDFromPath(request.URL.Path)
		if failure != nil {
			WriteError(writer, request, *failure)
			return
		}
		if !emptyQuery(request.URL.RawQuery) {
			WriteError(writer, request, NewAPIError(CodeInvalidProjectID, &ErrorDetails{Field: "query"}))
			return
		}
		if _, err := projects.Get(request.Context(), projectID, domainproject.Visibility{}); err != nil {
			writeProjectServiceError(writer, request, err)
			return
		}
		subscription, err := bus.Subscribe(request.Context(), eventbus.SubscribeRequest{
			ProjectID: projectID, LastEventID: eventCursor(request),
		})
		if err != nil {
			writeEventSubscriptionError(writer, request, err)
			return
		}
		serveEventStream(writer, request, subscription)
	}
}

func operationEventsEndpoint(projects ProjectService, operations OperationService, bus *eventbus.Bus) Endpoint {
	return func(_ application.Boundary, writer http.ResponseWriter, request *http.Request) {
		if !acceptsEventStream(request.Header.Values("Accept")) {
			WriteError(writer, request, NewAPIError(CodeUnsupportedMediaType, nil))
			return
		}
		projectID, operationID, caller, failure := operationTarget(request, "/events")
		if failure != nil {
			WriteError(writer, request, *failure)
			return
		}
		if _, err := projects.Get(request.Context(), projectID, domainproject.Visibility{}); err != nil {
			writeProjectServiceError(writer, request, err)
			return
		}
		if _, err := operations.Get(request.Context(), applicationoperations.Query{
			Caller: caller, ProjectID: projectID, OperationID: operationID,
		}); err != nil {
			writeOperationServiceError(writer, request, err)
			return
		}
		subscription, err := bus.Subscribe(request.Context(), eventbus.SubscribeRequest{
			ProjectID: projectID, OperationID: operationID, LastEventID: eventCursor(request),
		})
		if err != nil {
			writeEventSubscriptionError(writer, request, err)
			return
		}
		serveEventStream(writer, request, subscription)
	}
}

func projectEventIDFromPath(path string) (int64, *APIError) {
	prefix := "/api/v1/projects/"
	const suffix = "/events"
	if !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, suffix) {
		failure := NewAPIError(CodeInvalidProjectID, &ErrorDetails{Field: "project_id"})
		return 0, &failure
	}
	value := strings.TrimSuffix(strings.TrimPrefix(path, prefix), suffix)
	if strings.Contains(value, "/") {
		failure := NewAPIError(CodeInvalidProjectID, &ErrorDetails{Field: "project_id"})
		return 0, &failure
	}
	return parseCanonicalProjectID(value)
}

func parseCanonicalProjectID(value string) (int64, *APIError) {
	if value == "" || strings.HasPrefix(value, "+") || (len(value) > 1 && value[0] == '0') {
		failure := NewAPIError(CodeInvalidProjectID, &ErrorDetails{Field: "project_id"})
		return 0, &failure
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			failure := NewAPIError(CodeInvalidProjectID, &ErrorDetails{Field: "project_id"})
			return 0, &failure
		}
	}
	projectID, err := strconv.ParseInt(value, 10, 64)
	if err != nil || projectID <= 0 || strconv.FormatInt(projectID, 10) != value {
		failure := NewAPIError(CodeInvalidProjectID, &ErrorDetails{Field: "project_id"})
		return 0, &failure
	}
	return projectID, nil
}

func emptyQuery(raw string) bool { return raw == "" }

// eventCursor preserves an absent cursor but converts repeated header fields
// into the eventbus's explicit invalid-cursor/resync path. Choosing resync is
// safer than accepting an ambiguous reconnect boundary.
func eventCursor(request *http.Request) string {
	values := request.Header.Values("Last-Event-ID")
	if len(values) == 0 {
		return ""
	}
	if len(values) != 1 {
		return "invalid"
	}
	return values[0]
}

func writeEventSubscriptionError(writer http.ResponseWriter, request *http.Request, err error) {
	code := CodeInternal
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		code = CodeRequestTimeout
	case errors.Is(err, eventbus.ErrUnavailable), errors.Is(err, eventbus.ErrBusClosed):
		code = CodeServiceUnavailable
	case errors.Is(err, eventbus.ErrInvalidSubscription), errors.Is(err, eventbus.ErrInvalidEvent):
		code = CodeInvalidOperation
	}
	WriteError(writer, request, NewAPIError(code, nil))
}

// p13ChatEventsEndpoint narrows the project-level durable outbox to exactly
// one authorized conversation. It validates the conversation before opening
// the subscription so a cursor cannot be used to enumerate another scope.
func p13ChatEventsEndpoint(service ChatHTTPService, bus *eventbus.Bus) Endpoint {
	return func(_ application.Boundary, writer http.ResponseWriter, request *http.Request) {
		if !acceptsEventStream(request.Header.Values("Accept")) {
			WriteError(writer, request, NewAPIError(CodeUnsupportedMediaType, nil))
			return
		}
		target, failure := p13ChatTarget(request.URL.Path)
		if failure != nil || target.Action != "events" {
			p13WriteRouteError(writer, request, failure)
			return
		}
		if !emptyQuery(request.URL.RawQuery) {
			WriteError(writer, request, NewAPIError(CodeInvalidCursor, &ErrorDetails{Field: "query"}))
			return
		}
		if _, err := service.GetConversation(request.Context(), target.ProjectID, target.ConversationID); err != nil {
			writeP13ChatError(writer, request, err)
			return
		}
		subscription, err := bus.Subscribe(request.Context(), eventbus.SubscribeRequest{
			ProjectID: target.ProjectID, LastEventID: eventCursor(request),
		})
		if err != nil {
			writeEventSubscriptionError(writer, request, err)
			return
		}
		serveChatEventStream(writer, request, subscription, service, target.ProjectID, target.ConversationID)
	}
}
