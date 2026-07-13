package httpapi

import (
	"net/http"
	"strings"

	"github.com/lyming99/autoplan/backend/internal/application"
	"github.com/lyming99/autoplan/backend/internal/application/capabilities"
	applicationloop "github.com/lyming99/autoplan/backend/internal/application/loop"
)

const (
	PlanRunActionPath       = "/api/v1/plans/actions/run"
	PlanStopActionPath      = "/api/v1/plans/actions/stop"
	PlanResumeActionPath    = "/api/v1/plans/actions/resume"
	PlanReexecuteActionPath = "/api/v1/plans/actions/re-execute"
	PlanRecreateActionPath  = "/api/v1/plans/actions/recreate"
)

const (
	ProjectPlanStopActionPath      = "/api/v1/projects/{project_id}/plans/{plan_id}/actions/stop"
	ProjectPlanResumeActionPath    = "/api/v1/projects/{project_id}/plans/{plan_id}/actions/resume"
	ProjectPlanReexecuteActionPath = "/api/v1/projects/{project_id}/plans/{plan_id}/actions/re-execute"
	ProjectPlanRecreateActionPath  = "/api/v1/projects/{project_id}/plans/{plan_id}/actions/recreate"
)

func RegisterPlanActionRoutes(router *Router, security *Security) error {
	if router == nil || security == nil {
		return ErrSecurityConfiguration
	}
	for _, route := range []struct {
		path       string
		capability capabilities.ID
	}{
		{PlanRunActionPath, capabilities.PlansRun},
		{PlanStopActionPath, capabilities.PlansStop},
		{PlanResumeActionPath, capabilities.PlansResume},
		{PlanReexecuteActionPath, capabilities.PlansReexecute},
		{PlanRecreateActionPath, capabilities.PlansRecreate},
	} {
		if err := router.Handle(http.MethodPost, route.path, security.Protect(TransportREST,
			DisabledActionEndpoint(route.capability),
		)); err != nil {
			return err
		}
	}
	return nil
}

// RegisterProjectPlanActionRoutes is the P11 replacement for the historical
// target-free disabled endpoints above. It binds the path's project and plan
// identifiers to the one application runtime bridge.
func RegisterProjectPlanActionRoutes(router *Router, security *Security, service RuntimeActionService) error {
	if router == nil || security == nil || service == nil {
		return ErrRouterDependency
	}
	for _, route := range []struct {
		path string
		kind applicationloop.CommandKind
	}{
		{ProjectPlanStopActionPath, applicationloop.CommandPlanStop},
		{ProjectPlanResumeActionPath, applicationloop.CommandPlanResume},
		{ProjectPlanReexecuteActionPath, applicationloop.CommandPlanReexecute},
		{ProjectPlanRecreateActionPath, applicationloop.CommandPlanRecreate},
	} {
		kind := route.kind
		if err := router.HandlePattern(http.MethodPost, route.path, security.Protect(TransportREST,
			runtimeActionEndpoint(service, router.BodyLimitBytes(), func(writer http.ResponseWriter, request *http.Request, projectID int64) (applicationloop.Command, *APIError) {
				if failure := decodeEmptyRuntimeAction(writer, request, router.BodyLimitBytes()); failure != nil {
					return applicationloop.Command{}, failure
				}
				planID, failure := planIDFromRuntimeActionPath(request.URL.Path, projectID)
				if failure != nil {
					return applicationloop.Command{}, failure
				}
				return applicationloop.Command{Kind: kind, ProjectID: projectID, PlanID: planID}, nil
			}),
		)); err != nil {
			return err
		}
	}
	return nil
}

func planIDFromRuntimeActionPath(path string, projectID int64) (int64, *APIError) {
	parts := strings.Split(strings.TrimPrefix(path, "/api/v1/projects/"), "/")
	if len(parts) != 5 || parts[0] != decimalRuntimeID(projectID) || parts[1] != "plans" || parts[3] != "actions" {
		failure := NewAPIError(CodeInvalidOperation, &ErrorDetails{Field: "plan_id"})
		return 0, &failure
	}
	return parseRuntimeResourceID(parts[2], "plan_id")
}

// DisabledActionEndpoint intentionally does not decode the body, validate a
// target, inspect a repository, or invoke an action service. Once transport
// authorization succeeds, every shape of a disabled action gets the same
// target-independent error with a safe capability identifier.
func DisabledActionEndpoint(capability capabilities.ID) Endpoint {
	return func(_ application.Boundary, writer http.ResponseWriter, request *http.Request) {
		WriteError(writer, request, NewAPIError(CodeNotImplemented, &ErrorDetails{
			Capability: string(capability),
		}))
	}
}
