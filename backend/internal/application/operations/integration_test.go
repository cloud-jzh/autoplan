package operations

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	domainoperation "github.com/lyming99/autoplan/backend/internal/domain/operation"
	"github.com/lyming99/autoplan/backend/internal/repository"
)

// These integration fixtures model the observable transaction boundary: an
// outbox row is staged with each changed Operation and becomes visible only
// after the application transaction returns successfully.
type integrationOutboxStore struct {
	backing *operationMemoryStore
	events  []integrationOutboxEvent
}

type integrationOutboxEvent struct {
	OperationID string
	ProjectID   int64
	Status      domainoperation.Status
	Revision    int64
}

func newIntegrationOutboxStore(values ...domainoperation.Operation) *integrationOutboxStore {
	return &integrationOutboxStore{backing: newOperationMemoryStore(values...)}
}

func (store *integrationOutboxStore) Check(ctx context.Context) error {
	return store.backing.Check(ctx)
}
func (store *integrationOutboxStore) ListProjects(ctx context.Context) ([]repository.Project, error) {
	return store.backing.ListProjects(ctx)
}
func (store *integrationOutboxStore) Transact(ctx context.Context, operation func(Transaction) error) error {
	staged := make([]integrationOutboxEvent, 0, 2)
	err := store.backing.Transact(ctx, func(transaction Transaction) error {
		return operation(integrationOutboxTransaction{base: transaction, staged: &staged})
	})
	if err != nil {
		return err
	}
	for index := range staged {
		staged[index].Revision = int64(len(store.events) + index + 1)
	}
	store.events = append(store.events, staged...)
	return nil
}

type integrationOutboxTransaction struct {
	base   Transaction
	staged *[]integrationOutboxEvent
}

func (transaction integrationOutboxTransaction) Create(ctx context.Context, value domainoperation.Operation, scope string, payload json.RawMessage) (domainoperation.Operation, bool, error) {
	stored, changed, err := transaction.base.Create(ctx, value, scope, payload)
	if changed && err == nil {
		transaction.stage(stored)
	}
	return stored, changed, err
}
func (transaction integrationOutboxTransaction) Get(ctx context.Context, projectID int64, operationID string) (domainoperation.Operation, bool, error) {
	return transaction.base.Get(ctx, projectID, operationID)
}
func (transaction integrationOutboxTransaction) List(ctx context.Context, query ListQuery) ([]domainoperation.Operation, error) {
	return transaction.base.List(ctx, query)
}
func (transaction integrationOutboxTransaction) Transition(ctx context.Context, input Transition) (domainoperation.Operation, bool, error) {
	stored, changed, err := transaction.base.Transition(ctx, input)
	if changed && err == nil {
		transaction.stage(stored)
	}
	return stored, changed, err
}
func (transaction integrationOutboxTransaction) RequestCancellation(ctx context.Context, input CancelRequest) (domainoperation.Operation, bool, error) {
	stored, changed, err := transaction.base.RequestCancellation(ctx, input)
	if changed && err == nil {
		transaction.stage(stored)
	}
	return stored, changed, err
}
func (transaction integrationOutboxTransaction) stage(value domainoperation.Operation) {
	*transaction.staged = append(*transaction.staged, integrationOutboxEvent{
		OperationID: value.OperationID, ProjectID: value.ProjectID, Status: value.Status,
	})
}

