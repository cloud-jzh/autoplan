package sqlite

import (
	"context"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"

	"github.com/lyming99/autoplan/backend/internal/repository"
)

func TestProjectContractIdempotencyReplayReadsCommittedReferenceWithoutWriting(t *testing.T) {
	projectID := int64(7)
	resultJSON := `{"kind":"active-project","project_id":7}`
	backend := &scriptBackend{steps: []scriptStep{
		queryStep("FROM operations WHERE idempotency_scope", idempotencyTestColumns(), []driver.Value{
			"operation-fixture", projectID, "projects:update", "request-fixture", "caller:projects:update:7",
			"intent-fixture", strings.Repeat("a", 64), "succeeded", resultJSON, nil, int64(2),
			"2026-07-11T00:00:00.000Z", "2026-07-11T00:00:01.000Z",
		}),
	}}
	writer, cleanup := newTestWriter(t, backend)
	defer cleanup()
	var record repository.IdempotencyRecord
	var found bool
	err := writer.Transact(context.Background(), func(transaction repository.WriteTransaction) error {
		var err error
		record, found, err = transaction.FindIdempotency(context.Background(), "caller:projects:update:7", "intent-fixture")
		return err
	})
	if err != nil || !found || record.Status != "succeeded" || record.ResultJSON == nil ||
		*record.ResultJSON != resultJSON || record.Version != 2 || record.ProjectID == nil || *record.ProjectID != projectID {
		t.Fatalf("committed replay record drifted: found=%v record=%#v err=%v", found, record, err)
	}
	backend.assertFinished(t, 1, 0)
}

func TestProjectContractSameKeyDifferentPayloadRollsBackWithoutSecondSideEffect(t *testing.T) {
	projectID := int64(8)
	backend := &scriptBackend{steps: []scriptStep{
		queryStep("FROM operations WHERE idempotency_scope", idempotencyTestColumns(), []driver.Value{
			"operation-existing", projectID, "projects:update", "request-existing", "caller:projects:update:8",
			"same-intent", strings.Repeat("b", 64), "succeeded", `{"kind":"active-project","project_id":8}`,
			nil, int64(2), "2026-07-11T00:00:00.000Z", "2026-07-11T00:00:01.000Z",
		}),
	}}
	writer, cleanup := newTestWriter(t, backend)
	defer cleanup()
	err := writer.Transact(context.Background(), func(transaction repository.WriteTransaction) error {
		return transaction.ReserveIdempotency(context.Background(), repository.IdempotencyRecord{
			OperationID: "operation-new", ProjectID: &projectID, Route: "projects:update", RequestID: "request-new",
			Scope: "caller:projects:update:8", Key: "same-intent", RequestHash: strings.Repeat("a", 64),
			CreatedAt: "2026-07-11T00:00:02.000Z", UpdatedAt: "2026-07-11T00:00:02.000Z",
		})
	})
	if !errors.Is(err, repository.ErrIdempotencyKeyReuse) {
		t.Fatalf("same key with different payload returned %v", err)
	}
	backend.assertFinished(t, 0, 1)
}

func TestProjectContractDatabaseFailuresUseClosedStableErrors(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want error
	}{
		{"duplicate", errors.New("UNIQUE constraint failed: synthetic.value"), repository.ErrDuplicate},
		{"relation", errors.New("FOREIGN KEY constraint failed: synthetic.value"), repository.ErrRelationConflict},
		{"busy", errors.New("database is locked: synthetic detail"), ErrConnectionUnavailable},
		{"internal", errors.New("synthetic SQL statement and private value"), repository.ErrTransaction},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			actual := safeSQLError(context.Background(), item.err)
			if !errors.Is(actual, item.want) || strings.Contains(actual.Error(), "synthetic") ||
				strings.Contains(actual.Error(), "private") {
				t.Fatalf("unsafe SQL error mapping: %v", actual)
			}
		})
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if actual := safeSQLError(cancelled, errors.New("private SQL detail")); !errors.Is(actual, context.Canceled) {
		t.Fatalf("context cancellation mapping drifted: %v", actual)
	}
}
