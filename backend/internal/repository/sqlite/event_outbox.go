package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"time"

	domainevent "github.com/lyming99/autoplan/backend/internal/domain/event"
	domainevents "github.com/lyming99/autoplan/backend/internal/domain/events"
	"github.com/lyming99/autoplan/backend/internal/repository"
)

const outboxColumns = `event_id, schema_version, event_class, project_id, project_revision,
	 type, operation_id, request_id, occurred_at, data_json, published_at, attempts, created_at`

type OutboxQuery struct {
	ProjectID    int64
	OperationID  string
	AfterEventID string
	Limit        int
	PendingOnly  bool
}

type OutboxRecord struct {
	Envelope    domainevents.Envelope
	PublishedAt *string
	Attempts    int64
	CreatedAt   string
}

// BusinessEvent is the constrained write shape for runtime resource events.
// It deliberately contains only project-scoped identifiers and a previously
// validated, redacted payload; command lines, environment values, paths and
// process output never enter the durable event stream.
type BusinessEvent struct {
	ProjectID   int64
	Type        string
	OperationID *string
	RequestID   string
	OccurredAt  string
	Payload     json.RawMessage
}

type EventRetentionPolicy struct {
	Now             string
	MaximumAge      time.Duration
	GlobalLimit     int
	PerProjectLimit int
	BatchLimit      int
}

type EventRetentionResult struct {
	Deleted        int
	DeletedThrough map[int64]string
}

func (transaction *OperationTransaction) ListOutbox(ctx context.Context, query OutboxQuery) ([]OutboxRecord, error) {
	if transaction == nil || transaction.transaction == nil || query.ProjectID <= 0 ||
		(query.OperationID != "" && !validOpaque(query.OperationID, 128)) {
		return nil, repository.ErrTransaction
	}
	after, err := parseEventCursor(query.AfterEventID)
	if err != nil {
		return nil, repository.ErrTransaction
	}
	if query.Limit <= 0 {
		query.Limit = 100
	}
	if query.Limit > 500 {
		query.Limit = 500
	}
	where := "project_id = ? AND project_revision IS NOT NULL AND event_class IN ('business', 'operation')"
	arguments := []any{query.ProjectID}
	if after > 0 {
		where += " AND CAST(event_id AS INTEGER) > ?"
		arguments = append(arguments, after)
	}
	if query.OperationID != "" {
		where += " AND operation_id = ?"
		arguments = append(arguments, query.OperationID)
	}
	if query.PendingOnly {
		where += " AND published_at IS NULL"
	}
	arguments = append(arguments, query.Limit)
	rows, err := transaction.transaction.tx.QueryContext(ctx,
		"SELECT "+outboxColumns+" FROM event_outbox WHERE "+where+" ORDER BY CAST(event_id AS INTEGER) ASC LIMIT ?", arguments...)
	if err != nil {
		return nil, safeSQLError(ctx, err)
	}
	defer rows.Close()
	result := make([]OutboxRecord, 0)
	for rows.Next() {
		record, scanErr := scanOutboxRecord(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		result = append(result, record)
	}
	if err := rows.Err(); err != nil {
		return nil, safeSQLError(ctx, err)
	}
	return result, nil
}

// AppendBusinessEvent appends a project-revisioned event inside the current
// OperationTransaction. It is used by Script/Executor run archival and
// startup recovery so the resource write and event cannot commit separately.
func (transaction *OperationTransaction) AppendBusinessEvent(ctx context.Context, input BusinessEvent) (domainevents.Envelope, error) {
	if transaction == nil || transaction.transaction == nil {
		return domainevents.Envelope{}, repository.ErrTransaction
	}
	return transaction.transaction.appendBusinessEvent(ctx, input)
}

// MarkOutboxPublished is idempotent. It never changes a business Operation,
// so a dispatcher crash can replay a committed event without re-running its
// side effect.
func (transaction *OperationTransaction) MarkOutboxPublished(ctx context.Context, eventID, publishedAt string) (bool, error) {
	if transaction == nil || transaction.transaction == nil || !validP10EventID(eventID) || !validUTCTimestamp(publishedAt) {
		return false, repository.ErrTransaction
	}
	result, err := transaction.transaction.tx.ExecContext(ctx,
		`UPDATE event_outbox
		    SET published_at = ?, attempts = attempts + 1, last_error = NULL
		  WHERE event_id = ? AND project_revision IS NOT NULL AND published_at IS NULL`, publishedAt, eventID)
	if err != nil {
		return false, safeSQLError(ctx, err)
	}
	affected, err := rowsAffected(result)
	if err != nil {
		return false, err
	}
	if affected == 0 {
		return false, nil
	}
	if affected != 1 {
		return false, repository.ErrTransaction
	}
	if err := transaction.transaction.wrote("event-outbox:publish"); err != nil {
		return false, err
	}
	return true, nil
}

// RequiresResync reports whether a reconnect cursor precedes the durable
// deletion watermark. Invalid or future cursors are handled by the transport;
// this method answers only the retained-history boundary.
func (transaction *OperationTransaction) RequiresResync(ctx context.Context, projectID int64, lastEventID string) (bool, error) {
	if transaction == nil || transaction.transaction == nil || projectID <= 0 {
		return false, repository.ErrTransaction
	}
	last, err := parseEventCursor(lastEventID)
	if err != nil {
		return false, repository.ErrTransaction
	}
	if last == 0 {
		return false, nil
	}
	var deletedThrough string
	err = transaction.transaction.tx.QueryRowContext(ctx,
		"SELECT deleted_through_event_id FROM event_retention_watermarks WHERE project_id = ?", projectID).Scan(&deletedThrough)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, safeSQLError(ctx, err)
	}
	watermark, err := parseEventCursor(deletedThrough)
	if err != nil {
		return false, repository.ErrInvalidStore
	}
	return last <= watermark, nil
}

