package chat

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	domainchat "github.com/lyming99/autoplan/backend/internal/domain/chat"
)

func TestGoldenChatDTOsExposeOnlyMetadata(t *testing.T) {
	pinnedAt := "2026-07-11T00:00:02.000Z"
	status := "completed"
	conversation := conversationDTO(domainchat.Conversation{
		ID: 21, ProjectID: 7, Title: "fixture conversation", PinnedAt: &pinnedAt,
		CreatedAt: "2026-07-11T00:00:00.000Z", UpdatedAt: "2026-07-11T00:00:01.000Z",
	})
	message := messageDTO(domainchat.Message{
		ID: 31, ProjectID: 7, ConversationID: 21, Role: "assistant", Content: "private-message-content",
		ToolCalls: jsonPointer(`{"fixture":"private-tool-data"}`), ToolResult: jsonPointer(`{"fixture":"private-tool-result"}`), Status: &status,
		CreatedAt: "2026-07-11T00:00:03.000Z",
	})
	encoded, err := json.Marshal(struct {
		Conversation ConversationDTO `json:"conversation"`
		Message      MessageDTO      `json:"message"`
	}{conversation, message})
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	for _, forbidden := range []string{"private-message-content", "private-tool-data", "private-tool-result"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("chat golden leaked %q", forbidden)
		}
	}
	if !conversation.Pinned || !message.HasContent || !message.HasToolCalls || !message.HasToolResult {
		t.Fatal("chat presence metadata drifted")
	}
}

func TestGoldenChatRuntimeCapabilitiesRemainClosed(t *testing.T) {
	service := NewService(Dependencies{})
	for _, err := range []error{
		service.Send(context.Background(), 1, 1, "fixture"), service.Stop(context.Background(), 1, 1),
		service.Pump(context.Background(), 1, 1), service.GenerateTitle(context.Background(), 1, 1),
	} {
		if !errors.Is(err, ErrRuntimeDisabled) {
			t.Fatalf("runtime capability error=%v", err)
		}
	}
}

func jsonPointer(value string) *json.RawMessage {
	result := json.RawMessage(value)
	return &result
}