func TestOperationOutboxFaultMatrixPreservesOneTerminalHistory(t *testing.T) {
	store := newIntegrationOutboxStore()
	service := NewService(Dependencies{
		Store:            store,
		Clock:            operationTestClock{now: time.Date(2026, 7, 12, 9, 0, 0, 0, time.UTC)},
		NewID:            func() string { return "operation-integration-1" },
		RecoveryHandlers: []RecoveryHandler{operationTestHandler{operationType: "task.run"}},
	})
	caller := Caller{ID: "integration-runner", ProjectID: 7}
	create := CreateCommand{
		Caller: caller, ProjectID: 7, Type: "task.run", IdempotencyKey: "intent-7", RequestID: "create-7",
		RequestDigest: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	}
	queued, err := service.CreateOrReuse(context.Background(), create)
	if err != nil || !queued.Changed || queued.Operation.Status != domainoperation.StatusQueued {
		t.Fatalf("create = %#v, %v", queued, err)
	}
	if replay, replayErr := service.CreateOrReuse(context.Background(), create); replayErr != nil || replay.Changed || replay.Operation.OperationID != queued.Operation.OperationID {
		t.Fatalf("idempotency replay = %#v, %v", replay, replayErr)
	}
	if _, conflictErr := service.CreateOrReuse(context.Background(), CreateCommand{
		Caller: caller, ProjectID: 7, Type: "task.run", IdempotencyKey: "intent-7", RequestID: "create-conflict",
		RequestDigest: "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff",
	}); !errors.Is(conflictErr, ErrIdempotencyConflict) {
		t.Fatalf("idempotency conflict = %v", conflictErr)
	}
	if _, illegalErr := service.Succeed(context.Background(), CompleteCommand{
		Caller: caller, ProjectID: 7, OperationID: queued.Operation.OperationID, ExpectedVersion: 1, RequestID: "finish-queued",
	}); !errors.Is(illegalErr, ErrStateConflict) {
		t.Fatalf("queued completion = %v", illegalErr)
	}

	running, err := service.Claim(context.Background(), ClaimCommand{
		Caller: caller, ProjectID: 7, OperationID: queued.Operation.OperationID, ExpectedVersion: 1,
		RequestDigest: create.RequestDigest, RequestID: "claim-7",
	})
	if err != nil || !running.Changed || running.Operation.Status != domainoperation.StatusRunning || running.Operation.Version != 2 {
		t.Fatalf("claim = %#v, %v", running, err)
	}
	cancelRequested, err := service.RequestCancel(context.Background(), CancelCommand{
		Caller: caller, ProjectID: 7, OperationID: queued.Operation.OperationID, ExpectedVersion: 2, RequestID: "cancel-7",
	})
	if err != nil || !cancelRequested.Changed || cancelRequested.Operation.CancelRequestedAt == nil || cancelRequested.Operation.Version != 3 {
		t.Fatalf("cancel request = %#v, %v", cancelRequested, err)
	}
	completed, err := service.Succeed(context.Background(), CompleteCommand{
		Caller: caller, ProjectID: 7, OperationID: queued.Operation.OperationID, ExpectedVersion: 3, RequestID: "finish-7",
	})
	if err != nil || !completed.Changed || completed.Operation.Status != domainoperation.StatusSucceeded || completed.Operation.Version != 4 {
		t.Fatalf("completion = %#v, %v", completed, err)
	}
	if replay, replayErr := service.RequestCancel(context.Background(), CancelCommand{
		Caller: caller, ProjectID: 7, OperationID: queued.Operation.OperationID, ExpectedVersion: 3, RequestID: "cancel-replay-7",
	}); replayErr != nil || replay.Changed || replay.Operation.Status != domainoperation.StatusSucceeded {
		t.Fatalf("late cancel replay = %#v, %v", replay, replayErr)
	}

	if len(store.events) != 4 {
		t.Fatalf("event history length = %d, want 4", len(store.events))
	}
	for index, event := range store.events {
		if event.OperationID != queued.Operation.OperationID || event.ProjectID != 7 || event.Revision != int64(index+1) {
			t.Fatalf("outbox event[%d] = %#v", index, event)
		}
	}
	if statuses := []domainoperation.Status{store.events[0].Status, store.events[1].Status, store.events[2].Status, store.events[3].Status}; statuses[0] != domainoperation.StatusQueued || statuses[1] != domainoperation.StatusRunning ||
		statuses[2] != domainoperation.StatusRunning || statuses[3] != domainoperation.StatusSucceeded {
		t.Fatalf("outbox status history = %v", statuses)
	}
}

func TestRecoveryMatrixInterruptsWithoutRunnerSideEffects(t *testing.T) {
	createdAt := "2026-07-10T00:00:00Z"
	keyRunning, keyQueued := "recover-running", "recover-queued"
	startedAt := createdAt
	store := newIntegrationOutboxStore(
		domainoperation.Operation{OperationID: "operation-running", ProjectID: 7, Type: "task.run", Status: domainoperation.StatusRunning,
			RequestID: "request-running", IdempotencyKey: &keyRunning, RequestDigest: "1111111111111111111111111111111111111111111111111111111111111111",
			Version: 2, CreatedAt: createdAt, UpdatedAt: createdAt, StartedAt: &startedAt},
		domainoperation.Operation{OperationID: "operation-queued", ProjectID: 7, Type: "task.run", Status: domainoperation.StatusQueued,
			RequestID: "request-queued", IdempotencyKey: &keyQueued, RequestDigest: "2222222222222222222222222222222222222222222222222222222222222222",
			Version: 1, CreatedAt: createdAt, UpdatedAt: createdAt},
	)
	service := NewService(Dependencies{
		Store: store, Projects: operationTestProjects{{ID: 7}},
		Clock:                operationTestClock{now: time.Date(2026, 7, 12, 9, 0, 0, 0, time.UTC)},
		QueuedRecoveryMaxAge: time.Hour,
	})
	result, err := service.Recover(context.Background())
	if err != nil || len(result) != 2 {
		t.Fatalf("recover = %#v, %v", result, err)
	}
	for _, operationID := range []string{"operation-running", "operation-queued"} {
		value := store.backing.operations[operationID]
		if value.Status != domainoperation.StatusInterrupted || value.Error == nil || value.Error.Code == "" {
			t.Fatalf("recovery result %s = %#v", operationID, value)
		}
	}
	if len(store.events) != 2 || store.events[0].Status != domainoperation.StatusInterrupted || store.events[1].Status != domainoperation.StatusInterrupted {
		t.Fatalf("recovery outbox history = %#v", store.events)
	}
}

var _ Store = (*integrationOutboxStore)(nil)
