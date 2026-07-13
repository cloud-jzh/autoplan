// Package automation owns the persistence-neutral Script and Executor rules.
package automation

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrInvalidScript   = errors.New("automation script is invalid")
	ErrInvalidExecutor = errors.New("automation executor is invalid")
	ErrInvalidOrder    = errors.New("automation order is invalid")
	ErrInvalidVersion  = errors.New("automation version is invalid")
)

const (
	DefaultScriptRuntime    = "node"
	DefaultScriptTrigger    = "manual"
	DefaultScriptSource     = "inline"
	DefaultContextInject    = "none"
	DefaultTimeoutSeconds   = int64(60)
	DefaultExecutorType     = "shell"
	DefaultDependsOrder     = "parallel"
	LastLogMaxChars         = 24000
	maximumAutomationString = 200000
)

type Script struct {
	ID             int64
	ProjectID      *int64
	Name           string
	Path           string
	Runtime        string
	Body           string
	Description    string
	TriggerMode    string
	HookStage      *string
	ScheduleCron   *string
	Enabled        bool
	WorkDir        string
	TimeoutSeconds int64
	FailAborts     bool
	ContextInject  string
	SortOrder      int64
	LastStatus     *string
	LastExitCode   *int64
	LastDurationMS *int64
	LastLog        *string
	LastRunAt      *string
	CreatedAt      string
	UpdatedAt      string
	SourceType     string
	Version        int64
}

type Executor struct {
	ID                 int64
	ProjectID          int64
	Label              string
	Type               string
	Command            string
	ArgsJSON           json.RawMessage
	ActionsJSON        *json.RawMessage
	OptionsJSON        json.RawMessage
	GroupKind          *string
	GroupIsDefault     bool
	PresentationJSON   json.RawMessage
	ProblemMatcherJSON *json.RawMessage
	DependsOnJSON      json.RawMessage
	DependsOrder       string
	Enabled            bool
	SortOrder          int64
	LastStatus         *string
	LastExitCode       *int64
	LastDurationMS     *int64
	LastLog            *string
	LastRunAt          *string
	PluginStateJSON    *json.RawMessage
	CreatedAt          string
	UpdatedAt          string
	Version            int64
}

// ScriptInput supports partial updates without using zero values to mean both
// "clear" and "leave unchanged". A nil field inherits current/default data.
type ScriptInput struct {
	Name           *string
	Path           *string
	Runtime        *string
	Body           *string
	Description    *string
	TriggerMode    *string
	HookStage      *string
	ScheduleCron   *string
	Enabled        *bool
	WorkDir        *string
	TimeoutSeconds *int64
	FailAborts     *bool
	ContextInject  *string
	SortOrder      *int64
	SourceType     *string
}

type ScriptConfig struct {
	Name           string
	Path           string
	Runtime        string
	Body           string
	Description    string
	TriggerMode    string
	HookStage      *string
	ScheduleCron   *string
	Enabled        bool
	WorkDir        string
	TimeoutSeconds int64
	FailAborts     bool
	ContextInject  string
	SortOrder      int64
	SourceType     string
}

type ExecutorInput struct {
	Label              *string
	Type               *string
	Command            *string
	ArgsJSON           *json.RawMessage
	ActionsJSON        *json.RawMessage
	OptionsJSON        *json.RawMessage
	GroupKind          *string
	GroupIsDefault     *bool
	PresentationJSON   *json.RawMessage
	ProblemMatcherJSON *json.RawMessage
	DependsOnJSON      *json.RawMessage
	DependsOrder       *string
	Enabled            *bool
	SortOrder          *int64
}

type ExecutorConfig struct {
	Label              string
	Type               string
	Command            string
	ArgsJSON           json.RawMessage
	ActionsJSON        *json.RawMessage
	OptionsJSON        json.RawMessage
	GroupKind          *string
	GroupIsDefault     bool
	PresentationJSON   json.RawMessage
	ProblemMatcherJSON *json.RawMessage
	DependsOnJSON      json.RawMessage
	DependsOrder       string
	Enabled            bool
	SortOrder          int64
}

