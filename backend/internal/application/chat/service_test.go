package chat

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	domainchat "github.com/lyming99/autoplan/backend/internal/domain/chat"
)

func TestPrepareAdmissionIsConversationScopedAndStable(t *testing.T) {
	command := SendCommand{
		ProjectID: 7, Message: "fixture", RequestID: "request-7",
		CallerScope: "renderer", IdempotencyKey: "key-7",
	}
	first, err := prepareAdmission(command, 11, "2026-07-12T00:00:00.000Z")
	if err != nil {
		t.Fatal(err)
	}
	second, err := prepareAdmission(command, 11, "2026-07-12T00:00:00.000Z")
	if err != nil {
		t.Fatal(err)
	}
	other, err := prepareAdmission(command, 12, "2026-07-12T00:00:00.000Z")
	if err != nil {
		t.Fatal(err)
	}
	if first.RequestHash == "" || first.AdmissionID == "" || first.RequestHash != second.RequestHash ||
		first.AdmissionID != second.AdmissionID || first.RequestHash == other.RequestHash {
		t.Fatal("chat admission fingerprint drifted")
	}
}

func TestQueueServiceFailsClosedWithoutQueueStore(t *testing.T) {
	service := NewService(Dependencies{})
	_, err := service.AdmitTurn(context.Background(), SendCommand{
		ProjectID: 1, ConversationID: 1, Message: "fixture", RequestID: "request-1",
	})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("admit error=%v", err)
	}
}

func TestChatMessageDTOUsesPublicStatusAndSuppressesUnsafeLegacyToolData(t *testing.T) {
	running := domainchat.StatusRunning
	safeCalls := json.RawMessage(`[{"name":"read"}]`)
	unsafeResult := json.RawMessage(`{"token":"redacted"}`)
	result := chatMessageDTO(domainchat.Message{
		ID: 1, ProjectID: 2, ConversationID: 3, Role: "user", Content: "fixture",
		ToolCalls: &safeCalls, ToolResult: &unsafeResult, Status: &running,
		CreatedAt: "2026-07-12T00:00:00.000Z",
	})
	if result.Status != domainchat.StatusQueued || result.ToolCallsRaw == nil || result.ToolCalls == nil ||
		result.ToolResultRaw != nil || result.ToolResult != nil {
		t.Fatal("history projection exposed an internal state or unsafe tool data")
	}
}
