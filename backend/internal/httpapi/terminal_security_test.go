package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lyming99/autoplan/backend/internal/platform/session"
)

func terminalSecurityRequest(router http.Handler, credential, target, origin string, headers map[string]string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, "http://"+testAuthority+target, nil)
	request.Header.Set("Origin", origin)
	request.Header.Set(session.HeaderName, credential)
	request.Header.Set(RequestIDHeader, "req_terminal_security")
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func TestTerminalSecurityRejectsOriginCredentialAndForwardingBypassBeforeList(t *testing.T) {
	fixture := newTerminalHTTPFixture()
	router, credential := newTerminalRouter(t, fixture, true)
	wrongOrigin := terminalSecurityRequest(router, credential, "/api/v1/projects/7/terminals", "https://attacker.invalid", nil)
	assertContractError(t, wrongOrigin, http.StatusForbidden, string(CodeOriginForbidden), false)
	queryCredential := terminalSecurityRequest(router, credential, "/api/v1/projects/7/terminals?token=forbidden", testOrigin, nil)
	assertContractError(t, queryCredential, http.StatusUnauthorized, string(CodeUnauthorized), false)
	forwarded := terminalSecurityRequest(router, credential, "/api/v1/projects/7/terminals", testOrigin, map[string]string{"X-Forwarded-For": "127.0.0.1"})
	assertContractError(t, forwarded, http.StatusForbidden, string(CodeOriginForbidden), false)
	if fixture.listCalls != 0 {
		t.Fatalf("rejected security request reached list %d times", fixture.listCalls)
	}
	for _, response := range []*httptest.ResponseRecorder{wrongOrigin, queryCredential, forwarded} {
		if strings.Contains(response.Body.String(), credential) {
			t.Fatal("security rejection leaked session material")
		}
	}
}
