package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
)

var ErrInvalidRegistry = errors.New("mcp registry is invalid")

// ToolDescriptor is transport-neutral catalog metadata. InputSchema is copied
// on every boundary so HTTP and stdio cannot mutate or diverge from the same
// frozen source of truth.
type ToolDescriptor struct {
	Name        string          `json:"name"`
	Title       string          `json:"title"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

type ToolTextContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// ToolResult is the shared MCP CallToolResult wire model. P008 provides the
// handlers that construct its business projection; P007 only guarantees one
// neutral error/result envelope for both transports.
type ToolResult struct {
	IsError           bool              `json:"isError,omitempty"`
	Content           []ToolTextContent `json:"content"`
	StructuredContent any               `json:"structuredContent"`
}

type ToolCall struct {
	Name      string
	Arguments json.RawMessage
	Context   ToolContext
	Transport Transport
}

type ToolHandler interface {
	Call(context.Context, ToolCall) (ToolResult, error)
}

type ToolHandlerFunc func(context.Context, ToolCall) (ToolResult, error)

func (function ToolHandlerFunc) Call(ctx context.Context, call ToolCall) (ToolResult, error) {
	return function(ctx, call)
}

// AdapterFactory is the sole extension point for P008. The factory receives
// one descriptor from this immutable catalog and must return a handler backed
// by the shared application services, never a transport-specific implementation.
type AdapterFactory interface {
	Handler(ToolDescriptor) ToolHandler
}

type AdapterFactoryFunc func(ToolDescriptor) ToolHandler

func (function AdapterFactoryFunc) Handler(descriptor ToolDescriptor) ToolHandler {
	return function(descriptor)
}

type registeredTool struct {
	descriptor ToolDescriptor
	handler    ToolHandler
}

// Registry has no mutator. A transport is constructed with one Registry and
// P008 can create a new, complete registry with its adapter factory instead of
// replacing individual handlers while requests are in flight.
type Registry struct {
	ordered []ToolDescriptor
	tools   map[string]registeredTool
}

func NewRegistry(descriptors []ToolDescriptor, factory AdapterFactory) (*Registry, error) {
	if len(descriptors) == 0 || len(descriptors) > 64 {
		return nil, ErrInvalidRegistry
	}
	result := &Registry{ordered: make([]ToolDescriptor, 0, len(descriptors)), tools: make(map[string]registeredTool, len(descriptors))}
	for _, source := range descriptors {
		descriptor, err := normalizedDescriptor(source)
		if err != nil {
			return nil, ErrInvalidRegistry
		}
		if _, duplicate := result.tools[descriptor.Name]; duplicate {
			return nil, ErrInvalidRegistry
		}
		var handler ToolHandler
		if factory != nil {
			handler = factory.Handler(cloneDescriptor(descriptor))
		}
		result.tools[descriptor.Name] = registeredTool{descriptor: descriptor, handler: handler}
		result.ordered = append(result.ordered, descriptor)
	}
	sort.Slice(result.ordered, func(left, right int) bool { return result.ordered[left].Name < result.ordered[right].Name })
	return result, nil
}

func NewFrozenRegistry(factory AdapterFactory) (*Registry, error) {
	return NewRegistry(FrozenToolDescriptors(), factory)
}

func (registry *Registry) List() []ToolDescriptor {
	if registry == nil {
		return nil
	}
	result := make([]ToolDescriptor, 0, len(registry.ordered))
	for _, descriptor := range registry.ordered {
		result = append(result, cloneDescriptor(descriptor))
	}
	return result
}

func (registry *Registry) Get(name string) (ToolDescriptor, bool) {
	if registry == nil {
		return ToolDescriptor{}, false
	}
	tool, exists := registry.tools[name]
	return cloneDescriptor(tool.descriptor), exists
}

func (registry *Registry) Call(ctx context.Context, call ToolCall) (ToolResult, string) {
	if registry == nil {
		return errorToolResult("mcp_tool_unavailable"), "mcp_tool_unavailable"
	}
	tool, exists := registry.tools[call.Name]
	if !exists {
		return errorToolResult("mcp_tool_not_found"), "mcp_tool_not_found"
	}
	if tool.handler == nil {
		return errorToolResult("mcp_tool_unavailable"), "mcp_tool_unavailable"
	}
	result, err := tool.handler.Call(ctx, call)
	if err == nil {
		return normalizedToolResult(result), "ok"
	}
	var known ToolError
	if errors.As(err, &known) && stableToolCode(known.Code) {
		return errorToolResult(known.Code), known.Code
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return errorToolResult("mcp_tool_timeout"), "mcp_tool_timeout"
	}
	return errorToolResult("mcp_tool_internal"), "mcp_tool_internal"
}

func normalizedDescriptor(value ToolDescriptor) (ToolDescriptor, error) {
	value.Name = strings.TrimSpace(value.Name)
	value.Title = strings.TrimSpace(value.Title)
	value.Description = strings.TrimSpace(value.Description)
	if !validToolName(value.Name) || value.Title == "" || len(value.Title) > 200 ||
		value.Description == "" || len(value.Description) > 4000 || len(value.InputSchema) == 0 || len(value.InputSchema) > 64<<10 ||
		!json.Valid(value.InputSchema) {
		return ToolDescriptor{}, ErrInvalidRegistry
	}
	var schema any
	if json.Unmarshal(value.InputSchema, &schema) != nil {
		return ToolDescriptor{}, ErrInvalidRegistry
	}
	if _, object := schema.(map[string]any); !object {
		return ToolDescriptor{}, ErrInvalidRegistry
	}
	return cloneDescriptor(value), nil
}

func cloneDescriptor(value ToolDescriptor) ToolDescriptor {
	value.InputSchema = append(json.RawMessage(nil), value.InputSchema...)
	return value
}

func normalizedToolResult(value ToolResult) ToolResult {
	if len(value.Content) == 0 {
		return errorToolResult("mcp_tool_internal")
	}
	if len(value.Content) > 1 {
		value.Content = value.Content[:1]
	}
	value.Content[0].Type = "text"
	if len(value.Content[0].Text) > 65536 {
		value.Content[0].Text = value.Content[0].Text[:65536]
	}
	if value.StructuredContent == nil {
		return errorToolResult("mcp_tool_internal")
	}
	return value
}

func errorToolResult(code string) ToolResult {
	if !stableToolCode(code) {
		code = "mcp_tool_internal"
	}
	return ToolResult{
		IsError:           true,
		Content:           []ToolTextContent{{Type: "text", Text: code}},
		StructuredContent: map[string]string{"error": code, "code": code, "errorCode": code},
	}
}

func stableToolCode(value string) bool {
	switch value {
	case "mcp_tool_not_found", "mcp_tool_invalid", "mcp_tool_forbidden", "mcp_tool_conflict", "mcp_tool_unavailable", "mcp_tool_timeout", "mcp_tool_internal",
		"invalid_intake", "invalid_attachment", "attachment_path_denied", "attachment_recovery_required", "not_found", "duplicate_intake",
		"precondition_failed", "relation_conflict", "idempotency_key_reused", "request_in_progress", "unsupported_media_type", "request_timeout", "service_unavailable", "internal_error", "DUPLICATE_INTAKE":
		return true
	default:
		return false
	}
}

func validToolName(value string) bool {
	if len(value) == 0 || len(value) > 128 {
		return false
	}
	for _, current := range value {
		if !(current >= 'a' && current <= 'z') && current != '_' {
			return false
		}
	}
	return true
}

// FrozenToolDescriptors is the P13B migration catalog. P007 deliberately
// registers no business handler; P008 supplies one shared adapter factory.
func FrozenToolDescriptors() []ToolDescriptor {
	const input = `{"type":"object","additionalProperties":true}`
	result := make([]ToolDescriptor, 0, 28)
	for _, name := range []string{
		"list_projects", "get_project", "create_project",
		"list_requirements", "create_requirement", "get_requirement", "update_requirement", "delete_requirement",
		"list_requirement_plan_links", "replace_requirement_plan_links", "upload_requirement_attachment",
		"list_feedback", "create_feedback", "get_feedback", "update_feedback", "delete_feedback",
		"list_feedback_plan_links", "replace_feedback_plan_links", "upload_feedback_attachment", "delete_attachment",
		"list_plans", "get_plan", "list_tasks", "list_executors", "run_executor", "stop_executor", "start_loop", "stop_loop",
	} {
		result = append(result, ToolDescriptor{
			Name: name, Title: strings.ReplaceAll(name, "_", " "),
			Description: "Autoplan " + strings.ReplaceAll(name, "_", " ") + " operation.",
			InputSchema: json.RawMessage(input),
		})
	}
	return result
}
