package eventbus

import (
	"context"
	"sort"
	"sync"
	"testing"
	"time"

	domainevents "github.com/lyming99/autoplan/backend/internal/domain/events"
)

func TestSubscribeReplaysThenContinuesLiveWithoutBoundaryGap(t *testing.T) {
	store := &fakeStore{events: []domainevents.Envelope{persistentEvent("1", 1)}}
	bus := NewBus(Options{Store: store, Clock: fixedClock{time: fixedTime()}, SubscriptionBuffer: 2, ReplayLimit: 10})
	subscription, err := bus.Subscribe(context.Background(), SubscribeRequest{ProjectID: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := bus.publishCommitted(persistentEvent("2", 2)); err != nil {
		t.Fatal(err)
	}
	first, err := subscription.Next(context.Background())
	if err != nil || first.Envelope.EventID == nil || *first.Envelope.EventID != "1" || !first.Replay {
		t.Fatalf("replay delivery = %#v, %v", first, err)
	}
	second, err := subscription.Next(context.Background())
	if err != nil || second.Envelope.EventID == nil || *second.Envelope.EventID != "2" || second.Replay {
		t.Fatalf("live delivery = %#v, %v", second, err)
	}
}

func TestSlowSubscriberReceivesResyncInsteadOfDroppingContinuity(t *testing.T) {
	store := &fakeStore{}
	bus := NewBus(Options{Store: store, Clock: fixedClock{time: fixedTime()}, SubscriptionBuffer: 1, ReplayLimit: 10})
	subscription, err := bus.Subscribe(context.Background(), SubscribeRequest{ProjectID: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := bus.publishCommitted(persistentEvent("1", 1)); err != nil {
		t.Fatal(err)
	}
	if err := bus.publishCommitted(persistentEvent("2", 2)); err != nil {
		t.Fatal(err)
	}
	delivery, err := subscription.Next(context.Background())
	if err != nil || delivery.Envelope.Class != domainevents.ClassControl || delivery.Envelope.Type != domainevents.TypeResyncRequired {
		t.Fatalf("slow consumer result = %#v, %v", delivery, err)
	}
	if _, err := subscription.Next(context.Background()); err != ErrSubscriptionClosed {
		t.Fatalf("subscription remained live after resync: %v", err)
	}
}

func TestReplayRevisionGapRequiresResync(t *testing.T) {
	store := &fakeStore{events: []domainevents.Envelope{persistentEvent("1", 1), persistentEvent("3", 3)}}
	bus := NewBus(Options{Store: store, Clock: fixedClock{time: fixedTime()}, SubscriptionBuffer: 1, ReplayLimit: 10})
	subscription, err := bus.Subscribe(context.Background(), SubscribeRequest{ProjectID: 1, LastEventID: "1"})
	if err != nil {
		t.Fatal(err)
	}
	delivery, err := subscription.Next(context.Background())
	if err != nil || delivery.Envelope.Type != domainevents.TypeResyncRequired {
		t.Fatalf("revision gap result = %#v, %v", delivery, err)
	}
}

func TestDispatcherPublishesCommittedOutboxBeforeAcknowledging(t *testing.T) {
	store := &fakeStore{events: []domainevents.Envelope{persistentEvent("1", 1)}}
	clock := fixedClock{time: fixedTime()}
	bus := NewBus(Options{Store: store, Clock: clock, SubscriptionBuffer: 1, ReplayLimit: 10})
	dispatcher := NewDispatcher(DispatcherOptions{
		Store: store, Bus: bus, Clock: clock, DispatchBatch: 10,
		Retention: RetentionPolicy{MaximumAge: time.Hour, GlobalLimit: 10, PerProjectLimit: 10, BatchLimit: 1},
	})
	result, err := dispatcher.DispatchOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Published != 1 || result.Acknowledged != 1 || !store.isPublished("1") {
		t.Fatalf("dispatch result = %#v, published=%v", result, store.isPublished("1"))
	}
}

func TestDispatcherAcknowledgesProjectsInGlobalEventOrder(t *testing.T) {
	store := &fakeStore{events: []domainevents.Envelope{
		persistentProjectEvent(2, "2", 1), persistentProjectEvent(1, "1", 1),
	}}
	clock := fixedClock{time: fixedTime()}
	bus := NewBus(Options{Store: store, Clock: clock, SubscriptionBuffer: 1, ReplayLimit: 10})
	dispatcher := NewDispatcher(DispatcherOptions{
		Store: store, Bus: bus, Clock: clock, DispatchBatch: 10,
		Retention: RetentionPolicy{MaximumAge: time.Hour, GlobalLimit: 10, PerProjectLimit: 10, BatchLimit: 1},
	})
	if _, err := dispatcher.DispatchOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	got := append([]string(nil), store.acknowledgements...)
	store.mu.Unlock()
	if len(got) != 2 || got[0] != "1" || got[1] != "2" {
		t.Fatalf("acknowledgement order = %v", got)
	}
}

type fixedClock struct{ time time.Time }

func (clock fixedClock) Now() time.Time { return clock.time }

func fixedTime() time.Time { return time.Date(2026, 7, 12, 8, 0, 0, 0, time.UTC) }

func persistentEvent(eventID string, revision int64) domainevents.Envelope {
	return persistentProjectEvent(1, eventID, revision)
}

func persistentProjectEvent(projectID int64, eventID string, revision int64) domainevents.Envelope {
	operationID := "operation-1"
	requestID := "request-1"
	return domainevents.Envelope{
		SchemaVersion: domainevents.SchemaVersion, Class: domainevents.ClassOperation, EventID: &eventID,
		ProjectID: projectID, ProjectRevision: &revision, Type: domainevents.TypeOperationRunning,
		OperationID: &operationID, RequestID: &requestID, OccurredAt: fixedTime().Format(time.RFC3339Nano),
		Payload: []byte(`{"status":"running"}`),
	}
}

type fakeStore struct {
	mu               sync.Mutex
	events           []domainevents.Envelope
	published        map[string]bool
	acknowledgements []string
}

func (store *fakeStore) Check(ctx context.Context) error { return ctx.Err() }

func (store *fakeStore) ListProjects(context.Context) ([]int64, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	seen := make(map[int64]struct{})
	for _, event := range store.events {
		seen[event.ProjectID] = struct{}{}
	}
	result := make([]int64, 0, len(seen))
	for projectID := range seen {
		result = append(result, projectID)
	}
	sort.Slice(result, func(left, right int) bool { return result[left] > result[right] })
	return result, nil
}

func (store *fakeStore) Transact(ctx context.Context, operation func(Transaction) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return operation(fakeTransaction{store: store})
}

func (store *fakeStore) isPublished(eventID string) bool {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.published[eventID]
}

type fakeTransaction struct{ store *fakeStore }

func (transaction fakeTransaction) ListOutbox(_ context.Context, query OutboxQuery) ([]domainevents.Envelope, error) {
	transaction.store.mu.Lock()
	defer transaction.store.mu.Unlock()
	items := make([]domainevents.Envelope, 0)
	for _, item := range transaction.store.events {
		if item.ProjectID != query.ProjectID || item.EventID == nil || cursorValue(*item.EventID) <= cursorValue(query.AfterEventID) ||
			(query.OperationID != "" && (item.OperationID == nil || *item.OperationID != query.OperationID)) ||
			(query.PendingOnly && transaction.store.published[*item.EventID]) {
			continue
		}
		items = append(items, item)
	}
	sort.Slice(items, func(left, right int) bool {
		return cursorValue(*items[left].EventID) < cursorValue(*items[right].EventID)
	})
	if query.Limit > 0 && len(items) > query.Limit {
		items = items[:query.Limit]
	}
	return items, nil
}

func (transaction fakeTransaction) RequiresResync(context.Context, int64, string) (bool, error) {
	return false, nil
}

func (transaction fakeTransaction) MarkPublished(_ context.Context, eventID, _ string) (bool, error) {
	transaction.store.mu.Lock()
	defer transaction.store.mu.Unlock()
	if transaction.store.published == nil {
		transaction.store.published = make(map[string]bool)
	}
	if transaction.store.published[eventID] {
		return false, nil
	}
	transaction.store.published[eventID] = true
	transaction.store.acknowledgements = append(transaction.store.acknowledgements, eventID)
	return true, nil
}

func (transaction fakeTransaction) Prune(context.Context, RetentionPolicy) (RetentionResult, error) {
	return RetentionResult{DeletedThrough: map[int64]string{}}, nil
}
