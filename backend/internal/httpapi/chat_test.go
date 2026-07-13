package httpapi

import (
	"encoding/json"
	"testing"
)

func TestP13ChatTargetAcceptsFrozenPaths(t *testing.T) {
	target, failure := p13ChatTarget("/api/v1/projects/7/conversations/9/queue/11")
	if failure != nil {
		t.Fatalf("p13ChatTarget returned failure: %s", failure.Code())
	}
	if target.ProjectID != 7 || target.ConversationID != 9 || target.MessageID != 11 || target.Action != "queue_item" {
		t.Fatalf("unexpected target: %#v", target)
	}
}

func TestP13ChatTargetRejectsNonCanonicalConversation(t *testing.T) {
	_, failure := p13ChatTarget("/api/v1/projects/7/conversations/09/messages")
	if failure == nil || failure.Code() != CodeInvalidConversation {
		t.Fatalf("unexpected failure: %#v", failure)
	}
}

func TestValidP13IdempotencyKey(t *testing.T) {
	if !validP13IdempotencyKey("chat.send-1") || validP13IdempotencyKey("contains space") {
		t.Fatal("idempotency validation drifted from the transport contract")
	}
}

func TestP13StopPatternUsesConversationIdentifier(t *testing.T) {
	if !validResourceRoutePattern(ProjectConversationStopPath) ||
		!resourceRoutePatternMatches(ProjectConversationStopPath, "/api/v1/projects/7/conversations/9:stop") {
		t.Fatal("the frozen stop route is not a valid bounded router pattern")
	}
}

func TestP13ConversationRequestNullConfigClearsBinding(t *testing.T) {
	var request p13ConversationRequest
	if err := json.Unmarshal([]byte(`{"ai_config_id":null}`), &request); err != nil {
		t.Fatal(err)
	}
	input, failure := request.toConversationInput(true)
	if failure != nil || input.AIConfigID == nil || *input.AIConfigID != 0 {
		t.Fatalf("null config did not become an explicit clear: %#v, %#v", input, failure)
	}
}