// PruneOutbox deletes only already-published P10 events. Candidate collection,
// deletion, and watermark advancement happen in one transaction; an outbox
// row that still needs delivery is never a retention candidate.
func (transaction *OperationTransaction) PruneOutbox(ctx context.Context, policy EventRetentionPolicy) (EventRetentionResult, error) {
	if transaction == nil || transaction.transaction == nil || !validRetentionPolicy(policy) {
		return EventRetentionResult{}, repository.ErrTransaction
	}
	batch := policy.BatchLimit
	if batch == 0 {
		batch = 200
	}
	result := EventRetentionResult{DeletedThrough: make(map[int64]string)}
	candidates := make(map[int64]outboxCandidate)
	if policy.MaximumAge > 0 {
		now, _ := time.Parse(time.RFC3339Nano, policy.Now)
		cutoff := now.Add(-policy.MaximumAge).UTC().Format(time.RFC3339Nano)
		if err := transaction.transaction.collectOutboxCandidates(ctx, candidates,
			`SELECT id, project_id, event_id FROM event_outbox
			  WHERE project_revision IS NOT NULL AND published_at IS NOT NULL AND occurred_at < ?
			  ORDER BY CAST(event_id AS INTEGER) ASC LIMIT ?`, []any{cutoff, batch}); err != nil {
			return EventRetentionResult{}, err
		}
	}
	if len(candidates) < batch && policy.GlobalLimit > 0 {
		remaining := batch - len(candidates)
		if err := transaction.transaction.collectOutboxCandidates(ctx, candidates,
			`SELECT id, project_id, event_id FROM event_outbox
			  WHERE project_revision IS NOT NULL AND published_at IS NOT NULL
			  ORDER BY CAST(event_id AS INTEGER) DESC LIMIT ? OFFSET ?`, []any{remaining, policy.GlobalLimit}); err != nil {
			return EventRetentionResult{}, err
		}
	}
	if len(candidates) < batch && policy.PerProjectLimit > 0 {
		projects, err := transaction.transaction.outboxProjects(ctx)
		if err != nil {
			return EventRetentionResult{}, err
		}
		for _, projectID := range projects {
			if len(candidates) >= batch {
				break
			}
			remaining := batch - len(candidates)
			if err := transaction.transaction.collectOutboxCandidates(ctx, candidates,
				`SELECT id, project_id, event_id FROM event_outbox
				  WHERE project_id = ? AND project_revision IS NOT NULL AND published_at IS NOT NULL
				  ORDER BY CAST(event_id AS INTEGER) DESC LIMIT ? OFFSET ?`, []any{projectID, remaining, policy.PerProjectLimit}); err != nil {
				return EventRetentionResult{}, err
			}
		}
	}
	ordered := make([]outboxCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		ordered = append(ordered, candidate)
	}
	sort.Slice(ordered, func(left, right int) bool {
		return ordered[left].cursor < ordered[right].cursor
	})
	for _, candidate := range ordered {
		if result.Deleted >= batch {
			break
		}
		deleteResult, err := transaction.transaction.tx.ExecContext(ctx,
			"DELETE FROM event_outbox WHERE id = ? AND project_revision IS NOT NULL AND published_at IS NOT NULL", candidate.id)
		if err != nil {
			return EventRetentionResult{}, safeSQLError(ctx, err)
		}
		affected, err := rowsAffected(deleteResult)
		if err != nil {
			return EventRetentionResult{}, err
		}
		if affected == 0 {
			continue
		}
		if affected != 1 {
			return EventRetentionResult{}, repository.ErrTransaction
		}
		if err := transaction.transaction.advanceRetentionWatermark(ctx, candidate.projectID, candidate.eventID, policy.Now); err != nil {
			return EventRetentionResult{}, err
		}
		result.Deleted++
		result.DeletedThrough[candidate.projectID] = candidate.eventID
		if err := transaction.transaction.wrote("event-outbox:prune"); err != nil {
			return EventRetentionResult{}, err
		}
	}
	return result, nil
}

