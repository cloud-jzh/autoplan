package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"unicode"

	domainconfig "github.com/lyming99/autoplan/backend/internal/domain/config"
	"github.com/lyming99/autoplan/backend/internal/repository"
)

const maximumIdempotencyJSONBytes = 2 << 20

func (transaction *writeTransaction) FindIdempotency(
	ctx context.Context,
	scope string,
	key string,
) (repository.IdempotencyRecord, bool, error) {
	if !validOpaque(scope, 512) || !validOpaque(key, 256) {
		return repository.IdempotencyRecord{}, false, repository.ErrTransaction
	}
	record, err := scanIdempotency(transaction.tx.QueryRowContext(ctx,
		`SELECT operation_id, project_id, type, request_id, idempotency_scope, idempotency_key,
		        request_hash, status, result_json, error_json, version, created_at, updated_at
		   FROM operations WHERE idempotency_scope = ? AND idempotency_key = ?`, scope, key))
	if err == sql.ErrNoRows {
		return repository.IdempotencyRecord{}, false, nil
	}
	if err != nil {
		return repository.IdempotencyRecord{}, false, safeSQLError(ctx, err)
	}
	return record, true, nil
}

func (transaction *writeTransaction) ReserveIdempotency(
	ctx context.Context,
	record repository.IdempotencyRecord,
) error {
	if !validOpaque(record.OperationID, 256) || !validOpaque(record.RequestID, 256) ||
		!validOpaque(record.Route, 128) || !validOpaque(record.Scope, 512) || !validOpaque(record.Key, 256) ||
		!validHash(record.RequestHash) || !domainconfig.ValidUTCTimestamp(record.CreatedAt) ||
		!domainconfig.ValidUTCTimestamp(record.UpdatedAt) ||
		(record.ProjectID != nil && *record.ProjectID <= 0) {
		return repository.ErrTransaction
	}
	existing, found, err := transaction.FindIdempotency(ctx, record.Scope, record.Key)
	if err != nil {
		return err
	}
	if found {
		if existing.Route != record.Route || existing.RequestHash != record.RequestHash ||
			!equalOptionalInt(existing.ProjectID, record.ProjectID) {
			return repository.ErrIdempotencyKeyReuse
		}
		return repository.ErrDuplicate
	}
	status := record.Status
	if status == "" {
		status = "queued"
	}
	if status != "queued" && status != "running" {
		return repository.ErrTransaction
	}
	var startedAt *string
	if status == "running" {
		value := record.CreatedAt
		startedAt = &value
	}
	_, err = transaction.tx.ExecContext(ctx,
		`INSERT INTO operations (
		 operation_id, project_id, type, status, request_id, idempotency_scope,
		 idempotency_key, request_hash, created_at, updated_at, started_at, version
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1)`,
		record.OperationID, record.ProjectID, record.Route, status, record.RequestID,
		record.Scope, record.Key, record.RequestHash, record.CreatedAt, record.UpdatedAt, startedAt)
	if err != nil {
		if safeSQLError(ctx, err) == repository.ErrDuplicate {
			existing, found, findErr := transaction.FindIdempotency(ctx, record.Scope, record.Key)
			if findErr == nil && found && existing.Route == record.Route && existing.RequestHash == record.RequestHash &&
				equalOptionalInt(existing.ProjectID, record.ProjectID) {
				return repository.ErrDuplicate
			}
			if findErr != nil {
				return findErr
			}
			return repository.ErrIdempotencyKeyReuse
		}
		return safeSQLError(ctx, err)
	}
	return transaction.wrote("operations:reserve")
}

func (transaction *writeTransaction) CompleteIdempotency(
	ctx context.Context,
	scope string,
	key string,
	status string,
	resultJSON *string,
	errorJSON *string,
	updatedAt string,
) error {
	if !validOpaque(scope, 512) || !validOpaque(key, 256) || !domainconfig.ValidUTCTimestamp(updatedAt) ||
		!validCompletion(status, resultJSON, errorJSON) {
		return repository.ErrTransaction
	}
	result, err := transaction.tx.ExecContext(ctx,
		`UPDATE operations
		    SET status = ?, result_json = ?, error_json = ?, updated_at = ?, version = version + 1,
		        finished_at = CASE WHEN ? IN ('succeeded', 'failed', 'cancelled', 'interrupted') THEN ? ELSE finished_at END
		  WHERE idempotency_scope = ? AND idempotency_key = ? AND status IN ('queued', 'running')`,
		status, resultJSON, errorJSON, updatedAt, status, updatedAt, scope, key)
	if err != nil {
		return safeSQLError(ctx, err)
	}
	if err := requireOneRow(result); err != nil {
		return err
	}
	return transaction.wrote("operations:complete")
}

func scanIdempotency(row rowScanner) (repository.IdempotencyRecord, error) {
	var result repository.IdempotencyRecord
	var projectID sql.NullInt64
	var key sql.NullString
	var resultJSON, errorJSON sql.NullString
	if err := row.Scan(
		&result.OperationID, &projectID, &result.Route, &result.RequestID, &result.Scope, &key,
		&result.RequestHash, &result.Status, &resultJSON, &errorJSON, &result.Version,
		&result.CreatedAt, &result.UpdatedAt,
	); err != nil {
		return repository.IdempotencyRecord{}, err
	}
	if !key.Valid {
		return repository.IdempotencyRecord{}, repository.ErrTransaction
	}
	result.Key = key.String
	if projectID.Valid {
		value := projectID.Int64
		result.ProjectID = &value
	}
	result.ResultJSON = nullStringPointer(resultJSON)
	result.ErrorJSON = nullStringPointer(errorJSON)
	return result, nil
}

func validCompletion(status string, resultJSON, errorJSON *string) bool {
	switch status {
	case "queued", "running":
		return resultJSON == nil && errorJSON == nil
	case "succeeded":
		return validJSON(resultJSON) && errorJSON == nil
	case "failed", "cancelled", "interrupted":
		return resultJSON == nil && validJSON(errorJSON)
	default:
		return false
	}
}

func validJSON(value *string) bool {
	return value != nil && len(*value) > 0 && len(*value) <= maximumIdempotencyJSONBytes && json.Valid([]byte(*value))
}

func validHash(value string) bool {
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

func validOpaque(value string, maximum int) bool {
	if value == "" || len(value) > maximum || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func equalOptionalInt(left, right *int64) bool {
	return (left == nil && right == nil) || (left != nil && right != nil && *left == *right)
}
