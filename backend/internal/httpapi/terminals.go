package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lyming99/autoplan/backend/internal/application"
	applicationterminal "github.com/lyming99/autoplan/backend/internal/application/terminal"
	domainterminal "github.com/lyming99/autoplan/backend/internal/domain/terminal"
	terminalruntime "github.com/lyming99/autoplan/backend/internal/runtime/terminal"
)

const (
	ProjectTerminalsPath = "/api/v1/projects/{project_id}/terminals"
	TerminalPath         = "/api/v1/terminals/{id}"
	TerminalWritePath    = "/api/v1/terminals/{id}/actions/write"
	TerminalResizePath   = "/api/v1/terminals/{id}/actions/resize"
	TerminalKillPath     = "/api/v1/terminals/{id}/actions/kill"
	TerminalClosePath    = "/api/v1/terminals/{id}/actions/close"
	TerminalRenamePath   = "/api/v1/terminals/{id}/actions/rename"
	TerminalReplayPath   = "/api/v1/terminals/{id}/replay"
	TerminalClearPath    = "/api/v1/terminals/{id}/actions/clear"

	terminalAdmissionLimit = 128
	terminalAdmissionTTL   = 5 * time.Minute
	terminalMaximumSeq     = uint64(9007199254740991)
	terminalMaximumList    = 32
)

var errTerminalIdempotencyConflict = errors.New("terminal idempotency key conflict")

// TerminalService is the sole HTTP dependency for P14 control operations.
// It deliberately exposes application commands only; HTTP handlers never see
// PTYs, process trees, Files Policy implementations or a session map.
type TerminalService interface {
	Create(context.Context, applicationterminal.CreateCommand) (domainterminal.Session, error)
	List(context.Context, domainterminal.Caller, int64) ([]domainterminal.Session, error)
	Get(context.Context, applicationterminal.SessionCommand) (domainterminal.Session, error)
	Write(context.Context, applicationterminal.WriteCommand) (int, error)
	Resize(context.Context, applicationterminal.ResizeCommand) error
	Kill(context.Context, applicationterminal.SessionCommand) (domainterminal.Session, error)
	Close(context.Context, applicationterminal.SessionCommand) (domainterminal.Session, error)
	Rename(context.Context, applicationterminal.RenameCommand) (domainterminal.Session, error)
	Clear(context.Context, applicationterminal.SessionCommand) error
	Replay(context.Context, applicationterminal.ReplayCommand) (domainterminal.Replay, error)
}

var _ TerminalService = (*applicationterminal.Service)(nil)

// TerminalRoutesOptions keeps the REST decision separate from construction of
// the service. P005 receives the same feature decision for the WebSocket path;
// a false value rejects every REST action without falling back to Node.
type TerminalRoutesOptions struct {
	Service        TerminalService
	FeatureEnabled bool
	AdmissionLimit int
	AdmissionTTL   time.Duration
}

type terminalRoutes struct {
	service        TerminalService
	featureEnabled bool
	bodyLimit      int64
	admissions     *terminalAdmissionStore
}

// RegisterTerminals installs the P14 REST control plane behind the existing
// REST security policy. The Go terminal feature remains disabled unless the
// caller passes the single shared atomic gate decision.
func RegisterTerminals(router *Router, security *Security, options TerminalRoutesOptions) error {
	if router == nil || security == nil || options.Service == nil {
		return ErrRouterDependency
	}
	limit := options.AdmissionLimit
	if limit <= 0 || limit > terminalAdmissionLimit {
		limit = terminalAdmissionLimit
	}
	ttl := options.AdmissionTTL
	if ttl <= 0 || ttl > terminalAdmissionTTL {
		ttl = terminalAdmissionTTL
	}
	routes := terminalRoutes{
		service: options.Service, featureEnabled: options.FeatureEnabled, bodyLimit: router.BodyLimitBytes(),
		admissions: newTerminalAdmissionStore(limit, ttl),
	}
	collection := security.Protect(TransportREST, routes.collectionEndpoint)
	session := security.Protect(TransportREST, routes.sessionEndpoint)
	write := security.Protect(TransportREST, routes.writeEndpoint)
	resize := security.Protect(TransportREST, routes.resizeEndpoint)
	kill := security.Protect(TransportREST, routes.killEndpoint)
	closeEndpoint := security.Protect(TransportREST, routes.closeEndpoint)
	rename := security.Protect(TransportREST, routes.renameEndpoint)
	replay := security.Protect(TransportREST, routes.replayEndpoint)
	clear := security.Protect(TransportREST, routes.clearEndpoint)
	for _, route := range []struct {
		method   string
		path     string
		endpoint Endpoint
	}{
		{http.MethodGet, ProjectTerminalsPath, collection},
		{http.MethodHead, ProjectTerminalsPath, collection},
		{http.MethodPost, ProjectTerminalsPath, collection},
		{http.MethodDelete, TerminalPath, session},
		{http.MethodPost, TerminalWritePath, write},
		{http.MethodPost, TerminalResizePath, resize},
		{http.MethodPost, TerminalKillPath, kill},
		{http.MethodPost, TerminalClosePath, closeEndpoint},
		{http.MethodPost, TerminalRenamePath, rename},
		{http.MethodGet, TerminalReplayPath, replay},
		{http.MethodHead, TerminalReplayPath, replay},
		{http.MethodPost, TerminalClearPath, clear},
	} {
		if err := router.HandlePattern(route.method, route.path, route.endpoint); err != nil {
			return err
		}
	}
	return nil
}