type outboxCandidate struct {
	id        int64
	projectID int64
	eventID   string
	cursor    int64
}

func (transaction *writeTransaction) collectOutboxCandidates(
	ctx context.Context,
	candidates map[int64]outboxCandidate,
	query string,
	arguments []any,
) error {
	rows, err := transaction.tx.QueryContext(ctx, query, arguments...)
	if err != nil {
		return safeSQLError(ctx, err)
	}
	defer rows.Close()
	for rows.Next() {
		var candidate outboxCandidate
		if err := rows.Scan(&candidate.id, &candidate.projectID, &candidate.eventID); err != nil {
			return safeSQLError(ctx, err)
		}
		cursor, parseErr := parseEventCursor(candidate.eventID)
		if candidate.id <= 0 || candidate.projectID <= 0 || parseErr != nil || cursor <= 0 {
			return repository.ErrInvalidStore
		}
		candidate.cursor = cursor
		candidates[candidate.id] = candidate
	}
	if err := rows.Err(); err != nil {
		return safeSQLError(ctx, err)
	}
	return nil
}

func (transaction *writeTransaction) outboxProjects(ctx context.Context) ([]int64, error) {
	rows, err := transaction.tx.QueryContext(ctx,
		`SELECT DISTINCT project_id FROM event_outbox
		  WHERE project_id IS NOT NULL AND project_revision IS NOT NULL AND published_at IS NOT NULL
		  ORDER BY project_id ASC`)
	if err != nil {
		return nil, safeSQLError(ctx, err)
	}
	defer rows.Close()
	projects := make([]int64, 0)
	for rows.Next() {
		var projectID int64
		if err := rows.Scan(&projectID); err != nil || projectID <= 0 {
			return nil, repository.ErrInvalidStore
		}
		projects = append(projects, projectID)
	}
	if err := rows.Err(); err != nil {
		return nil, safeSQLError(ctx, err)
	}
	return projects, nil
}

func (transaction *writeTransaction) advanceRetentionWatermark(ctx context.Context, projectID int64, eventID, updatedAt string) error {
	if projectID <= 0 || !validP10EventID(eventID) || !validUTCTimestamp(updatedAt) {
		return repository.ErrTransaction
	}
	_, err := transaction.tx.ExecContext(ctx,
		`INSERT INTO event_retention_watermarks (project_id, deleted_through_event_id, updated_at)
		 VALUES (?, ?, ?)
		 ON CONFLICT(project_id) DO UPDATE SET
		   deleted_through_event_id = CASE
		     WHEN CAST(excluded.deleted_through_event_id AS INTEGER) > CAST(event_retention_watermarks.deleted_through_event_id AS INTEGER)
		     THEN excluded.deleted_through_event_id ELSE event_retention_watermarks.deleted_through_event_id END,
		   updated_at = excluded.updated_at`, projectID, eventID, updatedAt)
	if err != nil {
		return safeSQLError(ctx, err)
	}
	return nil
}

