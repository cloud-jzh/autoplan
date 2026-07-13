package sqlite

import (
	"context"
	"database/sql/driver"
	"errors"
	"testing"
	"time"
)

func TestListOutboxUsesP10CursorAndProjectBoundary(t *testing.T) {
	backend := &scriptBackend{steps: []scriptStep{
		queryStep("FROM event_outbox WHERE project_id", outboxTestColumns(), outboxTestValues("41", 1, 4, "operation.queued", "operation-41", nil)),
	}}
	writer, cleanup := newTestWriter(t, backend)
	defer cleanup()

	var records []OutboxRecord
	err := writer.TransactOperations(context.Background(), func(transaction *OperationTransaction) error {
		var listErr error
		records, listErr = transaction.ListOutbox(context.Background(), OutboxQuery{ProjectID: 1, AfterEventID: "40", Limit: 10})
		return listErr
	})
	if err != nil || len(records) != 1 || records[0].Envelope.ProjectID != 1 || *records[0].Envelope.EventID != "41" {
		t.Fatalf("outbox records = %#v, error = %v", records, err)
	}
	backend.assertFinished(t, 1, 0)
}

func TestRetentionSkipsUndeliveredRowsAndAdvancesWatermarkAtomically(t *testing.T) {
	backend := &scriptBackend{steps: []scriptStep{
		queryStep("occurred_at <", []string{"id", "project_id", "event_id"}, []driver.Value{int64(9), int64(1), "41"}),
		execStep("DELETE FROM event_outbox", 1, 0),
		execStep("INSERT INTO event_retention_watermarks", 1, 0),
	}}
	writer, cleanup := newTestWriter(t, backend)
	defer cleanup()

	var result EventRetentionResult
	err := writer.TransactOperations(context.Background(), func(transaction *OperationTransaction) error {
		var pruneErr error
		result, pruneErr = transaction.PruneOutbox(context.Background(), EventRetentionPolicy{
			Now: operationTestTime, MaximumAge: time.Hour, BatchLimit: 10,
		})
		return pruneErr
	})
	if err != nil || result.Deleted != 1 || result.DeletedThrough[1] != "41" {
		t.Fatalf("retention result = %#v, error = %v", result, err)
	}
	backend.assertFinished(t, 1, 0)
}

func TestRetentionWatermarkRequiresResyncAndPublishIsIdempotent(t *testing.T) {
	backend := &scriptBackend{steps: []scriptStep{
		queryStep("FROM event_retention_watermarks", []string{"deleted_through_event_id"}, []driver.Value{"41"}),
		execStep("UPDATE event_outbox", 1, 0),
		execStep("UPDATE event_outbox", 0, 0),
	}}
	writer, cleanup := newTestWriter(t, backend)
	defer cleanup()

	err := writer.TransactOperations(context.Background(), func(transaction *OperationTransaction) error {
		resync, resyncErr := transaction.RequiresResync(context.Background(), 1, "40")
		if resyncErr != nil || !resync {
			return errors.New("retained history gap was not detected")
		}
		changed, publishErr := transaction.MarkOutboxPublished(context.Background(), "42", "2026-07-12T00:00:02.000Z")
		if publishErr != nil || !changed {
			return errors.New("first publish acknowledgement did not write")
		}
		changed, publishErr = transaction.MarkOutboxPublished(context.Background(), "42", "2026-07-12T00:00:03.000Z")
		if publishErr != nil || changed {
			return errors.New("publish replay was not idempotent")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("watermark and publish error = %v", err)
	}
	backend.assertFinished(t, 1, 0)
}

func outboxTestColumns() []string {
	return []string{
		"event_id", "schema_version", "event_class", "project_id", "project_revision",
		"type", "operation_id", "request_id", "occurred_at", "data_json", "published_at", "attempts", "created_at",
	}
}

func outboxTestValues(eventID string, projectID, revision int64, eventType, operationID string, publishedAt *string) []driver.Value {
	var operation, published driver.Value
	if operationID != "" {
		operation = operationID
	}
	if publishedAt != nil {
		published = *publishedAt
	}
	return []driver.Value{
		eventID, int64(1), "operation", projectID, revision, eventType, operation,
		"request-operation", operationTestTime, `{"status":"queued"}`, published, int64(0), operationTestTime,
	}
}
