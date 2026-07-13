package httpapi

import (
	"encoding/json"
	"strings"
	"testing"

	domainevents "github.com/lyming99/autoplan/backend/internal/domain/events"
)

func TestP13ChatSSEProjectsOrderedChunkThenSingleTerminalEvent(t *testing.T) {
	chunk := p13FixtureChatEnvelope(t, "41", 41, "business.chat_partial", map[string]any{
		"conversation_id": int64(9), "message_id": int64(11), "turn_id": "chat-turn-11", "sequence": int64(1), "delta_bytes": int64(3), "status": "streaming",
	})
	done := p13FixtureChatEnvelope(t, "42", 42, "business.chat_queue", map[string]any{
		"conversation_id": int64(9), "message_id": int64(11), "turn_id": "chat-turn-11", "status": "done", "queue_count": int64(0),
	})
	first, included := p13ChatEvent(chunk, nil, nil, 7, 9)
	if !included || first.Type != "chat_chunk" {
		t.Fatalf("chunk projection=%#v included=%t", first, included)
	}
	second, included := p13ChatEvent(done, nil, nil, 7, 9)
	if !included || second.Type != "chat_done" {
		t.Fatalf("done projection=%#v included=%t", second, included)
	}
	writer := newSSEWriter()
	if err := writeP13ChatSSEEnvelope(writer, first); err != nil {
		t.Fatal(err)
	}
	if err := writeP13ChatSSEEnvelope(writer, second); err != nil {
		t.Fatal(err)
	}
	body := writer.Body.String()
	if strings.Index(body, "event: chat_chunk") >= strings.Index(body, "event: chat_done") || strings.Count(body, "event: chat_done") != 1 || strings.Contains(body, "fixture-secret") {
		t.Fatalf("Chat stream ordering or redaction drifted: %q", body)
	}
}

func TestP13ChatSSEExcludesOtherConversationBeforeSerialization(t *testing.T) {
	envelope := p13FixtureChatEnvelope(t, "43", 43, "business.chat_partial", map[string]any{
		"conversation_id": int64(10), "message_id": int64(12), "delta_bytes": int64(1), "status": "streaming",
	})
	if _, included := p13ChatEvent(envelope, nil, nil, 7, 9); included {
		t.Fatal("cross-conversation Chat event entered the stream")
	}
}

func p13FixtureChatEnvelope(t *testing.T, eventID string, revision int64, eventType string, payload map[string]any) domainevents.Envelope {
	t.Helper()
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	requestID := "request-fixture"
	return domainevents.Envelope{
		SchemaVersion: domainevents.SchemaVersion, Class: domainevents.ClassBusiness, EventID: &eventID,
		ProjectID: 7, ProjectRevision: &revision, RequestID: &requestID, OccurredAt: "2026-07-12T00:00:00Z", Type: eventType, Payload: encoded,
	}
}
