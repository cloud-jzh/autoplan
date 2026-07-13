// Package config owns Settings, ProjectState and LoopConfig invariants.
package config

import (
	"encoding/json"
	"errors"
	"strings"
	"time"
)

var (
	ErrInvalid       = errors.New("config is invalid")
	ErrVersionNeeded = errors.New("config version is required")
	ErrVersionStale  = errors.New("config version conflicts")
)

const (
	InitialVersion                int64 = 1
	DefaultIntervalSeconds        int64 = 5
	DefaultAgentCLIProvider             = "codex"
	DefaultCodexReasoningEffort         = "medium"
	DefaultPlanGenerationStrategy       = "external-cli-markdown"
	DefaultPlanExecutionStrategy        = "external-cli"
	PhaseIdle                           = "idle"
	PhaseStopped                        = "stopped"
	PhaseRunning                        = "running"
	PhaseScan                           = "scan"
	PhaseGeneratePlan                   = "generate-plan"
	PhaseExecuteTask                    = "execute-task"
	PhaseValidate                       = "validate"
)

type Setting struct {
	Key     string
	Value   string
	Version int64
}

// ProjectState mirrors schema v1. Secret-bearing values are intentionally
// untagged domain fields: they must never be formatted, logged, or transported.
type ProjectState struct {
	ProjectID            int64
	Running              int64
	Phase                string
	IntervalSeconds      int64
	ValidationCommand    string
	ProjectPrompt        string
	AgentCLIProvider     string
	AgentCLICommand      string
	CodexReasoningEffort *string

	PlanGenerationStrategy             string
	PlanGenerationProvider             *string
	PlanGenerationCommand              string
	PlanGenerationModel                string
	PlanGenerationCodexReasoningEffort *string
	PlanGenerationClaudeBaseURL        string
	PlanGenerationClaudeAuthToken      string
	PlanGenerationClaudeModel          string
	PlanGenerationClaudeConfigID       int64

	PlanExecutionStrategy             string
	PlanExecutionProvider             *string
	PlanExecutionCommand              string
	PlanExecutionModel                string
	PlanExecutionCodexReasoningEffort *string
	PlanExecutionClaudeBaseURL        string
	PlanExecutionClaudeAuthToken      string
	PlanExecutionClaudeModel          string
	PlanExecutionClaudeConfigID       int64

	LastIssueHash *string
	LastError     *string
	EnvVars       string
	UpdatedAt     string
	Version       int64
}

// LoopConfig is the mutable ProjectState subset. Runtime phase/error fields
// remain repository-owned and are never reset as a side effect of configuring.
type LoopConfig struct {
	IntervalSeconds      int64
	ValidationCommand    string
	ProjectPrompt        string
	AgentCLIProvider     string
	AgentCLICommand      string
	CodexReasoningEffort *string

	PlanGenerationStrategy             string
	PlanGenerationProvider             *string
	PlanGenerationCommand              string
	PlanGenerationModel                string
	PlanGenerationCodexReasoningEffort *string
	PlanGenerationClaudeBaseURL        string
	PlanGenerationClaudeAuthToken      string
	PlanGenerationClaudeModel          string
	PlanGenerationClaudeConfigID       int64

	PlanExecutionStrategy             string
	PlanExecutionProvider             *string
	PlanExecutionCommand              string
	PlanExecutionModel                string
	PlanExecutionCodexReasoningEffort *string
	PlanExecutionClaudeBaseURL        string
	PlanExecutionClaudeAuthToken      string
	PlanExecutionClaudeModel          string
	PlanExecutionClaudeConfigID       int64

	EnvVars string
}

type EnvVar struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

func DefaultLoopConfig() LoopConfig {
	effort := DefaultCodexReasoningEffort
	return LoopConfig{
		IntervalSeconds:        DefaultIntervalSeconds,
		AgentCLIProvider:       DefaultAgentCLIProvider,
		CodexReasoningEffort:   &effort,
		PlanGenerationStrategy: DefaultPlanGenerationStrategy,
		PlanExecutionStrategy:  DefaultPlanExecutionStrategy,
	}
}

