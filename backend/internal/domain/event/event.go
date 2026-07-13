// Package event owns safe, persistence-neutral audit event values.
package event

import (
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"

	domainplan "github.com/lyming99/autoplan/backend/internal/domain/plan"
)

var ErrInvalid = errors.New("event is invalid")

type Event struct {
	ID        int64
	ProjectID int64
	Type      string
	Message   string
	MetaJSON  *string
	CreatedAt string
}

// PendingEvent is written to both the compatibility events table and the
// transactional outbox. Publishing is deliberately outside this package and
// can happen only after the surrounding SQLite transaction commits.
type PendingEvent struct {
	EventID     string
	StreamKey   string
	Sequence    int64
	Type        string
	RequestID   string
	OperationID *string
	ProjectID   int64
	Message     string
	MetaJSON    *string
	OccurredAt  string
	CreatedAt   string
}

type ListOptions struct {
	ProjectID int64
	Limit     int
	Offset    int
}

func ValidateRecord(value Event) error {
	if value.ID <= 0 || value.ProjectID <= 0 || !validOpaque(value.Type, 128) ||
		!validText(value.Message, 10000, true) || !domainplan.ValidUTCTimestamp(value.CreatedAt) ||
		!validStoredMeta(value.MetaJSON) {
		return ErrInvalid
	}
	return nil
}

func ValidatePending(value PendingEvent) error {
	if value.ProjectID <= 0 || value.Sequence < 0 || !validOpaque(value.EventID, 256) ||
		!validOpaque(value.StreamKey, 512) || !validOpaque(value.Type, 128) ||
		!validOpaque(value.RequestID, 256) || !validText(value.Message, 10000, true) ||
		!domainplan.ValidUTCTimestamp(value.OccurredAt) || !domainplan.ValidUTCTimestamp(value.CreatedAt) ||
		(value.OperationID != nil && !validOpaque(*value.OperationID, 256)) || !validPendingMeta(value.MetaJSON) {
		return ErrInvalid
	}
	return nil
}

func validStoredMeta(value *string) bool {
	if value == nil {
		return true
	}
	if len(*value) > 65536 || !utf8.ValidString(*value) {
		return false
	}
	return json.Valid([]byte(*value))
}

func validPendingMeta(value *string) bool {
	if !validStoredMeta(value) || value == nil {
		return value == nil
	}
	var object any
	decoder := json.NewDecoder(strings.NewReader(*value))
	decoder.UseNumber()
	if decoder.Decode(&object) != nil || object == nil {
		return false
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return false
	}
	if _, ok := object.(map[string]any); !ok {
		return false
	}
	return !containsSensitiveValue(object)
}

func containsSensitiveValue(value any) bool {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if sensitiveKey(key) || containsSensitiveValue(child) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if containsSensitiveValue(child) {
				return true
			}
		}
	case string:
		return unsafePathLikeString(typed)
	}
	return false
}

func sensitiveKey(value string) bool {
	key := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(value, "-", "_"), " ", "_"))
	for _, forbidden := range []string{
		"token", "secret", "password", "api_key", "apikey", "auth", "session", "stored_path", "file_path", "filepath", "workspace_path",
	} {
		if strings.Contains(key, forbidden) {
			return true
		}
	}
	return false
}

// SanitizeMetaJSON removes credential, session, and filesystem-capability
// fields from historical event metadata before it crosses the repository
// boundary. New event writes are rejected instead (ValidatePending), so this
// function is only a compatibility read guard.
func SanitizeMetaJSON(value *string) *string {
	if !validStoredMeta(value) || value == nil {
		return nil
	}
	var decoded any
	decoder := json.NewDecoder(strings.NewReader(*value))
	decoder.UseNumber()
	if decoder.Decode(&decoded) != nil {
		return nil
	}
	safe, keep := sanitizeValue(decoded)
	if !keep {
		return nil
	}
	encoded, err := json.Marshal(safe)
	if err != nil {
		return nil
	}
	result := string(encoded)
	return &result
}

func sanitizeValue(value any) (any, bool) {
	switch typed := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(typed))
		for key, child := range typed {
			if sensitiveKey(key) {
				continue
			}
			if normalized, keep := sanitizeValue(child); keep {
				result[key] = normalized
			}
		}
		return result, true
	case []any:
		result := make([]any, 0, len(typed))
		for _, child := range typed {
			if normalized, keep := sanitizeValue(child); keep {
				result = append(result, normalized)
			}
		}
		return result, true
	case string:
		if unsafePathLikeString(typed) {
			return nil, false
		}
	}
	return value, true
}

func unsafePathLikeString(value string) bool {
	trimmed := strings.TrimSpace(value)
	lower := strings.ToLower(trimmed)
	return strings.HasPrefix(lower, "file:") || strings.HasPrefix(trimmed, "/") ||
		(len(trimmed) >= 3 && trimmed[1] == ':' && (trimmed[2] == '\\' || trimmed[2] == '/'))
}

func validOpaque(value string, maximum int) bool {
	return value != "" && len(value) <= maximum && strings.TrimSpace(value) == value && !strings.ContainsFunc(value, unicode.IsControl)
}

func validText(value string, maximum int, emptyAllowed bool) bool {
	return utf8.ValidString(value) && !strings.ContainsRune(value, 0) && utf8.RuneCountInString(value) <= maximum &&
		(emptyAllowed || strings.TrimSpace(value) != "")
}
