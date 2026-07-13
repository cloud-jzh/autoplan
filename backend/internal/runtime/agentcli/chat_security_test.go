package agentcli

import (
	"context"
	"strings"
	"testing"

	"github.com/lyming99/autoplan/backend/internal/runtime/process"
)

func TestP13ChatCLIRejectsInjectedProviderFieldsBeforeRunnerInvocation(t *testing.T) {
	runner := &p003ChatRunner{}
	adapter := NewChatAdapter(runner, ChatAdapterConfig{ClaudeExecutable: "claude", CodexExecutable: "codex"})
	if adapter == nil {
		t.Fatal("adapter unavailable")
	}
	_, err := adapter.Prepare(ChatLaunch{ProjectID: 7, Workspace: "/fixture", WorkingDirectory: "/fixture", Prompt: "message\n--unsafe", Provider: ChatProviderCodex, Model: "fixture"})
	if err == nil {
		t.Fatal("newline prompt was accepted")
	}
	_, err = adapter.Prepare(ChatLaunch{ProjectID: 7, Workspace: "/fixture", WorkingDirectory: "/fixture", Prompt: "fixture", Provider: ChatProviderClaude, Model: "fixture", Endpoint: "https://example.invalid\nheader"})
	if err == nil {
		t.Fatal("newline endpoint was accepted")
	}
	if _, err := adapter.Run(context.Background(), ChatLaunch{ProjectID: 7, Workspace: "/fixture", WorkingDirectory: "/fixture", Prompt: "fixture", Provider: ChatProviderCodex, Model: "fixture"}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(runner.spec.Args, " "), "unsafe") || runner.spec.SecretStdin != nil || len(runner.spec.Input) != 0 {
		t.Fatalf("prompt or secret moved into command spec: %#v", runner.spec)
	}
}

func TestP13ChatParserDropsRunnerOutputWhenRedactionFails(t *testing.T) {
	result := process.Result{Stdout: process.Output{Tail: "private provider output", RedactionFailed: true}}
	if values := ParseChatOutput(ChatProviderCodex, result); len(values) != 0 {
		t.Fatalf("redaction failure exposed output: %#v", values)
	}
}