func DefaultProjectState(projectID int64, updatedAt string) (ProjectState, error) {
	if projectID <= 0 || !ValidUTCTimestamp(updatedAt) {
		return ProjectState{}, ErrInvalid
	}
	defaults := DefaultLoopConfig()
	return ProjectState{
		ProjectID: projectID, Running: 0, Phase: PhaseIdle, IntervalSeconds: defaults.IntervalSeconds,
		ValidationCommand: defaults.ValidationCommand, ProjectPrompt: defaults.ProjectPrompt,
		AgentCLIProvider: defaults.AgentCLIProvider, AgentCLICommand: defaults.AgentCLICommand,
		CodexReasoningEffort:   nil,
		PlanGenerationStrategy: defaults.PlanGenerationStrategy,
		PlanGenerationProvider: copyString(defaults.PlanGenerationProvider),
		PlanGenerationCommand:  defaults.PlanGenerationCommand, PlanGenerationModel: defaults.PlanGenerationModel,
		PlanGenerationCodexReasoningEffort: copyString(defaults.PlanGenerationCodexReasoningEffort),
		PlanGenerationClaudeBaseURL:        defaults.PlanGenerationClaudeBaseURL,
		PlanGenerationClaudeAuthToken:      defaults.PlanGenerationClaudeAuthToken,
		PlanGenerationClaudeModel:          defaults.PlanGenerationClaudeModel,
		PlanGenerationClaudeConfigID:       defaults.PlanGenerationClaudeConfigID,
		PlanExecutionStrategy:              defaults.PlanExecutionStrategy,
		PlanExecutionProvider:              copyString(defaults.PlanExecutionProvider),
		PlanExecutionCommand:               defaults.PlanExecutionCommand, PlanExecutionModel: defaults.PlanExecutionModel,
		PlanExecutionCodexReasoningEffort: copyString(defaults.PlanExecutionCodexReasoningEffort),
		PlanExecutionClaudeBaseURL:        defaults.PlanExecutionClaudeBaseURL,
		PlanExecutionClaudeAuthToken:      defaults.PlanExecutionClaudeAuthToken,
		PlanExecutionClaudeModel:          defaults.PlanExecutionClaudeModel,
		PlanExecutionClaudeConfigID:       defaults.PlanExecutionClaudeConfigID,
		EnvVars:                           defaults.EnvVars, UpdatedAt: updatedAt, Version: InitialVersion,
	}, nil
}

func LoopConfigFromState(state ProjectState) LoopConfig {
	return LoopConfig{
		IntervalSeconds: state.IntervalSeconds, ValidationCommand: state.ValidationCommand,
		ProjectPrompt: state.ProjectPrompt, AgentCLIProvider: state.AgentCLIProvider,
		AgentCLICommand: state.AgentCLICommand, CodexReasoningEffort: copyString(state.CodexReasoningEffort),
		PlanGenerationStrategy: state.PlanGenerationStrategy,
		PlanGenerationProvider: copyString(state.PlanGenerationProvider),
		PlanGenerationCommand:  state.PlanGenerationCommand, PlanGenerationModel: state.PlanGenerationModel,
		PlanGenerationCodexReasoningEffort: copyString(state.PlanGenerationCodexReasoningEffort),
		PlanGenerationClaudeBaseURL:        state.PlanGenerationClaudeBaseURL,
		PlanGenerationClaudeAuthToken:      state.PlanGenerationClaudeAuthToken,
		PlanGenerationClaudeModel:          state.PlanGenerationClaudeModel,
		PlanGenerationClaudeConfigID:       state.PlanGenerationClaudeConfigID,
		PlanExecutionStrategy:              state.PlanExecutionStrategy,
		PlanExecutionProvider:              copyString(state.PlanExecutionProvider),
		PlanExecutionCommand:               state.PlanExecutionCommand, PlanExecutionModel: state.PlanExecutionModel,
		PlanExecutionCodexReasoningEffort: copyString(state.PlanExecutionCodexReasoningEffort),
		PlanExecutionClaudeBaseURL:        state.PlanExecutionClaudeBaseURL,
		PlanExecutionClaudeAuthToken:      state.PlanExecutionClaudeAuthToken,
		PlanExecutionClaudeModel:          state.PlanExecutionClaudeModel,
		PlanExecutionClaudeConfigID:       state.PlanExecutionClaudeConfigID, EnvVars: state.EnvVars,
	}
}

