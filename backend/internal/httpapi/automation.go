package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/lyming99/autoplan/backend/internal/application"
	applicationautomation "github.com/lyming99/autoplan/backend/internal/application/automation"
	domainautomation "github.com/lyming99/autoplan/backend/internal/domain/automation"
	"github.com/lyming99/autoplan/backend/internal/repository"
)

const (
	ProjectScriptsPath         = "/api/v1/projects/{project_id}/scripts"
	ProjectScriptPath          = "/api/v1/projects/{project_id}/scripts/{script_id}"
	ProjectScriptTogglePath    = "/api/v1/projects/{project_id}/scripts/{script_id}/toggle"
	ProjectScriptReorderPath   = "/api/v1/projects/{project_id}/scripts/reorder"
	ProjectExecutorsPath       = "/api/v1/projects/{project_id}/executors"
	ProjectExecutorPath        = "/api/v1/projects/{project_id}/executors/{executor_id}"
	ProjectExecutorTogglePath  = "/api/v1/projects/{project_id}/executors/{executor_id}/toggle"
	ProjectExecutorReorderPath = "/api/v1/projects/{project_id}/executors/reorder"
	ProjectExecutorImportPath  = "/api/v1/projects/{project_id}/executors/import"
)

type AutomationService interface {
	ListScripts(context.Context, applicationautomation.ListQuery) ([]applicationautomation.ScriptDTO, error)
	GetScript(context.Context, int64, int64) (applicationautomation.ScriptDTO, error)
	CreateScript(context.Context, applicationautomation.CreateScriptCommand) (applicationautomation.ScriptDTO, error)
	UpdateScript(context.Context, applicationautomation.UpdateScriptCommand) (applicationautomation.ScriptDTO, error)
	DeleteScript(context.Context, int64, int64, int64) (applicationautomation.ScriptDTO, error)
	ToggleScript(context.Context, int64, int64, int64) (applicationautomation.ScriptDTO, error)
	ReorderScripts(context.Context, applicationautomation.ReorderCommand) ([]applicationautomation.ScriptDTO, error)
	ListExecutors(context.Context, applicationautomation.ListQuery) ([]applicationautomation.ExecutorDTO, error)
	GetExecutor(context.Context, int64, int64) (applicationautomation.ExecutorDTO, error)
	CreateExecutor(context.Context, applicationautomation.CreateExecutorCommand) (applicationautomation.ExecutorDTO, error)
	UpdateExecutor(context.Context, applicationautomation.UpdateExecutorCommand) (applicationautomation.ExecutorDTO, error)
	DeleteExecutor(context.Context, int64, int64, int64) (applicationautomation.ExecutorDTO, error)
	ToggleExecutor(context.Context, int64, int64, int64) (applicationautomation.ExecutorDTO, error)
	ReorderExecutors(context.Context, applicationautomation.ReorderCommand) ([]applicationautomation.ExecutorDTO, error)
	ImportExecutors(context.Context, applicationautomation.ImportExecutorsCommand) ([]applicationautomation.ExecutorDTO, error)
}

var _ AutomationService = (*applicationautomation.Service)(nil)

type scriptRequest struct {
	Name           *string `json:"name"`
	Path           *string `json:"path"`
	Runtime        *string `json:"runtime"`
	Body           *string `json:"body"`
	Description    *string `json:"description"`
	TriggerMode    *string `json:"trigger_mode"`
	HookStage      *string `json:"hook_stage"`
	ScheduleCron   *string `json:"schedule_cron"`
	Enabled        *bool   `json:"enabled"`
	WorkDir        *string `json:"work_dir"`
	TimeoutSeconds *int64  `json:"timeout_seconds"`
	FailAborts     *bool   `json:"fail_aborts"`
	ContextInject  *string `json:"context_inject"`
	SortOrder      *int64  `json:"sort_order"`
	SourceType     *string `json:"source_type"`
	Version        *int64  `json:"version"`
}

