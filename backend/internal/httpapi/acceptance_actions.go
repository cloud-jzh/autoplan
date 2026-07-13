package httpapi

import (
	"net/http"
	"strings"

	applicationloop "github.com/lyming99/autoplan/backend/internal/application/loop"
)

const (
	AcceptanceAcceptActionPath        = "/api/v1/projects/{project_id}/acceptance/actions/accept"
	AcceptanceUnacceptActionPath      = "/api/v1/projects/{project_id}/acceptance/actions/unaccept"
	AcceptanceRedoActionPath          = "/api/v1/projects/{project_id}/acceptance/actions/redo"
	AcceptanceAcceptBatchActionPath   = "/api/v1/projects/{project_id}/acceptance/actions/accept-batch"
	AcceptanceUnacceptBatchActionPath = "/api/v1/projects/{project_id}/acceptance/actions/unaccept-batch"
)

const (
	commandAcceptanceAccept        applicationloop.CommandKind = "acceptance.accept"
	commandAcceptanceUnaccept      applicationloop.CommandKind = "acceptance.unaccept"
	commandAcceptanceRedo          applicationloop.CommandKind = "acceptance.redo"
	commandAcceptanceAcceptBatch   applicationloop.CommandKind = "acceptance.accept_batch"
	commandAcceptanceUnacceptBatch applicationloop.CommandKind = "acceptance.unaccept_batch"
)

type acceptanceTargetRequest struct {
	TargetType string `json:"target_type"`
	ID         int64  `json:"id"`
	Supplement string `json:"supplement,omitempty"`
}

type acceptanceTargetsRequest struct {
	Targets []acceptanceTargetRequest `json:"targets"`
}

func RegisterAcceptanceActionRoutes(router *Router, security *Security, service RuntimeActionService) error {
	if router == nil || security == nil || service == nil {
		return ErrRouterDependency
	}
	for _, route := range []struct {
		path string
		kind applicationloop.CommandKind
	}{
		{AcceptanceAcceptActionPath, commandAcceptanceAccept},
		{AcceptanceUnacceptActionPath, commandAcceptanceUnaccept},
		{AcceptanceRedoActionPath, commandAcceptanceRedo},
	} {
		kind := route.kind
		if err := router.HandlePattern(http.MethodPost, route.path, security.Protect(TransportREST,
			runtimeActionEndpoint(service, router.BodyLimitBytes(), func(writer http.ResponseWriter, request *http.Request, projectID int64) (applicationloop.Command, *APIError) {
				var input acceptanceTargetRequest
				if failure := DecodeJSON(writer, request, &input, router.BodyLimitBytes()); failure != nil {
					return applicationloop.Command{}, failure
				}
				if !validAcceptanceTarget(input) || (kind != commandAcceptanceRedo && input.Supplement != "") || len(input.Supplement) > 2000 {
					failure := NewAPIError(CodeRuntimeCommand, &ErrorDetails{Field: "target"})
					return applicationloop.Command{}, &failure
				}
				return acceptanceCommand(kind, projectID, input), nil
			}),
		)); err != nil {
			return err
		}
	}
	for _, route := range []struct {
		path string
		kind applicationloop.CommandKind
	}{
		{AcceptanceAcceptBatchActionPath, commandAcceptanceAcceptBatch},
		{AcceptanceUnacceptBatchActionPath, commandAcceptanceUnacceptBatch},
	} {
		kind := route.kind
		if err := router.HandlePattern(http.MethodPost, route.path, security.Protect(TransportREST,
			runtimeActionEndpoint(service, router.BodyLimitBytes(), func(writer http.ResponseWriter, request *http.Request, projectID int64) (applicationloop.Command, *APIError) {
				var input acceptanceTargetsRequest
				if failure := DecodeJSON(writer, request, &input, router.BodyLimitBytes()); failure != nil {
					return applicationloop.Command{}, failure
				}
				if len(input.Targets) == 0 || len(input.Targets) > 100 {
					failure := NewAPIError(CodeRuntimeCommand, &ErrorDetails{Field: "targets"})
					return applicationloop.Command{}, &failure
				}
				for _, target := range input.Targets {
					if !validAcceptanceTarget(target) || target.Supplement != "" {
						failure := NewAPIError(CodeRuntimeCommand, &ErrorDetails{Field: "targets"})
						return applicationloop.Command{}, &failure
					}
				}
				// The closed Command intentionally has no unbounded target list.
				// Batch ownership remains with the future acceptance executor; the
				// adapter communicates only the bounded action intent.
				return applicationloop.Command{Kind: kind, ProjectID: projectID, Action: "batch"}, nil
			}),
		)); err != nil {
			return err
		}
	}
	return nil
}

func acceptanceCommand(kind applicationloop.CommandKind, projectID int64, input acceptanceTargetRequest) applicationloop.Command {
	command := applicationloop.Command{Kind: kind, ProjectID: projectID, Action: input.TargetType}
	if input.TargetType == "plan" {
		command.PlanID = input.ID
	} else {
		command.TaskID = input.ID
	}
	return command
}

func validAcceptanceTarget(input acceptanceTargetRequest) bool {
	return input.ID > 0 && (input.TargetType == "plan" || input.TargetType == "task") &&
		!strings.ContainsAny(input.Supplement, "\x00\r")
}