type ListOptions struct {
	ProjectID int64
	Limit     int
	Offset    int
}

type ScriptCreate struct {
	ProjectID int64
	Input     ScriptInput
	CreatedAt string
}

type ScriptUpdate struct {
	ProjectID       int64
	ScriptID        int64
	Input           ScriptInput
	ExpectedVersion int64
	UpdatedAt       string
}

type ExecutorCreate struct {
	ProjectID int64
	Input     ExecutorInput
	CreatedAt string
}

type ExecutorUpdate struct {
	ProjectID       int64
	ExecutorID      int64
	Input           ExecutorInput
	ExpectedVersion int64
	UpdatedAt       string
}

type Toggle struct {
	ProjectID       int64
	ID              int64
	ExpectedVersion int64
	UpdatedAt       string
}

type Delete struct {
	ProjectID       int64
	ID              int64
	ExpectedVersion int64
}

type Reorder struct {
	ProjectID       int64
	IDs             []int64
	ExpectedVersion map[int64]int64
	UpdatedAt       string
}

type Import struct {
	ProjectID    int64
	Items        []ExecutorInput
	DedupeLabels *bool
	UpdatedAt    string
}

func NormalizeScriptInput(input ScriptInput, current *Script) (ScriptConfig, error) {
	name := trimInput(input.Name, scriptString(current, func(value *Script) string { return value.Name }))
	if name == "" || !validText(name, 500) {
		return ScriptConfig{}, ErrInvalidScript
	}
	trigger := enumOrDefault(trimInput(input.TriggerMode, scriptString(current, func(value *Script) string { return value.TriggerMode })),
		[]string{"hook", "manual", "schedule"}, DefaultScriptTrigger)
	runtime := enumOrDefault(trimInput(input.Runtime, scriptString(current, func(value *Script) string { return value.Runtime })),
		[]string{"node", "bash", "ps", "cmd"}, DefaultScriptRuntime)
	source := enumOrDefault(trimInput(input.SourceType, scriptString(current, func(value *Script) string { return value.SourceType })),
		[]string{"inline", "file"}, DefaultScriptSource)
	contextInject := enumOrDefault(trimInput(input.ContextInject, scriptString(current, func(value *Script) string { return value.ContextInject })),
		[]string{"env", "stdin", "none"}, DefaultContextInject)
	hook := optionalEnum(input.HookStage, scriptOptional(current, func(value *Script) *string { return value.HookStage }),
		[]string{"plan:after", "task:after", "validation:before", "loop:end", "on:fail"})
	cron := optionalText(input.ScheduleCron, scriptOptional(current, func(value *Script) *string { return value.ScheduleCron }))
	if trigger != "schedule" {
		cron = nil
	} else if cron == nil || !validCron(*cron) {
		return ScriptConfig{}, ErrInvalidScript
	}
	timeout := DefaultTimeoutSeconds
	if current != nil {
		timeout = current.TimeoutSeconds
	}
	if input.TimeoutSeconds != nil {
		timeout = *input.TimeoutSeconds
	}
	if timeout <= 0 || timeout > 2147483647 {
		if current == nil {
			timeout = DefaultTimeoutSeconds
		} else {
			return ScriptConfig{}, ErrInvalidScript
		}
	}
	enabled := boolInput(input.Enabled, current != nil && current.Enabled, current == nil)
	failAborts := boolInput(input.FailAborts, current != nil && current.FailAborts, false)
	sortOrder := intInput(input.SortOrder, scriptInt(current, func(value *Script) int64 { return value.SortOrder }), 0)
	result := ScriptConfig{
		Name: name, Path: trimInput(input.Path, scriptString(current, func(value *Script) string { return value.Path })),
		Runtime: runtime, Body: trimInput(input.Body, scriptString(current, func(value *Script) string { return value.Body })),
		Description: trimInput(input.Description, scriptString(current, func(value *Script) string { return value.Description })),
		TriggerMode: trigger, HookStage: hook, ScheduleCron: cron, Enabled: enabled,
		WorkDir:        trimInput(input.WorkDir, scriptString(current, func(value *Script) string { return value.WorkDir })),
		TimeoutSeconds: timeout, FailAborts: failAborts, ContextInject: contextInject,
		SortOrder: sortOrder, SourceType: source,
	}
	if !validScriptConfig(result) {
		return ScriptConfig{}, ErrInvalidScript
	}
	return result, nil
}