func NormalizeLoopConfig(value LoopConfig) (LoopConfig, error) {
	if value.IntervalSeconds <= 0 {
		value.IntervalSeconds = DefaultIntervalSeconds
	}
	value.AgentCLIProvider = normalizeProvider(value.AgentCLIProvider)
	value.AgentCLICommand = strings.TrimSpace(value.AgentCLICommand)
	if value.AgentCLIProvider == DefaultAgentCLIProvider {
		effort := normalizeEffort(value.CodexReasoningEffort)
		value.CodexReasoningEffort = &effort
	} else {
		value.CodexReasoningEffort = nil
	}
	value.PlanGenerationStrategy = normalizeGenerationStrategy(value.PlanGenerationStrategy)
	value.PlanExecutionStrategy = normalizeExecutionStrategy(value.PlanExecutionStrategy)
	value.PlanGenerationProvider = normalizePlanProvider(value.PlanGenerationProvider, value.PlanGenerationStrategy)
	value.PlanExecutionProvider = normalizePlanProvider(value.PlanExecutionProvider, value.PlanExecutionStrategy)
	value.PlanGenerationCodexReasoningEffort = normalizeOptionalEffort(value.PlanGenerationCodexReasoningEffort, value.PlanGenerationProvider)
	value.PlanExecutionCodexReasoningEffort = normalizeOptionalEffort(value.PlanExecutionCodexReasoningEffort, value.PlanExecutionProvider)
	value.PlanGenerationCommand = strings.TrimSpace(value.PlanGenerationCommand)
	value.PlanExecutionCommand = strings.TrimSpace(value.PlanExecutionCommand)
	value.PlanGenerationModel = strings.TrimSpace(value.PlanGenerationModel)
	value.PlanExecutionModel = strings.TrimSpace(value.PlanExecutionModel)
	value.PlanGenerationClaudeBaseURL = strings.TrimSpace(value.PlanGenerationClaudeBaseURL)
	value.PlanExecutionClaudeBaseURL = strings.TrimSpace(value.PlanExecutionClaudeBaseURL)
	value.PlanGenerationClaudeAuthToken = strings.TrimSpace(value.PlanGenerationClaudeAuthToken)
	value.PlanExecutionClaudeAuthToken = strings.TrimSpace(value.PlanExecutionClaudeAuthToken)
	value.PlanGenerationClaudeModel = strings.TrimSpace(value.PlanGenerationClaudeModel)
	value.PlanExecutionClaudeModel = strings.TrimSpace(value.PlanExecutionClaudeModel)
	if value.PlanGenerationClaudeConfigID < 0 {
		value.PlanGenerationClaudeConfigID = 0
	}
	if value.PlanExecutionClaudeConfigID < 0 {
		value.PlanExecutionClaudeConfigID = 0
	}
	if value.EnvVars != "" {
		var entries []EnvVar
		if json.Unmarshal([]byte(value.EnvVars), &entries) != nil {
			return LoopConfig{}, ErrInvalid
		}
		value.EnvVars = NormalizeEnvVars(entries)
		if value.EnvVars == "" {
			return LoopConfig{}, ErrInvalid
		}
	}
	return value, nil
}

func NormalizeEnvVars(entries []EnvVar) string {
	seen := make(map[string]struct{}, len(entries))
	normalized := make([]EnvVar, 0, len(entries))
	for _, entry := range entries {
		entry.Name = strings.TrimSpace(entry.Name)
		if entry.Name == "" {
			continue
		}
		if _, duplicate := seen[entry.Name]; duplicate {
			continue
		}
		seen[entry.Name] = struct{}{}
		normalized = append(normalized, entry)
	}
	encoded, err := json.Marshal(normalized)
	if err != nil {
		return ""
	}
	return string(encoded)
}

