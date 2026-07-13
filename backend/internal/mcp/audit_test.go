package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"
)

func TestAuditRecordsOnlyBoundedTransportFacts(t *testing.T) {
	audit := &auditCapture{}
	registry, err := NewRegistry([]ToolDescriptor{{Name: "list_projects", Title: "List projects", Description: "Shared fixture.", InputSchema: json.RawMessage(`{"type":"object"}`)}}, AdapterFactoryFunc(func(ToolDescriptor) ToolHandler {
		return ToolHandlerFunc(func(context.Context, ToolCall) (ToolResult, error) {
			return ToolResult{Content: []ToolTextContent{{Type: "text", Text: "ok"}}, StructuredContent: map[string]any{"ok": true}}, nil
		})
	}))
	if err != nil {
		t.Fatal(err)
	}
	configuration := DefaultConfig()
	configuration.Enabled, configuration.Transport = true, TransportStdio
	server, err := NewServer(ServerOptions{Config: configuration, Registry: registry, Audit: audit, SessionToken: bytes.Repeat([]byte{'a'}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close(context.Background()) })
	_, _ = server.processFrame(context.Background(), []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list_projects","arguments":{"token":"not-recorded"}}}`), TransportStdio, ToolContext{})
	audit.mu.Lock()
	defer audit.mu.Unlock()
	if len(audit.events) != 1 || audit.events[0].Tool != "list_projects" || audit.events[0].Outcome != "ok" || audit.events[0].OccurredAt.IsZero() || audit.events[0].OccurredAt.After(time.Now().UTC()) {
		t.Fatalf("audit = %#v", audit.events)
	}
	encoded, _ := json.Marshal(audit.events[0])
	if bytes.Contains(encoded, []byte("not-recorded")) || bytes.Contains(encoded, []byte("request")) || bytes.Contains(encoded, []byte("mcp-stdio")) {
		t.Fatal("audit retained request-specific data")
	}
}

type auditCapture struct {
	mu     sync.Mutex
	events []AuditEvent
}

func (capture *auditCapture) Record(_ context.Context, event AuditEvent) {
	capture.mu.Lock()
	defer capture.mu.Unlock()
	capture.events = append(capture.events, event)
}
