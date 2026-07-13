package agentcli

import (
	"context"
	"strings"
	"testing"

	"github.com/lyming99/autoplan/backend/internal/runtime/process"
)

type p003ChatRunner struct {
	spec  process.Spec
	input []byte
}

func (runner *p003ChatRunner) RunWithInput(_ context.Context, spec process.Spec, input []byte) (process.Result, error) {
	runner.spec = spec
	runner.input = append([]byte(nil), input...)
	return process.Result{}, nil
}

func TestChatAdapterUsesFixedArgumentArraysAndStdin(t *testing.T) {
	runner := &p003ChatRunner{}
	adapter := NewChatAdapter(runner, ChatAdapterConfig{ClaudeExecutable: "claude", CodexExecutable: "codex"})
	if adapter == nil {
		t.Fatal("adapter unavailable")
	}
	spec, err := adapter.Prepare(ChatLaunch{
		ProjectID: 7, Workspace: "/workspace", WorkingDirectory: "/workspace", Prompt: "fixture prompt",
		Provider: ChatProviderCodex, Model: "fixture-model", ReasoningEffort: "high",
	})
	if err != nil || spec.Executable != "codex" || len(spec.Args) == 0 || spec.Args[len(spec.Args)-1] != "-" ||
		strings.Contains(strings.Join(spec.Args, " "), "fixture prompt") || strings.Contains(strings.Join(spec.Args, " "), "danger-full-access") {
		t.Fatalf("spec=%#v error=%v", spec, err)
	}
	if _, err := adapter.Run(context.Background(), ChatLaunch{
		ProjectID: 7, Workspace: "/workspace", WorkingDirectory: "/workspace", Prompt: "fixture prompt",
		Provider: ChatProviderClaude, Model: "fixture-model",
	}); err != nil || string(runner.input) != "fixture prompt" || runner.spec.SecretStdin != nil || len(runner.spec.Input) != 0 {
		t.Fatalf("run spec=%#v input=%q error=%v", runner.spec, runner.input, err)
	}
}

func TestParseChatOutputSkipsMalformedClaudeLines(t *testing.T) {
	result := process.Result{Stdout: process.Output{Tail: "not-json\n{\"type\":\"content_block_delta\",\"delta\":{\"text\":\"safe\"}}"}}
	values := ParseChatOutput(ChatProviderClaude, result)
	if len(values) != 1 || values[0].Text != "safe" {
		t.Fatalf("values=%#v", values)
	}
}

var _ ChatRunner = (*p003ChatRunner)(nil)