func (transaction *writeTransaction) appendBusinessEvent(ctx context.Context, input BusinessEvent) (domainevents.Envelope, error) {
	if transaction == nil || input.ProjectID <= 0 || !validOpaque(input.RequestID, 64) || !validUTCTimestamp(input.OccurredAt) {
		return domainevents.Envelope{}, repository.ErrTransaction
	}
	revision, err := transaction.allocateProjectRevision(ctx, input.ProjectID)
	if err != nil {
		return domainevents.Envelope{}, err
	}
	eventID, err := transaction.allocateEventID(ctx)
	if err != nil {
		return domainevents.Envelope{}, err
	}
	revisionValue := revision
	requestID := input.RequestID
	envelope := domainevents.Envelope{
		SchemaVersion: domainevents.SchemaVersion, Class: domainevents.ClassBusiness,
		EventID: &eventID, ProjectID: input.ProjectID, ProjectRevision: &revisionValue,
		Type: input.Type, OperationID: input.OperationID, RequestID: &requestID,
		OccurredAt: input.OccurredAt, Payload: cloneRawValue(input.Payload),
	}
	if envelope.Validate() != nil {
		return domainevents.Envelope{}, repository.ErrTransaction
	}
	_, err = transaction.tx.ExecContext(ctx,
		`INSERT INTO event_outbox (
		 event_id, schema_version, event_class, stream_key, sequence, type, request_id, operation_id,
		 project_id, project_revision, occurred_at, data_json, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		eventID, envelope.SchemaVersion, string(envelope.Class), "p10:project:"+strconv.FormatInt(input.ProjectID, 10), revision,
		envelope.Type, input.RequestID, optionalString(input.OperationID), input.ProjectID, revision,
		input.OccurredAt, string(envelope.Payload), input.OccurredAt)
	if err != nil {
		return domainevents.Envelope{}, safeSQLError(ctx, err)
	}
	if err := transaction.wrote("event-outbox:append-business"); err != nil {
		return domainevents.Envelope{}, err
	}
	return envelope, nil
}

// appendRuntimeAudit keeps the compatibility audit trail in the same writer
// transaction as the P10 outbox row. Its payload follows the legacy audit
// sanitizer and contains only opaque IDs, stable state and bounded metrics.
func (transaction *writeTransaction) appendRuntimeAudit(
	ctx context.Context,
	projectID int64,
	typeName, message, occurredAt string,
	payload json.RawMessage,
) error {
	meta := string(payload)
	probe := domainevent.Event{
		ID: 1, ProjectID: projectID, Type: typeName, Message: message, MetaJSON: &meta, CreatedAt: occurredAt,
	}
	if domainevent.ValidateRecord(probe) != nil {
		return repository.ErrTransaction
	}
	if _, err := transaction.tx.ExecContext(ctx,
		"INSERT INTO events (project_id, type, message, meta, created_at) VALUES (?, ?, ?, ?, ?)",
		projectID, typeName, message, meta, occurredAt); err != nil {
		return safeSQLError(ctx, err)
	}
	return transaction.wrote("events:append-runtime")
}

func scanOutboxRecord(row rowScanner) (OutboxRecord, error) {
	var result OutboxRecord
	var eventClass string
	var projectID sql.NullInt64
	var revision sql.NullInt64
	var operationID, requestID, publishedAt sql.NullString
	var payload string
	if err := row.Scan(
		&result.Envelope.EventID, &result.Envelope.SchemaVersion, &eventClass, &projectID, &revision,
		&result.Envelope.Type, &operationID, &requestID, &result.Envelope.OccurredAt, &payload,
		&publishedAt, &result.Attempts, &result.CreatedAt,
	); err != nil {
		return OutboxRecord{}, err
	}
	if !projectID.Valid || !revision.Valid || result.Attempts < 0 || !validUTCTimestamp(result.CreatedAt) {
		return OutboxRecord{}, repository.ErrInvalidStore
	}
	result.Envelope.ProjectID = projectID.Int64
	result.Envelope.ProjectRevision = int64Pointer(revision.Int64)
	result.Envelope.Class = domainevents.Class(eventClass)
	result.Envelope.OperationID = nullStringPointer(operationID)
	result.Envelope.RequestID = nullStringPointer(requestID)
	result.Envelope.Payload = json.RawMessage([]byte(payload))
	result.PublishedAt = nullStringPointer(publishedAt)
	if result.PublishedAt != nil && !validUTCTimestamp(*result.PublishedAt) {
		return OutboxRecord{}, repository.ErrInvalidStore
	}
	if result.Envelope.Validate() != nil {
		return OutboxRecord{}, repository.ErrInvalidStore
	}
	return result, nil
}

func parseEventCursor(value string) (int64, error) {
	if value == "" || value == "0" {
		return 0, nil
	}
	if !validP10EventID(value) {
		return 0, errors.New("invalid event cursor")
	}
	return strconv.ParseInt(value, 10, 64)
}

func validP10EventID(value string) bool {
	if value == "" || len(value) > 19 || value[0] == '0' {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	_, err := strconv.ParseInt(value, 10, 64)
	return err == nil
}

func validRetentionPolicy(policy EventRetentionPolicy) bool {
	if !validUTCTimestamp(policy.Now) || policy.MaximumAge < 0 || policy.GlobalLimit < 0 ||
		policy.PerProjectLimit < 0 || policy.BatchLimit < 0 || policy.BatchLimit > 1000 {
		return false
	}
	return policy.MaximumAge > 0 || policy.GlobalLimit > 0 || policy.PerProjectLimit > 0
}
