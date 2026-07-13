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

	applicationterminal "github.com/lyming99/autoplan/backend/internal/application/terminal"
	"github.com/lyming99/autoplan/backend/internal/config"
	domainterminal "github.com/lyming99/autoplan/backend/internal/domain/terminal"
	"github.com/lyming99/autoplan/backend/internal/platform/session"
)

type terminalHTTPFixture struct {
	createCalls int
	listCalls   int
	getCalls    int
	writeCalls  int
	lastCreate  applicationterminal.CreateCommand
	lastSession applicationterminal.SessionCommand
	session     domainterminal.Session
}

func (fixture *terminalHTTPFixture) Create(_ context.Context, command applicationterminal.CreateCommand) (domainterminal.Session, error) {
	fixture.createCalls++
	fixture.lastCreate = command
	return fixture.session.Copy(), nil
}

func (fixture *terminalHTTPFixture) List(_ context.Context, _ domainterminal.Caller, projectID int64) ([]domainterminal.Session, error) {
	fixture.listCalls++
	if projectID != fixture.session.ProjectID {
		return nil, domainterminal.ErrForbidden
	}
	return []domainterminal.Session{fixture.session.Copy()}, nil
}

func (fixture *terminalHTTPFixture) Get(_ context.Context, command applicationterminal.SessionCommand) (domainterminal.Session, error) {
	fixture.getCalls++
	fixture.lastSession = command
	if command.ProjectID != fixture.session.ProjectID || command.SessionID != fixture.session.ID {
		return domainterminal.Session{}, domainterminal.ErrNotFound
	}
	return fixture.session.Copy(), nil
}

func (fixture *terminalHTTPFixture) Write(_ context.Context, command applicationterminal.WriteCommand) (int, error) {
	fixture.writeCalls++
	fixture.lastSession = command.SessionCommand
	return len(command.Data), nil
}

func (fixture *terminalHTTPFixture) Resize(_ context.Context, command applicationterminal.ResizeCommand) error {
	fixture.lastSession = command.SessionCommand
	return nil
}

func (fixture *terminalHTTPFixture) Kill(_ context.Context, command applicationterminal.SessionCommand) (domainterminal.Session, error) {
	fixture.lastSession = command
	return fixture.session.Copy(), nil
}

func (fixture *terminalHTTPFixture) Close(_ context.Context, command applicationterminal.SessionCommand) (domainterminal.Session, error) {
	fixture.lastSession = command
	return fixture.session.Copy(), nil
}

func (fixture *terminalHTTPFixture) Rename(_ context.Context, command applicationterminal.RenameCommand) (domainterminal.Session, error) {
	fixture.lastSession = command.SessionCommand
	result := fixture.session.Copy()
	result.Title = command.Title
	return result, nil
}

func (fixture *terminalHTTPFixture) Clear(_ context.Context, command applicationterminal.SessionCommand) error {
	fixture.lastSession = command
	return nil
}

func (fixture *terminalHTTPFixture) Replay(_ context.Context, command applicationterminal.ReplayCommand) (domainterminal.Replay, error) {
	fixture.lastSession = command.SessionCommand
	return domainterminal.Replay{Session: fixture.session.Copy(), LastSeq: command.LastSeq, Entries: []domainterminal.Output{}}, nil
}

var _ TerminalService = (*terminalHTTPFixture)(nil)

func TestTerminalRESTGateAndIdempotentCreate(t *testing.T) {
	fixture := newTerminalHTTPFixture()
	router, credential := newTerminalRouter(t, fixture, false)
	response := serveTerminalRequest(router, credential, http.MethodPost, "/api/v1/projects/7/terminals", `{"cwd":"/safe"}`, "terminal-create")
	assertContractError(t, response, http.StatusServiceUnavailable, string(CodeTerminalFeatureDisabled), false)
	if fixture.createCalls != 0 {
		t.Fatal("disabled terminal route reached application service")
	}

	router, credential = newTerminalRouter(t, fixture, true)
	requestBody := `{"cwd":"/safe","profile_id":"default","title":"Terminal","env":{"TERM":"xterm-256color"}}`
	first := serveTerminalRequest(router, credential, http.MethodPost, "/api/v1/projects/7/terminals", requestBody, "terminal-create")
	second := serveTerminalRequest(router, credential, http.MethodPost, "/api/v1/projects/7/terminals", requestBody, "terminal-create")
	if first.Code != http.StatusCreated || second.Code != http.StatusCreated || fixture.createCalls != 1 {
		t.Fatalf("idempotent create statuses=%d/%d calls=%d", first.Code, second.Code, fixture.createCalls)
	}
	if fixture.lastCreate.Caller.ID == "" || fixture.lastCreate.ProjectID != 7 || fixture.lastCreate.CWD != "/safe" || fixture.lastCreate.Environment["TERM"] != "xterm-256color" {
		t.Fatalf("unsafe or incomplete create command: %#v", fixture.lastCreate)
	}
	for _, body := range []string{first.Body.String(), second.Body.String()} {
		if strings.Contains(body, "xterm-256color") {
			t.Fatalf("create response leaked write-only environment: %s", body)
		}
	}
}

