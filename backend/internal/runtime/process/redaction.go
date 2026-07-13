package process

import (
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

var (
	environmentAssignmentPattern = regexp.MustCompile(`(?m)^(?:export\s+)?[A-Za-z_][A-Za-z0-9_]*=.*$`)
	promptPattern                = regexp.MustCompile(`(?m)^(?:PS\s+)?(?:[A-Za-z]:[^\r\n>]*|[~\/][^\r\n>]*)>\s*`)
	sensitiveOutputPattern       = regexp.MustCompile(`(?im)\b(?:password|passphrase|secret|token|api[_-]?key|authorization|cookie)\b\s*(?:=|:)\s*[^\r\n]*`)
	pathPattern                  = regexp.MustCompile(`(?i)(?:[a-z]:[\\/][^\s\r\n"'<>|]*|\\\\[^\s\r\n"'<>|]+|/(?:[^\s\r\n"'<>|]+/?)+)`)
)

// Redactor removes material which is unsafe to persist in a process result.
// It deliberately works on the bounded tail only; the runner never creates an
// unbounded in-memory copy of a child stream.
type Redactor struct {
	values            []string
	paths             []string
	hasMultilineValue bool
}

func NewRedactor(environment map[string]string, locations ...string) Redactor {
	valueSet := make(map[string]struct{}, len(environment))
	for _, value := range environment {
		if value != "" && utf8.ValidString(value) {
			valueSet[value] = struct{}{}
		}
	}
	pathSet := make(map[string]struct{}, len(locations))
	for _, location := range locations {
		if location == "" || !utf8.ValidString(location) {
			continue
		}
		pathSet[location] = struct{}{}
		pathSet[filepath.Clean(location)] = struct{}{}
	}
	values := make([]string, 0, len(valueSet))
	for value := range valueSet {
		values = append(values, value)
	}
	paths := make([]string, 0, len(pathSet))
	for value := range pathSet {
		paths = append(paths, value)
	}
	sort.Slice(values, func(left, right int) bool { return len(values[left]) > len(values[right]) })
	sort.Slice(paths, func(left, right int) bool { return len(paths[left]) > len(paths[right]) })
	return Redactor{values: values, paths: paths, hasMultilineValue: hasMultiline(values)}
}

// WithSensitiveValues extends a redactor with transient values injected by
// stdin secret references. The returned copy has no provider metadata and is
// used only while the child is alive.
func (redactor Redactor) WithSensitiveValues(sensitive ...string) Redactor {
	valueSet := make(map[string]struct{}, len(redactor.values)+len(sensitive))
	for _, value := range redactor.values {
		if value != "" && utf8.ValidString(value) {
			valueSet[value] = struct{}{}
		}
	}
	for _, value := range sensitive {
		if value != "" && utf8.ValidString(value) {
			valueSet[value] = struct{}{}
		}
	}
	values := make([]string, 0, len(valueSet))
	for value := range valueSet {
		values = append(values, value)
	}
	sort.Slice(values, func(left, right int) bool { return len(values[left]) > len(values[right]) })
	redactor.values = values
	redactor.hasMultilineValue = hasMultiline(values)
	return redactor
}

func (redactor Redactor) WithPaths(locations ...string) Redactor {
	pathSet := make(map[string]struct{}, len(redactor.paths)+len(locations)*2)
	for _, value := range redactor.paths {
		if value != "" && utf8.ValidString(value) {
			pathSet[value] = struct{}{}
		}
	}
	for _, location := range locations {
		if location == "" || !utf8.ValidString(location) {
			continue
		}
		pathSet[location] = struct{}{}
		pathSet[filepath.Clean(location)] = struct{}{}
	}
	paths := make([]string, 0, len(pathSet))
	for value := range pathSet {
		paths = append(paths, value)
	}
	sort.Slice(paths, func(left, right int) bool { return len(paths[left]) > len(paths[right]) })
	redactor.paths = paths
	return redactor
}

// Redact returns false only when its input cannot be safely represented as
// text. Callers then discard the tail rather than risking an unsafe result.
func (redactor Redactor) Redact(value string) (string, bool) {
	if strings.ContainsRune(value, 0) || !utf8.ValidString(value) {
		return "", false
	}
	for _, secret := range redactor.values {
		value = strings.ReplaceAll(value, secret, "<redacted>")
	}
	for _, location := range redactor.paths {
		value = strings.ReplaceAll(value, location, "<path>")
	}
	value = environmentAssignmentPattern.ReplaceAllString(value, "<redacted environment>")
	value = promptPattern.ReplaceAllString(value, "<prompt>")
	value = sensitiveOutputPattern.ReplaceAllString(value, "<redacted sensitive output>")
	value = pathPattern.ReplaceAllString(value, "<path>")
	return value, true
}

// streamingRedactor holds only an unfinished line before redaction. This
// catches values split across os/exec writes while keeping raw process bytes
// out of the retained tail. If a child emits an overlong unterminated line (or
// a multiline secret cannot be proven safe), the collector discards it rather
// than risking a partial secret in memory or a later transport.
type streamingRedactor struct {
	redactor Redactor
	limit    int
	hold     int
	pending  string
	failed   bool
}

func newStreamingRedactor(redactor Redactor, pendingLimit int) *streamingRedactor {
	if pendingLimit <= 0 {
		pendingLimit = 1
	}
	hold := redactor.boundaryBytes()
	return &streamingRedactor{redactor: redactor, limit: pendingLimit, hold: hold}
}

func (stream *streamingRedactor) RedactChunk(value string) (string, bool) {
	if stream == nil {
		return value, true
	}
	if stream.failed || !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
		stream.failed = true
		stream.pending = ""
		return "", false
	}
	stream.pending += value
	if stream.redactor.hasMultilineValue {
		if len(stream.pending) > stream.limit {
			stream.failed = true
			stream.pending = ""
			return "", false
		}
		return "", true
	}
	cut := strings.LastIndex(stream.pending, "\n") + 1
	if cut == 0 {
		if len(stream.pending) <= stream.limit {
			return "", true
		}
		if stream.hold >= stream.limit {
			stream.failed = true
			stream.pending = ""
			return "", false
		}
		cut = len(stream.pending) - stream.hold
		for cut > 0 && !utf8.RuneStart(stream.pending[cut]) {
			cut--
		}
		if cut == 0 {
			stream.failed = true
			stream.pending = ""
			return "", false
		}
	}
	ready := stream.pending[:cut]
	stream.pending = stream.pending[cut:]
	redacted, safe := stream.redactor.Redact(ready)
	if !safe {
		stream.failed = true
		stream.pending = ""
		return "", false
	}
	return redacted, true
}

func (stream *streamingRedactor) Flush() (string, bool) {
	if stream == nil {
		return "", true
	}
	if stream.failed {
		return "", false
	}
	value := stream.pending
	stream.pending = ""
	redacted, safe := stream.redactor.Redact(value)
	if !safe {
		stream.failed = true
		return "", false
	}
	return redacted, true
}

func hasMultiline(values []string) bool {
	for _, value := range values {
		if strings.ContainsAny(value, "\r\n") {
			return true
		}
	}
	return false
}

func (redactor Redactor) boundaryBytes() int {
	maximum := 1
	for _, value := range redactor.values {
		if len(value) > maximum {
			maximum = len(value)
		}
	}
	for _, value := range redactor.paths {
		if len(value) > maximum {
			maximum = len(value)
		}
	}
	// Generic path/prompt patterns may cross a write boundary even when no
	// configured path is present. Retaining this finite suffix avoids exposing
	// a partial system path while still allowing a no-newline stream to drain.
	if maximum < 4096 {
		maximum = 4096
	}
	return maximum
}
