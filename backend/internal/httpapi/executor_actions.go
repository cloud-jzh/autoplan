package httpapi

import (
	"context"
	"net/http"
	"strings"

	"github.com/lyming99/autoplan/backend/internal/application"
	applicationexecutors "github.com/lyming99/autoplan/backend/internal/application/executors"
)

const (
	ProjectExecutorRunActionPath    = "/api/v1/projects/{project_id}/executors/{executor_id}/actions/run"
	ProjectExecutorStopActionPath   = "/api/v1/projects/{project_id}/executors/{executor_id}/actions/stop"
	ProjectExecutorPluginActionPath = "/api/v1/projects/{project_id}/executors/{executor_id}/actions/{action}"
)

// ExecutorActionService is the shared project-scoped Executor service. HTTP
// supplies only stable resource IDs and a lifecycle action; persisted executor
// definitions remain the only source of command, args, cwd, env and plugin
// details.
type ExecutorActionService interface {
	Run(context.Context, applicationexecutors.RunCommand) (applicationexecutors.Result, error)
	Stop(context.Context, applicationexecutors.StopCommand) (applicationexecutors.StopResult, error)
	RunAction(context.Context, applicationexecutors.ActionCommand) (applicationexecutors.Result, error)
}

var _ ExecutorActionService = (*applicationexecutors.Service)(nil)

func RegisterExecutorActionRoutes(router *Router, security *Security, service ExecutorActionService) error {
	if router == nil || security == nil || service == nil {
		return ErrRouterDependency
	}
	run := security.Protect(TransportREST, executorRunEndpoint(service, router.BodyLimitBytes()))
	stop := security.Protect(TransportREST, executorStopEndpoint(service, router.BodyLimitBytes()))
	action := security.Protect(TransportREST, executorPluginActionEndpoint(service, router.BodyLimitBytes()))
	for _, route := range []struct {
		path     string
		endpoint Endpoint
	}{
		{ProjectExecutorRunActionPath, run},
		{ProjectExecutorStopActionPath, stop},
		{ProjectExecutorPluginActionPath, action},
	} {
		if err := router.HandlePattern(http.MethodPost, route.path, route.endpoint); err != nil {
			return err
		}
	}
	return nil
}

func executorRunEndpoint(service ExecutorActionService, bodyLimit int64) Endpoint {
	return func(_ application.Boundary, writer http.ResponseWriter, request *http.Request) {
		metadata, failure := processMutationContext(request)
		if failure != nil {
			WriteError(writer, request, *failure)
			return
		}
		projectID, executorID, failure := processResourceActionTarget(request, "executors", "executor_id", "run")
		if failure != nil {
			WriteError(writer, request, *failure)
			return
		}
		if failure := decodeEmptyProcessAction(writer, request, bodyLimit); failure != nil {
			WriteError(writer, request, *failure)
			return
		}
		result, err := service.Run(request.Context(), applicationexecutors.RunCommand{
			Caller:    applicationexecutors.Caller{ID: metadata.CallerScope, ProjectID: projectID},
			ProjectID: projectID, ExecutorID: executorID, RequestID: metadata.RequestID, IdempotencyKey: metadata.IdempotencyKey,
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

func executorStopEndpoint(service ExecutorActionService, bodyLimit int64) Endpoint {
	return func(_ application.Boundary, writer http.ResponseWriter, request *http.Request) {
		metadata, failure := processMutationContext(request)
		if failure != nil {
			WriteError(writer, request, *failure)
			return
		}
		projectID, executorID, failure := processResourceActionTarget(request, "executors", "executor_id", "stop")
		if failure != nil {
			WriteError(writer, request, *failure)
			return
		}
		if failure := decodeEmptyProcessAction(writer, request, bodyLimit); failure != nil {
			WriteError(writer, request, *failure)
			return
		}
		result, err := service.Stop(request.Context(), applicationexecutors.StopCommand{
			Caller:    applicationexecutors.Caller{ID: metadata.CallerScope, ProjectID: projectID},
			ProjectID: projectID, ExecutorID: executorID, RequestID: metadata.RequestID,
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

func executorPluginActionEndpoint(service ExecutorActionService, bodyLimit int64) Endpoint {
	return func(_ application.Boundary, writer http.ResponseWriter, request *http.Request) {
		metadata, failure := processMutationContext(request)
		if failure != nil {
			WriteError(writer, request, *failure)
			return
		}
		projectID, executorID, action, failure := executorPluginActionTarget(request)
		if failure != nil {
			WriteError(writer, request, *failure)
			return
		}
		if failure := decodeEmptyProcessAction(writer, request, bodyLimit); failure != nil {
			WriteError(writer, request, *failure)
			return
		}
		result, err := service.RunAction(request.Context(), applicationexecutors.ActionCommand{
			Caller:    applicationexecutors.Caller{ID: metadata.CallerScope, ProjectID: projectID},
			ProjectID: projectID, ExecutorID: executorID, Action: action,
			RequestID: metadata.RequestID, IdempotencyKey: metadata.IdempotencyKey,
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

func executorPluginActionTarget(request *http.Request) (int64, int64, string, *APIError) {
	if request == nil || request.URL == nil || request.URL.RawQuery != "" {
		failure := NewAPIError(CodeRuntimeCommand, &ErrorDetails{Field: "query"})
		return 0, 0, "", &failure
	}
	parts := strings.Split(strings.TrimPrefix(request.URL.Path, "/api/v1/projects/"), "/")
	if len(parts) != 5 || parts[1] != "executors" || parts[3] != "actions" {
		failure := NewAPIError(CodeRuntimeCommand, &ErrorDetails{Field: "executor_id"})
		return 0, 0, "", &failure
	}
	projectID, failure := parseCanonicalProjectID(parts[0])
	if failure != nil {
		return 0, 0, "", failure
	}
	executorID, failure := parseRuntimeResourceID(parts[2], "executor_id")
	if failure != nil {
		return 0, 0, "", failure
	}
	if parts[4] != "start" && parts[4] != "reload" {
		invalid := NewAPIError(CodeRuntimeCommand, &ErrorDetails{Field: "action"})
		return 0, 0, "", &invalid
	}
	return projectID, executorID, parts[4], nil
}