type executorRequest struct {
	Label          *string          `json:"label"`
	Type           *string          `json:"type"`
	Command        *string          `json:"command"`
	Args           *json.RawMessage `json:"args"`
	Actions        *json.RawMessage `json:"actions"`
	Options        *json.RawMessage `json:"options"`
	GroupKind      *string          `json:"group_kind"`
	GroupIsDefault *bool            `json:"group_is_default"`
	Presentation   *json.RawMessage `json:"presentation"`
	ProblemMatcher *json.RawMessage `json:"problem_matcher"`
	DependsOn      *json.RawMessage `json:"depends_on"`
	DependsOrder   *string          `json:"depends_order"`
	Enabled        *bool            `json:"enabled"`
	SortOrder      *int64           `json:"sort_order"`
	Version        *int64           `json:"version"`
}

type automationReorderRequest struct {
	IDs      []int64          `json:"ids"`
	Versions map[string]int64 `json:"versions"`
}

type executorImportRequest struct {
	Items        []executorRequest `json:"items"`
	DedupeLabels *bool             `json:"dedupe_labels"`
}

func RegisterAutomation(router *Router, security *Security, service AutomationService) error {
	if router == nil || security == nil || service == nil {
		return ErrRouterDependency
	}
	listScripts := security.Protect(TransportREST, automationListScripts(service))
	script := security.Protect(TransportREST, automationScript(service, router.BodyLimitBytes()))
	toggleScript := security.Protect(TransportREST, automationToggleScript(service, router.BodyLimitBytes()))
	reorderScripts := security.Protect(TransportREST, automationReorderScripts(service, router.BodyLimitBytes()))
	listExecutors := security.Protect(TransportREST, automationListExecutors(service))
	executor := security.Protect(TransportREST, automationExecutor(service, router.BodyLimitBytes()))
	toggleExecutor := security.Protect(TransportREST, automationToggleExecutor(service, router.BodyLimitBytes()))
	reorderExecutors := security.Protect(TransportREST, automationReorderExecutors(service, router.BodyLimitBytes()))
	importExecutors := security.Protect(TransportREST, automationImportExecutors(service, router.BodyLimitBytes()))
	for _, route := range []struct {
		method   string
		path     string
		endpoint Endpoint
	}{
		{http.MethodGet, ProjectScriptsPath, listScripts}, {http.MethodHead, ProjectScriptsPath, listScripts},
		{http.MethodPost, ProjectScriptsPath, script}, {http.MethodGet, ProjectScriptPath, script},
		{http.MethodHead, ProjectScriptPath, script}, {http.MethodPatch, ProjectScriptPath, script}, {http.MethodDelete, ProjectScriptPath, script},
		{http.MethodPost, ProjectScriptTogglePath, toggleScript}, {http.MethodPost, ProjectScriptReorderPath, reorderScripts},
		{http.MethodGet, ProjectExecutorsPath, listExecutors}, {http.MethodHead, ProjectExecutorsPath, listExecutors},
		{http.MethodPost, ProjectExecutorsPath, executor}, {http.MethodGet, ProjectExecutorPath, executor},
		{http.MethodHead, ProjectExecutorPath, executor}, {http.MethodPatch, ProjectExecutorPath, executor}, {http.MethodDelete, ProjectExecutorPath, executor},
		{http.MethodPost, ProjectExecutorTogglePath, toggleExecutor}, {http.MethodPost, ProjectExecutorReorderPath, reorderExecutors},
		{http.MethodPost, ProjectExecutorImportPath, importExecutors},
	} {
		if err := router.HandlePattern(route.method, route.path, route.endpoint); err != nil {
			return err
		}
	}
	return nil
}

func automationListScripts(service AutomationService) Endpoint {
	return func(_ application.Boundary, writer http.ResponseWriter, request *http.Request) {
		projectID, _, failure := automationPath(request.URL.Path, "scripts", false)
		if failure != nil {
			WriteError(writer, request, *failure)
			return
		}
		limit, offset, failure := automationPagination(request.URL)
		if failure != nil {
			WriteError(writer, request, *failure)
			return
		}
		result, err := service.ListScripts(request.Context(), applicationautomation.ListQuery{ProjectID: projectID, Limit: limit, Offset: offset})
		if err != nil {
			writeAutomationServiceError(writer, request, err)
			return
		}
		WriteResponse(writer, request, http.StatusOK, responseEnvelope{Data: result, RequestID: RequestID(request.Context())})
	}
}

