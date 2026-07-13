package httpapi

import (
	"net/http"
	"strings"

	"github.com/lyming99/autoplan/backend/internal/application/capabilities"
	applicationloop "github.com/lyming99/autoplan/backend/internal/application/loop"
)

const (
	TaskRunActionPath        = "/api/v1/tasks/actions/run"
	TaskRunBatchesActionPath = "/api/v1/tasks/actions/run-batches"
	TaskStopActionPath       = "/api/v1/tasks/actions/stop"
)

const (
	ProjectTaskRunActionPath        = "/api/v1/projects/{project_id}/tasks/{task_id}/actions/run"
	ProjectTaskStopActionPath       = "/api/v1/projects/{project_id}/tasks/{task_id}/actions/stop"
	ProjectTaskRunBatchesActionPath = "/api/v1/projects/{project_id}/plans/{plan_id}/actions/run-batches"
)

func RegisterTaskActionRoutes(router *Router, security *Security) error {
	if router == nil || security == nil {
		return ErrSecurityConfiguration
	}
	for _, route := range []struct {
		path       string
		capability capabilities.ID
	}{
		{TaskRunActionPath, capabilities.TasksRun},
		{TaskRunBatchesActionPath, capabilities.TasksRunBatches},
		{TaskStopActionPath, capabilities.TasksStop},
	} {
		if err := router.Handle(http.MethodPost, route.path, security.Protect(TransportREST,
			DisabledActionEndpoint(route.capability),
		)); err != nil {
			return err
		}
	}
	return nil
}

type taskRuntimeActionRequest struct {
	PlanID int64 `json:"plan_id,omitempty"`
}

type taskBatchesRuntimeActionRequest struct {
	Batches []applicationloop.TaskBatch `json:"batches"`
}

func RegisterProjectTaskActionRoutes(router *Router, security *Security, service RuntimeActionService) error {
	if router == nil || security == nil || service == nil {
		return ErrRouterDependency
	}
	for _, route := range []struct {
		path string
		kind applicationloop.CommandKind
	}{
		{ProjectTaskRunActionPath, applicationloop.CommandTaskRun},
		{ProjectTaskStopActionPath, applicationloop.CommandTaskStop},
	} {
		kind := route.kind
		if err := router.HandlePattern(http.MethodPost, route.path, security.Protect(TransportREST,
			runtimeActionEndpoint(service, router.BodyLimitBytes(), func(writer http.ResponseWriter, request *http.Request, projectID int64) (applicationloop.Command, *APIError) {
				var input taskRuntimeActionRequest
				if failure := DecodeJSON(writer, request, &input, router.BodyLimitBytes()); failure != nil {
					return applicationloop.Command{}, failure
				}
				taskID, failure := taskIDFromRuntimeActionPath(request.URL.Path, projectID)
				if failure != nil {
					return applicationloop.Command{}, failure
				}
				if input.PlanID < 0 {
					failure := NewAPIError(CodeRuntimeCommand, &ErrorDetails{Field: "plan_id"})
					return applicationloop.Command{}, &failure
				}
				return applicationloop.Command{Kind: kind, ProjectID: projectID, PlanID: input.PlanID, TaskID: taskID}, nil
			}),
		)); err != nil {
			return err
		}
	}
	return router.HandlePattern(http.MethodPost, ProjectTaskRunBatchesActionPath, security.Protect(TransportREST,
		runtimeActionEndpoint(service, router.BodyLimitBytes(), func(writer http.ResponseWriter, request *http.Request, projectID int64) (applicationloop.Command, *APIError) {
			var input taskBatchesRuntimeActionRequest
			if failure := DecodeJSON(writer, request, &input, router.BodyLimitBytes()); failure != nil {
				return applicationloop.Command{}, failure
			}
			planID, failure := planIDFromRuntimeActionPath(request.URL.Path, projectID)
			if failure != nil {
				return applicationloop.Command{}, failure
			}
			return applicationloop.Command{Kind: applicationloop.CommandTaskRunBatches, ProjectID: projectID, PlanID: planID, Batches: input.Batches}, nil
		}),
	))
}

func taskIDFromRuntimeActionPath(path string, projectID int64) (int64, *APIError) {
	parts := strings.Split(strings.TrimPrefix(path, "/api/v1/projects/"), "/")
	if len(parts) != 5 || parts[0] != decimalRuntimeID(projectID) || parts[1] != "tasks" || parts[3] != "actions" {
		failure := NewAPIError(CodeInvalidOperation, &ErrorDetails{Field: "task_id"})
		return 0, &failure
	}
	return parseRuntimeResourceID(parts[2], "task_id")
}