func (routes terminalRoutes) collectionEndpoint(_ application.Boundary, writer http.ResponseWriter, request *http.Request) {
	if !routes.available(writer, request) {
		return
	}
	projectID, failure := terminalProjectIDFromCollectionPath(request.URL.Path)
	if failure != nil {
		WriteError(writer, request, *failure)
		return
	}
	if request.URL.RawQuery != "" {
		failure := NewAPIError(CodeTerminalInvalidPayload, &ErrorDetails{Field: "query"})
		WriteError(writer, request, failure)
		return
	}
	caller, failure := terminalAuthenticatedCaller(request)
	if failure != nil {
		WriteError(writer, request, *failure)
		return
	}
	switch request.Method {
	case http.MethodGet, http.MethodHead:
		sessions, err := routes.service.List(request.Context(), caller, projectID)
		if err != nil {
			writeTerminalServiceError(writer, request, err, CodeTerminalPTYUnavailable)
			return
		}
		sessions = append([]domainterminal.Session(nil), sessions...)
		sort.SliceStable(sessions, func(left, right int) bool {
			if sessions[left].CreatedAt.Equal(sessions[right].CreatedAt) {
				return sessions[left].ID < sessions[right].ID
			}
			return sessions[left].CreatedAt.After(sessions[right].CreatedAt)
		})
		if len(sessions) > terminalMaximumList {
			sessions = sessions[:terminalMaximumList]
		}
		result := make([]terminalSessionDTO, 0, len(sessions))
		for _, session := range sessions {
			result = append(result, terminalSessionProjection(session))
		}
		WriteResponse(writer, request, http.StatusOK, terminalSessionsEnvelope{Data: result, RequestID: RequestID(request.Context())})
	case http.MethodPost:
		key, failure := IdempotencyKey(request)
		if failure != nil {
			WriteError(writer, request, *failure)
			return
		}
		var input terminalCreateRequest
		if failure := DecodeJSON(writer, request, &input, routes.bodyLimit); failure != nil {
			WriteError(writer, request, *failure)
			return
		}
		command, valid := input.command(caller, projectID)
		if !valid {
			WriteError(writer, request, NewAPIError(CodeTerminalInvalidPayload, nil))
			return
		}
		create := func() (domainterminal.Session, error) { return routes.service.Create(request.Context(), command) }
		var session domainterminal.Session
		var err error
		if key == "" {
			session, err = create()
		} else {
			fingerprint := terminalCreateFingerprint(projectID, input)
			session, err = routes.admissions.create(request.Context(), terminalAdmissionKey(caller.ID, projectID, key), fingerprint, create)
		}
		if err != nil {
			writeTerminalServiceError(writer, request, err, CodeTerminalPTYUnavailable)
			return
		}
		WriteResponse(writer, request, http.StatusCreated, terminalSessionEnvelope{Data: terminalSessionProjection(session), RequestID: RequestID(request.Context())})
	}
}

