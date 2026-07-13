// Package intake owns transport- and persistence-independent Intake invariants.
package intake

import (
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"
)

var (
	ErrInvalid       = errors.New("intake is invalid")
	ErrInvalidType   = errors.New("intake type is invalid")
	ErrInvalidStatus = errors.New("intake status is invalid")
	ErrInvalidLink   = errors.New("intake plan link is invalid")
)

type Type string

const (
	Requirement Type = "requirement"
	Feedback    Type = "feedback"
)

func (value Type) Valid() bool { return value == Requirement || value == Feedback }

func (value Type) Table() string {
	if value == Feedback {
		return "feedback"
	}
	return "requirements"
}

type Status string

const (
	StatusDraft     Status = "draft"
	StatusOpen      Status = "open"
	StatusCompleted Status = "completed"
	StatusClosed    Status = "closed"
)

func (value Status) Valid() bool {
	switch value {
	case StatusDraft, StatusOpen, StatusCompleted, StatusClosed:
		return true
	default:
		return false
	}
}

type AgentCLIConfig struct {
	Provider             *string
	Command              string
	CodexReasoningEffort *string
}

type PlanGenerationConfig struct {
	Strategy             *string
	Provider             *string
	Command              string
	Model                string
	CodexReasoningEffort *string
	ClaudeBaseURL        string
	ClaudeAuthToken      string
	ClaudeModel          string
	ClaudeConfigID       int64
}

// GenerationFailure uses opaque references for legacy log/source columns so
// repository callers do not interpret or open them as filesystem paths.
type GenerationFailure struct {
	Count                int64
	LastFailedAt         *string
	LastError            *string
	LastLogRef           *string
	LastAgentCLIProvider *string
	LastCodexEffort      *string
}

type Intake struct {
	ID             int64
	ProjectID      int64
	Type           Type
	RequirementID  *int64
	Title          string
	Body           string
	Status         Status
	AgentCLI       AgentCLIConfig
	PlanGeneration PlanGenerationConfig
	Failure        GenerationFailure
	SourceRef      *string
	SourceDigest   *string
	LinkedPlanID   *int64
	CreatedAt      string
	UpdatedAt      string
	AcceptedAt     *string
	SessionID      *string
}

type Create struct {
	ProjectID      int64
	Type           Type
	RequirementID  *int64
	Title          string
	Body           string
	Status         Status
	AgentCLI       AgentCLIConfig
	PlanGeneration PlanGenerationConfig
	CreatedAt      string
	UpdatedAt      string
}

type Update struct {
	RequirementID  *int64
	Title          string
	Body           string
	Status         Status
	AgentCLI       AgentCLIConfig
	PlanGeneration PlanGenerationConfig
	Failure        GenerationFailure
	AcceptedAt     *string
	SessionID      *string
	UpdatedAt      string
}

type ListOptions struct {
	ProjectID int64
	Type      Type
	Status    *Status
	Limit     int
	Offset    int
}

type DuplicateQuery struct {
	ProjectID     int64
	Type          Type
	RequirementID *int64
	Title         string
	Body          string
}

type PlanLink struct {
	ID         int64
	ProjectID  int64
	IntakeType Type
	IntakeID   int64
	PlanID     int64
	PhaseIndex int64
	PhaseTitle string
	CreatedAt  string
	UpdatedAt  string
}

type PlanLinkInput struct {
	PlanID     int64
	PhaseIndex int64
	PhaseTitle string
}

type IntakeRef struct {
	ProjectID  int64
	IntakeType Type
	IntakeID   int64
}

type DeleteResult struct {
	Intake           Intake
	FeedbackDetached int64
	PlanIDs          []int64
	AttachmentIDs    []int64
	DeletedTaskCount int64
	DeletedScanCount int64
}

type PlanDeleteResult struct {
	PlanID           int64
	LinkedIntakes    []IntakeRef
	DeletedTaskCount int64
	DeletedScanCount int64
}

type PendingEvent struct {
	EventID     string
	StreamKey   string
	Sequence    int64
	Type        string
	RequestID   string
	OperationID *string
	ProjectID   int64
	Message     string
	DataJSON    string
	OccurredAt  string
	CreatedAt   string
}

func NormalizeCreate(value Create) Create {
	value.Title = strings.TrimSpace(value.Title)
	if value.Type == Feedback {
		if value.Title == "" {
			value.Title = DefaultTitle(value.Body, "未命名反馈")
		}
	} else if value.Title == "" {
		value.Title = DefaultTitle(value.Body, "未命名需求")
	}
	if value.Status == "" {
		value.Status = StatusOpen
	}
	value.PlanGeneration = normalizePlanGeneration(value.PlanGeneration)
	value.AgentCLI = normalizeAgentCLI(value.AgentCLI)
	return value
}

