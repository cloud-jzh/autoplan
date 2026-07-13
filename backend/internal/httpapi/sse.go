package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/lyming99/autoplan/backend/internal/application"
	domainevents "github.com/lyming99/autoplan/backend/internal/domain/events"
	"github.com/lyming99/autoplan/backend/internal/runtime/eventbus"
)

const (
	SSESkeletonPath      = "/api/v1/skeleton/sse"
	sseRetryMilliseconds = 3000
	sseHeartbeatInterval = 15 * time.Second
)

func RegisterSSESkeleton(router *Router, security *Security) error {
	if router == nil || security == nil {
		return ErrSecurityConfiguration
	}
	endpoint := security.Protect(TransportSSE, func(
		app application.Boundary,
		writer http.ResponseWriter,
		request *http.Request,
	) {
		if !acceptsEventStream(request.Header.Values("Accept")) {
			WriteError(writer, request, NewAPIError(CodeNotImplemented, nil))
			return
		}
		_ = app.Capabilities(request.Context())
		writer.Header().Set(TransportVersionHeader, TransportVersion)
		WriteError(writer, request, NewAPIError(CodeNotImplemented, nil))
	})
	return router.Handle(http.MethodGet, SSESkeletonPath, endpoint)
}

func acceptsEventStream(values []string) bool {
	for _, value := range values {
		for _, mediaRange := range strings.Split(value, ",") {
			mediaType := strings.TrimSpace(strings.SplitN(mediaRange, ";", 2)[0])
			if strings.EqualFold(mediaType, "text/event-stream") {
				return true
			}
		}
	}
	return false
}

// serveEventStream owns one authenticated subscription until the client,
// server, or backpressure policy closes it. Persistent entries retain their
// event_id; control entries deliberately omit it so they never alter a
// reconnect cursor.
func serveEventStream(writer http.ResponseWriter, request *http.Request, subscription *eventbus.Subscription) {
	if subscription == nil {
		WriteError(writer, request, NewAPIError(CodeServiceUnavailable, nil))
		return
	}
	defer subscription.Close()
	writer.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	writer.Header().Set("X-Accel-Buffering", "no")
	writer.Header().Set(TransportVersionHeader, TransportVersion)
	if _, err := fmt.Fprintf(writer, "retry: %d\n\n", sseRetryMilliseconds); err != nil {
		return
	}
	flushSSE(writer)

	context := request.Context()
	ticker := time.NewTicker(sseHeartbeatInterval)
	defer ticker.Stop()
	next := make(chan sseNextResult, 1)
	requestNext(context, subscription, next)
	for {
		select {
		case result := <-next:
			if result.err != nil {
				return
			}
			if writeSSEEnvelope(writer, result.delivery.Envelope) != nil {
				return
			}
			flushSSE(writer)
			if result.delivery.Envelope.Type == domainevents.TypeResyncRequired {
				return
			}
			requestNext(context, subscription, next)
		case <-ticker.C:
			if writeSSEEnvelope(writer, heartbeatEnvelope(subscription.ProjectID())) != nil {
				return
			}
			flushSSE(writer)
		case <-context.Done():
			return
		}
	}
}

type sseNextResult struct {
	delivery eventbus.Delivery
	err      error
}

func requestNext(ctx context.Context, subscription *eventbus.Subscription, result chan<- sseNextResult) {
	go func() {
		delivery, err := subscription.Next(ctx)
		result <- sseNextResult{delivery: delivery, err: err}
	}()
}

func heartbeatEnvelope(projectID int64) domainevents.Envelope {
	return domainevents.Envelope{
		SchemaVersion: domainevents.SchemaVersion, Class: domainevents.ClassControl, ProjectID: projectID,
		Type: domainevents.TypeHeartbeat, OccurredAt: time.Now().UTC().Format(time.RFC3339Nano), Payload: json.RawMessage(`{}`),
	}
}

