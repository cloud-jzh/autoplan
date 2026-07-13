package httpapi

import (
	"net/http"
	"strings"

	applicationloop "github.com/lyming99/autoplan/backend/internal/application/loop"
)

const IntakeRetryPlanGenerationActionPath = "/api/v1/projects/{project_id}/intake/{intake_type}/{intake_id}/actions/retry-plan-generation"

const commandIntakeRetryPlanGeneration applicationloop.CommandKind = "intake.retry_plan_generation"

// RegisterIntakeActionRoutes keeps retry input intentionally narrow. Provider
// and command overrides are configuration-owned write-only inputs and are not
// accepted by this runtime adapter until their application executor exists.
func RegisterIntakeActionRoutes(router *Router, security *Security, service RuntimeActionService) error {
	if router == nil || security == nil || service == nil {
		return ErrRouterDependency
	}
	return router.HandlePattern(http.MethodPost, IntakeRetryPlanGenerationActionPath, security.Protect(TransportREST,
		runtimeActionEndpoint(service, router.BodyLimitBytes(), func(writer http.ResponseWriter, request *http.Request, projectID int64) (applicationloop.Command, *APIError) {
			if failure := decodeEmptyRuntimeAction(writer, request, router.BodyLimitBytes()); failure != nil {
				return applicationloop.Command{}, failure
			}
			intakeType, intakeID, failure := intakeTargetFromRuntimeActionPath(request.URL.Path, projectID)
			if failure != nil {
				return applicationloop.Command{}, failure
			}
			return applicationloop.Command{Kind: commandIntakeRetryPlanGeneration, ProjectID: projectID, IntakeID: intakeID, Action: intakeType}, nil
		}),
	))
}

func intakeTargetFromRuntimeActionPath(path string, projectID int64) (string, int64, *APIError) {
	parts := strings.Split(strings.TrimPrefix(path, "/api/v1/projects/"), "/")
	if len(parts) != 6 || parts[0] != decimalRuntimeID(projectID) || parts[1] != "intake" || parts[4] != "actions" || parts[5] != "retry-plan-generation" {
		failure := NewAPIError(CodeInvalidIntake, &ErrorDetails{Field: "intake_id"})
		return "", 0, &failure
	}
	if parts[2] != "requirement" && parts[2] != "feedback" {
		failure := NewAPIError(CodeInvalidIntake, &ErrorDetails{Field: "intake_type"})
		return "", 0, &failure
	}
	id, failure := parseRuntimeResourceID(parts[3], "intake_id")
	if failure != nil {
		return "", 0, failure
	}
	return parts[2], id, nil
}
