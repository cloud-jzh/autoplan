package scripts

import (
	"strings"
)

// Context mirrors the Node Script context vocabulary while keeping callers
// away from process configuration. Workspace is assigned from the persisted
// project only; the caller cannot replace it.
type Context struct {
	PlanID            *int64
	PlanFilePath      string
	TaskKey           string
	TaskID            *int64
	Scope             string
	ScopeFiles        []string
	IntakeType        string
	IntakeID          *int64
	ValidationCommand string
	Error             string
	Summary           string
}

type runtimeContext struct {
	Stage             string   `json:"stage"`
	Workspace         string   `json:"workspace"`
	Trigger           Trigger  `json:"trigger"`
	PlanID            *int64   `json:"planId"`
	PlanFilePath      *string  `json:"planFilePath"`
	TaskKey           *string  `json:"taskKey"`
	TaskID            *int64   `json:"taskId"`
	ScopeFiles        []string `json:"scopeFiles"`
	IntakeType        *string  `json:"intakeType"`
	IntakeID          *int64   `json:"intakeId"`
	ValidationCommand *string  `json:"validationCommand"`
	Error             *string  `json:"error"`
	Summary           *string  `json:"summary"`
}

func (value Context) Valid() bool {
	if len(value.ScopeFiles) > 200 || len(value.Scope) > 32768 {
		return false
	}
	for _, text := range append([]string{value.PlanFilePath, value.TaskKey, value.IntakeType, value.ValidationCommand, value.Error, value.Summary}, value.ScopeFiles...) {
		if !safeContextText(text) {
			return false
		}
	}
	return validOptionalID(value.PlanID) && validOptionalID(value.TaskID) && validOptionalID(value.IntakeID)
}

func (value Context) withRuntime(stage string, trigger Trigger, workspace string) (runtimeContext, error) {
	if !value.Valid() || !trigger.valid() || strings.TrimSpace(stage) == "" || strings.TrimSpace(workspace) == "" {
		return runtimeContext{}, ErrInvalidCommand
	}
	files := value.ScopeFiles
	if len(files) == 0 && strings.TrimSpace(value.Scope) != "" {
		files = parseScopeFiles(value.Scope)
	}
	result := runtimeContext{
		Stage: stage, Workspace: workspace, Trigger: trigger, PlanID: copyID(value.PlanID), TaskID: copyID(value.TaskID),
		ScopeFiles: append([]string(nil), files...), IntakeID: copyID(value.IntakeID),
	}
	result.PlanFilePath = optionalText(value.PlanFilePath)
	result.TaskKey = optionalText(value.TaskKey)
	result.IntakeType = optionalText(value.IntakeType)
	result.ValidationCommand = optionalText(value.ValidationCommand)
	result.Error = optionalText(value.Error)
	result.Summary = optionalText(value.Summary)
	return result, nil
}

func parseScopeFiles(value string) []string {
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			result = append(result, part)
		}
	}
	return result
}

func safeContextText(value string) bool {
	return len(value) <= 32768 && !strings.ContainsAny(value, "\x00\r\n")
}

func validOptionalID(value *int64) bool { return value == nil || *value > 0 }

func copyID(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func optionalText(value string) *string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return &value
}
