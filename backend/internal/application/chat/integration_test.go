package chat

import (
	"encoding/json"
	"strings"
	"testing"

	domainchat "github.com/lyming99/autoplan/backend/internal/domain/chat"
)

// P13A admission values are persisted before a provider is ever scheduled.
// These checks keep the FIFO/rollback boundary deterministic without invoking
// a provider, process, database, or external service.
func TestP13AdmissionAndQueueEventsStayConversationScoped(t *testing.T) {
	command := SendCommand{
		ProjectID: 7, Message: "  fixture message  ", RequestID: "request-fixture",
		CallerScope: "renderer", IdempotencyKey: "chat-fixture-1",
	}
	first, err := prepareAdmission(command, 11, "2026-07-12T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	second, err := prepareAdmission(command, 11, "2026-07-12T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	other, err := prepareAdmission(command, 12, "2026-07-12T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	if first.Content != "fixture message" || first.AdmissionID == "" || first.AdmissionID != second.AdmissionID ||
		first.RequestHash != second.RequestHash || first.RequestHash == other.RequestHash {
		t.Fatalf("admission scope/idempotency drifted: first=%#v second=%#v other=%#v", first, second, other)
	}

	queued := queueDTO(7, 11, []domainchat.QueueItem{{ID: 21, Content: "first", State: domainchat.StatusQueued}, {ID: 22, Content: "second", State: "processing"}})
	if queued.ProjectID != 7 || queued.ProjectId != 7 || queued.ConversationID != 11 || queued.ConversationId != 11 ||
		queued.Count != 2 || queued.Items[0].ID != 21 || queued.Items[1].ID != 22 {
		t.Fatalf("queue snapshot lost FIFO or compatibility aliases: %#v", queued)
	}

	event := queueEvent(7, 11, 21, turnID(21), domainchat.StatusDone, queued.Count, "request-fixture", "2026-07-12T00:00:01Z", "chat-finish")
	var payload map[string]any
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if event.Type != "business.chat_queue" || payload["conversation_id"] != float64(11) || payload["turn_id"] != "chat-turn-21" ||
		payload["status"] != domainchat.StatusDone || !terminalChatStatus(domainchat.StatusDone) || terminalChatStatus(domainchat.StatusRunning) {
		t.Fatalf("terminal queue event drifted: event=%#v payload=%#v", event, payload)
	}
}

func TestP13RecoveryAndPartialEventsDoNotContainProviderInput(t *testing.T) {
	event := partialEvent(7, 11, 21, 4, "request-fixture", "2026-07-12T00:00:00Z", "chat-partial")
	if event.Type != "business.chat_partial" {
		// The assertion below is kept separately to make a malformed event
		// failure explainable without exposing provider output.
		t.Fatalf("partial event contract invalid: %#v", event)
	}
	if err := domainchat.ValidateQueueEvent(event); err != nil {
		t.Fatalf("partial event contract invalid: %v", err)
	}
	if strings.Contains(string(event.Payload), "fixture message") {
		t.Fatalf("partial event exposed provider input: %s", event.Payload)
	}
	for _, status := range []string{domainchat.StatusDone, domainchat.StatusAborted, domainchat.StatusError, domainchat.StatusMaxRounds, domainchat.StatusInterrupted} {
		if !terminalChatStatus(status) {
			t.Fatalf("terminal status %q was not retained", status)
		}
	}
}
