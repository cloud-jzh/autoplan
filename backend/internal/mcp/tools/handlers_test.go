package tools

import (
	"context"
	"encoding/json"
	"testing"

	applicationprojects "github.com/lyming99/autoplan/backend/internal/application/projects"
	"github.com/lyming99/autoplan/backend/internal/domain/contracts"
	domainproject "github.com/lyming99/autoplan/backend/internal/domain/project"
	"github.com/lyming99/autoplan/backend/internal/mcp"
)

func TestFactoryRegistersEveryFrozenToolWithoutASecondImplementation(t *testing.T) {
	factory := NewFactory(Dependencies{})
	registry, err := mcp.NewRegistry(Catalog(), factory)
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}
	tools := registry.List()
	if len(tools) != 28 {
		t.Fatalf("tools = %d, want 28", len(tools))
	}
	for _, descriptor := range tools {
		if factory.Handler(descriptor) == nil {
			t.Fatalf("tool %q has no shared adapter", descriptor.Name)
		}
	}
	result, code := registry.Call(context.Background(), mcp.ToolCall{
		Name: ListProjects, Arguments: json.RawMessage(`{}`), Context: mcp.ToolContext{CallerScope: "mcp-test", RequestID: "request-test"},
	})
	if code != "mcp_tool_unavailable" || !result.IsError {
		t.Fatalf("unconfigured adapter result = %#v, %q", result, code)
	}
}

func TestProjectListHandlerDelegatesToSharedApplicationService(t *testing.T) {
	projects := &projectSpy{items: []contracts.Project{{ID: 7, Name: "Alpha", Description: "shared"}}}
	factory := NewFactory(Dependencies{Projects: projects})
	handler := factory.Handler(mcp.ToolDescriptor{Name: ListProjects})
	result, err := handler.Call(context.Background(), mcp.ToolCall{Name: ListProjects, Arguments: json.RawMessage(`{"query":"alp","limit":10}`)})
	if err != nil || !projects.listed || result.IsError {
		t.Fatalf("result=%#v err=%v listed=%t", result, err, projects.listed)
	}
	content, ok := result.StructuredContent.(map[string]any)
	if !ok || len(content["projects"].([]contracts.Project)) != 1 {
		t.Fatalf("unexpected projection: %#v", result.StructuredContent)
	}
}

type projectSpy struct {
	items  []contracts.Project
	listed bool
}

func (spy *projectSpy) List(context.Context, domainproject.Visibility) ([]contracts.Project, error) {
	spy.listed = true
	return spy.items, nil
}

func (spy *projectSpy) Get(context.Context, int64, domainproject.Visibility) (contracts.Project, error) {
	return contracts.Project{}, domainproject.ErrNotFound
}

func (spy *projectSpy) Snapshot(context.Context, *int64, domainproject.Visibility) (contracts.AppSnapshot, error) {
	return contracts.AppSnapshot{}, nil
}

func (spy *projectSpy) Create(context.Context, applicationprojects.CreateCommand, domainproject.Visibility) (contracts.AppSnapshot, error) {
	return contracts.AppSnapshot{}, nil
}

func TestMapperRejectsUnknownAndClientSuppliedTransportMetadata(t *testing.T) {
	if _, err := decodeObject(json.RawMessage(`{"projectId":1,"callerScope":"forged"}`), "projectId"); err == nil {
		t.Fatal("unknown caller metadata was accepted")
	}
	request := caller(mcp.ToolContext{})
	if request.CallerScope != "mcp-local" || request.RequestID != "mcp-request" {
		t.Fatalf("caller defaults = %#v", request)
	}
}
