package sqlite

import (
	"testing"

	domainchat "github.com/lyming99/autoplan/backend/internal/domain/chat"
)

func TestChatQueueIdentifiersRemainScoped(t *testing.T) {
	input := domainchat.Admission{
		ProjectID: 3, ConversationID: 5, Content: "fixture", RequestID: "request-3",
		CallerScope: "renderer", IdempotencyKey: "key-3",
		RequestHash: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		AdmissionID: "chat-admit-fixture", OccurredAt: "2026-07-12T00:00:00.000Z",
	}
	if chatAdmissionScope(input) == chatAdmissionScope(domainchat.Admission{
		ProjectID: 3, ConversationID: 6, CallerScope: "renderer",
	}) || chatTurnID(0) != "chat-turn-0" || chatTurnID(7) != "chat-turn-7" {
		t.Fatal("chat queue identifiers are not deterministic and conversation scoped")
	}
}
