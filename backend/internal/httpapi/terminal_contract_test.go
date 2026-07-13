package httpapi

import (
	"net/http"
	"strings"
	"testing"
)

func TestTerminalRESTControlActionsKeepFrozenScopeAndSafeDTOs(t *testing.T) {
	fixture := newTerminalHTTPFixture()
	router, credential := newTerminalRouter(t, fixture, true)
	requests := []struct {
		path string
		body string
	}{
		{"/api/v1/terminals/term_fixture7/actions/resize?project_id=7", `{"cols":120,"rows":40}`},
		{"/api/v1/terminals/term_fixture7/actions/rename?project_id=7", `{"title":"Renamed"}`},
		{"/api/v1/terminals/term_fixture7/actions/clear?project_id=7", `{}`},
		{"/api/v1/terminals/term_fixture7/actions/kill?project_id=7", `{}`},
		{"/api/v1/terminals/term_fixture7/actions/close?project_id=7", `{}`},
	}
	for _, item := range requests {
		response := serveTerminalRequest(router, credential, http.MethodPost, item.path, item.body, "")
		if response.Code != http.StatusOK || strings.Contains(response.Body.String(), "TERM=") {
			t.Fatalf("control action %s status=%d body=%s", item.path, response.Code, response.Body.String())
		}
	}
	replay := serveTerminalRequest(router, credential, http.MethodGet, "/api/v1/terminals/term_fixture7/replay?project_id=7&last_seq=0", "", "")
	if replay.Code != http.StatusOK || !strings.Contains(replay.Body.String(), `"replay_complete":true`) {
		t.Fatalf("replay response status=%d body=%s", replay.Code, replay.Body.String())
	}
}

func TestTerminalRESTMalformedIDsAndPayloadsNeverReachService(t *testing.T) {
	fixture := newTerminalHTTPFixture()
	router, credential := newTerminalRouter(t, fixture, true)
	response := serveTerminalRequest(router, credential, http.MethodPost, "/api/v1/terminals/term_../actions/write?project_id=7", `{"data":"x"}`, "")
	assertContractError(t, response, http.StatusBadRequest, string(CodeTerminalInvalidSession), false)
	if fixture.writeCalls != 0 || fixture.getCalls != 0 {
		t.Fatal("invalid terminal ID reached application service")
	}
	response = serveTerminalRequest(router, credential, http.MethodPost, "/api/v1/terminals/term_fixture7/actions/write?project_id=7", `{"data":"","extra":true}`, "")
	assertContractError(t, response, http.StatusBadRequest, string(CodeInvalidJSON), false)
	if fixture.writeCalls != 0 {
		t.Fatal("invalid write payload reached application service")
	}
}
