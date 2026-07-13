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
	"github.com/lyming99/autoplan/backend/internal/domain"
	"github.com/lyming99/autoplan/backend/internal/domain/contracts"
	"github.com/lyming99/autoplan/backend/internal/platform/logging"
)

type testApplication struct {
	calls int
}

func (application *testApplication) Capabilities(context.Context) []domain.Service {
	application.calls++
	return []domain.Service{domain.ServiceProjects}
}

type fixedClock struct {
	value time.Time
}

func (clock fixedClock) Now() time.Time { return clock.value }

type fixedRequestIDs struct{}

func (fixedRequestIDs) NewRequestID() (string, error) { return "req_generated_fixture", nil }

type recordingLogger struct {
	events []logging.Event
}

func (logger *recordingLogger) Log(event logging.Event) error {
	logger.events = append(logger.events, event)
	return nil
}

type testReadiness struct {
	ready        bool
	shuttingDown bool
}

func (readiness *testReadiness) Ready() bool        { return readiness.ready }
func (readiness *testReadiness) ShuttingDown() bool { return readiness.shuttingDown }

func TestHTTPFoundationStableResponses(t *testing.T) {
	application := &testApplication{}
	logger := &recordingLogger{}
	clock := fixedClock{value: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}
	router, err := NewRouter(RouterOptions{
		Application: application, Logger: logger, Clock: clock,
		RequestIDs: fixedRequestIDs{}, BodyLimitBytes: 64,
	})
	if err != nil {
		t.Fatal(err)
	}
	readiness := &testReadiness{ready: true}
	if err := RegisterProbes(router, readiness); err != nil {
		t.Fatal(err)
	}
	if err := router.Handle(http.MethodGet, "/panic", panicEndpoint); err != nil {
		t.Fatal(err)
	}
	if err := router.Handle(http.MethodPost, "/decode", decodeEndpoint); err != nil {
		t.Fatal(err)
	}

	assertProbe(t, router, "/healthz", "ok")
	assertProbe(t, router, "/readyz", "ready")

	tests := []struct {
		name      string
		method    string
		path      string
		body      string
		content   string
		status    int
		code      string
		retryable bool
	}{
		{"unknown route", http.MethodGet, "/missing", "", "", http.StatusNotFound, "not_found", false},
		{"wrong method", http.MethodPost, "/healthz", "", "", http.StatusMethodNotAllowed, "method_not_allowed", false},
		{"oversized body", http.MethodPost, "/decode", strings.Repeat("x", 65), "application/json", http.StatusRequestEntityTooLarge, "body_too_large", false},
		{"unsupported media", http.MethodPost, "/decode", `{}`, "text/plain", http.StatusUnsupportedMediaType, "unsupported_media_type", false},
		{"invalid json", http.MethodPost, "/decode", `{`, "application/json", http.StatusBadRequest, "invalid_json", false},
		{"unknown json field", http.MethodPost, "/decode", `{"name":"fixture","extra":true}`, "application/json", http.StatusBadRequest, "invalid_json", false},
		{"panic", http.MethodGet, "/panic", "", "", http.StatusInternalServerError, "internal_error", false},
	}
	for _, item := range tests {
		item := item
		t.Run(item.name, func(t *testing.T) {
			request := httptest.NewRequest(item.method, item.path, strings.NewReader(item.body))
			if item.content != "" {
				request.Header.Set("Content-Type", item.content)
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			assertContractError(t, response, item.status, item.code, item.retryable)
			if strings.Contains(response.Body.String(), "panic-fixture-material") {
				t.Fatal("panic material crossed the HTTP boundary")
			}
		})
	}

	readiness.ready = false
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	assertContractError(t, response, http.StatusServiceUnavailable, "service_unavailable", true)
	readiness.shuttingDown = true
	response = httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	assertContractError(t, response, http.StatusServiceUnavailable, "shutting_down", true)

	if application.calls != 0 {
		t.Fatal("probe and foundation errors must not invoke application capabilities")
	}
	if len(logger.events) == 0 {
		t.Fatal("HTTP access events were not recorded")
	}
	for _, event := range logger.events {
		encoded, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(encoded, []byte("panic-fixture-material")) {
			t.Fatal("log event contains panic material")
		}
	}
}

func panicEndpoint(_ application.Boundary, _ http.ResponseWriter, _ *http.Request) {
	panic("panic-fixture-material")
}

type decodeRequest struct {
	Name string `json:"name"`
}

func decodeEndpoint(_ application.Boundary, writer http.ResponseWriter, request *http.Request) {
	var payload decodeRequest
	if failure := DecodeJSON(writer, request, &payload, 64); failure != nil {
		WriteError(writer, request, *failure)
		return
	}
	WriteResponse(writer, request, http.StatusOK, map[string]bool{"ok": true})
}

func assertProbe(t *testing.T, router *Router, path, status string) {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, path, nil)
	request.Header.Set(RequestIDHeader, "req_caller_fixture")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get(RequestIDHeader) != "req_caller_fixture" {
		t.Fatalf("probe response mismatch: status=%d request_id=%s", response.Code, response.Header().Get(RequestIDHeader))
	}
	var body map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["status"] != status || body["request_id"] != "req_caller_fixture" || len(body) != 2 {
		t.Fatalf("probe contract drift: %#v", body)
	}
}

func assertContractError(t *testing.T, response *httptest.ResponseRecorder, status int, code string, retryable bool) {
	t.Helper()
	if response.Code != status {
		t.Fatalf("status=%d want=%d body=%s", response.Code, status, response.Body.String())
	}
	if response.Header().Get("Content-Type") != "application/json; charset=utf-8" ||
		response.Header().Get("Cache-Control") != "no-store" ||
		response.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("secure JSON headers drifted")
	}
	requestID := response.Header().Get(RequestIDHeader)
	var failure contracts.Error
	if err := contracts.DecodeStrict(response.Body.Bytes(), &failure); err != nil {
		t.Fatalf("error response violates frozen contract: %v", err)
	}
	if failure.Code != code || failure.RequestID != requestID || failure.Retryable != retryable {
		t.Fatalf("error contract drift: %#v", failure)
	}
}