func ValidateCreate(value Create) error {
	value = NormalizeCreate(value)
	if value.ProjectID <= 0 || !value.Type.Valid() || !value.Status.Valid() ||
		!validText(value.Title, 200, false) || !validText(value.Body, 100000, true) ||
		!ValidUTCTimestamp(value.CreatedAt) || !ValidUTCTimestamp(value.UpdatedAt) ||
		(value.Type == Requirement && value.RequirementID != nil) ||
		(value.RequirementID != nil && *value.RequirementID <= 0) ||
		!validAgentCLI(value.AgentCLI) || !validPlanGeneration(value.PlanGeneration) {
		return ErrInvalid
	}
	return nil
}

func ValidateUpdate(intakeType Type, value Update) error {
	value = NormalizeUpdate(value)
	if !intakeType.Valid() || !value.Status.Valid() || !validText(value.Title, 200, true) ||
		!validText(value.Body, 100000, true) || !ValidUTCTimestamp(value.UpdatedAt) ||
		(intakeType == Requirement && value.RequirementID != nil) ||
		(value.RequirementID != nil && *value.RequirementID <= 0) || value.Failure.Count < 0 ||
		!validOptionalTimestamp(value.AcceptedAt) || !validOptionalTimestamp(value.Failure.LastFailedAt) ||
		!validAgentCLI(normalizeAgentCLI(value.AgentCLI)) ||
		!validPlanGeneration(normalizePlanGeneration(value.PlanGeneration)) {
		return ErrInvalid
	}
	return nil
}

func NormalizeUpdate(value Update) Update {
	value.AgentCLI = normalizeAgentCLI(value.AgentCLI)
	value.PlanGeneration = normalizePlanGeneration(value.PlanGeneration)
	return value
}

// ValidateRecord permits a non-empty historical status so legacy rows remain
// readable, while new mutations use ValidateCreate/ValidateUpdate's enum.
func ValidateRecord(value Intake) error {
	if value.ID <= 0 || value.ProjectID <= 0 || !value.Type.Valid() ||
		strings.TrimSpace(string(value.Status)) == "" || len(value.Status) > 32 ||
		!validText(value.Title, 200, true) || !validText(value.Body, 100000, true) ||
		!ValidUTCTimestamp(value.CreatedAt) || !ValidUTCTimestamp(value.UpdatedAt) ||
		!validOptionalTimestamp(value.AcceptedAt) || !validOptionalTimestamp(value.Failure.LastFailedAt) ||
		value.Failure.Count < 0 || (value.Type == Requirement && value.RequirementID != nil) ||
		(value.RequirementID != nil && *value.RequirementID <= 0) ||
		(value.LinkedPlanID != nil && *value.LinkedPlanID <= 0) {
		return ErrInvalid
	}
	return nil
}

func ValidatePlanLinks(projectID, intakeID int64, intakeType Type, links []PlanLinkInput) error {
	if projectID <= 0 || intakeID <= 0 || !intakeType.Valid() {
		return ErrInvalidLink
	}
	plans := make(map[int64]struct{}, len(links))
	phases := make(map[int64]struct{}, len(links))
	for _, link := range links {
		if link.PlanID <= 0 || link.PhaseIndex <= 0 || utf8.RuneCountInString(link.PhaseTitle) > 500 || strings.ContainsRune(link.PhaseTitle, 0) {
			return ErrInvalidLink
		}
		if _, exists := plans[link.PlanID]; exists {
			return ErrInvalidLink
		}
		if _, exists := phases[link.PhaseIndex]; exists {
			return ErrInvalidLink
		}
		plans[link.PlanID] = struct{}{}
		phases[link.PhaseIndex] = struct{}{}
	}
	return nil
}

func ValidatePendingEvent(value PendingEvent) error {
	if value.ProjectID <= 0 || value.Sequence < 0 || !validOpaque(value.EventID, 256) ||
		!validOpaque(value.StreamKey, 512) || !validOpaque(value.Type, 128) ||
		!validOpaque(value.RequestID, 256) || !validText(value.Message, 10000, true) ||
		!ValidUTCTimestamp(value.OccurredAt) || !ValidUTCTimestamp(value.CreatedAt) ||
		(value.OperationID != nil && !validOpaque(*value.OperationID, 256)) ||
		!json.Valid([]byte(value.DataJSON)) {
		return ErrInvalid
	}
	var object map[string]any
	if json.Unmarshal([]byte(value.DataJSON), &object) != nil || object == nil {
		return ErrInvalid
	}
	return nil
}