func (routes terminalRoutes) sessionEndpoint(_ application.Boundary, writer http.ResponseWriter, request *http.Request) {
	if !routes.available(writer, request) {
		return
	}
	if _, failure := IdempotencyKey(request); failure != nil {
		WriteError(writer, request, *failure)
		return
	}
	command, failure := terminalSessionCommand(request, "")
	if failure != nil {
		WriteError(writer, request, *failure)
		return
	}
	session, err := routes.service.Close(request.Context(), command)
	if err != nil {
		writeTerminalServiceError(writer, request, err, CodeTerminalKillFailed)
		return
	}
	WriteResponse(writer, request, http.StatusOK, terminalSessionEnvelope{Data: terminalSessionProjection(session), RequestID: RequestID(request.Context())})
}

func (routes terminalRoutes) writeEndpoint(_ application.Boundary, writer http.ResponseWriter, request *http.Request) {
	if !routes.available(writer, request) {
		return
	}
	if _, failure := IdempotencyKey(request); failure != nil {
		WriteError(writer, request, *failure)
		return
	}
	command, failure := terminalSessionCommand(request, "/actions/write")
	if failure != nil {
		WriteError(writer, request, *failure)
		return
	}
	var input terminalWriteRequest
	if failure := DecodeJSON(writer, request, &input, routes.bodyLimit); failure != nil {
		WriteError(writer, request, *failure)
		return
	}
	if input.Data == nil {
		WriteError(writer, request, NewAPIError(CodeTerminalInvalidPayload, &ErrorDetails{Field: "data"}))
		return
	}
	before, err := routes.service.Get(request.Context(), command)
	if err == nil {
		_, err = routes.service.Write(request.Context(), applicationterminal.WriteCommand{SessionCommand: command, Data: *input.Data})
	}
	if err != nil {
		writeTerminalServiceError(writer, request, err, CodeTerminalWriteFailed)
		return
	}
	WriteResponse(writer, request, http.StatusOK, terminalSessionEnvelope{Data: terminalSessionProjection(before), RequestID: RequestID(request.Context())})
}

func (routes terminalRoutes) resizeEndpoint(_ application.Boundary, writer http.ResponseWriter, request *http.Request) {
	if !routes.available(writer, request) {
		return
	}
	if _, failure := IdempotencyKey(request); failure != nil {
		WriteError(writer, request, *failure)
		return
	}
	command, failure := terminalSessionCommand(request, "/actions/resize")
	if failure != nil {
		WriteError(writer, request, *failure)
		return
	}
	var input terminalResizeRequest
	if failure := DecodeJSON(writer, request, &input, routes.bodyLimit); failure != nil {
		WriteError(writer, request, *failure)
		return
	}
	if input.Cols == nil || input.Rows == nil {
		WriteError(writer, request, NewAPIError(CodeTerminalInvalidPayload, nil))
		return
	}
	before, err := routes.service.Get(request.Context(), command)
	if err == nil {
		err = routes.service.Resize(request.Context(), applicationterminal.ResizeCommand{SessionCommand: command, Cols: *input.Cols, Rows: *input.Rows})
	}
	if err != nil {
		writeTerminalServiceError(writer, request, err, CodeTerminalResizeFailed)
		return
	}
	before.Cols, before.Rows = *input.Cols, *input.Rows
	WriteResponse(writer, request, http.StatusOK, terminalSessionEnvelope{Data: terminalSessionProjection(before), RequestID: RequestID(request.Context())})
}

func (routes terminalRoutes) killEndpoint(_ application.Boundary, writer http.ResponseWriter, request *http.Request) {
	if !routes.available(writer, request) {
		return
	}
	if _, failure := IdempotencyKey(request); failure != nil {
		WriteError(writer, request, *failure)
		return
	}
	command, failure := terminalSessionCommand(request, "/actions/kill")
	if failure != nil {
		WriteError(writer, request, *failure)
		return
	}
	if failure := decodeTerminalEmpty(writer, request, routes.bodyLimit); failure != nil {
		WriteError(writer, request, *failure)
		return
	}
	session, err := routes.service.Kill(request.Context(), command)
	if err != nil {
		writeTerminalServiceError(writer, request, err, CodeTerminalKillFailed)
		return
	}
	WriteResponse(writer, request, http.StatusOK, terminalSessionEnvelope{Data: terminalSessionProjection(session), RequestID: RequestID(request.Context())})
}

