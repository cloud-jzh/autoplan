package httpapi

import (
	"encoding/json"
	"testing"

	domainevents "github.com/lyming99/autoplan/backend/internal/domain/events"
)

func TestP13ChatEventFiltersOtherConversation(t *testing.T) {
	eventID, requestID := "12", "req_chat_1"
	revision := int64(12)
	payload, err := json.Marshal(map[string]any{"conversation_id": int64(8), "status": "queued"})
	if err != nil {
		t.Fatal(err)
	}
	_, included := p13ChatEvent(domainevents.Envelope{
		SchemaVersion: domainevents.SchemaVersion, Class: domainevents.ClassBusiness,
		EventID: &eventID, ProjectID: 7, ProjectRevision: &revision, RequestID: &requestID,
		OccurredAt: "2026-07-12T00:00:00Z", Type: "business.chat_queue", Payload: payload,
	}, nil, nil, 7, 9)
	if included {
		t.Fatal("an event from a different conversation entered the stream")
	}
}

func TestP13ChatEventProjectsTerminalTurn(t *testing.T) {
	eventID, requestID := "13", "req_chat_2"
	revision := int64(13)
	payload, err := json.Marshal(map[string]any{
		"conversation_id": int64(9), "message_id": int64(11), "turn_id": "chat-turn-11", "status": "done",
	})
	if err != nil {
		t.Fatal(err)
	}
	event, included := p13ChatEvent(domainevents.Envelope{
		SchemaVersion: domainevents.SchemaVersion, Class: domainevents.ClassBusiness,
		EventID: &eventID, ProjectID: 7, ProjectRevision: &revision, RequestID: &requestID,
		OccurredAt: "2026-07-12T00:00:00Z", Type: "business.chat_queue", Payload: payload,
	}, nil, nil, 7, 9)
	if !included || event.Type != "chat_done" {
		t.Fatalf("unexpected terminal projection: %#v, included=%t", event, included)
	}
}

func TestP13ChatEventProjectsPartialAsBoundedChunk(t *testing.T) {
	eventID, requestID := "14", "req_chat_3"
	revision := int64(14)
	payload, err := json.Marshal(map[string]any{
		"conversation_id": int64(9), "message_id": int64(12), "delta_bytes": int64(5), "status": "streaming",
	})
	if err != nil {
		t.Fatal(err)
	}
	event, included := p13ChatEvent(domainevents.Envelope{
		SchemaVersion: domainevents.SchemaVersion, Class: domainevents.ClassBusiness,
		EventID: &eventID, ProjectID: 7, ProjectRevision: &revision, RequestID: &requestID,
		OccurredAt: "2026-07-12T00:00:00Z", Type: "business.chat_partial", Payload: payload,
	}, nil, nil, 7, 9)
	if !included || event.Type != "chat_chunk" {
		t.Fatalf("unexpected partial projection: %#v, included=%t", event, included)
	}
}
