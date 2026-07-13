package chat

import (
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"unicode/utf8"

	domainsecrets "github.com/lyming99/autoplan/backend/internal/domain/secrets"
)

var (
	ErrInvalidProvider = errors.New("chat provider is invalid")
	ErrInvalidChunk    = errors.New("chat provider chunk is invalid")
)

const (
	ProviderOpenAI    ProviderKind = "openai"
	ProviderAnthropic ProviderKind = "anthropic"
	ProviderClaudeCLI ProviderKind = "claude_cli"
	ProviderCodexCLI  ProviderKind = "codex_cli"

	ChunkText       ChunkKind = "text"
	ChunkReasoning  ChunkKind = "reasoning"
	ChunkToolCall   ChunkKind = "tool_call"
	ChunkToolResult ChunkKind = "tool_result"
)

const (
	maximumProviderModelBytes = 500
	maximumProviderChunkBytes = 16 << 10
)

type ProviderKind string

type ChunkKind string

func (kind ProviderKind) UsesHTTP() bool {
	return kind == ProviderOpenAI || kind == ProviderAnthropic
}

func (kind ProviderKind) UsesCLI() bool {
	return kind == ProviderClaudeCLI || kind == ProviderCodexCLI
}

func (kind ProviderKind) Valid() bool { return kind.UsesHTTP() || kind.UsesCLI() }

// Credential is an internal opaque lookup. Reference must never enter a
// transport DTO, Operation payload, event, log, command line, or process
// environment value; the platform mapper consumes it immediately before use.
type Credential struct {
	Binding   domainsecrets.Binding
	Reference string
}

type ProviderProfile struct {
	Kind            ProviderKind
	Model           string
	Endpoint        string
	Credential      *Credential
	ReasoningEffort string
}

// ProviderChunk is the provider-neutral, pre-transport streaming unit.
// Sequence is deliberately assigned by the application collector after it has
// bounded and screened the value; providers cannot choose event order.
type ProviderChunk struct {
	Kind       ChunkKind
	Text       string
	ToolCalls  *json.RawMessage
	ToolResult *json.RawMessage
}

func ValidateProviderProfile(value ProviderProfile) error {
	if !value.Kind.Valid() || !validProviderText(value.Model, maximumProviderModelBytes) {
		return ErrInvalidProvider
	}
	if value.Kind.UsesHTTP() {
		if !validProviderEndpoint(value.Endpoint) || value.Credential == nil || ValidateCredential(*value.Credential) != nil {
			return ErrInvalidProvider
		}
	} else if value.Kind == ProviderClaudeCLI {
		if value.Endpoint != "" && !validProviderEndpoint(value.Endpoint) {
			return ErrInvalidProvider
		}
		if value.Credential != nil && ValidateCredential(*value.Credential) != nil {
			return ErrInvalidProvider
		}
	} else if value.Endpoint != "" || value.Credential != nil {
		return ErrInvalidProvider
	}
	if value.ReasoningEffort != "" && value.Kind != ProviderCodexCLI {
		return ErrInvalidProvider
	}
	if value.ReasoningEffort != "" && value.ReasoningEffort != "low" && value.ReasoningEffort != "medium" &&
		value.ReasoningEffort != "high" && value.ReasoningEffort != "xhigh" {
		return ErrInvalidProvider
	}
	return nil
}

func ValidateCredential(value Credential) error {
	if domainsecrets.ValidateScope(value.Binding.Kind, value.Binding.Owner) != nil || value.Binding.Version <= 0 ||
		!validProviderSecretReference(value.Reference) {
		return ErrInvalidProvider
	}
	return nil
}

func validProviderSecretReference(value string) bool {
	if len(value) < 20 || len(value) > 200 {
		return false
	}
	for _, character := range value {
		if !(character >= 'a' && character <= 'z') && !(character >= 'A' && character <= 'Z') &&
			!(character >= '0' && character <= '9') && character != '_' && character != '-' {
			return false
		}
	}
	return true
}

func ValidateProviderChunk(value ProviderChunk) error {
	if value.Kind != ChunkText && value.Kind != ChunkReasoning && value.Kind != ChunkToolCall && value.Kind != ChunkToolResult {
		return ErrInvalidChunk
	}
	switch value.Kind {
	case ChunkText, ChunkReasoning:
		if value.Text == "" || len(value.Text) > maximumProviderChunkBytes || !utf8.ValidString(value.Text) || strings.ContainsRune(value.Text, 0) ||
			value.ToolCalls != nil || value.ToolResult != nil {
			return ErrInvalidChunk
		}
	case ChunkToolCall:
		if value.Text != "" || value.ToolCalls == nil || value.ToolResult != nil || !SafeToolCalls(value.ToolCalls) || !safeProviderToolJSON(*value.ToolCalls) {
			return ErrInvalidChunk
		}
	case ChunkToolResult:
		if value.Text != "" || value.ToolCalls != nil || value.ToolResult == nil || !SafeToolResult(value.ToolResult) || !safeProviderToolJSON(*value.ToolResult) {
			return ErrInvalidChunk
		}
	}
	return nil
}

func safeProviderToolJSON(value json.RawMessage) bool {
	lower := strings.ToLower(string(value))
	for _, marker := range []string{"bearer ", "token=", "secret=", "password=", "api_key=", "authorization:", "cookie:", "env[", "env_", "userdata"} {
		if strings.Contains(lower, marker) {
			return false
		}
	}
	if strings.Contains(lower, "\\\\") || strings.Contains(lower, "/home/") || strings.Contains(lower, "/users/") {
		return false
	}
	for index := 0; index+2 < len(value); index++ {
		if ((value[index] >= 'A' && value[index] <= 'Z') || (value[index] >= 'a' && value[index] <= 'z')) &&
			value[index+1] == ':' && (value[index+2] == '/' || value[index+2] == '\\') {
			return false
		}
	}
	return true
}

func validProviderText(value string, maximum int) bool {
	return value != "" && len(value) <= maximum && utf8.ValidString(value) && strings.TrimSpace(value) == value &&
		!strings.ContainsAny(value, "\x00\r\n")
}

func validProviderEndpoint(value string) bool {
	if len(value) == 0 || len(value) > 4096 || strings.TrimSpace(value) != value || strings.ContainsAny(value, "\x00\r\n") {
		return false
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Host == "" ||
		(parsed.Scheme != "https" && parsed.Scheme != "http") {
		return false
	}
	return true
}
