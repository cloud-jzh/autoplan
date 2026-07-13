package httpapi

import (
	"net/http"

	"github.com/lyming99/autoplan/backend/internal/runtime/eventbus"
)

// RegisterChat exposes the frozen P13A REST and SSE surface over the shared
// Chat application service. Registration is explicit so a bootstrap can keep
// legacy routes isolated while migrating one bounded HTTP surface at a time.
func RegisterChat(router *Router, security *Security, service ChatHTTPService, bus *eventbus.Bus) error {
	if router == nil || security == nil || service == nil || bus == nil {
		return ErrRouterDependency
	}
	bodyLimit := router.BodyLimitBytes()
	conversations := security.Protect(TransportREST, p13ConversationsEndpoint(service, bodyLimit))
	conversation := security.Protect(TransportREST, p13ConversationEndpoint(service, bodyLimit))
	messages := security.Protect(TransportREST, p13MessagesEndpoint(service, bodyLimit))
	queue := security.Protect(TransportREST, p13QueueEndpoint(service))
	queueItem := security.Protect(TransportREST, p13QueueItemEndpoint(service, bodyLimit))
	stop := security.Protect(TransportREST, p13StopEndpoint(service))
	events := security.Protect(TransportSSE, p13ChatEventsEndpoint(service, bus))

	for _, route := range []struct {
		method   string
		path     string
		endpoint Endpoint
	}{
		{http.MethodGet, ProjectConversationsPath, conversations},
		{http.MethodPost, ProjectConversationsPath, conversations},
		{http.MethodGet, ProjectConversationPath, conversation},
		{http.MethodPatch, ProjectConversationPath, conversation},
		{http.MethodDelete, ProjectConversationPath, conversation},
		{http.MethodGet, ProjectMessagesPath, messages},
		{http.MethodPost, ProjectMessagesPath, messages},
		{http.MethodDelete, ProjectMessagesPath, messages},
		{http.MethodGet, ProjectConversationQueuePath, queue},
		{http.MethodDelete, ProjectConversationQueuePath, queue},
		{http.MethodPatch, ProjectConversationQueueItemPath, queueItem},
		{http.MethodDelete, ProjectConversationQueueItemPath, queueItem},
		{http.MethodPost, ProjectConversationStopPath, stop},
		{http.MethodGet, ProjectConversationEventsPath, events},
	} {
		if err := router.HandlePattern(route.method, route.path, route.endpoint); err != nil {
			return err
		}
	}
	return nil
}
