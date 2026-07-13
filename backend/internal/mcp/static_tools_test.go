package mcp

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestStaticToolsAdvertisePersistenceOnlyCapabilities(t *testing.T) {
	tools := NewStaticTools(StaticDependencies{})
	names := strings.Join(tools.Names(), ",")
	for _, forbidden := range []string{".run", ".stop", ".send", ".stream", ".queue", ".start"} {
		if strings.Contains(names, forbidden) {
			t.Fatalf("runtime capability leaked into static tool names: %s", forbidden)
		}
	}
	if !strings.Contains(names, "automation.list_scripts") || !strings.Contains(names, "chat.list_messages") || !strings.Contains(names, "config.get_mcp") {
		t.Fatal("static tool inventory drifted")
	}
}

func TestStaticToolsFailClosedWithoutAnApplication(t *testing.T) {
	tools := NewStaticTools(StaticDependencies{})
	_, err := tools.ListScripts(context.Background(), StaticListRequest{ProjectID: 1})
	var failure ToolError
	if !errors.As(err, &failure) || failure.Code != "service_unavailable" {
		t.Fatalf("missing automation application error=%v", err)
	}
	_, err = tools.ListConversations(context.Background(), StaticConversationRequest{ProjectID: 1})
	if !errors.As(err, &failure) || failure.Code != "service_unavailable" {
		t.Fatalf("missing chat application error=%v", err)
	}
}
