package operations

import (
	"bytes"
	"strings"
	"unicode/utf8"

	domainoperation "github.com/lyming99/autoplan/backend/internal/domain/operation"
)

// OutputCapture is write-only runner input. The service never returns either
// byte slice and the repository persists only OutputMetadata.
type OutputCapture struct {
	Stdout []byte
	Stderr []byte
}

type OutputAssessment struct {
	Metadata   *domainoperation.OutputMetadata
	Diagnostic *domainoperation.ErrorSummary
}

// OutputArchive is the only text-shaped representation suitable for a
// resource last_log. Operation DTOs and P10 event payloads continue to carry
// metadata only. Tails contain accepted, line-complete, redacted-safe text;
// unsafe data is discarded rather than partially masked.
type OutputArchive struct {
	StdoutTail string
	StderrTail string
	Metadata   *domainoperation.OutputMetadata
}

// AssessOutput enforces the frozen P10 limits before output can influence an
// Operation write. Suspicious or malformed chunks are discarded, not masked
// optimistically; terminal text is never returned from this package.
func AssessOutput(capture *OutputCapture) OutputAssessment {
	if capture == nil {
		return OutputAssessment{}
	}
	stdoutBytes, stdoutLines, stdoutTruncated, stdoutUnsafe := assessStream(capture.Stdout)
	stderrBytes, stderrLines, stderrTruncated, stderrUnsafe := assessStream(capture.Stderr)
	metadata := &domainoperation.OutputMetadata{
		StdoutBytes: stdoutBytes, StdoutLines: stdoutLines, StdoutTruncated: stdoutTruncated,
		StderrBytes: stderrBytes, StderrLines: stderrLines, StderrTruncated: stderrTruncated,
		RedactionFailed: stdoutUnsafe || stderrUnsafe,
	}
	assessment := OutputAssessment{Metadata: metadata}
	if metadata.RedactionFailed {
		assessment.Diagnostic = &domainoperation.ErrorSummary{Code: "OUTPUT_REDACTION_FAILED", Summary: "Operation output was discarded by the redaction policy."}
	} else if metadata.StdoutTruncated || metadata.StderrTruncated {
		assessment.Diagnostic = &domainoperation.ErrorSummary{Code: "OUTPUT_TRUNCATED", Summary: "Operation output exceeded the retained diagnostic limit."}
	}
	return assessment
}

// ArchiveOutput applies the same limits as AssessOutput while retaining a
// bounded diagnostic tail for repository archival. It is intentionally
// conservative around split lines and malformed UTF-8 so output cannot be
// reconstructed into a secret or absolute path after persistence.
func ArchiveOutput(capture *OutputCapture, tailBytes, tailLines int) OutputArchive {
	if capture == nil || tailBytes <= 0 || tailLines <= 0 {
		return OutputArchive{}
	}
	if tailBytes > domainoperation.MaximumPersistedStreamBytes {
		tailBytes = domainoperation.MaximumPersistedStreamBytes
	}
	if tailLines > domainoperation.MaximumPersistedStreamLines {
		tailLines = domainoperation.MaximumPersistedStreamLines
	}
	stdoutTail, stdoutBytes, stdoutLines, stdoutTruncated, stdoutUnsafe := archiveStream(capture.Stdout, tailBytes, tailLines)
	stderrTail, stderrBytes, stderrLines, stderrTruncated, stderrUnsafe := archiveStream(capture.Stderr, tailBytes, tailLines)
	metadata := &domainoperation.OutputMetadata{
		StdoutBytes: stdoutBytes, StdoutLines: stdoutLines, StdoutTruncated: stdoutTruncated,
		StderrBytes: stderrBytes, StderrLines: stderrLines, StderrTruncated: stderrTruncated,
		RedactionFailed: stdoutUnsafe || stderrUnsafe,
	}
	if metadata.RedactionFailed {
		stdoutTail, stderrTail = "", ""
	}
	return OutputArchive{StdoutTail: stdoutTail, StderrTail: stderrTail, Metadata: metadata}
}

func assessStream(value []byte) (int64, int64, bool, bool) {
	if !utf8.Valid(value) {
		return 0, 0, true, true
	}
	var storedBytes, storedLines int64
	truncated := false
	unsafe := false
	for _, line := range bytes.SplitAfter(value, []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		text := strings.TrimSuffix(string(line), "\n")
		if unsafeOutputLine(text) {
			unsafe = true
			truncated = true
			continue
		}
		lineBytes := int64(len(line))
		if storedBytes+lineBytes > domainoperation.MaximumPersistedStreamBytes ||
			storedLines+1 > domainoperation.MaximumPersistedStreamLines {
			truncated = true
			continue
		}
		storedBytes += lineBytes
		storedLines++
	}
	return storedBytes, storedLines, truncated, unsafe
}

func archiveStream(value []byte, tailBytes, tailLines int) (string, int64, int64, bool, bool) {
	if !utf8.Valid(value) {
		return "", 0, 0, true, true
	}
	var storedBytes, storedLines int64
	truncated := false
	unsafe := false
	tail := ""
	for _, line := range bytes.SplitAfter(value, []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		text := strings.TrimSuffix(string(line), "\n")
		if unsafeOutputLine(text) {
			unsafe = true
			truncated = true
			continue
		}
		lineBytes := int64(len(line))
		if storedBytes+lineBytes > domainoperation.MaximumPersistedStreamBytes ||
			storedLines+1 > domainoperation.MaximumPersistedStreamLines {
			truncated = true
			continue
		}
		storedBytes += lineBytes
		storedLines++
		tail = appendArchiveTail(tail, string(line), tailBytes, tailLines)
	}
	return tail, storedBytes, storedLines, truncated, unsafe
}

func appendArchiveTail(existing, addition string, byteLimit, lineLimit int) string {
	value := existing + addition
	for strings.Count(value, "\n") > lineLimit {
		if index := strings.IndexByte(value, '\n'); index >= 0 {
			value = value[index+1:]
		} else {
			break
		}
	}
	if len(value) <= byteLimit {
		return value
	}
	start := len(value) - byteLimit
	for start < len(value) && !utf8.RuneStart(value[start]) {
		start++
	}
	value = value[start:]
	if index := strings.IndexByte(value, '\n'); index >= 0 {
		value = value[index+1:]
	}
	return value
}

func unsafeOutputLine(value string) bool {
	trimmed := strings.TrimSpace(value)
	lower := strings.ToLower(trimmed)
	if strings.HasPrefix(trimmed, "/") || strings.HasPrefix(lower, "file:") ||
		containsAbsolutePath(trimmed) {
		return true
	}
	for _, marker := range []string{
		"bearer ", "token=", "secret=", "password=", "api_key=", "authorization:", "cookie:",
		"export ", "set ", "env[", "env_", "userdata", "user data",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	if equal := strings.IndexByte(trimmed, '='); equal > 0 {
		key := strings.TrimSpace(trimmed[:equal])
		if key != "" && strings.IndexFunc(key, func(character rune) bool {
			return !(character == '_' || (character >= 'A' && character <= 'Z') || (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9'))
		}) < 0 {
			return true
		}
	}
	return false
}

func containsAbsolutePath(value string) bool {
	for index := 0; index+2 < len(value); index++ {
		if ((value[index] >= 'A' && value[index] <= 'Z') || (value[index] >= 'a' && value[index] <= 'z')) &&
			value[index+1] == ':' && (value[index+2] == '\\' || value[index+2] == '/') {
			return true
		}
	}
	return strings.Contains(value, "/home/") || strings.Contains(value, "/users/") || strings.Contains(value, "/var/")
}
