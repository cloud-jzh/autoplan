package sqlite

import (
	"context"
	"encoding/json"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	domainautomation "github.com/lyming99/autoplan/backend/internal/domain/automation"
	domainoperation "github.com/lyming99/autoplan/backend/internal/domain/operation"
	"github.com/lyming99/autoplan/backend/internal/repository"
)

const (
	maximumRuntimeArchiveTailBytes = 8 << 10
	maximumRuntimeArchiveTailLines = 256
)

// RuntimeOutputArchive is the bounded, already-redacted process output that
// may be retained in a Script/Executor last_log. It is intentionally separate
// from Operation OutputMetadata, which never carries text.
type RuntimeOutputArchive struct {
	StdoutTail      string
	StderrTail      string
	StdoutBytes     int64
	StdoutLines     int64
	StdoutTruncated bool
	StderrBytes     int64
	StderrLines     int64
	StderrTruncated bool
	RedactionFailed bool
}

func (value RuntimeOutputArchive) Metadata() domainoperation.OutputMetadata {
	return domainoperation.OutputMetadata{
		StdoutBytes: value.StdoutBytes, StdoutLines: value.StdoutLines, StdoutTruncated: value.StdoutTruncated,
		StderrBytes: value.StderrBytes, StderrLines: value.StderrLines, StderrTruncated: value.StderrTruncated,
		RedactionFailed: value.RedactionFailed,
	}
}

func (value RuntimeOutputArchive) Truncated() bool {
	return value.StdoutTruncated || value.StderrTruncated || value.RedactionFailed
}

// RuntimeRunArchive is persisted only through a Finalize*Run method. Its
// resource identity is supplied by the server-side application service, not a
// transport. The archive contains neither command construction nor PID data.
type RuntimeRunArchive struct {
	Status      string
	ExitCode    *int64
	DurationMS  *int64
	Output      RuntimeOutputArchive
	FailureCode string
	OccurredAt  string
}

type ScriptRunFinalization struct {
	Transition TransitionOperation
	ScriptID   int64
	Archive    RuntimeRunArchive
}

// FinalizeScriptRun gives a process completion exactly one durable outcome.
// It writes the Operation terminal state, Script last_* archive and both P10
// events in the existing writer transaction. A repeated completion of the
// same terminal state is a no-op and therefore cannot append a second,
// contradictory resource event.
func (transaction *OperationTransaction) FinalizeScriptRun(ctx context.Context, input ScriptRunFinalization) (OperationMutation, error) {
	if transaction == nil || transaction.transaction == nil || input.ScriptID <= 0 ||
		input.Transition.ProjectID <= 0 || input.Transition.OperationID == "" ||
		!input.Transition.Target.Terminal() || !runtimeArchiveMatchesTerminal(input.Archive.Status, input.Transition.Target) ||
		input.Archive.OccurredAt != input.Transition.UpdatedAt {
		return OperationMutation{}, repository.ErrTransaction
	}
	archive, logTail, err := normalizeRuntimeRunArchive(input.Archive)
	if err != nil {
		return OperationMutation{}, err
	}
	metadata := archive.Output.Metadata()
	if metadata.Validate() != nil {
		return OperationMutation{}, repository.ErrTransaction
	}
	if input.Transition.Output != nil && *input.Transition.Output != metadata {
		return OperationMutation{}, repository.ErrTransaction
	}
	if input.Transition.Output == nil {
		input.Transition.Output = &metadata
	}
	mutation, err := transaction.Transition(ctx, input.Transition)
	if err != nil || !mutation.Changed {
		return mutation, err
	}
	if err := transaction.transaction.archiveScriptRun(ctx, input.Transition.ProjectID, input.ScriptID, archive, logTail); err != nil {
		return OperationMutation{}, err
	}
	payload, err := runtimeArchivePayload(input.ScriptID, archive)
	if err != nil {
		return OperationMutation{}, err
	}
	operationID := mutation.Operation.OperationID
	if _, err := transaction.transaction.appendBusinessEvent(ctx, BusinessEvent{
		ProjectID: input.Transition.ProjectID, Type: "business.script.run", OperationID: &operationID,
		RequestID: input.Transition.RequestID, OccurredAt: input.Transition.UpdatedAt, Payload: payload,
	}); err != nil {
		return OperationMutation{}, err
	}
	return mutation, nil
}

func (transaction *writeTransaction) archiveScriptRun(
	ctx context.Context,
	projectID, scriptID int64,
	archive RuntimeRunArchive,
	logTail string,
) error {
	current, found, err := transaction.GetScript(ctx, projectID, scriptID)
	if err != nil {
		return err
	}
	if !found {
		return repository.ErrNotFound
	}
	if domainautomation.ValidateScriptRecord(current) != nil || !archiveAfter(archive.OccurredAt, current.CreatedAt) {
		return repository.ErrTransaction
	}
	result, err := transaction.tx.ExecContext(ctx,
		`UPDATE scripts
		    SET last_status = ?, last_exit_code = ?, last_duration_ms = ?, last_log = ?, last_run_at = ?, updated_at = ?
		  WHERE id = ? AND project_id = ?`,
		archive.Status, nullableRuntimeInt64(archive.ExitCode), nullableRuntimeInt64(archive.DurationMS), nullableRuntimeText(logTail),
		archive.OccurredAt, archive.OccurredAt, scriptID, projectID)
	if err != nil {
		return safeSQLError(ctx, err)
	}
	if err := requireOneRow(result); err != nil {
		return err
	}
	return transaction.wrote("scripts:archive-run")
}

