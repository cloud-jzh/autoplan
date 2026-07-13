package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/lyming99/autoplan/backend/internal/application"
	applicationoperations "github.com/lyming99/autoplan/backend/internal/application/operations"
	domainoperation "github.com/lyming99/autoplan/backend/internal/domain/operation"
	"github.com/lyming99/autoplan/backend/internal/repository"
)

const (
	OperationPath              = "/api/v1/operations/{operation_id}"
	OperationCancelPath        = "/api/v1/operations/{operation_id}/actions/cancel"
	maximumOperationCancelBody = 1024
)

var operationIdentifier = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

// OperationService is the transport-safe application surface. It deliberately
// excludes repository access and all runner-owned state transitions.
type OperationService interface {
	Get(context.Context, applicationoperations.Query) (domainoperation.Operation, error)
	RequestCancel(context.Context, applicationoperations.CancelCommand) (applicationoperations.Result, error)
}

var _ OperationService = (*applicationoperations.Service)(nil)

type operationEnvelope struct {
	Data      domainoperation.Operation `json:"data"`
	RequestID string                    `json:"request_id"`
}

type cancelOperationRequest struct {
	ExpectedVersion int64 `json:"expected_version"`
}

func RegisterOperations(router *Router, security *Security, service OperationService) error {
	if router == nil || security == nil || service == nil {
		return ErrRouterDependency
	}
	get := security.Protect(TransportREST, getOperationEndpoint(service))
	cancel := security.Protect(TransportREST, cancelOperationEndpoint(service, router.BodyLimitBytes()))
	for _, route := range []struct {
		method   string
		path     string
		endpoint Endpoint
	}{
		{http.MethodGet, OperationPath, get},
		{http.MethodHead, OperationPath, get},
		{http.MethodPost, OperationCancelPath, cancel},
	} {
		if err := router.HandlePattern(route.method, route.path, route.endpoint); err != nil {
			return err
		}
	}
	return nil
}

func getOperationEndpoint(service OperationService) Endpoint {
	return func(_ application.Boundary, writer http.ResponseWriter, request *http.Request) {
		projectID, operationID, caller, failure := operationTarget(request, "")
		if failure != nil {
			WriteError(writer, request, *failure)
			return
		}
		operation, err := service.Get(request.Context(), applicationoperations.Query{
			Caller: caller, ProjectID: projectID, OperationID: operationID,
		})
		if err != nil {
			writeOperationServiceError(writer, request, err)
			return
		}
		WriteResponse(writer, request, http.StatusOK, operationEnvelope{Data: operation, RequestID: RequestID(request.Context())})
	}
}

func cancelOperationEndpoint(service OperationService, bodyLimit int64) Endpoint {
	if bodyLimit > maximumOperationCancelBody {
		bodyLimit = maximumOperationCancelBody
	}
	return func(_ application.Boundary, writer http.ResponseWriter, request *http.Request) {
		projectID, operationID, caller, failure := operationTarget(request, "/actions/cancel")
		if failure != nil {
			WriteError(writer, request, *failure)
			return
		}
		var input cancelOperationRequest
		if failure := DecodeJSON(writer, request, &input, bodyLimit); failure != nil {
			WriteError(writer, request, *failure)
			return
		}
		if input.ExpectedVersion <= 0 {
			WriteError(writer, request, NewAPIError(CodeInvalidOperation, &ErrorDetails{Field: "expected_version"}))
			return
		}
		result, err := service.RequestCancel(request.Context(), applicationoperations.CancelCommand{
			Caller: caller, ProjectID: projectID, OperationID: operationID,
			ExpectedVersion: input.ExpectedVersion, RequestID: RequestID(request.Context()),
		})
		if err != nil {
			writeOperationServiceError(writer, request, err)
			return
		}
		WriteResponse(writer, request, http.StatusOK, operationEnvelope{Data: result.Operation, RequestID: RequestID(request.Context())})
	}
}