func NormalizeExecutorInput(input ExecutorInput, current *Executor) (ExecutorConfig, error) {
	label := trimInput(input.Label, executorString(current, func(value *Executor) string { return value.Label }))
	if label == "" || !validText(label, 500) {
		return ExecutorConfig{}, ErrInvalidExecutor
	}
	typ := enumOrDefault(trimInput(input.Type, executorString(current, func(value *Executor) string { return value.Type })),
		[]string{"shell", "process", "plugin"}, DefaultExecutorType)
	command := trimInput(input.Command, executorString(current, func(value *Executor) string { return value.Command }))
	args, err := normalizedArgs(input.ArgsJSON, executorJSON(current, func(value *Executor) json.RawMessage { return value.ArgsJSON }))
	if err != nil {
		return ExecutorConfig{}, ErrInvalidExecutor
	}
	actions, err := normalizedActions(input.ActionsJSON, executorOptionalJSON(current, func(value *Executor) *json.RawMessage { return value.ActionsJSON }), typ)
	if err != nil {
		return ExecutorConfig{}, ErrInvalidExecutor
	}
	if typ == "plugin" {
		startCommand, startArgs, found := pluginStartAction(actions)
		if found {
			if command == "" {
				command = startCommand
			}
			if emptyJSONArray(args) {
				args = startArgs
			}
		}
	}
	if command == "" || !validText(command, maximumAutomationString) {
		return ExecutorConfig{}, ErrInvalidExecutor
	}
	options, err := normalizedOptions(input.OptionsJSON, executorJSON(current, func(value *Executor) json.RawMessage { return value.OptionsJSON }))
	if err != nil {
		return ExecutorConfig{}, ErrInvalidExecutor
	}
	presentation, err := normalizedPresentation(input.PresentationJSON, executorJSON(current, func(value *Executor) json.RawMessage { return value.PresentationJSON }))
	if err != nil {
		return ExecutorConfig{}, ErrInvalidExecutor
	}
	problem, err := normalizedJSON(input.ProblemMatcherJSON, executorOptionalJSON(current, func(value *Executor) *json.RawMessage { return value.ProblemMatcherJSON }), true)
	if err != nil {
		return ExecutorConfig{}, ErrInvalidExecutor
	}
	depends, err := normalizedDepends(input.DependsOnJSON, executorJSON(current, func(value *Executor) json.RawMessage { return value.DependsOnJSON }))
	if err != nil {
		return ExecutorConfig{}, ErrInvalidExecutor
	}
	groupKind := optionalText(input.GroupKind, executorOptional(current, func(value *Executor) *string { return value.GroupKind }))
	if groupKind != nil && !validText(*groupKind, 500) {
		return ExecutorConfig{}, ErrInvalidExecutor
	}
	dependsOrder := enumOrDefault(trimInput(input.DependsOrder, executorString(current, func(value *Executor) string { return value.DependsOrder })),
		[]string{"parallel", "sequence"}, DefaultDependsOrder)
	result := ExecutorConfig{
		Label: label, Type: typ, Command: command, ArgsJSON: args, ActionsJSON: actions,
		OptionsJSON: options, GroupKind: groupKind,
		GroupIsDefault:   boolInput(input.GroupIsDefault, current != nil && current.GroupIsDefault, false),
		PresentationJSON: presentation, ProblemMatcherJSON: problem, DependsOnJSON: depends,
		DependsOrder: dependsOrder, Enabled: boolInput(input.Enabled, current != nil && current.Enabled, current == nil),
		SortOrder: intInput(input.SortOrder, executorInt(current, func(value *Executor) int64 { return value.SortOrder }), 0),
	}
	if !validExecutorConfig(result) {
		return ExecutorConfig{}, ErrInvalidExecutor
	}
	return result, nil
}

