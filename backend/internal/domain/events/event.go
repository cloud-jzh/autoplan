// Package events owns the P10 durable-event envelope. It is distinct from the
// pre-existing compatibility audit-event model: this package defines the
// ordered outbox/SSE contract consumed by the Operation runtime.
package events

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const SchemaVersion = 1

var (
	ErrInvalid = errors.New("event envelope is invalid")

	identifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
	requestIDPattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,63}$`)
	eventIDPattern    = regexp.MustCompile(`^[1-9][0-9]{0,18}$`)
	businessType      = regexp.MustCompile(`^(?:project\.(?:snapshot|patch)|business\.[a-z][a-z0-9]*(?:[._:-][a-z0-9]+)*)$`)
)

// Class separates durable business and Operation events from connection-local
// control messages. Control messages never consume event_id or project_revision.
type Class string

const (
	ClassBusiness  Class = "business"
	ClassOperation Class = "operation"
	ClassControl   Class = "control"
)

const (
	TypeProjectSnapshot      = "project.snapshot"
	TypeProjectPatch         = "project.patch"
	TypeOperationQueued      = "operation.queued"
	TypeOperationRunning     = "operation.running"
	TypeOperationSucceeded   = "operation.succeeded"
	TypeOperationFailed      = "operation.failed"
	TypeOperationCancelled   = "operation.cancelled"
	TypeOperationInterrupted = "operation.interrupted"
	TypeHeartbeat            = "heartbeat"
	TypeResyncRequired       = "resync_required"
)

// Envelope is the JSON shape stored in and replayed from the P10 outbox.
// EventID is a decimal string so every supported client can preserve its
// monotonic ordering without losing integer precision.
type Envelope struct {
	SchemaVersion   int             `json:"schema_version"`
	Class           Class           `json:"event_class"`
	EventID         *string         `json:"event_id"`
	ProjectID       int64           `json:"project_id"`
	ProjectRevision *int64          `json:"project_revision"`
	Type            string          `json:"type"`
	OperationID     *string         `json:"operation_id"`
	RequestID       *string         `json:"request_id"`
	OccurredAt      string          `json:"occurred_at"`
	Payload         json.RawMessage `json:"payload"`
}

func (value Envelope) Persistent() bool {
	return value.Class == ClassBusiness || value.Class == ClassOperation
}

func (value Envelope) Validate() error {
	if value.SchemaVersion != SchemaVersion || value.ProjectID <= 0 || !validTimestamp(value.OccurredAt) ||
		!validPayload(value.Payload) || !validOptionalIdentifier(value.OperationID) || !validOptionalRequestID(value.RequestID) {
		return ErrInvalid
	}
	switch value.Class {
	case ClassBusiness:
		if !validPersistentIdentity(value.EventID, value.ProjectRevision) || value.RequestID == nil || !businessType.MatchString(value.Type) {
			return ErrInvalid
		}
	case ClassOperation:
		if !validPersistentIdentity(value.EventID, value.ProjectRevision) || value.RequestID == nil || value.OperationID == nil || !operationType(value.Type) {
			return ErrInvalid
		}
	case ClassControl:
		if value.EventID != nil || value.ProjectRevision != nil || (value.Type != TypeHeartbeat && value.Type != TypeResyncRequired) {
			return ErrInvalid
		}
		if value.Type == TypeHeartbeat && (value.OperationID != nil || value.RequestID != nil) {
			return ErrInvalid
		}
		if value.Type == TypeHeartbeat && !bytes.Equal(bytes.TrimSpace(value.Payload), []byte("{}")) {
			return ErrInvalid
		}
		if value.Type == TypeResyncRequired && !validResyncPayload(value.Payload) {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	return nil
}

func validPersistentIdentity(eventID *string, revision *int64) bool {
	return eventID != nil && eventIDPattern.MatchString(*eventID) && revision != nil && *revision > 0
}

func operationType(value string) bool {
	switch value {
	case TypeOperationQueued, TypeOperationRunning, TypeOperationSucceeded, TypeOperationFailed, TypeOperationCancelled, TypeOperationInterrupted:
		return true
	default:
		return false
	}
}

func validTimestamp(value string) bool {
	if !strings.HasSuffix(value, "Z") {
		return false
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	return err == nil && parsed.Location() == time.UTC
}

func validOptionalIdentifier(value *string) bool {
	return value == nil || identifierPattern.MatchString(*value)
}
func validOptionalRequestID(value *string) bool {
	return value == nil || requestIDPattern.MatchString(*value)
}

func validPayload(value json.RawMessage) bool {
	if len(value) == 0 || len(value) > 8192 || !utf8.Valid(value) {
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(value))
	decoder.UseNumber()
	var decoded any
	if decoder.Decode(&decoded) != nil {
		return false
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return false
	}
	if _, ok := decoded.(map[string]any); !ok {
		return false
	}
	return safePayloadValue(decoded)
}

func validResyncPayload(value json.RawMessage) bool {
	var object map[string]json.RawMessage
	if json.Unmarshal(value, &object) != nil || len(object) != 1 {
		return false
	}
	raw, exists := object["reason"]
	if !exists {
		return false
	}
	var reason string
	if json.Unmarshal(raw, &reason) != nil {
		return false
	}
	switch reason {
	case "last_event_id_invalid", "last_event_id_future", "history_expired", "revision_gap", "project_mismatch", "slow_consumer":
		return true
	default:
		return false
	}
}

func safePayloadValue(value any) bool {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if sensitiveKey(key) || !safePayloadValue(child) {
				return false
			}
		}
	case []any:
		for _, child := range typed {
			if !safePayloadValue(child) {
				return false
			}
		}
	case string:
		return safeText(typed)
	}
	return true
}

func sensitiveKey(value string) bool {
	key := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(value, "-", "_"), " ", "_"))
	for _, forbidden := range []string{"token", "secret", "password", "api_key", "apikey", "authorization", "cookie", "credential", "private_key", "env", "path", "userdata", "user_data", "command", "stdout", "stderr"} {
		if strings.Contains(key, forbidden) {
			return true
		}
	}
	return false
}

func safeText(value string) bool {
	trimmed := strings.TrimSpace(value)
	if len(value) > 2048 || !utf8.ValidString(value) || strings.ContainsRune(value, 0) || strings.ContainsFunc(value, unicode.IsControl) ||
		strings.HasPrefix(trimmed, "/") || strings.HasPrefix(strings.ToLower(trimmed), "file:") ||
		(len(trimmed) >= 3 && trimmed[1] == ':' && (trimmed[2] == '\\' || trimmed[2] == '/')) {
		return false
	}
	lower := strings.ToLower(value)
	for _, marker := range []string{"bearer ", "token=", "secret=", "password=", "api_key=", "authorization:", "cookie:", "env[", "env_", "userdata", "user data"} {
		if strings.Contains(lower, marker) {
			return false
		}
	}
	return true
}
