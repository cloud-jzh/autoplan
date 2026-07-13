package mcp

import (
	"context"
	"errors"
	"testing"

	applicationexecutors "github.com/lyming99/autoplan/backend/internal/application/executors"
)

type p12ExecutorToolApplication struct{ calls int }

func (application *p12ExecutorToolApplication) Run(_ context.Context, _ applicationexecutors.RunCommand) (applicationexecutors.Result, error) {
	application.calls++
	return applicationexecutors.Result{}, nil
}

func (application *p12ExecutorToolApplication) Stop(_ context.Context, _ applicationexecutors.StopCommand) (applicationexecutors.StopResult, error) {
	application.calls++
	return applicationexecutors.StopResult{}, nil
}

func TestP12ExecutorToolsRejectControlCharacterSelectorsBeforeApplication(t *testing.T) {
	application := &p12ExecutorToolApplication{}
	tools := NewExecutorTools(ExecutorToolDependencies{Executors: application})
	for _, input := range []ExecutorToolRequest{
		{ProjectID: 7, Label: "build\nfixture"},
		{ProjectID: 7, Label: " build"},
		{ProjectID: 7, ExecutorID: int64Pointer(0)},
	} {
		_, err := tools.Run(context.Background(), ToolContext{CallerScope: "p12", RequestID: "p12-request"}, input)
		var failure ToolError
		if !errors.As(err, &failure) || failure.Code != "invalid_request" {
			t.Fatalf("input=%#v error=%v", input, err)
		}
	}
	if application.calls != 0 {
		t.Fatalf("unsafe selector reached executor service: %d", application.calls)
	}
}