func ValidateScriptRecord(value Script) error {
	if value.ID <= 0 || value.ProjectID == nil || *value.ProjectID <= 0 || value.Version <= 0 ||
		!validScriptConfig(ScriptConfig{Name: value.Name, Path: value.Path, Runtime: value.Runtime, Body: value.Body,
			Description: value.Description, TriggerMode: value.TriggerMode, HookStage: copyString(value.HookStage),
			ScheduleCron: copyString(value.ScheduleCron), Enabled: value.Enabled, WorkDir: value.WorkDir,
			TimeoutSeconds: value.TimeoutSeconds, FailAborts: value.FailAborts, ContextInject: value.ContextInject,
			SortOrder: value.SortOrder, SourceType: value.SourceType}) || !validTimes(value.CreatedAt, value.UpdatedAt) ||
		!validOptionalTime(value.LastRunAt) || !validNullableText(value.LastStatus, 500) ||
		!validNullableText(value.LastLog, LastLogMaxChars) || !validOptionalNonNegative(value.LastDurationMS) {
		return ErrInvalidScript
	}
	return nil
}

func ValidateExecutorRecord(value Executor) error {
	if value.ID <= 0 || value.ProjectID <= 0 || value.Version <= 0 ||
		!validExecutorConfig(ExecutorConfig{Label: value.Label, Type: value.Type, Command: value.Command,
			ArgsJSON: value.ArgsJSON, ActionsJSON: copyJSON(value.ActionsJSON), OptionsJSON: value.OptionsJSON,
			GroupKind: copyString(value.GroupKind), GroupIsDefault: value.GroupIsDefault,
			PresentationJSON: value.PresentationJSON, ProblemMatcherJSON: copyJSON(value.ProblemMatcherJSON),
			DependsOnJSON: value.DependsOnJSON, DependsOrder: value.DependsOrder, Enabled: value.Enabled,
			SortOrder: value.SortOrder}) || !validTimes(value.CreatedAt, value.UpdatedAt) ||
		!validOptionalTime(value.LastRunAt) || !validNullableText(value.LastStatus, 500) ||
		!validNullableText(value.LastLog, LastLogMaxChars) || !validOptionalNonNegative(value.LastDurationMS) ||
		!validOptionalObject(value.PluginStateJSON) {
		return ErrInvalidExecutor
	}
	return nil
}

func ValidateReorder(value Reorder) error {
	if value.ProjectID <= 0 || len(value.IDs) == 0 || len(value.IDs) != len(value.ExpectedVersion) ||
		!ValidUTCTimestamp(value.UpdatedAt) {
		return ErrInvalidOrder
	}
	seen := make(map[int64]struct{}, len(value.IDs))
	for _, id := range value.IDs {
		if id <= 0 || value.ExpectedVersion[id] <= 0 {
			return ErrInvalidOrder
		}
		if _, exists := seen[id]; exists {
			return ErrInvalidOrder
		}
		seen[id] = struct{}{}
	}
	return nil
}

