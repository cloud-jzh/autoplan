package sqlite

import (
	"context"
	"database/sql"

	domainevent "github.com/lyming99/autoplan/backend/internal/domain/event"
	"github.com/lyming99/autoplan/backend/internal/repository"
)

const maximumEventPageSize = 200

func (transaction *writeTransaction) ListEvents(
	ctx context.Context,
	options domainevent.ListOptions,
) ([]domainevent.Event, error) {
	if options.ProjectID <= 0 || options.Offset < 0 {
		return nil, repository.ErrInvalidEvent
	}
	found, err := transaction.projectExists(ctx, options.ProjectID)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, repository.ErrNotFound
	}
	limit := options.Limit
	if limit <= 0 {
		limit = 80
	}
	if limit > maximumEventPageSize {
		limit = maximumEventPageSize
	}
	rows, err := transaction.tx.QueryContext(ctx,
		`SELECT id, project_id, type, message, meta, created_at FROM events
		  WHERE project_id = ? ORDER BY id DESC LIMIT ? OFFSET ?`, options.ProjectID, limit, options.Offset)
	if err != nil {
		return nil, safeSQLError(ctx, err)
	}
	defer rows.Close()
	result := make([]domainevent.Event, 0)
	for rows.Next() {
		value, scanErr := scanEvent(rows)
		if scanErr != nil {
			return nil, safeSQLError(ctx, scanErr)
		}
		result = append(result, value)
	}
	if err := rows.Err(); err != nil {
		return nil, safeSQLError(ctx, err)
	}
	return result, nil
}

func (transaction *writeTransaction) AppendEvent(ctx context.Context, value domainevent.PendingEvent) error {
	if domainevent.ValidatePending(value) != nil {
		return repository.ErrInvalidEvent
	}
	found, err := transaction.projectExists(ctx, value.ProjectID)
	if err != nil {
		return err
	}
	if !found {
		return repository.ErrNotFound
	}
	if value.OperationID != nil {
		var projectID sql.NullInt64
		err := transaction.tx.QueryRowContext(ctx,
			"SELECT project_id FROM operations WHERE operation_id = ?", *value.OperationID).Scan(&projectID)
		if err == sql.ErrNoRows {
			return repository.ErrNotFound
		}
		if err != nil {
			return safeSQLError(ctx, err)
		}
		if !projectID.Valid || projectID.Int64 != value.ProjectID {
			return repository.ErrProjectMismatch
		}
	}
	if _, err := transaction.tx.ExecContext(ctx,
		"INSERT INTO events (project_id, type, message, meta, created_at) VALUES (?, ?, ?, ?, ?)",
		value.ProjectID, value.Type, value.Message, optionalString(value.MetaJSON), value.OccurredAt); err != nil {
		return safeSQLError(ctx, err)
	}
	if err := transaction.wrote("events:append-plan"); err != nil {
		return err
	}
	dataJSON := "{}"
	if value.MetaJSON != nil {
		dataJSON = *value.MetaJSON
	}
	if _, err := transaction.tx.ExecContext(ctx,
		`INSERT INTO event_outbox
		 (event_id, schema_version, stream_key, sequence, type, request_id, operation_id,
		  project_id, occurred_at, data_json, created_at)
		 VALUES (?, 1, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		value.EventID, value.StreamKey, value.Sequence, value.Type, value.RequestID,
		optionalString(value.OperationID), value.ProjectID, value.OccurredAt, dataJSON, value.CreatedAt); err != nil {
		return safeSQLError(ctx, err)
	}
	return transaction.wrote("event-outbox:append-plan")
}

func scanEvent(row rowScanner) (domainevent.Event, error) {
	var value domainevent.Event
	var meta sql.NullString
	if err := row.Scan(&value.ID, &value.ProjectID, &value.Type, &value.Message, &meta, &value.CreatedAt); err != nil {
		return domainevent.Event{}, err
	}
	value.MetaJSON = nullStringPointer(meta)
	if domainevent.ValidateRecord(value) != nil {
		return domainevent.Event{}, repository.ErrInvalidStore
	}
	value.MetaJSON = domainevent.SanitizeMetaJSON(value.MetaJSON)
	return value, nil
}
