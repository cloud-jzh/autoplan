package httpapi

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestP13ChatSSEEnvelopeUsesFrozenFieldsAndNeverSerializesSensitiveKeys(t *testing.T) {
	writer := newSSEWriter()
	envelope := p13ChatEventEnvelope{
		SchemaVersion: 1, EventID: "42", ProjectID: 7, ProjectRevision: 42,
		RequestID: "request-fixture", OccurredAt: "2026-07-12T00:00:00Z", Type: "chat_chunk",
		Data: p13ChatChunkEvent{ProjectID: 7, ProjectId: 7, ConversationID: 9, ConversationId: 9, TurnID: "chat-turn-11", Sequence: 1, Type: "status", Data: map[string]any{"delta_bytes": int64(3)}},
	}
	if err := writeP13ChatSSEEnvelope(writer, envelope); err != nil {
		t.Fatal(err)
	}
	body := writer.Body.String()
	for _, forbidden := range []string{"token", "secret", "authorization", "workspace_path", "fixture message"} {
		if strings.Contains(strings.ToLower(body), forbidden) {
			t.Fatalf("chat SSE leaked %q: %q", forbidden, body)
		}
	}
	if !strings.Contains(body, "id: 42\n") || !strings.Contains(body, "event: chat_chunk\n") {
		t.Fatalf("SSE envelope lost durable framing: %q", body)
	}

	var decoded map[string]any
	payload := bytes.TrimPrefix([]byte(strings.Split(body, "data: ")[1]), []byte(""))
	payload = bytes.TrimSpace(bytes.Split(payload, []byte("\n"))[0])
	if err := json.Unmarshal(payload, &decoded); err != nil || decoded["project_id"] != float64(7) || decoded["event_id"] != "42" {
		t.Fatalf("frozen Chat envelope invalid: decoded=%#v err=%v", decoded, err)
	}
}

func TestP13ChatContractRejectsUnsafeRouteAndIncompleteEnvelope(t *testing.T) {
	if _, failure := p13ChatTarget("/api/v1/projects/7/conversations/9/messages/extra"); failure == nil {
		t.Fatal("unbounded Chat path was accepted")
	}
	if err := writeP13ChatSSEEnvelope(newSSEWriter(), p13ChatEventEnvelope{SchemaVersion: 1, EventID: "1", ProjectID: 7, ProjectRevision: 1, RequestID: "request-fixture", OccurredAt: "2026-07-12T00:00:00Z", Type: "chat_done"}); err == nil {
		t.Fatal("incomplete Chat SSE envelope was accepted")
	}
}