func ValidUTCTimestamp(value string) bool {
	if !strings.HasSuffix(value, "Z") {
		return false
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	return err == nil && parsed.Location() == time.UTC
}

func copyString(value *string) *string {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}

func copyJSON(value *json.RawMessage) *json.RawMessage {
	if value == nil {
		return nil
	}
	result := append(json.RawMessage(nil), (*value)...)
	return &result
}

func scriptString(value *Script, get func(*Script) string) string {
	if value == nil {
		return ""
	}
	return get(value)
}

func scriptOptional(value *Script, get func(*Script) *string) *string {
	if value == nil {
		return nil
	}
	return get(value)
}

func scriptInt(value *Script, get func(*Script) int64) int64 {
	if value == nil {
		return 0
	}
	return get(value)
}

func executorString(value *Executor, get func(*Executor) string) string {
	if value == nil {
		return ""
	}
	return get(value)
}

func executorOptional(value *Executor, get func(*Executor) *string) *string {
	if value == nil {
		return nil
	}
	return get(value)
}

func executorInt(value *Executor, get func(*Executor) int64) int64 {
	if value == nil {
		return 0
	}
	return get(value)
}

func executorJSON(value *Executor, get func(*Executor) json.RawMessage) json.RawMessage {
	if value == nil {
		return nil
	}
	return get(value)
}

func executorOptionalJSON(value *Executor, get func(*Executor) *json.RawMessage) *json.RawMessage {
	if value == nil {
		return nil
	}
	return get(value)
}

func trimInput(input *string, fallback string) string {
	if input == nil {
		return strings.TrimSpace(fallback)
	}
	return strings.TrimSpace(*input)
}

func optionalText(input, fallback *string) *string {
	if input == nil {
		return copyString(fallback)
	}
	value := strings.TrimSpace(*input)
	if value == "" {
		return nil
	}
	return &value
}

func optionalEnum(input, fallback *string, allowed []string) *string {
	value := optionalText(input, fallback)
	if value == nil || !contains(allowed, *value) {
		return nil
	}
	return value
}

func enumOrDefault(value string, allowed []string, fallback string) string {
	if contains(allowed, value) {
		return value
	}
	return fallback
}

func boolInput(input *bool, current, fallback bool) bool {
	if input != nil {
		return *input
	}
	if current {
		return true
	}
	return fallback
}

func intInput(input *int64, current, fallback int64) int64 {
	if input != nil {
		return *input
	}
	if current != 0 {
		return current
	}
	return fallback
}

func validScriptConfig(value ScriptConfig) bool {
	if !validText(value.Name, 500) || !validText(value.Path, maximumAutomationString) ||
		!contains([]string{"node", "bash", "ps", "cmd"}, value.Runtime) ||
		!validText(value.Body, maximumAutomationString) || !validText(value.Description, 10000) ||
		!contains([]string{"hook", "manual", "schedule"}, value.TriggerMode) ||
		!validOptionalEnum(value.HookStage, []string{"plan:after", "task:after", "validation:before", "loop:end", "on:fail"}) ||
		!validText(value.WorkDir, maximumAutomationString) || value.TimeoutSeconds <= 0 ||
		!contains([]string{"env", "stdin", "none"}, value.ContextInject) ||
		!contains([]string{"inline", "file"}, value.SourceType) {
		return false
	}
	if value.TriggerMode == "schedule" {
		return value.ScheduleCron != nil && validCron(*value.ScheduleCron)
	}
	return value.ScheduleCron == nil
}

func validExecutorConfig(value ExecutorConfig) bool {
	if !validText(value.Label, 500) || !contains([]string{"shell", "process", "plugin"}, value.Type) ||
		!validText(value.Command, maximumAutomationString) || !validOptionalText(value.GroupKind, 500) ||
		!contains([]string{"parallel", "sequence"}, value.DependsOrder) ||
		!validJSONShape(value.ArgsJSON, '[') || !validJSONShape(value.OptionsJSON, '{') ||
		!validJSONShape(value.PresentationJSON, '{') || !validJSONShape(value.DependsOnJSON, '[') ||
		!validOptionalJSON(value.ActionsJSON, '{') || !validOptionalJSON(value.ProblemMatcherJSON, 0) {
		return false
	}
	if value.Type == "plugin" && value.ActionsJSON == nil {
		return false
	}
	return true
}

func validText(value string, maximum int) bool {
	return len(value) <= maximum && !strings.ContainsRune(value, 0)
}

func validNullableText(value *string, maximum int) bool {
	return value == nil || validText(*value, maximum)
}

func validOptionalText(value *string, maximum int) bool {
	return value == nil || (strings.TrimSpace(*value) != "" && validText(*value, maximum))
}

func validOptionalEnum(value *string, allowed []string) bool {
	return value == nil || contains(allowed, *value)
}

func validTimes(createdAt, updatedAt string) bool {
	if !ValidUTCTimestamp(createdAt) || !ValidUTCTimestamp(updatedAt) {
		return false
	}
	created, _ := time.Parse(time.RFC3339Nano, createdAt)
	updated, _ := time.Parse(time.RFC3339Nano, updatedAt)
	return !created.After(updated)
}

func validOptionalTime(value *string) bool { return value == nil || ValidUTCTimestamp(*value) }

func validOptionalNonNegative(value *int64) bool { return value == nil || *value >= 0 }

func contains(values []string, candidate string) bool {
	for _, value := range values {
		if value == candidate {
			return true
		}
	}
	return false
}

func normalizedArgs(input *json.RawMessage, current json.RawMessage) (json.RawMessage, error) {
	raw := current
	if input != nil {
		raw = *input
	}
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		raw = json.RawMessage("[]")
	}
	var entries []any
	if json.Unmarshal(raw, &entries) != nil {
		return nil, ErrInvalidExecutor
	}
	result := make([]any, 0, len(entries))
	for _, entry := range entries {
		switch value := entry.(type) {
		case nil:
			result = append(result, "")
		case string, bool, float64:
			result = append(result, stringifyScalar(value))
		case map[string]any:
			rawValue, found := value["value"]
			if !found {
				return nil, ErrInvalidExecutor
			}
			normalized := map[string]any{"value": stringifyScalar(rawValue)}
			if quoting, exists := value["quoting"]; exists && stringifyScalar(quoting) != "" {
				if !contains([]string{"escape", "strong", "weak"}, stringifyScalar(quoting)) {
					return nil, ErrInvalidExecutor
				}
				normalized["quoting"] = stringifyScalar(quoting)
			}
			result = append(result, normalized)
		default:
			return nil, ErrInvalidExecutor
		}
	}
	return marshalJSON(result)
}