func ValidUTCTimestamp(value string) bool {
	if !strings.HasSuffix(value, "Z") {
		return false
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	return err == nil && parsed.Location() == time.UTC
}

func normalizeGenerationStrategy(value string) string {
	strategy := strings.ToLower(strings.TrimSpace(value))
	switch strategy {
	case "external-cli-markdown", "external-cli-structured", "builtin-llm-structured":
		return strategy
	default:
		return DefaultPlanGenerationStrategy
	}
}

func normalizeExecutionStrategy(value string) string {
	strategy := strings.ToLower(strings.TrimSpace(value))
	switch strategy {
	case "external-cli", "builtin-llm":
		return strategy
	default:
		return DefaultPlanExecutionStrategy
	}
}

func normalizePlanProvider(value *string, strategy string) *string {
	if value == nil || strings.TrimSpace(*value) == "" {
		return nil
	}
	provider := strings.ToLower(strings.TrimSpace(*value))
	if strategy == "builtin-llm" || strategy == "builtin-llm-structured" {
		switch provider {
		case "openai", "deepseek", "anthropic":
			return &provider
		default:
			provider = DefaultAgentCLIProvider
			return &provider
		}
	}
	provider = normalizeProvider(provider)
	return &provider
}

func normalizeProvider(value string) string {
	provider := strings.ToLower(strings.TrimSpace(value))
	switch provider {
	case "codex", "claude", "opencode", "oh-my-pi":
		return provider
	default:
		return DefaultAgentCLIProvider
	}
}

func normalizeOptionalProvider(value *string) *string {
	if value == nil || strings.TrimSpace(*value) == "" {
		return nil
	}
	provider := normalizeProvider(*value)
	return &provider
}

func normalizeEffort(value *string) string {
	if value != nil {
		effort := strings.ToLower(strings.TrimSpace(*value))
		switch effort {
		case "low", "medium", "high", "xhigh":
			return effort
		}
	}
	return DefaultCodexReasoningEffort
}

func normalizeOptionalEffort(value, provider *string) *string {
	if provider != nil && *provider != DefaultAgentCLIProvider {
		return nil
	}
	if value == nil || strings.TrimSpace(*value) == "" {
		return nil
	}
	effort := normalizeEffort(value)
	return &effort
}

func Equal(left, right LoopConfig) bool {
	return left.IntervalSeconds == right.IntervalSeconds &&
		left.ValidationCommand == right.ValidationCommand && left.ProjectPrompt == right.ProjectPrompt &&
		left.AgentCLIProvider == right.AgentCLIProvider && left.AgentCLICommand == right.AgentCLICommand &&
		equalString(left.CodexReasoningEffort, right.CodexReasoningEffort) &&
		left.PlanGenerationStrategy == right.PlanGenerationStrategy &&
		equalString(left.PlanGenerationProvider, right.PlanGenerationProvider) &&
		left.PlanGenerationCommand == right.PlanGenerationCommand && left.PlanGenerationModel == right.PlanGenerationModel &&
		equalString(left.PlanGenerationCodexReasoningEffort, right.PlanGenerationCodexReasoningEffort) &&
		left.PlanGenerationClaudeBaseURL == right.PlanGenerationClaudeBaseURL &&
		left.PlanGenerationClaudeAuthToken == right.PlanGenerationClaudeAuthToken &&
		left.PlanGenerationClaudeModel == right.PlanGenerationClaudeModel &&
		left.PlanGenerationClaudeConfigID == right.PlanGenerationClaudeConfigID &&
		left.PlanExecutionStrategy == right.PlanExecutionStrategy &&
		equalString(left.PlanExecutionProvider, right.PlanExecutionProvider) &&
		left.PlanExecutionCommand == right.PlanExecutionCommand && left.PlanExecutionModel == right.PlanExecutionModel &&
		equalString(left.PlanExecutionCodexReasoningEffort, right.PlanExecutionCodexReasoningEffort) &&
		left.PlanExecutionClaudeBaseURL == right.PlanExecutionClaudeBaseURL &&
		left.PlanExecutionClaudeAuthToken == right.PlanExecutionClaudeAuthToken &&
		left.PlanExecutionClaudeModel == right.PlanExecutionClaudeModel &&
		left.PlanExecutionClaudeConfigID == right.PlanExecutionClaudeConfigID && left.EnvVars == right.EnvVars
}

func copyString(value *string) *string {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}

func equalString(left, right *string) bool {
	return (left == nil && right == nil) || (left != nil && right != nil && *left == *right)
}