func automationScript(service AutomationService, bodyLimit int64) Endpoint {
	return func(_ application.Boundary, writer http.ResponseWriter, request *http.Request) {
		projectID, scriptID, failure := automationPath(request.URL.Path, "scripts", request.Method != http.MethodPost)
		if failure != nil {
			WriteError(writer, request, *failure)
			return
		}
		switch request.Method {
		case http.MethodPost:
			var input scriptRequest
			if failure := DecodeJSON(writer, request, &input, bodyLimit); failure != nil {
				WriteError(writer, request, *failure)
				return
			}
			result, err := service.CreateScript(request.Context(), applicationautomation.CreateScriptCommand{ProjectID: projectID, Input: input.scriptInput()})
			if err != nil {
				writeAutomationServiceError(writer, request, err)
				return
			}
			WriteResponse(writer, request, http.StatusCreated, responseEnvelope{Data: result, RequestID: RequestID(request.Context())})
		case http.MethodGet, http.MethodHead:
			result, err := service.GetScript(request.Context(), projectID, scriptID)
			if err != nil {
				writeAutomationServiceError(writer, request, err)
				return
			}
			WriteResponse(writer, request, http.StatusOK, responseEnvelope{Data: result, RequestID: RequestID(request.Context())})
		case http.MethodPatch:
			var input scriptRequest
			if failure := DecodeJSON(writer, request, &input, bodyLimit); failure != nil {
				WriteError(writer, request, *failure)
				return
			}
			if input.Version == nil || *input.Version <= 0 {
				WriteError(writer, request, NewAPIError(CodeVersionRequired, &ErrorDetails{Field: "version"}))
				return
			}
			result, err := service.UpdateScript(request.Context(), applicationautomation.UpdateScriptCommand{ProjectID: projectID, ScriptID: scriptID, ExpectedVersion: *input.Version, Input: input.scriptInput()})
			if err != nil {
				writeAutomationServiceError(writer, request, err)
				return
			}
			WriteResponse(writer, request, http.StatusOK, responseEnvelope{Data: result, RequestID: RequestID(request.Context())})
		case http.MethodDelete:
			version, failure := expectedVersion(request.URL)
			if failure != nil {
				WriteError(writer, request, *failure)
				return
			}
			result, err := service.DeleteScript(request.Context(), projectID, scriptID, version)
			if err != nil {
				writeAutomationServiceError(writer, request, err)
				return
			}
			WriteResponse(writer, request, http.StatusOK, responseEnvelope{Data: result, RequestID: RequestID(request.Context())})
		}
	}
}

func automationToggleScript(service AutomationService, bodyLimit int64) Endpoint {
	return automationToggle(service, bodyLimit, true)
}
func automationToggleExecutor(service AutomationService, bodyLimit int64) Endpoint {
	return automationToggle(service, bodyLimit, false)
}

func automationToggle(service AutomationService, bodyLimit int64, script bool) Endpoint {
	return func(_ application.Boundary, writer http.ResponseWriter, request *http.Request) {
		kind := "executors"
		if script {
			kind = "scripts"
		}
		projectID, id, failure := automationPath(strings.TrimSuffix(request.URL.Path, "/toggle"), kind, true)
		if failure != nil {
			WriteError(writer, request, *failure)
			return
		}
		var input struct {
			Version *int64 `json:"version"`
		}
		if failure := DecodeJSON(writer, request, &input, bodyLimit); failure != nil {
			WriteError(writer, request, *failure)
			return
		}
		if input.Version == nil || *input.Version <= 0 {
			WriteError(writer, request, NewAPIError(CodeVersionRequired, &ErrorDetails{Field: "version"}))
			return
		}
		var result any
		var err error
		if script {
			result, err = service.ToggleScript(request.Context(), projectID, id, *input.Version)
		} else {
			result, err = service.ToggleExecutor(request.Context(), projectID, id, *input.Version)
		}
		if err != nil {
			writeAutomationServiceError(writer, request, err)
			return
		}
		WriteResponse(writer, request, http.StatusOK, responseEnvelope{Data: result, RequestID: RequestID(request.Context())})
	}
}

