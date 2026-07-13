package httpapi

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	domainterminal "github.com/lyming99/autoplan/backend/internal/domain/terminal"
	"github.com/lyming99/autoplan/backend/internal/platform/session"
)

func TestTerminalWebSocketCommandKeepsProjectCursorAndCallerScoped(t *testing.T) {
	request := httptest.NewRequest("GET", "http://"+testAuthority+"/api/v1/terminals/term_fixture7/ws?project_id=7&last_seq=42", nil)
	request.Header.Set(session.HeaderName, "fixture-credential")
	request = request.WithContext(context.WithValue(request.Context(), securityContextKey{}, SecurityContext{Transport: TransportWebSocket, Origin: testOrigin}))
	command, cursor, failure := terminalWebSocketCommand(request)
	if failure != nil || command.ProjectID != 7 || command.SessionID != "term_fixture7" || command.Caller.ID == "" || cursor != 42 {
		t.Fatalf("websocket command=%#v cursor=%d failure=%#v", command, cursor, failure)
	}
	invalid := httptest.NewRequest("GET", "http://"+testAuthority+"/api/v1/terminals/term_fixture7/ws?project_id=7&last_seq=01", nil)
	invalid.Header.Set(session.HeaderName, "fixture-credential")
	invalid = invalid.WithContext(context.WithValue(invalid.Context(), securityContextKey{}, SecurityContext{Transport: TransportWebSocket, Origin: testOrigin}))
	if _, _, failure := terminalWebSocketCommand(invalid); failure == nil || failure.Code() != CodeTerminalInvalidPayload {
		t.Fatalf("non-canonical cursor failure=%#v", failure)
	}
}

func TestTerminalWebSocketFramesContainOnlyFrozenTerminalEnvelopes(t *testing.T) {
	output, err := terminalWSEventFrame(domainterminal.Event{Type: domainterminal.EventOutput, Output: &domainterminal.Output{Seq: 3, Data: "safe"}})
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(output, &decoded); err != nil || decoded["type"] != "output" || decoded["seq"] != float64(3) || decoded["data"] != "safe" || len(decoded) != 3 {
		t.Fatalf("output frame=%s decoded=%#v err=%v", output, decoded, err)
	}
	if _, err := terminalWSEventFrame(domainterminal.Event{Type: domainterminal.EventOutput}); err == nil {
		t.Fatal("output frame without output was accepted")
	}
}
