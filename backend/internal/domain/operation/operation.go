// Package operation owns the persistence-neutral P10 long-running operation
// contract. It deliberately contains no transport, database, runner, or clock
// dependency so every adapter applies the same state and recovery rules.
package operation

import (
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	// MaximumInputChunkBytes bounds one read from a child stdout or stderr pipe.
	MaximumInputChunkBytes = 16 << 10
	// MaximumPersistedStreamBytes is the post-redaction limit for either stream.
	MaximumPersistedStreamBytes = 64 << 10
	// MaximumPersistedStreamLines is the post-redaction line limit for either stream.
	MaximumPersistedStreamLines = 1024
	// MaximumErrorSummaryBytes is the public, redacted error-summary limit.
	MaximumErrorSummaryBytes = 1024
)

var (
	ErrInvalid           = errors.New("operation is invalid")
	ErrInvalidTransition = errors.New("operation state transition is invalid")

	identifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
	requestIDPattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,63}$`)
	typePattern       = regexp.MustCompile(`^[a-z][a-z0-9]*(?:[._:-][a-z0-9]+)*$`)
	digestPattern     = regexp.MustCompile(`^[a-f0-9]{64}$`)
	errorCodePattern  = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)
)

// Status is the complete, closed lifecycle vocabulary for an Operation.
type Status string

const (
	StatusQueued      Status = "queued"
	StatusRunning     Status = "running"
	StatusSucceeded   Status = "succeeded"
	StatusFailed      Status = "failed"
	StatusCancelled   Status = "cancelled"
	StatusInterrupted Status = "interrupted"
)

func (status Status) Valid() bool {
	switch status {
	case StatusQueued, StatusRunning, StatusSucceeded, StatusFailed, StatusCancelled, StatusInterrupted:
		return true
	default:
		return false
	}
}

func (status Status) Terminal() bool {
	switch status {
	case StatusSucceeded, StatusFailed, StatusCancelled, StatusInterrupted:
		return true
	default:
		return false
	}
}

// ErrorSummary is the only failure detail that belongs to an Operation. It
// must already be redacted; raw command output and raw request content are not
// modelled by this contract.
type ErrorSummary struct {
	Code    string `json:"code"`
	Summary string `json:"summary"`
}

func (summary ErrorSummary) Validate() error {
	if !errorCodePattern.MatchString(summary.Code) || !validSafeText(summary.Summary, MaximumErrorSummaryBytes) {
		return ErrInvalid
	}
	return nil
}

// OutputMetadata carries only bounded accounting information. It intentionally
// has no stdout or stderr text fields, preventing terminal output from becoming
// part of an Operation DTO or event payload.
type OutputMetadata struct {
	StdoutBytes     int64 `json:"stdout_bytes"`
	StdoutLines     int64 `json:"stdout_lines"`
	StdoutTruncated bool  `json:"stdout_truncated"`
	StderrBytes     int64 `json:"stderr_bytes"`
	StderrLines     int64 `json:"stderr_lines"`
	StderrTruncated bool  `json:"stderr_truncated"`
	RedactionFailed bool  `json:"redaction_failed"`
}

func (metadata OutputMetadata) Validate() error {
	if metadata.StdoutBytes < 0 || metadata.StderrBytes < 0 || metadata.StdoutLines < 0 || metadata.StderrLines < 0 ||
		metadata.StdoutBytes > MaximumPersistedStreamBytes || metadata.StderrBytes > MaximumPersistedStreamBytes ||
		metadata.StdoutLines > MaximumPersistedStreamLines || metadata.StderrLines > MaximumPersistedStreamLines {
		return ErrInvalid
	}
	return nil
}

// Operation is the stored lifecycle record. RequestDigest is a SHA-256 digest
// of canonicalized request intent; the source request is intentionally absent.
type Operation struct {
	OperationID       string           `json:"operation_id"`
	ProjectID         int64            `json:"project_id"`
	Type              string           `json:"type"`
	Status            Status           `json:"status"`
	RequestID         string           `json:"request_id"`
	IdempotencyKey    *string          `json:"idempotency_key"`
	RequestDigest     string           `json:"request_digest"`
	Version           int64            `json:"version"`
	CancelRequestedAt *string          `json:"cancel_requested_at"`
	CreatedAt         string           `json:"created_at"`
	UpdatedAt         string           `json:"updated_at"`
	StartedAt         *string          `json:"started_at"`
	FinishedAt        *string          `json:"finished_at"`
	Result            *json.RawMessage `json:"result"`
	Error             *ErrorSummary    `json:"error"`
	Output            *OutputMetadata  `json:"-"`
}

func (value Operation) Validate() error {
	if !identifierPattern.MatchString(value.OperationID) || value.ProjectID <= 0 || !typePattern.MatchString(value.Type) ||
		!value.Status.Valid() || !requestIDPattern.MatchString(value.RequestID) || !digestPattern.MatchString(value.RequestDigest) ||
		value.Version <= 0 || (value.IdempotencyKey != nil && !identifierPattern.MatchString(*value.IdempotencyKey)) ||
		!validTimestamp(value.CreatedAt) || !validTimestamp(value.UpdatedAt) || later(value.CreatedAt, value.UpdatedAt) ||
		!validOptionalTimestamp(value.CancelRequestedAt) || !validOptionalTimestamp(value.StartedAt) ||
		!validOptionalTimestamp(value.FinishedAt) || !validOptionalResult(value.Result) {
		return ErrInvalid
	}
	if value.CancelRequestedAt != nil && (later(value.CreatedAt, *value.CancelRequestedAt) || later(*value.CancelRequestedAt, value.UpdatedAt)) {
		return ErrInvalid
	}
	if value.StartedAt != nil && (later(value.CreatedAt, *value.StartedAt) || later(*value.StartedAt, value.UpdatedAt)) {
		return ErrInvalid
	}
	if value.FinishedAt != nil && (later(value.CreatedAt, *value.FinishedAt) || later(*value.FinishedAt, value.UpdatedAt)) {
		return ErrInvalid
	}
	if value.StartedAt != nil && value.FinishedAt != nil && later(*value.StartedAt, *value.FinishedAt) {
		return ErrInvalid
	}
	if value.Error != nil && value.Error.Validate() != nil {
		return ErrInvalid
	}
	if value.Output != nil && value.Output.Validate() != nil {
		return ErrInvalid
	}
	switch value.Status {
	case StatusQueued:
		if value.StartedAt != nil || value.FinishedAt != nil || value.CancelRequestedAt != nil || value.Error != nil {
			return ErrInvalid
		}
	case StatusRunning:
		if value.StartedAt == nil || value.FinishedAt != nil || value.Error != nil {
			return ErrInvalid
		}
	case StatusSucceeded:
		if value.StartedAt == nil || value.FinishedAt == nil || value.Error != nil {
			return ErrInvalid
		}
	case StatusFailed:
		if value.StartedAt == nil || value.FinishedAt == nil || value.Error == nil {
			return ErrInvalid
		}
	case StatusInterrupted:
		if value.FinishedAt == nil || value.Error == nil {
			return ErrInvalid
		}
	case StatusCancelled:
		if value.FinishedAt == nil || value.Error != nil {
			return ErrInvalid
		}
	}
	return nil
}

// TransitionDisposition tells a caller whether a state command changes the
// row, is a replay of already-observed state, or must be rejected.
type TransitionDisposition string

const (
	TransitionApply  TransitionDisposition = "apply"
	TransitionNoop   TransitionDisposition = "noop"
	TransitionReject TransitionDisposition = "reject"
)

// ResolveTransition freezes all legal state changes. A repeated command for
// the current state is a no-op; callers must not increment version or publish
// another event for it. A different terminal state is never accepted.
func ResolveTransition(current, target Status) TransitionDisposition {
	if !current.Valid() || !target.Valid() {
		return TransitionReject
	}
	if current == target {
		return TransitionNoop
	}
	switch current {
	case StatusQueued:
		if target == StatusRunning || target == StatusCancelled || target == StatusInterrupted {
			return TransitionApply
		}
	case StatusRunning:
		if target == StatusSucceeded || target == StatusFailed || target == StatusCancelled || target == StatusInterrupted {
			return TransitionApply
		}
	}
	return TransitionReject
}

func CanTransition(current, target Status) bool {
	return ResolveTransition(current, target) == TransitionApply
}

// RecoveryAction is deliberately separate from a state transition: queued
// work can only be claimed by a registered business recovery handler.
type RecoveryAction string

const (
	RecoveryNone       RecoveryAction = "none"
	RecoveryInterrupt  RecoveryAction = "interrupt"
	RecoveryAwaitClaim RecoveryAction = "await_claim"
)

type RecoveryDecision struct {
	Action RecoveryAction
	Code   string
}

// DecideRecovery applies the startup policy. A zero or negative queued limit
// fails closed: queued records are interrupted instead of being executed by an
// owner that cannot prove a recovery lease.
func DecideRecovery(status Status, createdAt string, now time.Time, queuedLimit time.Duration) (RecoveryDecision, error) {
	if !status.Valid() || !validTimestamp(createdAt) || now.IsZero() {
		return RecoveryDecision{}, ErrInvalid
	}
	switch status {
	case StatusRunning:
		return RecoveryDecision{Action: RecoveryInterrupt, Code: "RECOVERY_INTERRUPTED"}, nil
	case StatusQueued:
		created, _ := time.Parse(time.RFC3339Nano, createdAt)
		if queuedLimit <= 0 || !now.Before(created.Add(queuedLimit)) {
			return RecoveryDecision{Action: RecoveryInterrupt, Code: "RECOVERY_EXPIRED"}, nil
		}
		return RecoveryDecision{Action: RecoveryAwaitClaim}, nil
	default:
		return RecoveryDecision{Action: RecoveryNone}, nil
	}
}

func validTimestamp(value string) bool {
	if !strings.HasSuffix(value, "Z") {
		return false
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	return err == nil && parsed.Location() == time.UTC
}

func validOptionalTimestamp(value *string) bool { return value == nil || validTimestamp(*value) }

func later(left, right string) bool {
	leftTime, leftErr := time.Parse(time.RFC3339Nano, left)
	rightTime, rightErr := time.Parse(time.RFC3339Nano, right)
	return leftErr != nil || rightErr != nil || leftTime.After(rightTime)
}

func validOptionalResult(value *json.RawMessage) bool {
	if value == nil {
		return true
	}
	if len(*value) > 8192 || !utf8.Valid(*value) {
		return false
	}
	var decoded any
	decoder := json.NewDecoder(strings.NewReader(string(*value)))
	decoder.UseNumber()
	if decoder.Decode(&decoded) != nil {
		return false
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return false
	}
	if _, ok := decoded.(map[string]any); !ok {
		return false
	}
	return safeJSONValue(decoded)
}

func safeJSONValue(value any) bool {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if sensitiveKey(key) || !safeJSONValue(child) {
				return false
			}
		}
	case []any:
		for _, child := range typed {
			if !safeJSONValue(child) {
				return false
			}
		}
	case string:
		return validSafeText(typed, 2048)
	}
	return true
}

func sensitiveKey(value string) bool {
	key := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(value, "-", "_"), " ", "_"))
	for _, forbidden := range []string{"token", "secret", "password", "api_key", "apikey", "authorization", "cookie", "credential", "private_key", "env", "path", "userdata", "user_data"} {
		if strings.Contains(key, forbidden) {
			return true
		}
	}
	return false
}

func validSafeText(value string, maximumBytes int) bool {
	if strings.TrimSpace(value) == "" || len(value) > maximumBytes || !utf8.ValidString(value) || strings.ContainsRune(value, 0) ||
		strings.ContainsFunc(value, unicode.IsControl) || unsafeSummaryFragment(value) {
		return false
	}
	return true
}

func unsafeSummaryFragment(value string) bool {
	lower := strings.ToLower(value)
	for _, marker := range []string{"bearer ", "token=", "secret=", "password=", "api_key=", "authorization:", "cookie:", "env[", "env_", "userdata", "user data"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	trimmed := strings.TrimSpace(value)
	return strings.HasPrefix(trimmed, "/") || strings.HasPrefix(strings.ToLower(trimmed), "file:") ||
		(len(trimmed) >= 3 && trimmed[1] == ':' && (trimmed[2] == '\\' || trimmed[2] == '/'))
}