func automationReorderScripts(service AutomationService, bodyLimit int64) Endpoint {
	return automationReorder(service, bodyLimit, true)
}
func automationReorderExecutors(service AutomationService, bodyLimit int64) Endpoint {
	return automationReorder(service, bodyLimit, false)
}

func automationReorder(service AutomationService, bodyLimit int64, script bool) Endpoint {
	return func(_ application.Boundary, writer http.ResponseWriter, request *http.Request) {
		kind := "executors"
		if script {
			kind = "scripts"
		}
		projectID, _, failure := automationPath(strings.TrimSuffix(request.URL.Path, "/reorder"), kind, false)
		if failure != nil {
			WriteError(writer, request, *failure)
			return
		}
		var input automationReorderRequest
		if failure := DecodeJSON(writer, request, &input, bodyLimit); failure != nil {
			WriteError(writer, request, *failure)
			return
		}
		versions, valid := parseVersionMap(input.IDs, input.Versions)
		if !valid {
			WriteError(writer, request, NewAPIError(CodeInvalidAutomation, nil))
			return
		}
		command := applicationautomation.ReorderCommand{ProjectID: projectID, IDs: input.IDs, ExpectedVersion: versions}
		var result any
		var err error
		if script {
			result, err = service.ReorderScripts(request.Context(), command)
		} else {
			result, err = service.ReorderExecutors(request.Context(), command)
		}
		if err != nil {
			writeAutomationServiceError(writer, request, err)
			return
		}
		WriteResponse(writer, request, http.StatusOK, responseEnvelope{Data: result, RequestID: RequestID(request.Context())})
	}
}

func automationListExecutors(service AutomationService) Endpoint {
	return func(_ application.Boundary, writer http.ResponseWriter, request *http.Request) {
		projectID, _, failure := automationPath(request.URL.Path, "executors", false)
		if failure != nil {
			WriteError(writer, request, *failure)
			return
		}
		limit, offset, failure := automationPagination(request.URL)
		if failure != nil {
			WriteError(writer, request, *failure)
			return
		}
		result, err := service.ListExecutors(request.Context(), applicationautomation.ListQuery{ProjectID: projectID, Limit: limit, Offset: offset})
		if err != nil {
			writeAutomationServiceError(writer, request, err)
			return
		}
		WriteResponse(writer, request, http.StatusOK, responseEnvelope{Data: result, RequestID: RequestID(request.Context())})
	}
}

func automationExecutor(service AutomationService, bodyLimit int64) Endpoint {
	return func(_ application.Boundary, writer http.ResponseWriter, request *http.Request) {
		projectID, executorID, failure := automationPath(request.URL.Path, "executors", request.Method != http.MethodPost)
		if failure != nil {
			WriteError(writer, request, *failure)
			return
		}
		switch request.Method {
		case http.MethodPost:
			var input executorRequest
			if failure := DecodeJSON(writer, request, &input, bodyLimit); failure != nil {
				WriteError(writer, request, *failure)
				return
			}
			result, err := service.CreateExecutor(request.Context(), applicationautomation.CreateExecutorCommand{ProjectID: projectID, Input: input.executorInput()})
			if err != nil {
				writeAutomationServiceError(writer, request, err)
				return
			}
			WriteResponse(writer, request, http.StatusCreated, responseEnvelope{Data: result, RequestID: RequestID(request.Context())})
		case http.MethodGet, http.MethodHead:
			result, err := service.GetExecutor(request.Context(), projectID, executorID)
			if err != nil {
				writeAutomationServiceError(writer, request, err)
				return
			}
			WriteResponse(writer, request, http.StatusOK, responseEnvelope{Data: result, RequestID: RequestID(request.Context())})
		case http.MethodPatch:
			var input executorRequest
			if failure := DecodeJSON(writer, request, &input, bodyLimit); failure != nil {
				WriteError(writer, request, *failure)
				return
			}
			if input.Version == nil || *input.Version <= 0 {
				WriteError(writer, request, NewAPIError(CodeVersionRequired, &ErrorDetails{Field: "version"}))
				return
			}
			result, err := service.UpdateExecutor(request.Context(), applicationautomation.UpdateExecutorCommand{ProjectID: projectID, ExecutorID: executorID, ExpectedVersion: *input.Version, Input: input.executorInput()})
			if err != nil {
				writeAutomationServiceError(writer, request, err)
				return
			}
			WriteResponse(writer, request, http.StatusOK, responseEnvelope{Data: result, RequestID: RequestID(request.Context())})
		case http.MethodDelete:
			version, failure := expectedVersion(request.URL)
			if failure != nil {
				WriteError(writer, request, *failure)
				return
			}
			result, err := service.DeleteExecutor(request.Context(), projectID, executorID, version)
			if err != nil {
				writeAutomationServiceError(writer, request, err)
				return
			}
			WriteResponse(writer, request, http.StatusOK, responseEnvelope{Data: result, RequestID: RequestID(request.Context())})
		}
	}
}