func normalizedActions(input, current *json.RawMessage, executorType string) (*json.RawMessage, error) {
	raw := current
	if input != nil {
		raw = input
	}
	if executorType != "plugin" {
		return nil, nil
	}
	if raw == nil || len(bytes.TrimSpace(*raw)) == 0 || bytes.Equal(bytes.TrimSpace(*raw), []byte("null")) {
		return nil, ErrInvalidExecutor
	}
	var actions map[string]any
	if json.Unmarshal(*raw, &actions) != nil {
		return nil, ErrInvalidExecutor
	}
	result := make(map[string]any)
	for _, name := range []string{"start", "reload", "stop"} {
		entry, exists := actions[name]
		if !exists || entry == nil {
			if name == "start" {
				return nil, ErrInvalidExecutor
			}
			continue
		}
		action, ok := entry.(map[string]any)
		if !ok {
			return nil, ErrInvalidExecutor
		}
		typ := stringifyScalar(action["type"])
		if typ == "" {
			typ = "command"
		}
		if !contains([]string{"command", "input"}, typ) {
			return nil, ErrInvalidExecutor
		}
		command := strings.TrimSpace(stringifyScalar(firstValue(action, "command", "cmd")))
		normalizedArgs, argsErr := normalizedArgsValue(action["args"])
		if argsErr != nil {
			return nil, ErrInvalidExecutor
		}
		normalized := map[string]any{"type": typ, "command": command, "args": normalizedArgs}
		if inputValue, exists := action["input"]; exists {
			normalized["input"] = strings.TrimSpace(stringifyScalar(inputValue))
		}
		if name == "start" && (typ != "command" || command == "") {
			return nil, ErrInvalidExecutor
		}
		if name == "stop" && (typ != "command" || command == "") {
			return nil, ErrInvalidExecutor
		}
		if name == "reload" && ((typ == "input" && stringifyScalar(normalized["input"]) == "") || (typ == "command" && command == "")) {
			return nil, ErrInvalidExecutor
		}
		result[name] = normalized
	}
	encoded, err := marshalJSON(result)
	if err != nil {
		return nil, err
	}
	return &encoded, nil
}

