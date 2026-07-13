package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/lyming99/autoplan/backend/internal/application"
	applicationfiles "github.com/lyming99/autoplan/backend/internal/application/files"
	domainfiles "github.com/lyming99/autoplan/backend/internal/domain/files"
	"github.com/lyming99/autoplan/backend/internal/repository"
)

const FilePolicyPath = "/api/v1/file-access-policy"

type FilePolicyService interface {
	Get(context.Context) (domainfiles.Policy, error)
	Save(context.Context, int64, domainfiles.Policy) (domainfiles.Policy, error)
}

var _ FilePolicyService = (*applicationfiles.Service)(nil)

type filePolicyRequest struct {
	Version           *int64            `json:"version"`
	Scope             domainfiles.Scope `json:"scope"`
	AllowCrossProject *bool             `json:"allow_cross_project"`
	AllowedRoots      *[]string         `json:"allowed_roots"`
}

type filePolicyResponse struct {
	Scope             domainfiles.Scope `json:"scope"`
	AllowCrossProject bool              `json:"allow_cross_project"`
	AllowedRoots      []string          `json:"allowed_roots"`
	Version           int64             `json:"version"`
	HighRisk          bool              `json:"high_risk"`
}

func RegisterFilePolicy(router *Router, security *Security, service FilePolicyService) error {
	if router == nil || security == nil || service == nil {
		return ErrRouterDependency
	}
	get := security.Protect(TransportREST, getFilePolicyEndpoint(service))
	patch := security.Protect(TransportREST, patchFilePolicyEndpoint(service, router.BodyLimitBytes()))
	for _, item := range []struct {
		method   string
		endpoint Endpoint
	}{{http.MethodGet, get}, {http.MethodHead, get}, {http.MethodPatch, patch}} {
		if err := router.Handle(item.method, FilePolicyPath, item.endpoint); err != nil {
			return err
		}
	}
	return nil
}

func getFilePolicyEndpoint(service FilePolicyService) Endpoint {
	return func(_ application.Boundary, writer http.ResponseWriter, request *http.Request) {
		policy, err := service.Get(request.Context())
		if err != nil {
			writeFilePolicyError(writer, request, err)
			return
		}
		WriteResponse(writer, request, http.StatusOK, responseEnvelope{
			Data: filePolicyContract(policy), RequestID: RequestID(request.Context()),
		})
	}
}

func patchFilePolicyEndpoint(service FilePolicyService, bodyLimit int64) Endpoint {
	return func(_ application.Boundary, writer http.ResponseWriter, request *http.Request) {
		if _, failure := IdempotencyKey(request); failure != nil {
			WriteError(writer, request, *failure)
			return
		}
		var input filePolicyRequest
		if failure := DecodeJSON(writer, request, &input, bodyLimit); failure != nil {
			WriteError(writer, request, *failure)
			return
		}
		if input.Version == nil || *input.Version <= 0 {
			WriteError(writer, request, NewAPIError(CodeVersionRequired, &ErrorDetails{Field: "version"}))
			return
		}
		if input.AllowCrossProject == nil || input.AllowedRoots == nil {
			WriteError(writer, request, NewAPIError(CodeInvalidConfig, nil))
			return
		}
		policy := domainfiles.Policy{
			Scope: input.Scope, AllowCrossProject: *input.AllowCrossProject,
			AllowedRoots: append([]string(nil), (*input.AllowedRoots)...),
		}
		if policy.Validate() != nil {
			WriteError(writer, request, NewAPIError(CodeInvalidConfig, nil))
			return
		}
		result, err := service.Save(request.Context(), *input.Version, policy)
		if err != nil {
			writeFilePolicyError(writer, request, err)
			return
		}
		WriteResponse(writer, request, http.StatusOK, responseEnvelope{
			Data: filePolicyContract(result), RequestID: RequestID(request.Context()),
		})
	}
}

func filePolicyContract(policy domainfiles.Policy) filePolicyResponse {
	roots := append([]string(nil), policy.AllowedRoots...)
	if roots == nil {
		roots = []string{}
	}
	return filePolicyResponse{
		Scope: policy.Scope, AllowCrossProject: policy.AllowCrossProject,
		AllowedRoots: roots, Version: policy.Version, HighRisk: policy.Scope == domainfiles.ScopeAll,
	}
}

func writeFilePolicyError(writer http.ResponseWriter, request *http.Request, err error) {
	code := CodeInternal
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		code = CodeRequestTimeout
	case errors.Is(err, domainfiles.ErrVersionRequired), errors.Is(err, repository.ErrVersionRequired):
		code = CodeVersionRequired
	case errors.Is(err, domainfiles.ErrVersionConflict), errors.Is(err, repository.ErrVersionConflict):
		code = CodeVersionConflict
	case errors.Is(err, domainfiles.ErrInvalidPolicy), errors.Is(err, domainfiles.ErrInvalidPath),
		errors.Is(err, domainfiles.ErrResolutionFailed):
		code = CodeInvalidConfig
	case errors.Is(err, repository.ErrNotConfigured), errors.Is(err, repository.ErrClosed),
		errors.Is(err, repository.ErrWriterUnauthorized):
		code = CodeRepositoryUnavailable
	}
	WriteError(writer, request, NewAPIError(code, nil))
}
