package sqlite

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"testing"

	domainoperation "github.com/lyming99/autoplan/backend/internal/domain/operation"
	"github.com/lyming99/autoplan/backend/internal/repository"
)

const operationTestTime = "2026-07-12T00:00:00.000Z"

func TestOperationCreateCommitsOperationRevisionAndOutboxTogether(t *testing.T) {
	backend := &scriptBackend{steps: []scriptStep{
		queryStep("SELECT 1 FROM projects", []string{"1"}, []driver.Value{int64(1)}),
		queryStep("FROM operations WHERE idempotency_scope", operationTestColumns()),
		execStep("INSERT INTO operations", 1, 0),
		execStep("INSERT OR IGNORE INTO project_revisions", 1, 0),
		execStep("UPDATE project_revisions SET revision", 1, 0),
		queryStep("SELECT revision FROM project_revisions", []string{"revision"}, []driver.Value{int64(1)}),
		execStep("UPDATE event_cursors SET next_event_id", 1, 0),
		queryStep("SELECT next_event_id FROM event_cursors", []string{"next_event_id"}, []driver.Value{int64(41)}),
		execStep("INSERT INTO event_outbox", 1, 0),
	}}
	writer, cleanup := newTestWriter(t, backend)
	defer cleanup()

	var mutation OperationMutation
	err := writer.TransactOperations(context.Background(), func(transaction *OperationTransaction) error {
		var createErr error
		mutation, createErr = transaction.Create(context.Background(), operationCreateFixture("operation-41", "digest-41"))
		return createErr
	})
	if err != nil || !mutation.Changed || mutation.Event == nil || *mutation.Event.EventID != "41" ||
		mutation.Operation.ProjectID != 1 || mutation.Operation.Status != domainoperation.StatusQueued {
		t.Fatalf("create mutation = %#v, error = %v", mutation, err)
	}
	backend.assertFinished(t, 1, 0)
}

func TestOperationCreateIdempotencyReplayAndConflictDoNotWrite(t *testing.T) {
	existing := operationTestValues("operation-existing", "digest-existing", domainoperation.StatusQueued, 1, nil, nil, nil)
	backend := &scriptBackend{steps: []scriptStep{
		queryStep("SELECT 1 FROM projects", []string{"1"}, []driver.Value{int64(1)}),
		queryStep("FROM operations WHERE idempotency_scope", operationTestColumns(), existing),
		queryStep("SELECT 1 FROM projects", []string{"1"}, []driver.Value{int64(1)}),
		queryStep("FROM operations WHERE idempotency_scope", operationTestColumns(), existing),
	}}
	writer, cleanup := newTestWriter(t, backend)
	defer cleanup()

	err := writer.TransactOperations(context.Background(), func(transaction *OperationTransaction) error {
		mutation, createErr := transaction.Create(context.Background(), operationCreateFixture("operation-new", "digest-existing"))
		if createErr != nil || mutation.Changed || mutation.Operation.OperationID != "operation-existing" {
			return errors.New("equivalent idempotency replay was not reused")
		}
		_, createErr = transaction.Create(context.Background(), operationCreateFixture("operation-new", "different-digest"))
		return createErr
	})
	if !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("idempotency conflict error = %v", err)
	}
	backend.assertFinished(t, 0, 1)
}

func TestOperationCancellationRequestAndOutboxRollbackTogether(t *testing.T) {
	startedAt := operationTestTime
	running := operationTestValues("operation-running", "digest-running", domainoperation.StatusRunning, 2, &startedAt, nil, nil)
	backend := &scriptBackend{steps: []scriptStep{
		queryStep("FROM operations WHERE project_id", operationTestColumns(), running),
		execStep("UPDATE operations", 1, 0),
		execStep("INSERT OR IGNORE INTO project_revisions", 1, 0),
		execStep("UPDATE project_revisions SET revision", 1, 0),
		queryStep("SELECT revision FROM project_revisions", []string{"revision"}, []driver.Value{int64(5)}),
		execStep("UPDATE event_cursors SET next_event_id", 1, 0),
		queryStep("SELECT next_event_id FROM event_cursors", []string{"next_event_id"}, []driver.Value{int64(51)}),
		execStep("INSERT INTO event_outbox", 1, 0),
	}}
	writer, cleanup := newTestWriter(t, backend)
	defer cleanup()
	writer.faults.afterWrite = func(label string) error {
		if label == "event-outbox:append-operation" {
			return errors.New("outbox failure")
		}
		return nil
	}

	err := writer.TransactOperations(context.Background(), func(transaction *OperationTransaction) error {
		_, cancelErr := transaction.RequestCancellation(context.Background(), CancelOperation{
			ProjectID: 1, OperationID: "operation-running", ExpectedVersion: 2,
			RequestID: "cancel-request-51", RequestedAt: "2026-07-12T00:00:01.000Z",
		})
		return cancelErr
	})
	if !errors.Is(err, repository.ErrTransaction) {
		t.Fatalf("atomic cancellation rollback error = %v", err)
	}
	backend.assertFinished(t, 0, 1)
}

func operationCreateFixture(id, digestSuffix string) CreateOperation {
	key := "key-operation"
	digest := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	if digestSuffix == "different-digest" {
		digest = "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	}
	if digestSuffix == "digest-existing" {
		digest = "1111111111111111111111111111111111111111111111111111111111111111"
	}
	return CreateOperation{
		Operation: domainoperation.Operation{
			OperationID: id, ProjectID: 1, Type: "task.run", Status: domainoperation.StatusQueued,
			RequestID: "request-operation", IdempotencyKey: &key, RequestDigest: digest,
			Version: 1, CreatedAt: operationTestTime, UpdatedAt: operationTestTime,
		},
		IdempotencyScope: "project:1:task.run",
		Payload:          json.RawMessage(`{"status":"queued"}`),
	}
}

func operationTestColumns() []string {
	return []string{
		"operation_id", "project_id", "type", "status", "request_id", "idempotency_scope",
		"idempotency_key", "request_hash", "cancel_requested_at", "created_at", "updated_at", "started_at",
		"finished_at", "result_json", "error_json", "output_json", "version",
	}
}

func operationTestValues(
	id, digest string,
	status domainoperation.Status,
	version int64,
	startedAt, finishedAt *string,
	errorSummary *domainoperation.ErrorSummary,
) []driver.Value {
	key := "key-operation"
	var started, finished, encodedError driver.Value
	if startedAt != nil {
		started = *startedAt
	}
	if finishedAt != nil {
		finished = *finishedAt
	}
	if errorSummary != nil {
		encoded, _ := json.Marshal(errorSummary)
		encodedError = string(encoded)
	}
	requestDigest := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	if digest == "digest-existing" {
		requestDigest = "1111111111111111111111111111111111111111111111111111111111111111"
	}
	if digest == "digest-running" {
		requestDigest = "2222222222222222222222222222222222222222222222222222222222222222"
	}
	return []driver.Value{
		id, int64(1), "task.run", string(status), "request-operation", "project:1:task.run",
		key, requestDigest, nil, operationTestTime, operationTestTime, started, finished, nil, encodedError, nil, version,
	}
}