func (routes terminalRoutes) closeEndpoint(_ application.Boundary, writer http.ResponseWriter, request *http.Request) {
	if !routes.available(writer, request) {
		return
	}
	if _, failure := IdempotencyKey(request); failure != nil {
		WriteError(writer, request, *failure)
		return
	}
	command, failure := terminalSessionCommand(request, "/actions/close")
	if failure != nil {
		WriteError(writer, request, *failure)
		return
	}
	if failure := decodeTerminalEmpty(writer, request, routes.bodyLimit); failure != nil {
		WriteError(writer, request, *failure)
		return
	}
	session, err := routes.service.Close(request.Context(), command)
	if err != nil {
		writeTerminalServiceError(writer, request, err, CodeTerminalKillFailed)
		return
	}
	WriteResponse(writer, request, http.StatusOK, terminalSessionEnvelope{Data: terminalSessionProjection(session), RequestID: RequestID(request.Context())})
}

func (routes terminalRoutes) renameEndpoint(_ application.Boundary, writer http.ResponseWriter, request *http.Request) {
	if !routes.available(writer, request) {
		return
	}
	if _, failure := IdempotencyKey(request); failure != nil {
		WriteError(writer, request, *failure)
		return
	}
	command, failure := terminalSessionCommand(request, "/actions/rename")
	if failure != nil {
		WriteError(writer, request, *failure)
		return
	}
	var input terminalRenameRequest
	if failure := DecodeJSON(writer, request, &input, routes.bodyLimit); failure != nil {
		WriteError(writer, request, *failure)
		return
	}
	if input.Title == nil {
		WriteError(writer, request, NewAPIError(CodeTerminalInvalidPayload, &ErrorDetails{Field: "title"}))
		return
	}
	session, err := routes.service.Rename(request.Context(), applicationterminal.RenameCommand{SessionCommand: command, Title: *input.Title})
	if err != nil {
		writeTerminalServiceError(writer, request, err, CodeTerminalInvalidPayload)
		return
	}
	WriteResponse(writer, request, http.StatusOK, terminalSessionEnvelope{Data: terminalSessionProjection(session), RequestID: RequestID(request.Context())})
}

func (routes terminalRoutes) replayEndpoint(_ application.Boundary, writer http.ResponseWriter, request *http.Request) {
	if !routes.available(writer, request) {
		return
	}
	command, lastSeq, failure := terminalReplayCommand(request)
	if failure != nil {
		WriteError(writer, request, *failure)
		return
	}
	replay, err := routes.service.Replay(request.Context(), applicationterminal.ReplayCommand{SessionCommand: command, LastSeq: lastSeq})
	if err != nil {
		writeTerminalServiceError(writer, request, err, CodeTerminalReplayGap)
		return
	}
	WriteResponse(writer, request, http.StatusOK, terminalReplayEnvelope{Data: terminalReplayProjection(replay), RequestID: RequestID(request.Context())})
}

func (routes terminalRoutes) clearEndpoint(_ application.Boundary, writer http.ResponseWriter, request *http.Request) {
	if !routes.available(writer, request) {
		return
	}
	if _, failure := IdempotencyKey(request); failure != nil {
		WriteError(writer, request, *failure)
		return
	}
	command, failure := terminalSessionCommand(request, "/actions/clear")
	if failure != nil {
		WriteError(writer, request, *failure)
		return
	}
	if failure := decodeTerminalEmpty(writer, request, routes.bodyLimit); failure != nil {
		WriteError(writer, request, *failure)
		return
	}
	before, err := routes.service.Get(request.Context(), command)
	if err == nil {
		err = routes.service.Clear(request.Context(), command)
	}
	if err != nil {
		writeTerminalServiceError(writer, request, err, CodeTerminalInvalidPayload)
		return
	}
	WriteResponse(writer, request, http.StatusOK, terminalSessionEnvelope{Data: terminalSessionProjection(before), RequestID: RequestID(request.Context())})
}

func (routes terminalRoutes) available(writer http.ResponseWriter, request *http.Request) bool {
	if routes.featureEnabled {
		return true
	}
	WriteError(writer, request, NewAPIError(CodeTerminalFeatureDisabled, nil))
	return false
}

type terminalCreateRequest struct {
	CWD          json.RawMessage `json:"cwd"`
	ProfileID    json.RawMessage `json:"profile_id"`
	Profile      json.RawMessage `json:"profile"`
	Title        json.RawMessage `json:"title"`
	Cols         json.RawMessage `json:"cols"`
	Rows         json.RawMessage `json:"rows"`
	RetainOnExit json.RawMessage `json:"retain_on_exit"`
	Environment  json.RawMessage `json:"env"`
}

