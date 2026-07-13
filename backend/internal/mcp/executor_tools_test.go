package mcp

import (
	"context"
	"errors"
	"testing"

	applicationautomation "github.com/lyming99/autoplan/backend/internal/application/automation"
	applicationexecutors "github.com/lyming99/autoplan/backend/internal/application/executors"
	domainoperation "github.com/lyming99/autoplan/backend/internal/domain/operation"
)

func TestExecutorToolsRunAndStopUseOnlySharedServices(t *testing.T) {
	executors := &executorToolApplicationFixture{}
	catalog := &executorToolCatalogFixture{items: []applicationautomation.ExecutorDTO{{ID: 8, ProjectID: 7, Label: "build"}}}
	tools := NewExecutorTools(ExecutorToolDependencies{Executors: executors, Catalog: catalog})
	request := ToolContext{CallerScope: "mcp-fixture", RequestID: "request-fixture"}

	run, err := tools.Run(context.Background(), request, ExecutorToolRequest{ProjectID: 7, Label: "build"})
	if err != nil || run.Operation.OperationID != "operation-executor" || !run.Changed {
		t.Fatalf("run=%#v err=%v", run, err)
	}
	if catalog.query.ProjectID != 7 || catalog.query.Limit != 200 || executors.run.ProjectID != 7 ||
		executors.run.ExecutorID != 8 || executors.run.Caller.ID != "mcp-fixture" || executors.run.IdempotencyKey == "" {
		t.Fatalf("catalog=%#v run=%#v", catalog.query, executors.run)
	}

	stopped, err := tools.Stop(context.Background(), request, ExecutorToolRequest{ProjectID: 7, ExecutorID: int64Pointer(8)})
	if err != nil || !stopped.Stopped || stopped.Operation == nil || stopped.Operation.OperationID != "operation-executor" {
		t.Fatalf("stop=%#v err=%v", stopped, err)
	}
	if executors.stop.ProjectID != 7 || executors.stop.ExecutorID != 8 || executors.stop.Caller.ID != "mcp-fixture" {
		t.Fatalf("stop command=%#v", executors.stop)
	}
}

func TestExecutorToolsRejectsAmbiguousOrUnavailableSelectorsBeforeRun(t *testing.T) {
	executors := &executorToolApplicationFixture{}
	tools := NewExecutorTools(ExecutorToolDependencies{Executors: executors})
	_, err := tools.Run(context.Background(), ToolContext{}, ExecutorToolRequest{ProjectID: 7})
	var failure ToolError
	if !errors.As(err, &failure) || failure.Code != "invalid_request" || executors.run.ExecutorID != 0 {
		t.Fatalf("missing selector error=%v command=%#v", err, executors.run)
	}
	_, err = tools.Run(context.Background(), ToolContext{}, ExecutorToolRequest{ProjectID: 7, ExecutorID: int64Pointer(8), Label: "build"})
	if !errors.As(err, &failure) || failure.Code != "invalid_request" {
		t.Fatalf("ambiguous selector error=%v", err)
	}
	if names := tools.Names(); len(names) != 2 || names[0] != "run_executor" || names[1] != "stop_executor" {
		t.Fatalf("tool names=%#v", names)
	}
}

type executorToolApplicationFixture struct {
	run  applicationexecutors.RunCommand
	stop applicationexecutors.StopCommand
}

func (fixture *executorToolApplicationFixture) Run(_ context.Context, command applicationexecutors.RunCommand) (applicationexecutors.Result, error) {
	fixture.run = command
	return applicationexecutors.Result{Operation: executorToolOperation(), Changed: true}, nil
}

func (fixture *executorToolApplicationFixture) Stop(_ context.Context, command applicationexecutors.StopCommand) (applicationexecutors.StopResult, error) {
	fixture.stop = command
	return applicationexecutors.StopResult{Operation: executorToolOperation(), Changed: true, Stopped: true}, nil
}

type executorToolCatalogFixture struct {
	items []applicationautomation.ExecutorDTO
	query applicationautomation.ListQuery
}

func (fixture *executorToolCatalogFixture) ListExecutors(_ context.Context, query applicationautomation.ListQuery) ([]applicationautomation.ExecutorDTO, error) {
	fixture.query = query
	return append([]applicationautomation.ExecutorDTO(nil), fixture.items...), nil
}

func executorToolOperation() domainoperation.Operation {
	return domainoperation.Operation{
		OperationID: "operation-executor", ProjectID: 7, Type: "executor.run", Status: domainoperation.StatusRunning,
		RequestID: "request-fixture", UpdatedAt: "2026-07-12T10:00:00Z",
	}
}

func int64Pointer(value int64) *int64 { return &value }