func normalizedArgsValue(value any) ([]any, error) {
	if value == nil {
		return []any{}, nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	raw := json.RawMessage(encoded)
	args, err := normalizedArgs(&raw, nil)
	if err != nil {
		return nil, err
	}
	var result []any
	return result, json.Unmarshal(args, &result)
}

func pluginStartAction(actions *json.RawMessage) (string, json.RawMessage, bool) {
	if actions == nil {
		return "", nil, false
	}
	var document map[string]struct {
		Command string          `json:"command"`
		Args    json.RawMessage `json:"args"`
	}
	if json.Unmarshal(*actions, &document) != nil {
		return "", nil, false
	}
	start, found := document["start"]
	return start.Command, append(json.RawMessage(nil), start.Args...), found
}

func emptyJSONArray(value json.RawMessage) bool {
	var entries []any
	return json.Unmarshal(value, &entries) == nil && len(entries) == 0
}

func normalizedOptions(input *json.RawMessage, current json.RawMessage) (json.RawMessage, error) {
	raw := current
	if input != nil {
		raw = *input
	}
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		raw = json.RawMessage("{}")
	}
	var source map[string]any
	if json.Unmarshal(raw, &source) != nil {
		return nil, ErrInvalidExecutor
	}
	result := map[string]any{"cwd": "", "env": map[string]any{}}
	if cwd, exists := source["cwd"]; exists {
		result["cwd"] = strings.TrimSpace(stringifyScalar(cwd))
	}
	if rawEnv, exists := source["env"]; exists && rawEnv != nil {
		env, ok := rawEnv.(map[string]any)
		if !ok {
			return nil, ErrInvalidExecutor
		}
		normalized := make(map[string]any, len(env))
		for key, value := range env {
			key = strings.TrimSpace(key)
			if key == "" {
				return nil, ErrInvalidExecutor
			}
			normalized[key] = stringifyScalar(value)
		}
		result["env"] = normalized
	}
	return marshalJSON(result)
}

func normalizedPresentation(input *json.RawMessage, current json.RawMessage) (json.RawMessage, error) {
	raw := current
	if input != nil {
		raw = *input
	}
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		raw = json.RawMessage("{}")
	}
	var source map[string]any
	if json.Unmarshal(raw, &source) != nil {
		return nil, ErrInvalidExecutor
	}
	result := make(map[string]any)
	for key, values := range map[string][]string{
		"reveal": {"always", "silent", "never"}, "panel": {"shared", "dedicated", "new"},
		"revealProblems": {"never", "onProblem", "always"},
	} {
		if value, exists := source[key]; exists {
			text := strings.TrimSpace(stringifyScalar(value))
			if !contains(values, text) {
				return nil, ErrInvalidExecutor
			}
			result[key] = text
		}
	}
	for _, key := range []string{"echo", "focus", "showReuseMessage", "clear", "close"} {
		if value, exists := source[key]; exists {
			result[key] = normalizeBoolean(value)
		}
	}
	return marshalJSON(result)
}

