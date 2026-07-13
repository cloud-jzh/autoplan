package eventbus

import (
	"context"
	"errors"
	"testing"
	"time"

	domainevents "github.com/lyming99/autoplan/backend/internal/domain/events"
)

func TestDispatcherRestartReplaysUnacknowledgedOutboxWithoutBusinessReplay(t *testing.T) {
	store := &fakeStore{events: []domainevents.Envelope{persistentEvent("1", 1)}}
	clock := fixedClock{time: fixedTime()}
	closedBus := NewBus(Options{Store: store, Clock: clock, SubscriptionBuffer: 2, ReplayLimit: 10})
	if err := closedBus.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	failed := NewDispatcher(DispatcherOptions{Store: store, Bus: closedBus, Clock: clock, DispatchBatch: 4,
		Retention: RetentionPolicy{MaximumAge: time.Hour, GlobalLimit: 10, PerProjectLimit: 10, BatchLimit: 1}})
	if _, err := failed.DispatchOnce(context.Background()); !errors.Is(err, ErrBusClosed) || store.isPublished("1") {
		t.Fatalf("crash-before-ack result = %v published=%v", err, store.isPublished("1"))
	}

	restartedBus := NewBus(Options{Store: store, Clock: clock, SubscriptionBuffer: 2, ReplayLimit: 10})
	restarted := NewDispatcher(DispatcherOptions{Store: store, Bus: restartedBus, Clock: clock, DispatchBatch: 4,
		Retention: RetentionPolicy{MaximumAge: time.Hour, GlobalLimit: 10, PerProjectLimit: 10, BatchLimit: 1}})
	result, err := restarted.DispatchOnce(context.Background())
	if err != nil || result.Published != 1 || result.Acknowledged != 1 || !store.isPublished("1") {
		t.Fatalf("restart dispatch = %#v, %v", result, err)
	}
	store.mu.Lock()
	acks := append([]string(nil), store.acknowledgements...)
	store.mu.Unlock()
	if len(acks) != 1 || acks[0] != "1" || len(store.events) != 1 {
		t.Fatalf("restart duplicated acks=%v events=%d", acks, len(store.events))
	}

	subscription, err := restartedBus.Subscribe(context.Background(), SubscribeRequest{ProjectID: 1, LastEventID: "1"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, nextErr := subscription.Next(ctx); !errors.Is(nextErr, context.Canceled) {
		t.Fatalf("reconnect replayed acknowledged cursor: %v", nextErr)
	}
}

func TestRestartRetentionWatermarkForcesResyncWithoutSynthesizingEvents(t *testing.T) {
	store := &recoveryStore{fakeStore: &fakeStore{events: []domainevents.Envelope{persistentEvent("3", 3)}}, expired: true}
	bus := NewBus(Options{Store: store, Clock: fixedClock{time: fixedTime()}, SubscriptionBuffer: 1, ReplayLimit: 10})
	subscription, err := bus.Subscribe(context.Background(), SubscribeRequest{ProjectID: 1, LastEventID: "1"})
	if err != nil {
		t.Fatal(err)
	}
	delivery, err := subscription.Next(context.Background())
	if err != nil || delivery.Envelope.Type != domainevents.TypeResyncRequired || delivery.Envelope.EventID != nil || delivery.Envelope.ProjectRevision != nil {
		t.Fatalf("retention resync = %#v, %v", delivery, err)
	}
	if len(store.events) != 1 || store.events[0].EventID == nil || *store.events[0].EventID != "3" {
		t.Fatalf("retention changed durable events: %#v", store.events)
	}
}

type recoveryStore struct {
	*fakeStore
	expired bool
}

func (store *recoveryStore) Transact(ctx context.Context, operation func(Transaction) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return operation(recoveryTransaction{base: fakeTransaction{store: store.fakeStore}, expired: store.expired})
}

type recoveryTransaction struct {
	base    fakeTransaction
	expired bool
}

func (transaction recoveryTransaction) ListOutbox(ctx context.Context, query OutboxQuery) ([]domainevents.Envelope, error) {
	return transaction.base.ListOutbox(ctx, query)
}
func (transaction recoveryTransaction) RequiresResync(context.Context, int64, string) (bool, error) {
	return transaction.expired, nil
}
func (transaction recoveryTransaction) MarkPublished(ctx context.Context, eventID, at string) (bool, error) {
	return transaction.base.MarkPublished(ctx, eventID, at)
}
func (transaction recoveryTransaction) Prune(ctx context.Context, policy RetentionPolicy) (RetentionResult, error) {
	return transaction.base.Prune(ctx, policy)
}

var _ Store = (*recoveryStore)(nil)
