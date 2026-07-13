package files

import (
	"errors"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	MaximumAttachmentCount = 20
	MaximumAttachmentBytes = int64(25 << 20)
	MaximumAttachmentTotal = int64(100 << 20)
)

var (
	ErrInvalidAttachment     = errors.New("attachment is invalid")
	ErrAttachmentTooLarge    = errors.New("attachment exceeds size limit")
	ErrAttachmentLimit       = errors.New("attachment count or total exceeds limit")
	ErrAttachmentContentType = errors.New("attachment content type is not allowed")
	ErrAttachmentContent     = errors.New("attachment content does not match type")
	ErrAttachmentState       = errors.New("attachment operation state is invalid")
	ErrAttachmentRecovery    = errors.New("attachment recovery is required")
)

type AttachmentOwner string

const (
	OwnerRequirement       AttachmentOwner = "requirement"
	OwnerRequirementLegacy AttachmentOwner = "requirements"
	OwnerFeedback          AttachmentOwner = "feedback"
)

func (owner AttachmentOwner) Valid() bool {
	return owner == OwnerRequirement || owner == OwnerRequirementLegacy || owner == OwnerFeedback
}

func (owner AttachmentOwner) Canonical() AttachmentOwner {
	if owner == OwnerRequirementLegacy {
		return OwnerRequirement
	}
	return owner
}

type Attachment struct {
	ID          int64
	ProjectID   int64
	OwnerType   AttachmentOwner
	OwnerID     int64
	DisplayName string
	StoredKey   string
	MIMEType    string
	Size        int64
	SHA256      string
	CreatedAt   string
}

type StagedAttachment struct {
	StageKey string
	ReadyKey string
	Size     int64
	SHA256   string
	Sample   []byte
}

type StoredAttachmentFile struct {
	Key        string
	Size       int64
	ModifiedAt time.Time
}

type OperationKind string

const (
	OperationUpload OperationKind = "upload"
	OperationDelete OperationKind = "delete"
)

func (kind OperationKind) Valid() bool { return kind == OperationUpload || kind == OperationDelete }

type OperationState string

const (
	StateStaged      OperationState = "staged"
	StateReady       OperationState = "ready"
	StateDeleting    OperationState = "deleting"
	StateQuarantined OperationState = "quarantined"
	StateComplete    OperationState = "complete"
	StateFailed      OperationState = "failed"
)

func (state OperationState) Valid() bool {
	switch state {
	case StateStaged, StateReady, StateDeleting, StateQuarantined, StateComplete, StateFailed:
		return true
	default:
		return false
	}
}

func (state OperationState) Recoverable() bool {
	return state == StateStaged || state == StateDeleting || state == StateQuarantined || state == StateFailed
}

type AttachmentOperation struct {
	ID            string
	ProjectID     int64
	Kind          OperationKind
	State         OperationState
	AttachmentID  *int64
	StoredKey     string
	StageKey      string
	QuarantineKey string
	Size          int64
	SHA256        string
	MIMEType      string
	CreatedAt     string
	UpdatedAt     string
}

func ValidateAttachment(value Attachment) error {
	if value.ID < 0 || value.ProjectID <= 0 || !value.OwnerType.Valid() || value.OwnerID <= 0 ||
		value.DisplayName == "" || !StorageKeyValid(value.StoredKey) || !AllowedMIMEType(value.MIMEType) ||
		value.Size <= 0 || !validSHA256(value.SHA256) || !validTimestamp(value.CreatedAt) {
		return ErrInvalidAttachment
	}
	return nil
}

func ValidateAttachmentOperation(value AttachmentOperation) error {
	if !validOperationID(value.ID) || value.ProjectID <= 0 || !value.Kind.Valid() || !value.State.Valid() ||
		(value.AttachmentID != nil && *value.AttachmentID <= 0) || value.Size < 0 ||
		(value.StoredKey != "" && !StorageKeyValid(value.StoredKey)) ||
		(value.StageKey != "" && !StorageKeyValid(value.StageKey)) ||
		(value.QuarantineKey != "" && !StorageKeyValid(value.QuarantineKey)) ||
		(value.SHA256 != "" && !validSHA256(value.SHA256)) ||
		(value.MIMEType != "" && !AllowedMIMEType(value.MIMEType)) ||
		!validTimestamp(value.CreatedAt) || !validTimestamp(value.UpdatedAt) {
		return ErrAttachmentState
	}
	return nil
}

func NormalizeDisplayName(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || value == "." || value == ".." || !utf8.ValidString(value) ||
		utf8.RuneCountInString(value) > 120 || strings.ContainsRune(value, 0) ||
		strings.ContainsAny(value, `<>:"/\\|?*`) || strings.Contains(value, ":") ||
		strings.HasSuffix(value, ".") || strings.HasSuffix(value, " ") || reservedName(value) || dangerousExtension(value) {
		return "", ErrInvalidAttachment
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return "", ErrInvalidAttachment
		}
	}
	return value, nil
}

func NormalizeMIMEType(value string) string {
	value = strings.ToLower(strings.TrimSpace(strings.Split(value, ";")[0]))
	return value
}

func AllowedMIMEType(value string) bool {
	switch NormalizeMIMEType(value) {
	case "application/pdf", "application/json", "text/plain",
		"image/apng", "image/avif", "image/bmp", "image/gif", "image/jpeg", "image/png", "image/webp":
		return true
	default:
		return false
	}
}

func StorageKeyValid(value string) bool {
	cleaned := filepath.ToSlash(filepath.Clean(value))
	if value == "" || filepath.IsAbs(value) || strings.ContainsRune(value, 0) ||
		strings.HasPrefix(cleaned, "../") || cleaned == ".." {
		return false
	}
	parts := strings.Split(cleaned, "/")
	if len(parts) != 2 || (parts[0] != "staged" && parts[0] != "ready" && parts[0] != "quarantine") || parts[1] == "" {
		return false
	}
	for _, character := range parts[1] {
		if !(character >= 'a' && character <= 'z') && !(character >= 'A' && character <= 'Z') &&
			!(character >= '0' && character <= '9') && character != '-' && character != '_' && character != '.' {
			return false
		}
	}
	return true
}

func reservedName(value string) bool {
	base := strings.ToUpper(strings.TrimSuffix(value, filepath.Ext(value)))
	switch base {
	case "CON", "PRN", "AUX", "NUL", "COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9",
		"LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9":
		return true
	default:
		return false
	}
}

func dangerousExtension(value string) bool {
	lower := strings.ToLower(value)
	for _, suffix := range []string{".exe", ".com", ".cmd", ".bat", ".ps1", ".msi", ".scr", ".js", ".jse", ".vbs", ".vbe", ".html", ".htm", ".svg", ".xhtml", ".jar"} {
		if strings.HasSuffix(lower, suffix) {
			return true
		}
	}
	return false
}

func validSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if !(character >= '0' && character <= '9') && !(character >= 'a' && character <= 'f') {
			return false
		}
	}
	return true
}

func validTimestamp(value string) bool {
	if !strings.HasSuffix(value, "Z") {
		return false
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	return err == nil && parsed.Location() == time.UTC
}

func validOperationID(value string) bool {
	if value == "" || len(value) > 160 {
		return false
	}
	for _, character := range value {
		if !(character >= 'a' && character <= 'z') && !(character >= 'A' && character <= 'Z') &&
			!(character >= '0' && character <= '9') && character != '-' && character != '_' {
			return false
		}
	}
	return true
}