func runtimeArchiveMatchesTerminal(status string, target domainoperation.Status) bool {
	switch target {
	case domainoperation.StatusSucceeded:
		return status == "ok"
	case domainoperation.StatusFailed:
		return status == "bad"
	case domainoperation.StatusCancelled:
		return status == "cancelled"
	case domainoperation.StatusInterrupted:
		return status == "interrupted"
	default:
		return false
	}
}

func normalizeRuntimeRunArchive(input RuntimeRunArchive) (RuntimeRunArchive, string, error) {
	if !validRuntimeStatus(input.Status) || !validUTCTimestamp(input.OccurredAt) || !validRuntimeMetric(input.ExitCode, -1, 255) ||
		!validRuntimeMetric(input.DurationMS, 0, int64(^uint32(0))) || !validRuntimeFailureCode(input.FailureCode) ||
		input.Output.Metadata().Validate() != nil {
		return RuntimeRunArchive{}, "", repository.ErrTransaction
	}
	if input.Output.RedactionFailed {
		input.Output.StdoutTail, input.Output.StderrTail = "", ""
		input.Output.StdoutTruncated, input.Output.StderrTruncated = true, true
		return input, "", nil
	}
	stdout, ok := normalizeRuntimeTail(input.Output.StdoutTail)
	if !ok {
		input.Output.StdoutTail, input.Output.StderrTail = "", ""
		input.Output.RedactionFailed = true
		input.Output.StdoutTruncated, input.Output.StderrTruncated = true, true
		return input, "", nil
	}
	stderr, ok := normalizeRuntimeTail(input.Output.StderrTail)
	if !ok {
		input.Output.StdoutTail, input.Output.StderrTail = "", ""
		input.Output.RedactionFailed = true
		input.Output.StdoutTruncated, input.Output.StderrTruncated = true, true
		return input, "", nil
	}
	input.Output.StdoutTail, input.Output.StderrTail = stdout, stderr
	return input, combineRuntimeTails(stdout, stderr), nil
}

func validRuntimeStatus(value string) bool {
	switch value {
	case "ok", "bad", "cancelled", "stopped", "running", "interrupted":
		return true
	default:
		return false
	}
}

func validRuntimeMetric(value *int64, minimum, maximum int64) bool {
	return value == nil || (*value >= minimum && *value <= maximum)
}

func validRuntimeFailureCode(value string) bool {
	if value == "" {
		return true
	}
	if len(value) > 64 || value[0] < 'A' || value[0] > 'Z' {
		return false
	}
	for _, character := range value {
		if !(character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '_') {
			return false
		}
	}
	return true
}

func normalizeRuntimeTail(value string) (string, bool) {
	if len(value) > maximumRuntimeArchiveTailBytes || !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
		return "", false
	}
	lines := strings.Split(value, "\n")
	if len(lines) > maximumRuntimeArchiveTailLines+1 {
		return "", false
	}
	for _, line := range lines {
		if strings.ContainsFunc(line, func(character rune) bool { return unicode.IsControl(character) && character != '\t' }) || unsafeRuntimeArchiveLine(line) {
			return "", false
		}
	}
	return value, true
}

func unsafeRuntimeArchiveLine(value string) bool {
	trimmed := strings.TrimSpace(value)
	lower := strings.ToLower(trimmed)
	if strings.HasPrefix(trimmed, "/") || strings.HasPrefix(lower, "file:") || containsRuntimeAbsolutePath(trimmed) {
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
			return !(character == '_' || character >= 'A' && character <= 'Z' || character >= 'a' && character <= 'z' || character >= '0' && character <= '9')
		}) < 0 {
			return true
		}
	}
	return false
}

func containsRuntimeAbsolutePath(value string) bool {
	for index := 0; index+2 < len(value); index++ {
		if ((value[index] >= 'A' && value[index] <= 'Z') || (value[index] >= 'a' && value[index] <= 'z')) &&
			value[index+1] == ':' && (value[index+2] == '\\' || value[index+2] == '/') {
			return true
		}
	}
	return strings.Contains(value, "/home/") || strings.Contains(value, "/users/") || strings.Contains(value, "/var/")
}

func combineRuntimeTails(stdout, stderr string) string {
	parts := make([]string, 0, 2)
	if stdout != "" {
		parts = append(parts, "stdout:\n"+stdout)
	}
	if stderr != "" {
		parts = append(parts, "stderr:\n"+stderr)
	}
	value := strings.Join(parts, "\n")
	if len(value) <= domainautomation.LastLogMaxChars {
		return value
	}
	start := len(value) - domainautomation.LastLogMaxChars
	for start < len(value) && !utf8.RuneStart(value[start]) {
		start++
	}
	return value[start:]
}

func runtimeArchivePayload(resourceID int64, archive RuntimeRunArchive) (json.RawMessage, error) {
	payload := map[string]any{
		"resource_id": resourceID,
		"status":      archive.Status,
		"truncated":   archive.Output.Truncated(),
	}
	if archive.ExitCode != nil {
		payload["exit_code"] = *archive.ExitCode
	}
	if archive.DurationMS != nil {
		payload["duration_ms"] = *archive.DurationMS
	}
	if archive.FailureCode != "" {
		payload["failure_code"] = archive.FailureCode
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, repository.ErrTransaction
	}
	return json.RawMessage(encoded), nil
}

func nullableRuntimeInt64(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

func nullableRuntimeText(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func archiveAfter(value, createdAt string) bool {
	archiveAt, archiveErr := time.Parse(time.RFC3339Nano, value)
	created, createdErr := time.Parse(time.RFC3339Nano, createdAt)
	return archiveErr == nil && createdErr == nil && !archiveAt.Before(created)
}
