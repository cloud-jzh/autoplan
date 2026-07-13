// Package agentcli adapts the supported Agent CLIs to the policy-constrained
// Process Runner. It owns argument construction and session metadata only;
// callers own Operations, persistence and transport events.
package agentcli

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	filesapp "github.com/lyming99/autoplan/backend/internal/application/files"
	domainsecrets "github.com/lyming99/autoplan/backend/internal/domain/secrets"
)

const (
	ProviderCodex    Provider = "codex"
	ProviderClaude   Provider = "claude"
	ProviderOpenCode Provider = "opencode"
	ProviderOhMyPi   Provider = "oh-my-pi"

	DefaultTimeout       = 45 * time.Minute
	DefaultReasoning     = "medium"
	maximumPromptBytes   = 512 << 10
	maximumInlinePrompt  = 8 << 10
	maximumSessionTitle  = 160
	maximumSessionLookup = 50
)

var (
	ErrUnavailable        = errors.New("agent cli service is unavailable")
	ErrInvalidRequest     = errors.New("agent cli request is invalid")
	ErrUnknownProvider    = errors.New("agent cli provider is unknown")
	ErrIncompleteConfig   = errors.New("agent cli configuration is incomplete")
	ErrPromptTransport    = errors.New("agent cli prompt transport is unavailable")
	ErrControlledArtifact = errors.New("agent cli controlled artifact failed")
	ErrOutputParse        = errors.New("agent cli output could not be parsed")
	ErrExecution          = errors.New("agent cli execution failed")
)

type Provider string

func (provider Provider) Valid() bool {
	switch provider {
	case ProviderCodex, ProviderClaude, ProviderOpenCode, ProviderOhMyPi:
		return true
	default:
		return false
	}
}

func (provider Provider) SupportsSession() bool {
	return provider == ProviderCodex || provider == ProviderClaude || provider == ProviderOpenCode
}

func defaultCommand(provider Provider) string {
	if provider == ProviderOhMyPi {
		return "omp"
	}
	return string(provider)
}

type PromptMode string

const (
	PromptStdin    PromptMode = "stdin"
	PromptArgument PromptMode = "argument"
)

// Request contains only internal runtime data. It is intentionally unsuitable
// for JSON transport: prompt, output paths, commands and secret selection are
// never exposed in REST, SSE, logs or ordinary application DTOs.
type Request struct {
	ProjectID        int64
	PlanID           int64
	Workspace        string
	WorkingDirectory string
	Prompt           string
	Provider         Provider
	Command          string
	Timeout          time.Duration
	LastOutputFile   string
	ReasoningEffort  string
	Session          Session
	PlanGeneration   bool
	OpenCodeTitle    string
	OpenCodeAgent    string
	ClaudeBaseURL    string
	ClaudeModel      string
	ClaudeAuthToken  *SecretRequest
}

// SecretRequest names an already-owned secret. No plaintext, provider name or
// opaque reference can enter an Agent CLI request from an external caller.
type SecretRequest struct {
	Kind  domainsecrets.Kind
	Owner domainsecrets.Owner
}

// Prepared is private launch material. It is held only for a single call and
// none of its fields are included in Result.
type Prepared struct {
	Executable  string
	Arguments   []string
	PromptMode  PromptMode
	Prompt      string
	Environment map[string]string
	Parser      ParserKind
	Session     Session
	Cleanup     func(context.Context) error
}

// Adapter is one provider's argument and prompt-channel contract.
type Adapter interface {
	Provider() Provider
	Prepare(context.Context, Request, ArtifactWriter) (Prepared, error)
}

// ArtifactWriter is implemented by the Files Policy-backed controlled writer.
// It is injected so adapters never use os.WriteFile or arbitrary workspace
// paths themselves.
type ArtifactWriter interface {
	EnsureOpenCodePlanAgent(context.Context, string) (string, error)
	AuthorizeAgentOutput(context.Context, string, string) error
	WriteOpenCodePrompt(context.Context, string, string) (filesapp.PromptAttachment, error)
	RemoveOpenCodePrompt(context.Context, filesapp.PromptAttachment) error
}

func validRequest(request Request) bool {
	if request.ProjectID <= 0 || !validTextPath(request.Workspace) || !validTextPath(request.WorkingDirectory) ||
		!validPrompt(request.Prompt) || !validCommand(request.Command) ||
		!validEnvironmentOverlay(request.ClaudeBaseURL) || !validEnvironmentOverlay(request.ClaudeModel) ||
		(request.Timeout < 0 || request.Timeout > 2*time.Hour) {
		return false
	}
	if request.Provider == ProviderCodex && !validTextPath(request.LastOutputFile) {
		return false
	}
	if request.ClaudeAuthToken != nil && domainsecrets.ValidateScope(request.ClaudeAuthToken.Kind, request.ClaudeAuthToken.Owner) != nil {
		return false
	}
	return true
}

func validPrompt(value string) bool {
	return value != "" && len(value) <= maximumPromptBytes && utf8.ValidString(value) && !strings.ContainsRune(value, 0)
}

func validTextPath(value string) bool {
	return value != "" && strings.TrimSpace(value) == value && len(value) <= 4096 && !strings.ContainsRune(value, 0)
}

func validCommand(value string) bool {
	if value == "" {
		return true
	}
	if strings.TrimSpace(value) != value || len(value) > 4096 || strings.ContainsAny(value, "\x00\r\n") {
		return false
	}
	// A bare command may not contain whitespace, preventing command-string
	// parsing. Executable paths may contain spaces when they include a path
	// separator and are still passed as one exec argument.
	return strings.ContainsAny(value, `\\/`) || !strings.ContainsAny(value, " \t")
}

func validEnvironmentOverlay(value string) bool {
	return value == "" || len(value) <= 64<<10 && utf8.ValidString(value) && !strings.ContainsAny(value, "\x00\r\n")
}

func resolvedCommand(provider Provider, command string) (string, error) {
	if !provider.Valid() || !validCommand(command) {
		return "", ErrIncompleteConfig
	}
	if command == "" {
		return defaultCommand(provider), nil
	}
	return command, nil
}

func effectiveTimeout(value time.Duration) time.Duration {
	if value <= 0 {
		return DefaultTimeout
	}
	return value
}

func copyArguments(values []string) []string { return append([]string(nil), values...) }

var _ ArtifactWriter = (*filesapp.ControlledWriter)(nil)
