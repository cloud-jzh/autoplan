package mcp

import (
	"context"
	"testing"

	"github.com/lyming99/autoplan/backend/internal/application/capabilities"
	applicationloop "github.com/lyming99/autoplan/backend/internal/application/loop"
)

type runtimeBridgeFixture struct{ command applicationloop.Command }

func (fixture *runtimeBridgeFixture) Execute(_ context.Context, command applicationloop.Command) (applicationloop.Result, error) {
	fixture.command = command
	return applicationloop.Result{Operation: capabilities.OperationReference{
		OperationID: "op-runtime-fixture", Type: string(command.Kind), Status: "accepted",
		RequestID: command.RequestID, AcceptedAt: "2026-07-12T00:00:00Z",
	}}, nil
}

func TestRuntimeToolsNamedLoopActionsUseSharedBridgeAndStableIdentity(t *testing.T) {
	fixture := &runtimeBridgeFixture{}
	tools := NewRuntimeTools(RuntimeDependencies{Bridge: fixture})
	result, err := tools.LoopStart(context.Background(), ToolContext{CallerScope: "mcp-fixture", RequestID: "mcp-request-7"}, 7)
	if err != nil || result.Operation.OperationID == "" {
		t.Fatalf("result=%#v error=%v", result, err)
	}
	if fixture.command.Kind != applicationloop.CommandLoopStart || fixture.command.ProjectID != 7 || fixture.command.CallerScope != "mcp-fixture" || fixture.command.IdempotencyKey == "" {
		t.Fatalf("command=%#v", fixture.command)
	}
	first := fixture.command.IdempotencyKey
	if _, err := tools.LoopStart(context.Background(), ToolContext{CallerScope: "mcp-fixture", RequestID: "mcp-request-7"}, 7); err != nil || fixture.command.IdempotencyKey != first {
		t.Fatalf("retry identity=%q error=%v", fixture.command.IdempotencyKey, err)
	}
}