func writeSSEEnvelope(writer http.ResponseWriter, envelope domainevents.Envelope) error {
	if envelope.Validate() != nil {
		return errors.New("event envelope is invalid")
	}
	payload, err := json.Marshal(envelope)
	if err != nil {
		return err
	}
	if envelope.EventID != nil {
		if _, err := fmt.Fprintf(writer, "id: %s\n", *envelope.EventID); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(writer, "event: %s\n", envelope.Type); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(writer, "data: %s\n\n", payload); err != nil {
		return err
	}
	return nil
}

func flushSSE(writer http.ResponseWriter) {
	if flusher, ok := writer.(http.Flusher); ok {
		flusher.Flush()
	}
}

type p13ChatEventEnvelope struct {
	SchemaVersion   int    `json:"schema_version"`
	EventID         string `json:"event_id"`
	ProjectID       int64  `json:"project_id"`
	ProjectRevision int64  `json:"project_revision"`
	RequestID       string `json:"request_id"`
	OccurredAt      string `json:"occurred_at"`
	Type            string `json:"type"`
	Data            any    `json:"data"`
}

type p13ChatOutboxPayload struct {
	ConversationID int64  `json:"conversation_id"`
	MessageID      int64  `json:"message_id"`
	TurnID         string `json:"turn_id"`
	Status         string `json:"status"`
	Sequence       int64  `json:"sequence"`
	DeltaBytes     int64  `json:"delta_bytes"`
}

type p13ChatDoneEvent struct {
	ProjectID      int64  `json:"project_id"`
	ProjectId      int64  `json:"projectId"`
	ConversationID int64  `json:"conversation_id"`
	ConversationId int64  `json:"conversationId"`
	TurnID         string `json:"turn_id"`
	Status         string `json:"status"`
}

type p13ChatChunkEvent struct {
	ProjectID      int64          `json:"project_id"`
	ProjectId      int64          `json:"projectId"`
	ConversationID int64          `json:"conversation_id"`
	ConversationId int64          `json:"conversationId"`
	TurnID         string         `json:"turn_id"`
	Sequence       int64          `json:"sequence"`
	Type           string         `json:"type"`
	Data           map[string]any `json:"data"`
}

// serveChatEventStream transforms the generic P10 envelope into the frozen
// Chat stream. The durable cursor remains the original event_id; filtering is
// performed before serialization, never by exposing the P10 payload directly.
func serveChatEventStream(
	writer http.ResponseWriter,
	request *http.Request,
	subscription *eventbus.Subscription,
	service ChatHTTPService,
	projectID, conversationID int64,
) {
	if subscription == nil || service == nil || subscription.ProjectID() != projectID {
		WriteError(writer, request, NewAPIError(CodeChatRuntimeUnavailable, nil))
		return
	}
	defer subscription.Close()
	writer.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	writer.Header().Set("X-Accel-Buffering", "no")
	writer.Header().Set(TransportVersionHeader, TransportVersion)
	if _, err := fmt.Fprintf(writer, "retry: %d\n\n", sseRetryMilliseconds); err != nil {
		return
	}
	flushSSE(writer)

	ctx := request.Context()
	ticker := time.NewTicker(sseHeartbeatInterval)
	defer ticker.Stop()
	next := make(chan sseNextResult, 1)
	requestNext(ctx, subscription, next)
	for {
		select {
		case result := <-next:
			if result.err != nil {
				return
			}
			if result.delivery.Envelope.Type == domainevents.TypeResyncRequired {
				if writeSSEEnvelope(writer, result.delivery.Envelope) == nil {
					flushSSE(writer)
				}
				return
			}
			event, include := p13ChatEvent(result.delivery.Envelope, service, ctx, projectID, conversationID)
			if include {
				if writeP13ChatSSEEnvelope(writer, event) != nil {
					return
				}
				flushSSE(writer)
			}
			requestNext(ctx, subscription, next)
		case <-ticker.C:
			if writeSSEEnvelope(writer, heartbeatEnvelope(projectID)) != nil {
				return
			}
			flushSSE(writer)
		case <-ctx.Done():
			return
		}
	}
}

func p13ChatEvent(
	envelope domainevents.Envelope,
	service ChatHTTPService,
	ctx context.Context,
	projectID, conversationID int64,
) (p13ChatEventEnvelope, bool) {
	if envelope.Validate() != nil || envelope.Class != domainevents.ClassBusiness || envelope.ProjectID != projectID ||
		envelope.EventID == nil || envelope.ProjectRevision == nil || envelope.RequestID == nil {
		return p13ChatEventEnvelope{}, false
	}
	var payload p13ChatOutboxPayload
	if json.Unmarshal(envelope.Payload, &payload) != nil || payload.ConversationID != conversationID {
		return p13ChatEventEnvelope{}, false
	}
	result := p13ChatEventEnvelope{
		SchemaVersion: 1, EventID: *envelope.EventID, ProjectID: projectID,
		ProjectRevision: *envelope.ProjectRevision, RequestID: *envelope.RequestID,
		OccurredAt: envelope.OccurredAt,
	}
	switch envelope.Type {
	case "business.chat_queue":
		if p13TerminalChatStatus(payload.Status) {
			if payload.TurnID == "" {
				return p13ChatEventEnvelope{}, false
			}
			result.Type = "chat_done"
			result.Data = p13ChatDoneEvent{
				ProjectID: projectID, ProjectId: projectID,
				ConversationID: conversationID, ConversationId: conversationID,
				TurnID: payload.TurnID, Status: payload.Status,
			}
			return result, true
		}
		queue, err := service.GetQueue(ctx, projectID, conversationID)
		if err != nil {
			return p13ChatEventEnvelope{}, false
		}
		result.Type, result.Data = "chat_queue", queue
		return result, true
	case "business.chat_chunk", "business.chat_partial":
		turnID := payload.TurnID
		if turnID == "" && payload.MessageID > 0 {
			turnID = "chat-turn-" + strconv.FormatInt(payload.MessageID, 10)
		}
		if turnID == "" {
			return p13ChatEventEnvelope{}, false
		}
		sequence := payload.Sequence
		if sequence <= 0 {
			sequence, _ = strconv.ParseInt(*envelope.EventID, 10, 64)
		}
		if sequence <= 0 {
			return p13ChatEventEnvelope{}, false
		}
		result.Type = "chat_chunk"
		result.Data = p13ChatChunkEvent{
			ProjectID: projectID, ProjectId: projectID,
			ConversationID: conversationID, ConversationId: conversationID,
			TurnID: turnID, Sequence: sequence, Type: "status", Data: map[string]any{"delta_bytes": payload.DeltaBytes},
		}
		return result, true
	default:
		return p13ChatEventEnvelope{}, false
	}
}

func p13TerminalChatStatus(status string) bool {
	switch status {
	case "done", "aborted", "error", "max_rounds", "interrupted":
		return true
	default:
		return false
	}
}

func writeP13ChatSSEEnvelope(writer http.ResponseWriter, envelope p13ChatEventEnvelope) error {
	if envelope.SchemaVersion != 1 || envelope.EventID == "" || envelope.ProjectID <= 0 ||
		envelope.ProjectRevision <= 0 || envelope.RequestID == "" || envelope.OccurredAt == "" ||
		(envelope.Type != "chat_chunk" && envelope.Type != "chat_queue" && envelope.Type != "chat_done") || envelope.Data == nil {
		return errors.New("chat event envelope is invalid")
	}
	payload, err := json.Marshal(envelope)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(writer, "id: %s\n", envelope.EventID); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(writer, "event: %s\n", envelope.Type); err != nil {
		return err
	}
	_, err = fmt.Fprintf(writer, "data: %s\n\n", payload)
	return err
}
