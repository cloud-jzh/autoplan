package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lyming99/autoplan/backend/internal/config"
	"github.com/lyming99/autoplan/backend/internal/platform/logging"
	"github.com/lyming99/autoplan/backend/internal/platform/session"
)

const (
	testAuthority = "127.0.0.1:43123"
	testOrigin    = "http://127.0.0.1:43124"
)

func TestSharedTransportSecurityRejectsBeforeApplication(t *testing.T) {
	application := &testApplication{}
	clock := fixedClock{value: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}
	var logOutput bytes.Buffer
	logger, err := logging.NewJSONLogger(&logOutput, clock)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := session.New(bytes.NewReader(bytes.Repeat([]byte{0x2a}, 32)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.Close)
	origins, err := config.NewOriginSet([]string{testOrigin})
	if err != nil {
		t.Fatal(err)
	}
	security, err := NewSecurity(SecurityOptions{
		Sessions: manager, Origins: origins, ExpectedHost: config.DefaultListenHost,
		ExpectedPort: 43123, Logger: logger, Clock: clock,
	})
	if err != nil {
		t.Fatal(err)
	}
	router, err := NewRouter(RouterOptions{
		Application: application, Logger: logger, Clock: clock,
		RequestIDs: fixedRequestIDs{}, BodyLimitBytes: 1024,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := RegisterTransportSkeletons(router, security); err != nil {
		t.Fatal(err)
	}
	credentialBytes := manager.CredentialCopy()
	if len(credentialBytes) == 0 {
		t.Fatal("session manager did not create an ephemeral credential")
	}
	credential := string(credentialBytes)
	for index := range credentialBytes {
		credentialBytes[index] = 0
	}

	for _, transport := range []string{"rest", "sse", "websocket"} {
		t.Run(transport+" missing session", func(t *testing.T) {
			request := transportRequest(t, transport)
			request.Header.Set("Origin", testOrigin)
			response := serveSecurityRequest(router, request)
			assertContractError(t, response, http.StatusUnauthorized, "unauthorized", false)
		})
	}
	if application.calls != 0 {
		t.Fatal("missing sessions reached the application boundary")
	}

	request := transportRequest(t, "rest")
	request.Header.Set(session.HeaderName, credential)
	response := serveSecurityRequest(router, request)
	assertContractError(t, response, http.StatusForbidden, "origin_forbidden", false)
	if response.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("missing Origin must not receive CORS authority")
	}

	request = transportRequest(t, "rest")
	request.Header.Set("Origin", "http://127.0.0.1:43125")
	request.Header.Set(session.HeaderName, credential)
	response = serveSecurityRequest(router, request)
	assertContractError(t, response, http.StatusForbidden, "origin_forbidden", false)

	request = transportRequest(t, "rest")
	request.Header.Set("Origin", testOrigin)
	request.Header.Set(session.HeaderName, "not-a-session-credential")
	response = serveSecurityRequest(router, request)
	assertContractError(t, response, http.StatusUnauthorized, "unauthorized", false)
	if response.Header().Get("Access-Control-Allow-Origin") != testOrigin {
		t.Fatal("an exact allowed Origin must be echoed on credential rejection")
	}

	for _, transport := range []string{"rest", "sse", "websocket"} {
		t.Run(transport+" authorized skeleton", func(t *testing.T) {
			request := transportRequest(t, transport)
			request.Header.Set("Origin", testOrigin)
			request.Header.Set(session.HeaderName, credential)
			response := serveSecurityRequest(router, request)
			assertContractError(t, response, http.StatusNotImplemented, "not_implemented", false)
			if response.Header().Get(TransportVersionHeader) != TransportVersion {
				t.Fatal("transport version header drifted")
			}
		})
	}
	if application.calls != 3 {
		t.Fatalf("authorized skeleton calls=%d want=3", application.calls)
	}

	request = transportRequest(t, "rest")
	request.Header.Set("Origin", testOrigin)
	request.AddCookie(&http.Cookie{Name: session.CookieName, Value: credential})
	response = serveSecurityRequest(router, request)
	assertContractError(t, response, http.StatusNotImplemented, "not_implemented", false)
	if application.calls != 4 {
		t.Fatal("host-only session cookie did not share the REST security policy")
	}

	request = transportRequest(t, "rest")
	request.Header.Set("Origin", testOrigin)
	request.Header.Set(session.HeaderName, credential)
	request.AddCookie(&http.Cookie{Name: session.CookieName, Value: credential})
	response = serveSecurityRequest(router, request)
	assertContractError(t, response, http.StatusUnauthorized, "unauthorized", false)
	if application.calls != 4 {
		t.Fatal("ambiguous header and cookie session reached the application boundary")
	}

	request = transportRequest(t, "rest")
	request.Header.Set("Origin", testOrigin)
	request.Header.Set(session.HeaderName, credential)
	request.Header.Set(IdempotencyKeyHeader, "contains whitespace")
	response = serveSecurityRequest(router, request)
	assertContractError(t, response, http.StatusBadRequest, "invalid_idempotency_key", false)
	if application.calls != 4 {
		t.Fatal("invalid idempotency key reached the application boundary")
	}

	request = transportRequest(t, "rest")
	request.URL.RawQuery = "auth=synthetic"
	request.Header.Set("Origin", testOrigin)
	request.Header.Set(session.HeaderName, credential)
	response = serveSecurityRequest(router, request)
	assertContractError(t, response, http.StatusUnauthorized, "unauthorized", false)

	request = transportRequest(t, "rest")
	request.Header.Set("Origin", testOrigin)
	request.Header.Set(session.HeaderName, credential)
	request.Header.Set("Authorization", "Bearer synthetic")
	response = serveSecurityRequest(router, request)
	assertContractError(t, response, http.StatusUnauthorized, "unauthorized", false)

	logs := logOutput.String()
	if strings.Contains(logs, credential) || strings.Contains(logs, "not-a-session-credential") ||
		strings.Contains(logs, "Bearer synthetic") {
		t.Fatal("security log contains credential material")
	}
	for _, line := range strings.Split(strings.TrimSpace(logs), "\n") {
		if line != "" && !jsonObject(line) {
			t.Fatalf("security log is not single-line JSON: %s", line)
		}
	}
}

func transportRequest(t *testing.T, transport string) *http.Request {
	t.Helper()
	method := http.MethodGet
	path := ""
	switch transport {
	case "rest":
		method = http.MethodPost
		path = RESTSkeletonPath
	case "sse":
		path = SSESkeletonPath
	case "websocket":
		path = WebSocketSkeletonPath
	default:
		t.Fatalf("unknown test transport %s", transport)
	}
	request := httptest.NewRequest(method, path, nil)
	request.Host = testAuthority
	request.Header.Set(RequestIDHeader, "req_security_fixture")
	if transport == "sse" {
		request.Header.Set("Accept", "text/event-stream")
	}
	if transport == "websocket" {
		request.Header.Set("Connection", "Upgrade")
		request.Header.Set("Upgrade", "websocket")
		request.Header.Set("Sec-WebSocket-Version", "13")
		request.Header.Set("Sec-WebSocket-Key", "AAECAwQFBgcICQoLDA0ODw==")
	}
	return request
}

func serveSecurityRequest(router *Router, request *http.Request) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func jsonObject(value string) bool {
	decoder := json.NewDecoder(strings.NewReader(value))
	var object map[string]any
	return decoder.Decode(&object) == nil && object != nil
}
