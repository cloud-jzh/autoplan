package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"unicode"

	applicationautomation "github.com/lyming99/autoplan/backend/internal/application/automation"
	"github.com/lyming99/autoplan/backend/internal/application/capabilities"
	applicationexecutors "github.com/lyming99/autoplan/backend/internal/application/executors"
	domainoperation "github.com/lyming99/autoplan/backend/internal/domain/operation"
	"github.com/lyming99/autoplan/backend/internal/repository"
)

// ExecutorApplication is the same closed application service used by REST.
// It has no repository, runner, process, file or secret capability in the MCP
// adapter.
type ExecutorApplication interface {
	Run(context.Context, applicationexecutors.RunCommand) (applicationexecutors.Result, error)
	Stop(context.Context, applicationexecutors.StopCommand) (applicationexecutors.StopResult, error)
}

// ExecutorCatalog is optional support for the legacy exact-label selector.
// It remains an application-service query, never a repository bypass. Labels
// are resolved before a process command is constructed and are not echoed in
// a result or error.
type ExecutorCatalog interface {
	ListExecutors(context.Context, applicationautomation.ListQuery) ([]applicationautomation.ExecutorDTO, error)
}

var _ ExecutorApplication = (*applicationexecutors.Service)(nil)
var _ ExecutorCatalog = (*applicationautomation.Service)(nil)

type ExecutorToolDependencies struct {
	Executors ExecutorApplication
	Catalog   ExecutorCatalog
}

type ExecutorTools struct {
	executors ExecutorApplication
	catalog   ExecutorCatalog
}

func NewExecutorTools(dependencies ExecutorToolDependencies) *ExecutorTools {
	return &ExecutorTools{executors: dependencies.Executors, catalog: dependencies.Catalog}
}

// Names intentionally exposes only the frozen Node-compatible executor tools.
// Script execution has no MCP alias in this migration, and plugin lifecycle
// action is available only through the authenticated REST resource route.
func (tools *ExecutorTools) Names() []string { return []string{"run_executor", "stop_executor"} }

type ExecutorToolRequest struct {
	ProjectID  int64
	ExecutorID *int64
	Label      string
}

type ExecutorToolResult struct {
	Operation capabilities.OperationReference `json:"operation"`
	Changed   bool                            `json:"changed"`
}

type ExecutorToolStopResult struct {
	Operation *capabilities.OperationReference `json:"operation,omitempty"`
	Stopped   bool                             `json:"stopped"`
	Changed   bool                             `json:"changed"`
}

func (tools *ExecutorTools) Run(ctx context.Context, request ToolContext, input ExecutorToolRequest) (ExecutorToolResult, error) {
	if tools == nil || tools.executors == nil {
		return ExecutorToolResult{}, ToolError{Code: "service_unavailable"}
	}
	executorID, err := tools.resolveExecutor(ctx, input)
	if err != nil {
		return ExecutorToolResult{}, mapExecutorToolError(err)
	}
	caller, requestID := executorToolIdentity(request)
	key := executorToolIdempotencyKey("run", request, input.ProjectID, executorID)
	result, runErr := tools.executors.Run(ctx, applicationexecutors.RunCommand{
		Caller:    applicationexecutors.Caller{ID: caller, ProjectID: input.ProjectID},
		ProjectID: input.ProjectID, ExecutorID: executorID, RequestID: requestID, IdempotencyKey: key,
	})
	if runErr != nil {
		return ExecutorToolResult{}, mapExecutorToolError(runErr)
	}
	return ExecutorToolResult{Operation: executorOperationReference(result.Operation), Changed: result.Changed}, nil
}

func (tools *ExecutorTools) Stop(ctx context.Context, request ToolContext, input ExecutorToolRequest) (ExecutorToolStopResult, error) {
	if tools == nil || tools.executors == nil {
		return ExecutorToolStopResult{}, ToolError{Code: "service_unavailable"}
	}
	executorID, err := tools.resolveExecutor(ctx, input)
	if err != nil {
		return ExecutorToolStopResult{}, mapExecutorToolError(err)
	}
	caller, requestID := executorToolIdentity(request)
	result, stopErr := tools.executors.Stop(ctx, applicationexecutors.StopCommand{
		Caller:    applicationexecutors.Caller{ID: caller, ProjectID: input.ProjectID},
		ProjectID: input.ProjectID, ExecutorID: executorID, RequestID: requestID,
	})
	if stopErr != nil {
		return ExecutorToolStopResult{}, mapExecutorToolError(stopErr)
	}
	return ExecutorToolStopResult{
		Operation: executorOperationReferencePointer(result.Operation), Stopped: result.Stopped, Changed: result.Changed,
	}, nil
}

