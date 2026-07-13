package automation

import (
	"encoding/json"

	domainautomation "github.com/lyming99/autoplan/backend/internal/domain/automation"
)

// ScriptDTO exposes static, non-secret metadata. Path, body, working
// directory, and process output remain write-only in this phase.
type ScriptDTO struct {
	ID             int64   `json:"id"`
	ProjectID      int64   `json:"project_id"`
	Name           string  `json:"name"`
	Runtime        string  `json:"runtime"`
	Description    string  `json:"description"`
	TriggerMode    string  `json:"trigger_mode"`
	HookStage      *string `json:"hook_stage"`
	ScheduleCron   *string `json:"schedule_cron"`
	Enabled        bool    `json:"enabled"`
	TimeoutSeconds int64   `json:"timeout_seconds"`
	FailAborts     bool    `json:"fail_aborts"`
	ContextInject  string  `json:"context_inject"`
	SortOrder      int64   `json:"sort_order"`
	LastStatus     *string `json:"last_status"`
	LastExitCode   *int64  `json:"last_exit_code"`
	LastDurationMS *int64  `json:"last_duration_ms"`
	LastRunAt      *string `json:"last_run_at"`
	SourceType     string  `json:"source_type"`
	HasPath        bool    `json:"has_path"`
	HasBody        bool    `json:"has_body"`
	HasWorkDir     bool    `json:"has_work_dir"`
	HasLastLog     bool    `json:"has_last_log"`
	CreatedAt      string  `json:"created_at"`
	UpdatedAt      string  `json:"updated_at"`
	Version        int64   `json:"version"`
}

// ExecutorDTO exposes validation and run-state metadata while keeping command
// material, cwd, environment values, plugin actions, and logs out of replies.
type ExecutorDTO struct {
	ID                 int64    `json:"id"`
	ProjectID          int64    `json:"project_id"`
	Label              string   `json:"label"`
	Type               string   `json:"type"`
	GroupKind          *string  `json:"group_kind"`
	GroupIsDefault     bool     `json:"group_is_default"`
	DependsOn          []string `json:"depends_on"`
	DependsOrder       string   `json:"depends_order"`
	Enabled            bool     `json:"enabled"`
	SortOrder          int64    `json:"sort_order"`
	LastStatus         *string  `json:"last_status"`
	LastExitCode       *int64   `json:"last_exit_code"`
	LastDurationMS     *int64   `json:"last_duration_ms"`
	LastRunAt          *string  `json:"last_run_at"`
	HasCommand         bool     `json:"has_command"`
	ArgumentCount      int      `json:"argument_count"`
	HasActions         bool     `json:"has_actions"`
	HasOptionsCwd      bool     `json:"has_options_cwd"`
	OptionsEnvKeyCount int      `json:"options_env_key_count"`
	HasProblemMatcher  bool     `json:"has_problem_matcher"`
	HasPluginState     bool     `json:"has_plugin_state"`
	HasLastLog         bool     `json:"has_last_log"`
	CreatedAt          string   `json:"created_at"`
	UpdatedAt          string   `json:"updated_at"`
	Version            int64    `json:"version"`
}

func scriptDTO(value domainautomation.Script) ScriptDTO {
	projectID := int64(0)
	if value.ProjectID != nil {
		projectID = *value.ProjectID
	}
	return ScriptDTO{
		ID: value.ID, ProjectID: projectID, Name: value.Name, Runtime: value.Runtime,
		Description: value.Description, TriggerMode: value.TriggerMode, HookStage: copyString(value.HookStage),
		ScheduleCron: copyString(value.ScheduleCron), Enabled: value.Enabled, TimeoutSeconds: value.TimeoutSeconds,
		FailAborts: value.FailAborts, ContextInject: value.ContextInject, SortOrder: value.SortOrder,
		LastStatus: copyString(value.LastStatus), LastExitCode: copyInt64(value.LastExitCode),
		LastDurationMS: copyInt64(value.LastDurationMS), LastRunAt: copyString(value.LastRunAt), SourceType: value.SourceType,
		HasPath: value.Path != "", HasBody: value.Body != "", HasWorkDir: value.WorkDir != "", HasLastLog: value.LastLog != nil && *value.LastLog != "",
		CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt, Version: value.Version,
	}
}

func executorDTO(value domainautomation.Executor) ExecutorDTO {
	var args []any
	_ = json.Unmarshal(value.ArgsJSON, &args)
	var depends []string
	_ = json.Unmarshal(value.DependsOnJSON, &depends)
	var options struct {
		Cwd string         `json:"cwd"`
		Env map[string]any `json:"env"`
	}
	_ = json.Unmarshal(value.OptionsJSON, &options)
	return ExecutorDTO{
		ID: value.ID, ProjectID: value.ProjectID, Label: value.Label, Type: value.Type,
		GroupKind: copyString(value.GroupKind), GroupIsDefault: value.GroupIsDefault, DependsOn: append([]string(nil), depends...),
		DependsOrder: value.DependsOrder, Enabled: value.Enabled, SortOrder: value.SortOrder,
		LastStatus: copyString(value.LastStatus), LastExitCode: copyInt64(value.LastExitCode),
		LastDurationMS: copyInt64(value.LastDurationMS), LastRunAt: copyString(value.LastRunAt),
		HasCommand: value.Command != "", ArgumentCount: len(args), HasActions: value.ActionsJSON != nil,
		HasOptionsCwd: options.Cwd != "", OptionsEnvKeyCount: len(options.Env),
		HasProblemMatcher: value.ProblemMatcherJSON != nil, HasPluginState: value.PluginStateJSON != nil,
		HasLastLog: value.LastLog != nil && *value.LastLog != "", CreatedAt: value.CreatedAt,
		UpdatedAt: value.UpdatedAt, Version: value.Version,
	}
}

func copyString(value *string) *string {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}

func copyInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}