func TestTerminalRESTRequiresProjectScopedSessionCommands(t *testing.T) {
	fixture := newTerminalHTTPFixture()
	router, credential := newTerminalRouter(t, fixture, true)
	missingProject := serveTerminalRequest(router, credential, http.MethodPost, "/api/v1/terminals/term_fixture7/actions/write", `{"data":"ls"}`, "")
	assertContractError(t, missingProject, http.StatusBadRequest, string(CodeTerminalInvalidPayload), false)
	if fixture.getCalls != 0 || fixture.writeCalls != 0 {
		t.Fatal("missing project scope reached terminal service")
	}

	crossProject := serveTerminalRequest(router, credential, http.MethodPost, "/api/v1/terminals/term_fixture7/actions/write?project_id=8", `{"data":"ls"}`, "")
	assertContractError(t, crossProject, http.StatusNotFound, string(CodeTerminalSessionNotFound), false)
	if fixture.writeCalls != 0 {
		t.Fatal("cross-project terminal command reached write")
	}

	valid := serveTerminalRequest(router, credential, http.MethodPost, "/api/v1/terminals/term_fixture7/actions/write?project_id=7", `{"data":"ls"}`, "")
	if valid.Code != http.StatusOK || fixture.writeCalls != 1 || fixture.lastSession.ProjectID != 7 || fixture.lastSession.Caller.ID == "" {
		t.Fatalf("valid scoped command failed status=%d session=%#v", valid.Code, fixture.lastSession)
	}
}

func TestTerminalRESTRejectsCustomProfileAndUnknownFields(t *testing.T) {
	fixture := newTerminalHTTPFixture()
	router, credential := newTerminalRouter(t, fixture, true)
	custom := serveTerminalRequest(router, credential, http.MethodPost, "/api/v1/projects/7/terminals", `{"cwd":"/safe","profile":{"id":"default","shell_path":"/bin/sh"}}`, "")
	assertContractError(t, custom, http.StatusBadRequest, string(CodeTerminalInvalidPayload), false)
	unknown := serveTerminalRequest(router, credential, http.MethodPost, "/api/v1/projects/7/terminals", `{"cwd":"/safe","unexpected":true}`, "")
	assertContractError(t, unknown, http.StatusBadRequest, string(CodeInvalidJSON), false)
	if fixture.createCalls != 0 {
		t.Fatal("invalid terminal create reached service")
	}
}

func newTerminalHTTPFixture() *terminalHTTPFixture {
	ended := time.Date(2026, 7, 12, 0, 0, 5, 0, time.UTC)
	code := 0
	return &terminalHTTPFixture{session: domainterminal.Session{
		ID: "term_fixture7", ProjectID: 7, Title: "Terminal", CWD: "/safe", Shell: "/bin/sh", Status: domainterminal.StatusExited,
		CreatedAt: ended.Add(-time.Second), EndedAt: &ended, ExitCode: &code, Cols: 80, Rows: 24, Runtime: domainterminal.RuntimeGo,
		Profile: domainterminal.Profile{ID: "default", Name: "default", Kind: "default", ShellPath: "/bin/sh", Args: []string{}},
	}}
}

func newTerminalRouter(t *testing.T, service TerminalService, enabled bool) (*Router, string) {
	t.Helper()
	clock := fixedClock{value: time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC)}
	manager, err := session.New(bytes.NewReader(bytes.Repeat([]byte{0x6b}, 32)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.Close)
	origins, err := config.NewOriginSet([]string{testOrigin})
	if err != nil {
		t.Fatal(err)
	}
	logger := &recordingLogger{}
	security, err := NewSecurity(SecurityOptions{
		Sessions: manager, Origins: origins, ExpectedHost: config.DefaultListenHost, ExpectedPort: 43123, Logger: logger, Clock: clock,
	})
	if err != nil {
		t.Fatal(err)
	}
	router, err := NewRouter(RouterOptions{Application: &testApplication{}, Logger: logger, Clock: clock, RequestIDs: fixedRequestIDs{}, BodyLimitBytes: 4096})
	if err != nil {
		t.Fatal(err)
	}
	if err := RegisterTerminals(router, security, TerminalRoutesOptions{Service: service, FeatureEnabled: enabled}); err != nil {
		t.Fatal(err)
	}
	credential := string(manager.CredentialCopy())
	return router, credential
}

func serveTerminalRequest(router http.Handler, credential, method, target, body, idempotencyKey string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, "http://"+testAuthority+target, strings.NewReader(body))
	request.Header.Set("Origin", testOrigin)
	request.Header.Set(session.HeaderName, credential)
	request.Header.Set(RequestIDHeader, "req_terminal_fixture")
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if idempotencyKey != "" {
		request.Header.Set(IdempotencyKeyHeader, idempotencyKey)
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func TestTerminalSessionProjectionUsesFrozenSnakeCaseAndEmptyEnv(t *testing.T) {
	projection := terminalSessionProjection(newTerminalHTTPFixture().session)
	encoded, err := json.Marshal(projection)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	profile, _ := decoded["profile"].(map[string]any)
	if decoded["project_id"] != float64(7) || decoded["runtime"] != "go" || profile == nil || len(profile["env"].(map[string]any)) != 0 {
		t.Fatalf("terminal DTO drift: %#v", decoded)
	}
}