func (tools *ExecutorTools) resolveExecutor(ctx context.Context, input ExecutorToolRequest) (int64, error) {
	if input.ProjectID <= 0 || (input.ExecutorID == nil && strings.TrimSpace(input.Label) == "") ||
		(input.ExecutorID != nil && strings.TrimSpace(input.Label) != "") {
		return 0, applicationexecutors.ErrInvalidCommand
	}
	if input.ExecutorID != nil {
		if *input.ExecutorID <= 0 {
			return 0, applicationexecutors.ErrInvalidCommand
		}
		return *input.ExecutorID, nil
	}
	label := strings.TrimSpace(input.Label)
	if label == "" || label != input.Label || len(label) > 500 || strings.ContainsFunc(label, unicode.IsControl) || tools.catalog == nil {
		return 0, applicationexecutors.ErrInvalidCommand
	}
	items, err := tools.catalog.ListExecutors(ctx, applicationautomation.ListQuery{ProjectID: input.ProjectID, Limit: 200})
	if err != nil {
		return 0, err
	}
	for _, item := range items {
		if item.ProjectID == input.ProjectID && item.ID > 0 && item.Label == label {
			return item.ID, nil
		}
	}
	return 0, applicationexecutors.ErrNotFound
}

func executorToolIdentity(request ToolContext) (string, string) {
	caller := strings.TrimSpace(request.CallerScope)
	if caller == "" {
		caller = "mcp-local"
	}
	requestID := strings.TrimSpace(request.RequestID)
	if requestID == "" {
		requestID = "mcp-request"
	}
	return caller, requestID
}

func executorToolIdempotencyKey(kind string, request ToolContext, projectID, executorID int64) string {
	if key := strings.TrimSpace(request.IdempotencyKey); key != "" {
		return key
	}
	caller, requestID := executorToolIdentity(request)
	sum := sha256.Sum256([]byte("autoplan-p12-mcp-executor\x00" + kind + "\x00" + caller + "\x00" + requestID + "\x00" + decimal(projectID) + "\x00" + decimal(executorID)))
	return "mcp-" + hex.EncodeToString(sum[:20])
}

func executorOperationReference(operation domainoperation.Operation) capabilities.OperationReference {
	return capabilities.OperationReference{
		OperationID: operation.OperationID, Type: operation.Type, Status: string(operation.Status),
		RequestID: operation.RequestID, AcceptedAt: operation.UpdatedAt,
	}
}

func executorOperationReferencePointer(operation domainoperation.Operation) *capabilities.OperationReference {
	if operation.OperationID == "" {
		return nil
	}
	value := executorOperationReference(operation)
	return &value
}

func mapExecutorToolError(err error) error {
	if err == nil {
		return nil
	}
	var tool ToolError
	if errors.As(err, &tool) {
		return tool
	}
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return ToolError{Code: "request_timeout"}
	case errors.Is(err, applicationautomation.ErrInvalidCommand), errors.Is(err, applicationexecutors.ErrInvalidCommand), errors.Is(err, applicationexecutors.ErrActionInvalid),
		errors.Is(err, applicationexecutors.ErrActionUnsupported):
		return ToolError{Code: "invalid_request"}
	case errors.Is(err, applicationexecutors.ErrNotFound), errors.Is(err, applicationexecutors.ErrDisabled),
		errors.Is(err, repository.ErrNotFound), errors.Is(err, repository.ErrProjectMismatch):
		return ToolError{Code: "not_found"}
	case errors.Is(err, applicationautomation.ErrStateConflict), errors.Is(err, applicationexecutors.ErrBusy), errors.Is(err, applicationexecutors.ErrStateConflict),
		errors.Is(err, applicationexecutors.ErrDependencyMissing), errors.Is(err, applicationexecutors.ErrDependencyCycle),
		errors.Is(err, applicationexecutors.ErrDependencyFailed):
		return ToolError{Code: "precondition_failed"}
	case errors.Is(err, applicationautomation.ErrUnavailable), errors.Is(err, applicationexecutors.ErrQueueFull), errors.Is(err, applicationexecutors.ErrUnavailable),
		errors.Is(err, repository.ErrNotConfigured), errors.Is(err, repository.ErrClosed), errors.Is(err, repository.ErrWriterUnauthorized):
		return ToolError{Code: "service_unavailable"}
	default:
		return ToolError{Code: "internal_error"}
	}
}
