package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"strings"

	domainfiles "github.com/lyming99/autoplan/backend/internal/domain/files"
	"github.com/lyming99/autoplan/backend/internal/repository"
)

const attachmentOperationPrefix = "attachment:"

func (transaction *writeTransaction) CreateAttachmentOperation(ctx context.Context, value domainfiles.AttachmentOperation) error {
	if domainfiles.ValidateAttachmentOperation(value) != nil {
		return repository.ErrTransaction
	}
	payload, err := encodeAttachmentOperation(value)
	if err != nil {
		return err
	}
	status := attachmentOperationStatus(value.State)
	_, err = transaction.tx.ExecContext(ctx,
		`INSERT INTO operations (
		 operation_id, project_id, type, status, request_id, idempotency_scope,
		 request_hash, result_json, created_at, updated_at, version
		) VALUES (?, ?, ?, ?, ?, '', '', ?, ?, ?, 1)`,
		value.ID, value.ProjectID, attachmentOperationPrefix+string(value.Kind), status,
		value.ID, payload, value.CreatedAt, value.UpdatedAt,
	)
	if err != nil {
		if safeSQLError(ctx, err) == repository.ErrDuplicate {
			return repository.ErrDuplicate
		}
		return safeSQLError(ctx, err)
	}
	return transaction.wrote("attachment-operations:create")
}

func (transaction *writeTransaction) UpdateAttachmentOperation(ctx context.Context, value domainfiles.AttachmentOperation) error {
	if domainfiles.ValidateAttachmentOperation(value) != nil {
		return repository.ErrTransaction
	}
	payload, err := encodeAttachmentOperation(value)
	if err != nil {
		return err
	}
	status := attachmentOperationStatus(value.State)
	result, err := transaction.tx.ExecContext(ctx,
		`UPDATE operations
		    SET status = ?, result_json = ?, error_json = NULL, updated_at = ?, version = version + 1,
		        finished_at = CASE WHEN ? IN ('succeeded', 'failed') THEN ? ELSE NULL END
		  WHERE operation_id = ? AND project_id = ? AND type = ?`,
		status, payload, value.UpdatedAt, status, value.UpdatedAt,
		value.ID, value.ProjectID, attachmentOperationPrefix+string(value.Kind),
	)
	if err != nil {
		return safeSQLError(ctx, err)
	}
	if err := requireOneRow(result); err != nil {
		return err
	}
	return transaction.wrote("attachment-operations:update")
}

func (transaction *writeTransaction) GetAttachmentOperation(
	ctx context.Context,
	operationID string,
) (domainfiles.AttachmentOperation, bool, error) {
	if operationID == "" {
		return domainfiles.AttachmentOperation{}, false, repository.ErrTransaction
	}
	var payload sql.NullString
	err := transaction.tx.QueryRowContext(ctx,
		"SELECT result_json FROM operations WHERE operation_id = ? AND type LIKE 'attachment:%'", operationID).Scan(&payload)
	if err == sql.ErrNoRows {
		return domainfiles.AttachmentOperation{}, false, nil
	}
	if err != nil || !payload.Valid {
		return domainfiles.AttachmentOperation{}, false, repository.ErrTransaction
	}
	value, err := decodeAttachmentOperation(payload.String)
	if err != nil {
		return domainfiles.AttachmentOperation{}, false, err
	}
	return value, true, nil
}

func (transaction *writeTransaction) ListAttachmentOperations(
	ctx context.Context,
	projectID int64,
	includeComplete bool,
) ([]domainfiles.AttachmentOperation, error) {
	if projectID <= 0 {
		return nil, repository.ErrTransaction
	}
	query := "SELECT result_json FROM operations WHERE project_id = ? AND type LIKE 'attachment:%'"
	arguments := []any{projectID}
	if !includeComplete {
		query += " AND status IN ('queued', 'running', 'failed', 'interrupted')"
	}
	query += " ORDER BY created_at ASC, operation_id ASC"
	rows, err := transaction.tx.QueryContext(ctx, query, arguments...)
	if err != nil {
		return nil, safeSQLError(ctx, err)
	}
	defer rows.Close()
	result := make([]domainfiles.AttachmentOperation, 0)
	for rows.Next() {
		var payload sql.NullString
		if err := rows.Scan(&payload); err != nil || !payload.Valid {
			return nil, repository.ErrTransaction
		}
		value, err := decodeAttachmentOperation(payload.String)
		if err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	if err := rows.Err(); err != nil {
		return nil, safeSQLError(ctx, err)
	}
	return result, nil
}

func encodeAttachmentOperation(value domainfiles.AttachmentOperation) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil || len(encoded) == 0 || len(encoded) > maximumIdempotencyJSONBytes {
		return "", repository.ErrTransaction
	}
	return string(encoded), nil
}

func decodeAttachmentOperation(value string) (domainfiles.AttachmentOperation, error) {
	var result domainfiles.AttachmentOperation
	decoder := json.NewDecoder(strings.NewReader(value))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&result) != nil || domainfiles.ValidateAttachmentOperation(result) != nil {
		return domainfiles.AttachmentOperation{}, repository.ErrTransaction
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return domainfiles.AttachmentOperation{}, repository.ErrTransaction
	}
	return result, nil
}

func attachmentOperationStatus(state domainfiles.OperationState) string {
	switch state {
	case domainfiles.StateReady, domainfiles.StateComplete:
		return "succeeded"
	case domainfiles.StateFailed:
		return "failed"
	default:
		return "running"
	}
}