func (input terminalCreateRequest) command(caller domainterminal.Caller, projectID int64) (applicationterminal.CreateCommand, bool) {
	cwd, present, valid := terminalOptionalString(input.CWD)
	if !valid || !present || cwd == "" {
		return applicationterminal.CreateCommand{}, false
	}
	profileIDValue, profileIDPresent, valid := terminalOptionalString(input.ProfileID)
	if !valid || profileIDPresent && profileIDValue == "" {
		return applicationterminal.CreateCommand{}, false
	}
	var profileID *string
	if profileIDPresent {
		profileID = &profileIDValue
	}
	profile, profileValid := terminalProfileSelection(profileID, input.Profile)
	if !profileValid {
		return applicationterminal.CreateCommand{}, false
	}
	titleValue, titlePresent, valid := terminalOptionalString(input.Title)
	if !valid || titlePresent && titleValue == "" {
		return applicationterminal.CreateCommand{}, false
	}
	colsValue, colsPresent, valid := terminalOptionalInt(input.Cols)
	if !valid || colsPresent && colsValue == 0 {
		return applicationterminal.CreateCommand{}, false
	}
	rowsValue, rowsPresent, valid := terminalOptionalInt(input.Rows)
	if !valid || rowsPresent && rowsValue == 0 {
		return applicationterminal.CreateCommand{}, false
	}
	retainOnExit, retainPresent, valid := terminalOptionalBool(input.RetainOnExit)
	if !valid || retainPresent && !retainOnExit {
		return applicationterminal.CreateCommand{}, false
	}
	environment, valid := terminalEnvironment(input.Environment)
	if !valid {
		return applicationterminal.CreateCommand{}, false
	}
	command := applicationterminal.CreateCommand{Caller: caller, ProjectID: projectID, CWD: cwd, Environment: environment, ProfileID: profile}
	if titlePresent {
		command.Title = titleValue
	}
	if colsPresent {
		command.Cols = colsValue
	}
	if rowsPresent {
		command.Rows = rowsValue
	}
	return command, true
}

func terminalOptionalString(raw json.RawMessage) (string, bool, bool) {
	if len(raw) == 0 {
		return "", false, true
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return "", true, false
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", true, false
	}
	return value, true, true
}

func terminalOptionalInt(raw json.RawMessage) (int, bool, bool) {
	if len(raw) == 0 {
		return 0, false, true
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return 0, true, false
	}
	var value int
	if err := json.Unmarshal(raw, &value); err != nil {
		return 0, true, false
	}
	return value, true, true
}

func terminalOptionalBool(raw json.RawMessage) (bool, bool, bool) {
	if len(raw) == 0 {
		return false, false, true
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return false, true, false
	}
	var value bool
	if err := json.Unmarshal(raw, &value); err != nil {
		return false, true, false
	}
	return value, true, true
}