func automationImportExecutors(service AutomationService, bodyLimit int64) Endpoint {
	return func(_ application.Boundary, writer http.ResponseWriter, request *http.Request) {
		projectID, _, failure := automationPath(strings.TrimSuffix(request.URL.Path, "/import"), "executors", false)
		if failure != nil {
			WriteError(writer, request, *failure)
			return
		}
		var input executorImportRequest
		if failure := DecodeJSON(writer, request, &input, bodyLimit); failure != nil {
			WriteError(writer, request, *failure)
			return
		}
		items := make([]domainautomation.ExecutorInput, len(input.Items))
		for index := range input.Items {
			items[index] = input.Items[index].executorInput()
		}
		result, err := service.ImportExecutors(request.Context(), applicationautomation.ImportExecutorsCommand{ProjectID: projectID, Items: items, DedupeLabels: input.DedupeLabels})
		if err != nil {
			writeAutomationServiceError(writer, request, err)
			return
		}
		WriteResponse(writer, request, http.StatusCreated, responseEnvelope{Data: result, RequestID: RequestID(request.Context())})
	}
}

func (input scriptRequest) scriptInput() domainautomation.ScriptInput {
	return domainautomation.ScriptInput{Name: input.Name, Path: input.Path, Runtime: input.Runtime, Body: input.Body, Description: input.Description, TriggerMode: input.TriggerMode, HookStage: input.HookStage, ScheduleCron: input.ScheduleCron, Enabled: input.Enabled, WorkDir: input.WorkDir, TimeoutSeconds: input.TimeoutSeconds, FailAborts: input.FailAborts, ContextInject: input.ContextInject, SortOrder: input.SortOrder, SourceType: input.SourceType}
}
func (input executorRequest) executorInput() domainautomation.ExecutorInput {
	return domainautomation.ExecutorInput{Label: input.Label, Type: input.Type, Command: input.Command, ArgsJSON: input.Args, ActionsJSON: input.Actions, OptionsJSON: input.Options, GroupKind: input.GroupKind, GroupIsDefault: input.GroupIsDefault, PresentationJSON: input.Presentation, ProblemMatcherJSON: input.ProblemMatcher, DependsOnJSON: input.DependsOn, DependsOrder: input.DependsOrder, Enabled: input.Enabled, SortOrder: input.SortOrder}
}

func automationPath(path, kind string, requireID bool) (int64, int64, *APIError) {
	segments := strings.Split(strings.TrimPrefix(path, "/api/v1/projects/"), "/")
	expected := 2
	if requireID {
		expected = 3
	}
	if len(segments) != expected || segments[1] != kind {
		failure := NewAPIError(CodeNotFound, nil)
		return 0, 0, &failure
	}
	projectID, valid := parseCanonicalPositiveID(segments[0])
	if !valid {
		failure := NewAPIError(CodeInvalidProjectID, &ErrorDetails{Field: "project_id"})
		return 0, 0, &failure
	}
	if !requireID {
		return projectID, 0, nil
	}
	id, valid := parseCanonicalPositiveID(segments[2])
	if !valid {
		failure := NewAPIError(CodeInvalidAutomation, &ErrorDetails{Field: kind[:len(kind)-1] + "_id"})
		return 0, 0, &failure
	}
	return projectID, id, nil
}

