// Package logging emits fixed-schema, single-line JSON events. It does not
// accept arbitrary maps, request bodies, headers, paths, or error chains.
package logging

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/lyming99/autoplan/backend/internal/platform/redaction"
)

type Clock interface {
	Now() time.Time
}

type Logger interface {
	Log(Event) error
}

// Event is the complete logging allowlist for the HTTP foundation.
type Event struct {
	OccurredAt time.Time `json:"occurred_at"`
	Level      string    `json:"level"`
	Code       string    `json:"code"`
	ErrorCode  string    `json:"error_code,omitempty"`
	RequestID  string    `json:"request_id,omitempty"`
	Method     string    `json:"method,omitempty"`
	Route      string    `json:"route,omitempty"`
	Status     int       `json:"status,omitempty"`
	DurationMS int64     `json:"duration_ms,omitempty"`
	Retryable  bool      `json:"retryable"`
}

var safeToken = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._:/-]{0,127}$`)
var safeRoute = regexp.MustCompile(`^/[a-zA-Z0-9._~!$&'()*+,;=:@%/-]{0,255}$`)

type JSONLogger struct {
	mu     sync.Mutex
	writer io.Writer
	clock  Clock
}

func NewJSONLogger(writer io.Writer, clock Clock) (*JSONLogger, error) {
	if writer == nil || clock == nil {
		return nil, errors.New("logging dependency is missing")
	}
	return &JSONLogger{writer: writer, clock: clock}, nil
}

func (logger *JSONLogger) Log(event Event) error {
	event = normalize(event, logger.clock)
	logger.mu.Lock()
	defer logger.mu.Unlock()
	return json.NewEncoder(logger.writer).Encode(event)
}

type Nop struct{}

func (Nop) Log(Event) error { return nil }

// StandardLogger adapts standard-library server diagnostics without forwarding
// their free-form text, which may contain paths, URLs, or parser details.
func StandardLogger(logger Logger, clock Clock) *log.Logger {
	return log.New(standardWriter{logger: logger, clock: clock}, "", 0)
}

type standardWriter struct {
	logger Logger
	clock  Clock
}

func (writer standardWriter) Write(content []byte) (written int, err error) {
	defer func() {
		if recover() != nil {
			written = len(content)
			err = nil
		}
	}()
	if writer.logger != nil && writer.clock != nil {
		_ = writer.logger.Log(Event{
			OccurredAt: clockTime(writer.clock), Level: "warn",
			Code: "server_diagnostic_redacted", Retryable: false,
		})
	}
	return len(content), nil
}

func normalize(event Event, clock Clock) Event {
	if event.OccurredAt.IsZero() {
		event.OccurredAt = clockTime(clock)
	}
	event.OccurredAt = event.OccurredAt.UTC()
	event.Level = safeValue(strings.ToLower(event.Level), "info")
	event.Code = safeValue(event.Code, "invalid_log_code")
	event.ErrorCode = safeOptional(event.ErrorCode)
	event.RequestID = safeOptional(event.RequestID)
	event.Method = safeOptional(event.Method)
	if event.Route != "" && event.Route != "unmatched" && !safeRoute.MatchString(event.Route) {
		event.Route = "redacted"
	}
	if event.Status < 0 || event.Status > 999 {
		event.Status = 0
	}
	if event.DurationMS < 0 {
		event.DurationMS = 0
	}
	return event
}

func clockTime(clock Clock) (result time.Time) {
	defer func() {
		if recover() != nil {
			result = time.Now().UTC()
		}
	}()
	result = clock.Now().UTC()
	if result.IsZero() {
		return time.Now().UTC()
	}
	return result
}

func safeValue(value, fallback string) string {
	value = redaction.String(value)
	if !safeToken.MatchString(value) {
		return fallback
	}
	return value
}

func safeOptional(value string) string {
	if value == "" {
		return ""
	}
	return safeValue(value, "redacted")
}