// terminalProfileSelection keeps P14's legacy profile object shape from
// becoming a shell/process configuration capability. Only a configured ID is
// accepted, and a simultaneous profile_id must select the identical profile.
func terminalProfileSelection(profileID *string, raw json.RawMessage) (string, bool) {
	if profileID != nil && *profileID == "" {
		return "", false
	}
	if len(raw) == 0 {
		if profileID == nil {
			return "", true
		}
		return *profileID, true
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return "", false
	}
	var profile struct {
		ID *string `json:"id"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&profile); err != nil || profile.ID == nil || *profile.ID == "" {
		return "", false
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return "", false
	}
	if profileID != nil && *profileID != *profile.ID {
		return "", false
	}
	return *profile.ID, true
}

func terminalEnvironment(raw json.RawMessage) (map[string]string, bool) {
	if len(raw) == 0 {
		return nil, true
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, false
	}
	var values map[string]string
	if err := json.Unmarshal(raw, &values); err != nil || values == nil {
		return nil, false
	}
	return values, true
}

type terminalWriteRequest struct {
	Data *string `json:"data"`
}

type terminalResizeRequest struct {
	Cols *int `json:"cols"`
	Rows *int `json:"rows"`
}

type terminalRenameRequest struct {
	Title *string `json:"title"`
}

func decodeTerminalEmpty(writer http.ResponseWriter, request *http.Request, limit int64) *APIError {
	var input struct{}
	return DecodeJSON(writer, request, &input, limit)
}

func terminalProjectIDFromCollectionPath(path string) (int64, *APIError) {
	segments := strings.Split(strings.TrimPrefix(path, "/api/v1/"), "/")
	if len(segments) != 3 || segments[0] != "projects" || segments[2] != "terminals" {
		failure := NewAPIError(CodeNotFound, nil)
		return 0, &failure
	}
	projectID, valid := parseCanonicalPositiveID(segments[1])
	if !valid {
		failure := NewAPIError(CodeInvalidProjectID, &ErrorDetails{Field: "project_id"})
		return 0, &failure
	}
	return projectID, nil
}

func terminalSessionCommand(request *http.Request, suffix string) (applicationterminal.SessionCommand, *APIError) {
	caller, failure := terminalAuthenticatedCaller(request)
	if failure != nil {
		return applicationterminal.SessionCommand{}, failure
	}
	sessionID, failure := terminalSessionIDFromPath(request.URL.Path, suffix)
	if failure != nil {
		return applicationterminal.SessionCommand{}, failure
	}
	projectID, failure := terminalProjectIDFromQuery(request.URL)
	if failure != nil {
		return applicationterminal.SessionCommand{}, failure
	}
	return applicationterminal.SessionCommand{Caller: caller, ProjectID: projectID, SessionID: sessionID}, nil
}

func terminalReplayCommand(request *http.Request) (applicationterminal.SessionCommand, uint64, *APIError) {
	caller, failure := terminalAuthenticatedCaller(request)
	if failure != nil {
		return applicationterminal.SessionCommand{}, 0, failure
	}
	sessionID, failure := terminalSessionIDFromPath(request.URL.Path, "/replay")
	if failure != nil {
		return applicationterminal.SessionCommand{}, 0, failure
	}
	values, err := url.ParseQuery(request.URL.RawQuery)
	if err != nil || len(values["project_id"]) != 1 || (len(values["last_seq"]) > 1) || len(values) > 2 {
		failure := NewAPIError(CodeTerminalInvalidPayload, &ErrorDetails{Field: "query"})
		return applicationterminal.SessionCommand{}, 0, &failure
	}
	projectID, valid := parseCanonicalPositiveID(values["project_id"][0])
	if !valid {
		failure := NewAPIError(CodeInvalidProjectID, &ErrorDetails{Field: "project_id"})
		return applicationterminal.SessionCommand{}, 0, &failure
	}
	lastSeq := uint64(0)
	if value, found := values["last_seq"]; found {
		parsed, err := strconv.ParseUint(value[0], 10, 64)
		if err != nil || parsed > terminalMaximumSeq || strconv.FormatUint(parsed, 10) != value[0] {
			failure := NewAPIError(CodeTerminalInvalidPayload, &ErrorDetails{Field: "last_seq"})
			return applicationterminal.SessionCommand{}, 0, &failure
		}
		lastSeq = parsed
	}
	return applicationterminal.SessionCommand{Caller: caller, ProjectID: projectID, SessionID: sessionID}, lastSeq, nil
}

func terminalProjectIDFromQuery(location *url.URL) (int64, *APIError) {
	if location == nil {
		failure := NewAPIError(CodeTerminalInvalidPayload, &ErrorDetails{Field: "query"})
		return 0, &failure
	}
	values, err := url.ParseQuery(location.RawQuery)
	if err != nil || len(values) != 1 || len(values["project_id"]) != 1 {
		failure := NewAPIError(CodeTerminalInvalidPayload, &ErrorDetails{Field: "query"})
		return 0, &failure
	}
	projectID, valid := parseCanonicalPositiveID(values["project_id"][0])
	if !valid {
		failure := NewAPIError(CodeInvalidProjectID, &ErrorDetails{Field: "project_id"})
		return 0, &failure
	}
	return projectID, nil
}

func terminalSessionIDFromPath(path, suffix string) (string, *APIError) {
	prefix := "/api/v1/terminals/"
	if !strings.HasPrefix(path, prefix) || (suffix != "" && !strings.HasSuffix(path, suffix)) {
		failure := NewAPIError(CodeNotFound, nil)
		return "", &failure
	}
	value := strings.TrimPrefix(path, prefix)
	if suffix != "" {
		value = strings.TrimSuffix(value, suffix)
	}
	if !validTerminalSessionID(value) {
		failure := NewAPIError(CodeTerminalInvalidSession, &ErrorDetails{Field: "id"})
		return "", &failure
	}
	return value, nil
}

func validTerminalSessionID(value string) bool {
	if !strings.HasPrefix(value, "term_") || len(value) < 11 || len(value) > 160 {
		return false
	}
	for index, character := range value[len("term_"):] {
		if index == 0 && !(character >= 'a' && character <= 'z' || character >= '0' && character <= '9') {
			return false
		}
		if !(character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || character == '_' || character == '-') {
			return false
		}
	}
	return true
}

type terminalProfileDTO struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	Kind      string            `json:"kind"`
	ShellPath string            `json:"shell_path"`
	Args      []string          `json:"args"`
	Env       map[string]string `json:"env"`
}

type terminalSessionDTO struct {
	ID        string             `json:"id"`
	ProjectID int64              `json:"project_id"`
	Title     string             `json:"title"`
	CWD       string             `json:"cwd"`
	Shell     string             `json:"shell"`
	Status    string             `json:"status"`
	CreatedAt string             `json:"created_at"`
	EndedAt   *string            `json:"ended_at"`
	ExitCode  *int               `json:"exit_code"`
	Cols      int                `json:"cols"`
	Rows      int                `json:"rows"`
	Profile   terminalProfileDTO `json:"profile"`
	Closed    bool               `json:"closed"`
	Runtime   string             `json:"runtime"`
}

type terminalOutputDTO struct {
	Seq  uint64 `json:"seq"`
	Data string `json:"data"`
}

type terminalReplayDTO struct {
	Session        terminalSessionDTO  `json:"session"`
	FirstSeq       uint64              `json:"first_seq"`
	LastSeq        uint64              `json:"last_seq"`
	Entries        []terminalOutputDTO `json:"entries"`
	ReplayComplete bool                `json:"replay_complete"`
}

type terminalSessionEnvelope struct {
	Data      terminalSessionDTO `json:"data"`
	RequestID string             `json:"request_id"`
}

type terminalSessionsEnvelope struct {
	Data      []terminalSessionDTO `json:"data"`
	RequestID string               `json:"request_id"`
}

type terminalReplayEnvelope struct {
	Data      terminalReplayDTO `json:"data"`
	RequestID string            `json:"request_id"`
}

func terminalSessionProjection(session domainterminal.Session) terminalSessionDTO {
	profile := session.Profile.Copy()
	result := terminalSessionDTO{
		ID: session.ID, ProjectID: session.ProjectID, Title: session.Title, CWD: session.CWD, Shell: session.Shell,
		Status: session.Status, CreatedAt: terminalTimestamp(session.CreatedAt), ExitCode: copyTerminalExitCode(session.ExitCode),
		Cols: session.Cols, Rows: session.Rows, Closed: session.Closed, Runtime: domainterminal.RuntimeGo,
		Profile: terminalProfileDTO{ID: profile.ID, Name: profile.Name, Kind: profile.Kind, ShellPath: profile.ShellPath, Args: append([]string{}, profile.Args...), Env: map[string]string{}},
	}
	if session.EndedAt != nil {
		value := terminalTimestamp(*session.EndedAt)
		result.EndedAt = &value
	}
	return result
}

func terminalReplayProjection(replay domainterminal.Replay) terminalReplayDTO {
	entries := make([]terminalOutputDTO, 0, len(replay.Entries))
	for _, entry := range replay.Entries {
		entries = append(entries, terminalOutputDTO{Seq: entry.Seq, Data: entry.Data})
	}
	return terminalReplayDTO{
		Session: terminalSessionProjection(replay.Session), FirstSeq: replay.FirstSeq, LastSeq: replay.LastSeq,
		Entries: entries, ReplayComplete: true,
	}
}

func terminalTimestamp(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }

func copyTerminalExitCode(value *int) *int {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func writeTerminalServiceError(writer http.ResponseWriter, request *http.Request, err error, fallback ErrorCode) {
	code := fallback
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		code = CodeRequestTimeout
	case errors.Is(err, domainterminal.ErrInvalidCommand):
		code = CodeTerminalInvalidPayload
	case errors.Is(err, domainterminal.ErrUnavailable):
		code = CodeTerminalPTYUnavailable
	case errors.Is(err, domainterminal.ErrForbidden):
		code = CodeTerminalForbidden
	case errors.Is(err, domainterminal.ErrNotFound), errors.Is(err, domainterminal.ErrClosed):
		code = CodeTerminalSessionNotFound
	case errors.Is(err, domainterminal.ErrReplayGap):
		code = CodeTerminalReplayGap
	case errors.Is(err, domainterminal.ErrCursorTooOld):
		code = CodeTerminalCursorTooOld
	case errors.Is(err, domainterminal.ErrSlowConsumer):
		code = CodeTerminalSlowConsumer
	case errors.Is(err, errTerminalIdempotencyConflict):
		code = CodeIdempotencyKeyReused
	case errors.Is(err, terminalruntime.ErrWorkingDirDenied), errors.Is(err, terminalruntime.ErrWorkingDirChanged):
		code = CodeTerminalCWDOutside
	case errors.Is(err, terminalruntime.ErrInvalidRequest):
		code = CodeTerminalInvalidPayload
	case errors.Is(err, terminalruntime.ErrInputLimit), errors.Is(err, terminalruntime.ErrResizeLimit):
		code = CodeTerminalRateLimited
	case errors.Is(err, terminalruntime.ErrSessionLimit):
		code = CodeTerminalSessionLimit
	case errors.Is(err, terminalruntime.ErrPlatformUnavailable):
		code = CodeTerminalPlatformBlocked
	case errors.Is(err, terminalruntime.ErrSessionClosed):
		code = CodeTerminalSessionNotFound
	case errors.Is(err, terminalruntime.ErrConfiguration), errors.Is(err, terminalruntime.ErrPolicyUnavailable), errors.Is(err, terminalruntime.ErrSpawn):
		code = CodeTerminalPTYUnavailable
	}
	WriteError(writer, request, NewAPIError(code, nil))
}

type terminalAdmission struct {
	fingerprint [sha256.Size]byte
	done        chan struct{}
	session     domainterminal.Session
	err         error
	expiresAt   time.Time
}

type terminalAdmissionStore struct {
	mu    sync.Mutex
	items map[string]*terminalAdmission
	limit int
	ttl   time.Duration
	now   func() time.Time
}

func newTerminalAdmissionStore(limit int, ttl time.Duration) *terminalAdmissionStore {
	return &terminalAdmissionStore{items: make(map[string]*terminalAdmission), limit: limit, ttl: ttl, now: func() time.Time { return time.Now().UTC() }}
}

func (store *terminalAdmissionStore) create(ctx context.Context, key string, fingerprint [sha256.Size]byte, create func() (domainterminal.Session, error)) (domainterminal.Session, error) {
	if store == nil || create == nil {
		return domainterminal.Session{}, domainterminal.ErrUnavailable
	}
	store.mu.Lock()
	store.pruneLocked(store.now())
	if current := store.items[key]; current != nil {
		if current.fingerprint != fingerprint {
			store.mu.Unlock()
			return domainterminal.Session{}, errTerminalIdempotencyConflict
		}
		done := current.done
		store.mu.Unlock()
		select {
		case <-done:
			return current.session.Copy(), current.err
		case <-ctx.Done():
			return domainterminal.Session{}, ctx.Err()
		}
	}
	if len(store.items) >= store.limit {
		store.mu.Unlock()
		return domainterminal.Session{}, domainterminal.ErrUnavailable
	}
	entry := &terminalAdmission{fingerprint: fingerprint, done: make(chan struct{})}
	store.items[key] = entry
	store.mu.Unlock()
	session, err := create()
	store.mu.Lock()
	entry.session, entry.err = session.Copy(), err
	if err == nil {
		entry.expiresAt = store.now().Add(store.ttl)
	} else {
		delete(store.items, key)
	}
	close(entry.done)
	store.mu.Unlock()
	return session, err
}

func (store *terminalAdmissionStore) pruneLocked(now time.Time) {
	for key, entry := range store.items {
		if !entry.expiresAt.IsZero() && !entry.expiresAt.After(now) {
			delete(store.items, key)
		}
	}
}

func terminalCreateFingerprint(projectID int64, input terminalCreateRequest) [sha256.Size]byte {
	encoded, _ := json.Marshal(struct {
		ProjectID int64                 `json:"project_id"`
		Input     terminalCreateRequest `json:"input"`
	}{ProjectID: projectID, Input: input})
	sum := sha256.Sum256(encoded)
	for index := range encoded {
		encoded[index] = 0
	}
	return sum
}

func terminalAdmissionKey(caller string, projectID int64, key string) string {
	return caller + "\x00" + strconv.FormatInt(projectID, 10) + "\x00" + key
}
