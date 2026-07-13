package httpapi

import (
	"context"
	"net/http"
	"strings"

	"github.com/lyming99/autoplan/backend/internal/application"
	"github.com/lyming99/autoplan/backend/internal/application/capabilities"
	applicationscripts "github.com/lyming99/autoplan/backend/internal/application/scripts"
	domainoperation "github.com/lyming99/autoplan/backend/internal/domain/operation"
)

const (
	ProjectScriptRunActionPath  = "/api/v1/projects/{project_id}/scripts/{script_id}/actions/run"
	ProjectScriptStopActionPath = "/api/v1/projects/{project_id}/scripts/{script_id}/actions/stop"
	maximumProcessActionBody    = 1024
)

// ScriptActionService is intentionally the shared Script application service.
// The HTTP adapter never receives a command, path, environment, process ID or
// workspace from the client.
type ScriptActionService interface {
	RunManual(context.Context, applicationscripts.RunCommand) (applicationscripts.Result, error)
	Stop(context.Context, applicationscripts.StopCommand) (applicationscripts.StopResult, error)
}

var _ ScriptActionService = (*applicationscripts.Service)(nil)

type processStopReference struct {
	Operation *capabilities.OperationReference `json:"operation,omitempty"`
	Stopped   bool                             `json:"stopped"`
	Changed   bool                             `json:"changed"`
}

type processStopEnvelope struct {
	Data      processStopReference `json:"data"`
	RequestID string               `json:"request_id"`
}

// RegisterProcessActionRoutes composes the two process resource families. It
// is deliberately separate from static Automation registration: persisting a
// Script/Executor definition never makes it executable through a different
// transport path.
func RegisterProcessActionRoutes(
	router *Router,
	security *Security,
	scripts ScriptActionService,
	executors ExecutorActionService,
) error {
	if err := RegisterScriptActionRoutes(router, security, scripts); err != nil {
		return err
	}
	return RegisterExecutorActionRoutes(router, security, executors)
}

func RegisterScriptActionRoutes(router *Router, security *Security, service ScriptActionService) error {
	if router == nil || security == nil || service == nil {
		return ErrRouterDependency
	}
	run := security.Protect(TransportREST, scriptRunEndpoint(service, router.BodyLimitBytes()))
	stop := security.Protect(TransportREST, scriptStopEndpoint(service, router.BodyLimitBytes()))
	for _, route := range []struct {
		path     string
		endpoint Endpoint
	}{
		{ProjectScriptRunActionPath, run},
		{ProjectScriptStopActionPath, stop},
	} {
		if err := router.HandlePattern(http.MethodPost, route.path, route.endpoint); err != nil {
			return err
		}
	}
	return nil
}

func scriptRunEndpoint(service ScriptActionService, bodyLimit int64) Endpoint {
	return func(_ application.Boundary, writer http.ResponseWriter, request *http.Request) {
		metadata, failure := processMutationContext(request)
		if failure != nil {
			WriteError(writer, request, *failure)
			return
		}
		projectID, scriptID, failure := processResourceActionTarget(request, "scripts", "script_id", "run")
		if failure != nil {
			WriteError(writer, request, *failure)
			return
		}
		if failure := decodeEmptyProcessAction(writer, request, bodyLimit); failure != nil {
			WriteError(writer, request, *failure)
			return
		}
		result, err := service.RunManual(request.Context(), applicationscripts.RunCommand{
			Caller:    applicationscripts.Caller{ID: metadata.CallerScope, ProjectID: projectID},
			ProjectID: projectID, ScriptID: scriptID, RequestID: metadata.RequestID, IdempotencyKey: metadata.IdempotencyKey,
		})
		if err != nil {
			writeProcessActionServiceError(writer, request, err)
			return
		}
		WriteResponse(writer, request, http.StatusAccepted, operationAcceptedEnvelope{
			Data: processOperationReference(result.Operation), RequestID: RequestID(request.Context()),
		})
	}
}

func scriptStopEndpoint(service ScriptActionService, bodyLimit int64) Endpoint {
	return func(_ application.Boundary, writer http.ResponseWriter, request *http.Request) {
		metadata, failure := processMutationContext(request)
		if failure != nil {
			WriteError(writer, request, *failure)
			return
		}
		projectID, scriptID, failure := processResourceActionTarget(request, "scripts", "script_id", "stop")
		if failure != nil {
			WriteError(writer, request, *failure)
			return
		}
		if failure := decodeEmptyProcessAction(writer, request, bodyLimit); failure != nil {
			WriteError(writer, request, *failure)
			return
		}
		result, err := service.Stop(request.Context(), applicationscripts.StopCommand{
			Caller:    applicationscripts.Caller{ID: metadata.CallerScope, ProjectID: projectID},
			ProjectID: projectID, ScriptID: scriptID, RequestID: metadata.RequestID,
		})
		if err != nil {
			writeProcessActionServiceError(writer, request, err)
			return
		}
		WriteResponse(writer, request, http.StatusAccepted, processStopEnvelope{
			Data:      processStopReference{Operation: processOperationReferencePointer(result.Operation), Stopped: result.Stopped, Changed: result.Changed},
			RequestID: RequestID(request.Context()),
		})
	}
}

func processMutationContext(request *http.Request) (mutationContext, *APIError) {
	metadata, failure := mutationRequestContext(request)
	if failure != nil {
		return mutationContext{}, failure
	}
	if metadata.IdempotencyKey == "" {
		invalid := NewAPIError(CodeInvalidIdempotencyKey, &ErrorDetails{Field: "idempotency_key"})
		return mutationContext{}, &invalid
	}
	return metadata, nil
}

func decodeEmptyProcessAction(writer http.ResponseWriter, request *http.Request, bodyLimit int64) *APIError {
	if bodyLimit <= 0 || bodyLimit > maximumProcessActionBody {
		bodyLimit = maximumProcessActionBody
	}
	var input struct{}
	return DecodeJSON(writer, request, &input, bodyLimit)
}

func processResourceActionTarget(request *http.Request, resource, field, expectedAction string) (int64, int64, *APIError) {
	if request == nil || request.URL == nil || request.URL.RawQuery != "" {
		failure := NewAPIError(CodeRuntimeCommand, &ErrorDetails{Field: "query"})
		return 0, 0, &failure
	}
	parts := strings.Split(strings.TrimPrefix(request.URL.Path, "/api/v1/projects/"), "/")
	if len(parts) != 5 || parts[1] != resource || parts[3] != "actions" || parts[4] != expectedAction {
		failure := NewAPIError(CodeRuntimeCommand, &ErrorDetails{Field: field})
		return 0, 0, &failure
	}
	projectID, failure := parseCanonicalProjectID(parts[0])
	if failure != nil {
		return 0, 0, failure
	}
	resourceID, failure := parseRuntimeResourceID(parts[2], field)
	if failure != nil {
		return 0, 0, failure
	}
	return projectID, resourceID, nil
}

func processOperationReference(operation domainoperation.Operation) capabilities.OperationReference {
	return capabilities.OperationReference{
		OperationID: operation.OperationID, Type: operation.Type, Status: string(operation.Status),
		RequestID: operation.RequestID, AcceptedAt: operation.UpdatedAt,
	}
}

func processOperationReferencePointer(operation domainoperation.Operation) *capabilities.OperationReference {
	if operation.OperationID == "" {
		return nil
	}
	result := processOperationReference(operation)
	return &result
}