func ValidUTCTimestamp(value string) bool {
	if !strings.HasSuffix(value, "Z") {
		return false
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	return err == nil && parsed.Location() == time.UTC
}

func DefaultTitle(body, fallback string) string {
	normalized := strings.ReplaceAll(strings.ReplaceAll(body, "\r\n", "\n"), "\r", "\n")
	for _, line := range strings.Split(normalized, "\n") {
		if title := strings.TrimSpace(line); title != "" {
			return truncateUTF16(title, 80)
		}
	}
	return fallback
}

// NormalizeDuplicateText mirrors the Node algorithm: line endings normalize
// to LF, inline whitespace collapses per line, line boundaries remain.
func NormalizeDuplicateText(value string) string {
	value = strings.ReplaceAll(strings.ReplaceAll(value, "\r\n", "\n"), "\r", "\n")
	lines := strings.Split(value, "\n")
	for index, line := range lines {
		var result strings.Builder
		spacePending := false
		for _, character := range line {
			if unicode.IsSpace(character) {
				spacePending = result.Len() > 0
				continue
			}
			if spacePending {
				result.WriteByte(' ')
				spacePending = false
			}
			result.WriteRune(character)
		}
		lines[index] = strings.TrimSpace(result.String())
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func DuplicateEquivalent(leftTitle, leftBody, rightTitle, rightBody string) bool {
	return NormalizeDuplicateText(leftTitle) == NormalizeDuplicateText(rightTitle) &&
		NormalizeDuplicateText(leftBody) == NormalizeDuplicateText(rightBody)
}

func validText(value string, maximum int, emptyAllowed bool) bool {
	return utf8.ValidString(value) && !strings.ContainsRune(value, 0) && utf8.RuneCountInString(value) <= maximum && (emptyAllowed || strings.TrimSpace(value) != "")
}

func validOpaque(value string, maximum int) bool {
	return value != "" && len(value) <= maximum && strings.TrimSpace(value) == value && !strings.ContainsFunc(value, unicode.IsControl)
}

func validOptionalTimestamp(value *string) bool { return value == nil || ValidUTCTimestamp(*value) }

func truncateUTF16(value string, maximum int) string {
	characters := utf16.Encode([]rune(value))
	if len(characters) > maximum {
		characters = characters[:maximum]
		// A Go string cannot preserve JavaScript's possible trailing lone high
		// surrogate. Drop it instead of introducing replacement content.
		if len(characters) != 0 && characters[len(characters)-1] >= 0xD800 && characters[len(characters)-1] <= 0xDBFF {
			characters = characters[:len(characters)-1]
		}
	}
	return string(utf16.Decode(characters))
}

func normalizeAgentCLI(value AgentCLIConfig) AgentCLIConfig {
	value.Command = strings.TrimSpace(value.Command)
	value.Provider = trimPointer(value.Provider)
	value.CodexReasoningEffort = trimPointer(value.CodexReasoningEffort)
	return value
}

func normalizePlanGeneration(value PlanGenerationConfig) PlanGenerationConfig {
	value.Strategy = trimPointer(value.Strategy)
	value.Provider = trimPointer(value.Provider)
	value.Command = strings.TrimSpace(value.Command)
	value.Model = strings.TrimSpace(value.Model)
	value.CodexReasoningEffort = trimPointer(value.CodexReasoningEffort)
	value.ClaudeBaseURL = strings.TrimSpace(value.ClaudeBaseURL)
	value.ClaudeAuthToken = strings.TrimSpace(value.ClaudeAuthToken)
	value.ClaudeModel = strings.TrimSpace(value.ClaudeModel)
	if value.ClaudeConfigID < 0 {
		value.ClaudeConfigID = 0
	}
	return value
}

func validAgentCLI(value AgentCLIConfig) bool {
	return len(value.Command) <= 1000 && validOptionalText(value.Provider, 64) && validOptionalText(value.CodexReasoningEffort, 32)
}

func validPlanGeneration(value PlanGenerationConfig) bool {
	return value.ClaudeConfigID >= 0 && len(value.Command) <= 1000 && len(value.Model) <= 200 &&
		len(value.ClaudeBaseURL) <= 4096 && len(value.ClaudeAuthToken) <= 16384 && len(value.ClaudeModel) <= 200 &&
		validOptionalText(value.Strategy, 64) && validOptionalText(value.Provider, 64) && validOptionalText(value.CodexReasoningEffort, 32)
}

func validOptionalText(value *string, maximum int) bool {
	return value == nil || (utf8.ValidString(*value) && utf8.RuneCountInString(*value) <= maximum && !strings.ContainsRune(*value, 0))
}

func trimPointer(value *string) *string {
	if value == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*value)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}