func automationPagination(location *url.URL) (int, int, *APIError) {
	limit, offset := 50, 0
	if location == nil {
		failure := NewAPIError(CodeInvalidPagination, nil)
		return 0, 0, &failure
	}
	values := location.Query()
	for name, entries := range values {
		if (name != "limit" && name != "offset") || len(entries) != 1 {
			failure := NewAPIError(CodeInvalidPagination, &ErrorDetails{Field: name})
			return 0, 0, &failure
		}
	}
	if value, exists := values["limit"]; exists {
		parsed, valid := parsePositiveInt(value[0], 200)
		if !valid {
			failure := NewAPIError(CodeInvalidPagination, &ErrorDetails{Field: "limit"})
			return 0, 0, &failure
		}
		limit = parsed
	}
	if value, exists := values["offset"]; exists {
		parsed, err := strconv.Atoi(value[0])
		if err != nil || parsed < 0 || strconv.Itoa(parsed) != value[0] {
			failure := NewAPIError(CodeInvalidPagination, &ErrorDetails{Field: "offset"})
			return 0, 0, &failure
		}
		offset = parsed
	}
	return limit, offset, nil
}

func expectedVersion(location *url.URL) (int64, *APIError) {
	if location == nil {
		failure := NewAPIError(CodeVersionRequired, &ErrorDetails{Field: "version"})
		return 0, &failure
	}
	query := location.Query()
	if len(query) != 1 || len(query["version"]) != 1 {
		failure := NewAPIError(CodeVersionRequired, &ErrorDetails{Field: "version"})
		return 0, &failure
	}
	value := query.Get("version")
	parsed, valid := parseCanonicalPositiveID(value)
	if !valid {
		failure := NewAPIError(CodeVersionRequired, &ErrorDetails{Field: "version"})
		return 0, &failure
	}
	return parsed, nil
}

func parseVersionMap(ids []int64, values map[string]int64) (map[int64]int64, bool) {
	if len(ids) == 0 || len(ids) != len(values) {
		return nil, false
	}
	result := make(map[int64]int64, len(values))
	for _, id := range ids {
		if id <= 0 || values[strconv.FormatInt(id, 10)] <= 0 {
			return nil, false
		}
		if _, exists := result[id]; exists {
			return nil, false
		}
		result[id] = values[strconv.FormatInt(id, 10)]
	}
	return result, true
}

func writeAutomationServiceError(writer http.ResponseWriter, request *http.Request, err error) {
	code := CodeInternal
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		code = CodeRequestTimeout
	case errors.Is(err, applicationautomation.ErrInvalidCommand), errors.Is(err, domainautomation.ErrInvalidScript), errors.Is(err, domainautomation.ErrInvalidExecutor), errors.Is(err, domainautomation.ErrInvalidOrder):
		code = CodeInvalidAutomation
	case errors.Is(err, applicationautomation.ErrStateConflict), errors.Is(err, repository.ErrVersionConflict), errors.Is(err, repository.ErrAutomationConflict):
		code = CodePreconditionFailed
	case errors.Is(err, repository.ErrNotFound), errors.Is(err, repository.ErrProjectMismatch):
		code = CodeAutomationNotFound
	case errors.Is(err, repository.ErrTransaction), errors.Is(err, repository.ErrCommit), errors.Is(err, repository.ErrRollback):
		code = CodeRepositoryBusy
	case errors.Is(err, repository.ErrSchemaDrift):
		code = CodeRepositorySchemaDrift
	case errors.Is(err, applicationautomation.ErrUnavailable), errors.Is(err, repository.ErrNotConfigured), errors.Is(err, repository.ErrClosed), errors.Is(err, repository.ErrWriterUnauthorized):
		code = CodeRepositoryUnavailable
	}
	WriteError(writer, request, NewAPIError(code, nil))
}
