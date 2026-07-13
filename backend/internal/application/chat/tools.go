package chat

import (
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"

	domainchat "github.com/lyming99/autoplan/backend/internal/domain/chat"
)

var (
	ErrProviderOutputLimit = errors.New("chat provider output exceeds limit")
	ErrProviderOutputSafe  = errors.New("chat provider output is unsafe")
)

const (
	maximumChatTurnBytes = 1000000
	chunkHoldBytes       = 256
)

// StreamChunk has a sequence assigned only after a provider value has passed
// UTF-8, size, cross-boundary secret and path screening.
type StreamChunk struct {
	Sequence   int64
	Kind       domainchat.ChunkKind
	Text       string
	ToolCalls  *json.RawMessage
	ToolResult *json.RawMessage
}

// ChunkCollector keeps only a short delayed suffix. Delaying that suffix lets
// it reject a token or absolute path split across provider chunks without
// holding unbounded output in memory.
type ChunkCollector struct {
	next        int64
	total       int
	pending     string
	pendingKind domainchat.ChunkKind
	terminated  bool
}

func (collector *ChunkCollector) Push(value domainchat.ProviderChunk) ([]StreamChunk, error) {
	if collector == nil || collector.terminated || domainchat.ValidateProviderChunk(value) != nil {
		return nil, ErrProviderOutputSafe
	}
	switch value.Kind {
	case domainchat.ChunkText:
		return collector.pushText(value.Kind, value.Text)
	case domainchat.ChunkReasoning:
		return collector.pushText(value.Kind, value.Text)
	case domainchat.ChunkToolCall:
		return collector.nextChunk(value.Kind, "", copyProviderJSON(value.ToolCalls), nil), nil
	case domainchat.ChunkToolResult:
		return collector.nextChunk(value.Kind, "", nil, copyProviderJSON(value.ToolResult)), nil
	default:
		return nil, ErrProviderOutputSafe
	}
}

func (collector *ChunkCollector) Flush() ([]StreamChunk, error) {
	if collector == nil || collector.terminated {
		return nil, ErrProviderOutputSafe
	}
	collector.terminated = true
	if collector.pending == "" {
		return nil, nil
	}
	if !safeProviderText(collector.pending) {
		collector.pending = ""
		return nil, ErrProviderOutputSafe
	}
	return collector.drainPending(), nil
}

func (collector *ChunkCollector) pushText(kind domainchat.ChunkKind, value string) ([]StreamChunk, error) {
	if !utf8.ValidString(value) || strings.ContainsRune(value, 0) || collector.total > maximumChatTurnBytes-len(value) {
		return nil, ErrProviderOutputLimit
	}
	result := make([]StreamChunk, 0, 1)
	if collector.pending != "" && collector.pendingKind != kind {
		result = append(result, collector.drainPending()...)
	}
	collector.total += len(value)
	combined := collector.pending + value
	if !safeProviderText(combined) {
		collector.pending = ""
		return nil, ErrProviderOutputSafe
	}
	if len(combined) <= chunkHoldBytes {
		collector.pending = combined
		collector.pendingKind = kind
		return result, nil
	}
	cut := len(combined) - chunkHoldBytes
	for cut > 0 && !utf8.RuneStart(combined[cut]) {
		cut--
	}
	if cut == 0 {
		return nil, ErrProviderOutputSafe
	}
	emit, pending := combined[:cut], combined[cut:]
	collector.pending = pending
	collector.pendingKind = kind
	return append(result, collector.nextChunk(kind, emit, nil, nil)...), nil
}

func (collector *ChunkCollector) drainPending() []StreamChunk {
	if collector.pending == "" {
		return nil
	}
	value, kind := collector.pending, collector.pendingKind
	collector.pending, collector.pendingKind = "", ""
	if kind == "" {
		kind = domainchat.ChunkText
	}
	return collector.nextChunk(kind, value, nil, nil)
}

func (collector *ChunkCollector) nextChunk(kind domainchat.ChunkKind, text string, calls, result *json.RawMessage) []StreamChunk {
	collector.next++
	return []StreamChunk{{Sequence: collector.next, Kind: kind, Text: text, ToolCalls: calls, ToolResult: result}}
}

func copyProviderJSON(value *json.RawMessage) *json.RawMessage {
	if value == nil {
		return nil
	}
	copy := append(json.RawMessage(nil), (*value)...)
	return &copy
}

func safeProviderText(value string) bool {
	if value == "" || !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
		return false
	}
	lower := strings.ToLower(value)
	for _, marker := range []string{
		"bearer ", "token=", "secret=", "password=", "api_key=", "authorization:", "cookie:",
		"env[", "env_", "userdata", "user data",
	} {
		if strings.Contains(lower, marker) {
			return false
		}
	}
	trimmed := strings.TrimSpace(value)
	if strings.HasPrefix(trimmed, "/") || strings.HasPrefix(strings.ToLower(trimmed), "file:") {
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