func operationTarget(request *http.Request, suffix string) (int64, string, applicationoperations.Caller, *APIError) {
	projectID, failure := operationProjectID(request.URL)
	if failure != nil {
		return 0, "", applicationoperations.Caller{}, failure
	}
	operationID, failure := operationIDFromPath(request.URL.Path, suffix)
	if failure != nil {
		return 0, "", applicationoperations.Caller{}, failure
	}
	callerID, failure := authenticatedCallerID(request)
	if failure != nil {
		return 0, "", applicationoperations.Caller{}, failure
	}
	return projectID, operationID, applicationoperations.Caller{ID: callerID, ProjectID: projectID}, nil
}

func operationProjectID(location *url.URL) (int64, *APIError) {
	if location == nil {
		failure := NewAPIError(CodeInvalidProjectID, &ErrorDetails{Field: "project_id"})
		return 0, &failure
	}
	values, err := url.ParseQuery(location.RawQuery)
	if err != nil || len(values) != 1 {
		failure := NewAPIError(CodeInvalidProjectID, &ErrorDetails{Field: "project_id"})
		return 0, &failure
	}
	items, exists := values["project_id"]
	if !exists || len(items) != 1 || items[0] == "" || strings.HasPrefix(items[0], "+") ||
		(len(items[0]) > 1 && items[0][0] == '0') {
		failure := NewAPIError(CodeInvalidProjectID, &ErrorDetails{Field: "project_id"})
		return 0, &failure
	}
	projectID, err := strconv.ParseInt(items[0], 10, 64)
	if err != nil || projectID <= 0 || strconv.FormatInt(projectID, 10) != items[0] {
		failure := NewAPIError(CodeInvalidProjectID, &ErrorDetails{Field: "project_id"})
		return 0, &failure
	}
	return projectID, nil
}

func operationIDFromPath(path, suffix string) (string, *APIError) {
	prefix := "/api/v1/operations/"
	if !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, suffix) {
		failure := NewAPIError(CodeInvalidOperation, &ErrorDetails{Field: "operation_id"})
		return "", &failure
	}
	value := strings.TrimSuffix(strings.TrimPrefix(path, prefix), suffix)
	if !operationIdentifier.MatchString(value) {
		failure := NewAPIError(CodeInvalidOperation, &ErrorDetails{Field: "operation_id"})
		return "", &failure
	}
	return value, nil
}

func writeOperationServiceError(writer http.ResponseWriter, request *http.Request, err error) {
	code := CodeInternal
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		code = CodeRequestTimeout
	case errors.Is(err, applicationoperations.ErrUnauthorized):
		code = CodeUnauthorized
	case errors.Is(err, applicationoperations.ErrNotFound), errors.Is(err, repository.ErrNotFound):
		code = CodeOperationNotFound
	case errors.Is(err, applicationoperations.ErrInvalidCommand):
		code = CodeInvalidOperation
	case errors.Is(err, applicationoperations.ErrVersionConflict), errors.Is(err, repository.ErrVersionConflict):
		code = CodeOperationVersion
	case errors.Is(err, applicationoperations.ErrStateConflict):
		code = CodeOperationState
	case errors.Is(err, applicationoperations.ErrIdempotencyConflict):
		code = CodeOperationState
	case errors.Is(err, applicationoperations.ErrUnavailable), errors.Is(err, repository.ErrNotConfigured),
		errors.Is(err, repository.ErrClosed), errors.Is(err, repository.ErrWriterUnauthorized),
		errors.Is(err, repository.ErrUnsafePath), errors.Is(err, repository.ErrInvalidStore), errors.Is(err, repository.ErrSourceChanged):
		code = CodeServiceUnavailable
	case errors.Is(err, repository.ErrTransaction), errors.Is(err, repository.ErrCommit), errors.Is(err, repository.ErrRollback):
		code = CodeRepositoryBusy
	case errors.Is(err, repository.ErrSchemaDrift):
		code = CodeRepositorySchemaDrift
	}
	WriteError(writer, request, NewAPIError(code, nil))
}