func normalizedDepends(input *json.RawMessage, current json.RawMessage) (json.RawMessage, error) {
	raw := current
	if input != nil {
		raw = *input
	}
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		raw = json.RawMessage("[]")
	}
	var decoded any
	if json.Unmarshal(raw, &decoded) != nil {
		return nil, ErrInvalidExecutor
	}
	items, ok := decoded.([]any)
	if !ok {
		items = []any{decoded}
	}
	seen := make(map[string]struct{}, len(items))
	result := make([]string, 0, len(items))
	for _, item := range items {
		switch item.(type) {
		case string, bool, float64:
		default:
			return nil, ErrInvalidExecutor
		}
		label := strings.TrimSpace(stringifyScalar(item))
		if label == "" || !validText(label, 500) {
			return nil, ErrInvalidExecutor
		}
		if _, exists := seen[label]; !exists {
			seen[label] = struct{}{}
			result = append(result, label)
		}
	}
	return marshalJSON(result)
}

func normalizedJSON(input, current *json.RawMessage, nullable bool) (*json.RawMessage, error) {
	raw := current
	if input != nil {
		raw = input
	}
	if raw == nil || len(bytes.TrimSpace(*raw)) == 0 || bytes.Equal(bytes.TrimSpace(*raw), []byte("null")) {
		if nullable {
			return nil, nil
		}
		return nil, ErrInvalidExecutor
	}
	var decoded any
	if json.Unmarshal(*raw, &decoded) != nil {
		return nil, ErrInvalidExecutor
	}
	encoded, err := marshalJSON(decoded)
	if err != nil {
		return nil, err
	}
	return &encoded, nil
}

func validJSONShape(raw json.RawMessage, expected byte) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && trimmed[0] == expected && json.Valid(trimmed)
}

func validOptionalJSON(raw *json.RawMessage, expected byte) bool {
	if raw == nil {
		return true
	}
	trimmed := bytes.TrimSpace(*raw)
	return len(trimmed) > 0 && json.Valid(trimmed) && (expected == 0 || trimmed[0] == expected)
}

func validOptionalObject(raw *json.RawMessage) bool { return validOptionalJSON(raw, '{') }

func marshalJSON(value any) (json.RawMessage, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(encoded), nil
}

func stringifyScalar(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case bool:
		if typed {
			return "true"
		}
		return "false"
	case float64:
		return fmt.Sprintf("%v", typed)
	default:
		return ""
	}
}

func firstValue(values map[string]any, names ...string) any {
	for _, name := range names {
		if value, exists := values[name]; exists {
			return value
		}
	}
	return nil
}

func normalizeBoolean(value any) bool {
	if boolean, ok := value.(bool); ok {
		return boolean
	}
	text := strings.ToLower(strings.TrimSpace(stringifyScalar(value)))
	return text != "" && text != "0" && text != "false" && text != "off" && text != "no" && text != "disabled"
}

func validCron(value string) bool {
	fields := strings.Fields(strings.TrimSpace(value))
	if len(fields) != 5 {
		return false
	}
	ranges := [][2]int{{0, 59}, {0, 23}, {1, 31}, {1, 12}, {0, 7}}
	for index, field := range fields {
		if !validCronField(field, ranges[index][0], ranges[index][1]) {
			return false
		}
	}
	return true
}

func validCronField(field string, minimum, maximum int) bool {
	for _, part := range strings.Split(field, ",") {
		if part == "" {
			return false
		}
		base := part
		step := 1
		if before, after, found := strings.Cut(part, "/"); found {
			base = before
			if _, err := fmt.Sscanf(after, "%d", &step); err != nil || step < 1 || fmt.Sprintf("%d", step) != after {
				return false
			}
		}
		if base == "*" {
			continue
		}
		low, high := 0, 0
		if before, after, found := strings.Cut(base, "-"); found {
			if _, err := fmt.Sscanf(before, "%d", &low); err != nil || fmt.Sprintf("%d", low) != before {
				return false
			}
			if _, err := fmt.Sscanf(after, "%d", &high); err != nil || fmt.Sprintf("%d", high) != after {
				return false
			}
		} else if _, err := fmt.Sscanf(base, "%d", &low); err != nil || fmt.Sprintf("%d", low) != base {
			return false
		} else {
			high = low
		}
		if low < minimum || high > maximum || low > high {
			return false
		}
	}
	return true
}
