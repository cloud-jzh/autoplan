package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lyming99/autoplan/backend/internal/application"
	applicationoperations "github.com/lyming99/autoplan/backend/internal/application/operations"
	"github.com/lyming99/autoplan/backend/internal/config"
	"github.com/lyming99/autoplan/backend/internal/domain"
	domainoperation "github.com/lyming99/autoplan/backend/internal/domain/operation"
	"github.com/lyming99/autoplan/backend/internal/platform/logging"
	"github.com/lyming99/autoplan/backend/internal/platform/session"
)

const (
	operationTestOrigin    = "http://127.0.0.1:43123"
	operationTestAuthority = "127.0.0.1:43123"
)

func TestOperationRoutesUseProjectScopedServiceAndStableDTO(t *testing.T) {
	service := &operationHTTPService{operation: operationHTTPFixture(7, "operation-7", 3)}
	router, credential := newOperationHTTPRouter(t, service)

	get := serveOperationRequest(router, credential, http.MethodGet, "/api/v1/operations/operation-7?project_id=7", "")
	if get.Code != http.StatusOK || service.getCalls != 1 || service.lastQuery.ProjectID != 7 ||
		service.lastQuery.OperationID != "operation-7" || service.lastQuery.Caller.ID == "" ||
		strings.Contains(service.lastQuery.Caller.ID, credential) {
		t.Fatalf("get operation routing drifted: status=%d query=%#v", get.Code, service.lastQuery)
	}
	var response operationEnvelope
	if err := json.Unmarshal(get.Body.Bytes(), &response); err != nil || response.Data.OperationID != "operation-7" ||
		response.Data.Output != nil || response.RequestID != "req_operation_fixture" {
		t.Fatalf("operation response=%s err=%v", get.Body.String(), err)
	}

	cancel := serveOperationRequest(router, credential, http.MethodPost,
		"/api/v1/operations/operation-7/actions/cancel?project_id=7", `{"expected_version":3}`)
	if cancel.Code != http.StatusOK || service.cancelCalls != 1 || service.lastCancel.ProjectID != 7 ||
		service.lastCancel.ExpectedVersion != 3 || service.lastCancel.RequestID != "req_operation_fixture" {
		t.Fatalf("cancel operation routing drifted: status=%d command=%#v", cancel.Code, service.lastCancel)
	}
}

func TestOperationRoutesRejectMissingScopeAndHideCrossProject(t *testing.T) {
	service := &operationHTTPService{operation: operationHTTPFixture(7, "operation-7", 1)}
	router, credential := newOperationHTTPRouter(t, service)

	missingScope := serveOperationRequest(router, credential, http.MethodGet, "/api/v1/operations/operation-7", "")
	assertContractError(t, missingScope, http.StatusBadRequest, string(CodeInvalidProjectID), false)
	if service.getCalls != 0 {
		t.Fatal("operation lookup ran without project scope")
	}

	service.getError = applicationoperations.ErrNotFound
	crossProject := serveOperationRequest(router, credential, http.MethodGet, "/api/v1/operations/operation-7?project_id=8", "")
	assertContractError(t, crossProject, http.StatusNotFound, string(CodeOperationNotFound), false)
	if strings.Contains(crossProject.Body.String(), "operation-7") {
		t.Fatal("cross-project operation identifier leaked in error response")
	}

	invalidVersion := serveOperationRequest(router, credential, http.MethodPost,
		"/api/v1/operations/operation-7/actions/cancel?project_id=7", `{"expected_version":0}`)
	assertContractError(t, invalidVersion, http.StatusBadRequest, string(CodeInvalidOperation), false)
	if service.cancelCalls != 0 {
		t.Fatal("invalid cancellation reached operation service")
	}
}

type operationHTTPService struct {
	operation   domainoperation.Operation
	getError    error
	cancelError error
	getCalls    int
	cancelCalls int
	lastQuery   applicationoperations.Query
	lastCancel  applicationoperations.CancelCommand
}

func (service *operationHTTPService) Get(_ context.Context, query applicationoperations.Query) (domainoperation.Operation, error) {
	service.getCalls++
	service.lastQuery = query
	if service.getError != nil {
		return domainoperation.Operation{}, service.getError
	}
	if query.ProjectID != service.operation.ProjectID || query.OperationID != service.operation.OperationID {
		return domainoperation.Operation{}, applicationoperations.ErrNotFound
	}
	return service.operation, nil
}

func (service *operationHTTPService) RequestCancel(_ context.Context, command applicationoperations.CancelCommand) (applicationoperations.Result, error) {
	service.cancelCalls++
	service.lastCancel = command
	if service.cancelError != nil {
		return applicationoperations.Result{}, service.cancelError
	}
	if command.ProjectID != service.operation.ProjectID || command.OperationID != service.operation.OperationID {
		return applicationoperations.Result{}, applicationoperations.ErrNotFound
	}
	return applicationoperations.Result{Operation: service.operation}, nil
}

func operationHTTPFixture(projectID int64, operationID string, version int64) domainoperation.Operation {
	key := "operation-intent"
	return domainoperation.Operation{
		OperationID: operationID, ProjectID: projectID, Type: "loop.run", Status: domainoperation.StatusQueued,
		RequestID: "request-operation", IdempotencyKey: &key, RequestDigest: strings.Repeat("a", 64), Version: version,
		CreatedAt: "2026-07-12T08:00:00Z", UpdatedAt: "2026-07-12T08:00:00Z",
	}
}

func newOperationHTTPRouter(t *testing.T, service OperationService) (*Router, string) {
	t.Helper()
	clock := operationHTTPClock{value: time.Date(2026, 7, 12, 8, 0, 0, 0, time.UTC)}
	manager, err := session.New(bytes.NewReader(bytes.Repeat([]byte{0x4a}, 32)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.Close)
	origins, err := config.NewOriginSet([]string{operationTestOrigin})
	if err != nil {
		t.Fatal(err)
	}
	logger := operationHTTPLogger{}
	security, err := NewSecurity(SecurityOptions{
		Sessions: manager, Origins: origins, ExpectedHost: config.DefaultListenHost, ExpectedPort: 43123,
		Logger: logger, Clock: clock,
	})
	if err != nil {
		t.Fatal(err)
	}
	router, err := NewRouter(RouterOptions{
		Application: operationHTTPBoundary{}, Logger: logger, Clock: clock,
		RequestIDs: operationHTTPRequestIDs{}, BodyLimitBytes: 1024,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := RegisterOperations(router, security, service); err != nil {
		t.Fatal(err)
	}
	return router, string(manager.CredentialCopy())
}

func serveOperationRequest(router http.Handler, credential, method, target, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, "http://"+operationTestAuthority+target, strings.NewReader(body))
	request.Header.Set("Origin", operationTestOrigin)
	request.Header.Set(session.HeaderName, credential)
	request.Header.Set(RequestIDHeader, "req_operation_fixture")
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

type operationHTTPBoundary struct{}

func (operationHTTPBoundary) Capabilities(context.Context) []domain.Service { return nil }

type operationHTTPClock struct{ value time.Time }

func (clock operationHTTPClock) Now() time.Time { return clock.value }

type operationHTTPLogger struct{}

func (operationHTTPLogger) Log(logging.Event) error { return nil }

type operationHTTPRequestIDs struct{}

func (operationHTTPRequestIDs) NewRequestID() (string, error) { return "req_operation_fixture", nil }

var _ application.Boundary = operationHTTPBoundary{}
