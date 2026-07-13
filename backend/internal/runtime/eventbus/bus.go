// Package eventbus delivers committed P10 outbox records to local streaming
// transports. It never creates business events or Operation mutations: the
// SQLite outbox remains the authority for both replay and crash recovery.
package eventbus

import (
	"context"
	"errors"
	"sync"
	"time"

	domainevents "github.com/lyming99/autoplan/backend/internal/domain/events"
)

var (
	ErrUnavailable         = errors.New("event bus is unavailable")
	ErrInvalidSubscription = errors.New("event subscription is invalid")
	ErrSubscriptionClosed  = errors.New("event subscription is closed")
	ErrBusClosed           = errors.New("event bus is closed")
	ErrInvalidEvent        = errors.New("event outbox record is invalid")
)

type Clock interface{ Now() time.Time }

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now().UTC() }

// Store is a runtime-shaped view of committed outbox data. Implementations
// may acknowledge delivery and prune already-published entries, but expose no
// operation or project mutation methods to the event bus.
type Store interface {
	Check(context.Context) error
	ListProjects(context.Context) ([]int64, error)
	Transact(context.Context, func(Transaction) error) error
}

type Transaction interface {
	ListOutbox(context.Context, OutboxQuery) ([]domainevents.Envelope, error)
	RequiresResync(context.Context, int64, string) (bool, error)
	MarkPublished(context.Context, string, string) (bool, error)
	Prune(context.Context, RetentionPolicy) (RetentionResult, error)
}

type OutboxQuery struct {
	ProjectID    int64
	OperationID  string
	AfterEventID string
	Limit        int
	PendingOnly  bool
}

// Options must be bounded by the caller. NewBus defensively applies safe
// defaults as a second line of protection for direct runtime construction.
type Options struct {
	Store              Store
	Clock              Clock
	SubscriptionBuffer int
	ReplayLimit        int
}

type Bus struct {
	mu                 sync.Mutex
	store              Store
	clock              Clock
	subscriptionBuffer int
	replayLimit        int
	subscriptions      map[uint64]*Subscription
	nextSubscriptionID uint64
	closed             bool
}

func NewBus(options Options) *Bus {
	clock := options.Clock
	if clock == nil {
		clock = systemClock{}
	}
	if options.SubscriptionBuffer <= 0 || options.SubscriptionBuffer > 1024 {
		options.SubscriptionBuffer = 64
	}
	if options.ReplayLimit <= 0 || options.ReplayLimit > 20000 {
		options.ReplayLimit = 5000
	}
	return &Bus{
		store: options.Store, clock: clock, subscriptionBuffer: options.SubscriptionBuffer,
		replayLimit: options.ReplayLimit, subscriptions: make(map[uint64]*Subscription),
	}
}

func (bus *Bus) Configured() bool {
	return bus != nil && bus.store != nil && bus.clock != nil
}

// Subscribe establishes a replay/live boundary under the bus lock. A
// dispatcher cannot publish between the durable replay read and registration,
// so clients see a sequence suitable for event_id de-duplication.
func (bus *Bus) Subscribe(ctx context.Context, request SubscribeRequest) (*Subscription, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if bus == nil || !bus.Configured() {
		return nil, ErrUnavailable
	}
	if !request.valid() {
		return nil, ErrInvalidSubscription
	}
	if err := bus.store.Check(ctx); err != nil {
		return nil, err
	}

	bus.mu.Lock()
	defer bus.mu.Unlock()
	if bus.closed {
		return nil, ErrBusClosed
	}

	if !validCursor(request.LastEventID) {
		return bus.resyncSubscriptionLocked(request, "last_event_id_invalid"), nil
	}
	lastEventID := cursorValue(request.LastEventID)
	initialRevision := int64(0)
	var replay []domainevents.Envelope
	resyncReason := ""
	err := bus.store.Transact(ctx, func(transaction Transaction) error {
		expired, err := transaction.RequiresResync(ctx, request.ProjectID, request.LastEventID)
		if err != nil {
			return err
		}
		if expired {
			resyncReason = "history_expired"
			return nil
		}
		var truncated bool
		replay, truncated, err = readReplay(ctx, transaction, request, bus.replayLimit)
		if err != nil {
			return err
		}
		if truncated {
			resyncReason = "history_expired"
			return nil
		}
		if lastEventID > 0 {
			revision, found, cursorErr := cursorRevision(ctx, transaction, request, lastEventID)
			if cursorErr != nil {
				return cursorErr
			}
			if found {
				initialRevision = revision
				return nil
			}
			latest, found, latestErr := latestEventID(ctx, transaction, request.ProjectID)
			if latestErr != nil {
				return latestErr
			}
			if !found || lastEventID > latest {
				resyncReason = "last_event_id_future"
			} else {
				resyncReason = "project_mismatch"
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if resyncReason != "" {
		return bus.resyncSubscriptionLocked(request, resyncReason), nil
	}

	subscription := bus.newSubscriptionLocked(request, lastEventID, initialRevision)
	for _, envelope := range replay {
		if reason := subscription.appendReplay(envelope); reason != "" {
			return bus.resyncSubscriptionLocked(request, reason), nil
		}
	}
	bus.subscriptions[subscription.id] = subscription
	return subscription, nil
}

// publishCommitted is called only by Dispatcher after it has read a committed
// durable outbox record. It performs no persistence and therefore cannot
// re-run a business mutation after a process crash.
func (bus *Bus) publishCommitted(envelope domainevents.Envelope) error {
	if bus == nil || !bus.Configured() {
		return ErrUnavailable
	}
	if !envelope.Persistent() || envelope.Validate() != nil || envelope.EventID == nil || envelope.ProjectRevision == nil {
		return ErrInvalidEvent
	}
	bus.mu.Lock()
	defer bus.mu.Unlock()
	if bus.closed {
		return ErrBusClosed
	}
	for id, subscription := range bus.subscriptions {
		if subscription.projectID != envelope.ProjectID ||
			(subscription.operationID != "" && (envelope.OperationID == nil || *envelope.OperationID != subscription.operationID)) {
			continue
		}
		if reason := subscription.offerLive(envelope); reason != "" {
			subscription.resync(reason)
			delete(bus.subscriptions, id)
		}
	}
	return nil
}

// Close is idempotent and releases every subscriber without waiting for a
// slow transport. Dispatchers are closed first by bootstrap, so no event is
// acknowledged after the bus begins shutdown.
func (bus *Bus) Close(context.Context) error {
	if bus == nil {
		return nil
	}
	bus.mu.Lock()
	defer bus.mu.Unlock()
	if bus.closed {
		return nil
	}
	bus.closed = true
	for id, subscription := range bus.subscriptions {
		subscription.close()
		delete(bus.subscriptions, id)
	}
	return nil
}

func (bus *Bus) newSubscriptionLocked(request SubscribeRequest, lastEventID, initialRevision int64) *Subscription {
	bus.nextSubscriptionID++
	return newSubscription(bus, bus.nextSubscriptionID, request, lastEventID, initialRevision, bus.subscriptionBuffer)
}

func (bus *Bus) resyncSubscriptionLocked(request SubscribeRequest, reason string) *Subscription {
	subscription := bus.newSubscriptionLocked(request, cursorValue(request.LastEventID), 0)
	subscription.resync(reason)
	return subscription
}

func cursorRevision(ctx context.Context, transaction Transaction, request SubscribeRequest, cursor int64) (int64, bool, error) {
	after := ""
	previous := int64(0)
	for {
		items, err := transaction.ListOutbox(ctx, OutboxQuery{
			ProjectID: request.ProjectID, OperationID: request.OperationID, AfterEventID: after, Limit: 500,
		})
		if err != nil {
			return 0, false, err
		}
		if len(items) == 0 {
			return 0, false, nil
		}
		if len(items) > 500 {
			return 0, false, ErrInvalidEvent
		}
		for _, item := range items {
			if item.Validate() != nil || !item.Persistent() || item.EventID == nil || item.ProjectRevision == nil || item.ProjectID != request.ProjectID {
				return 0, false, ErrInvalidEvent
			}
			eventID := cursorValue(*item.EventID)
			if eventID <= previous {
				return 0, false, ErrInvalidEvent
			}
			previous = eventID
			if eventID == cursor {
				return *item.ProjectRevision, true, nil
			}
			if eventID > cursor {
				return 0, false, nil
			}
		}
		after = *items[len(items)-1].EventID
		if len(items) < 500 {
			return 0, false, nil
		}
	}
}

func readReplay(ctx context.Context, transaction Transaction, request SubscribeRequest, maximum int) ([]domainevents.Envelope, bool, error) {
	result := make([]domainevents.Envelope, 0)
	after := request.LastEventID
	previous := cursorValue(request.LastEventID)
	for len(result) < maximum {
		limit := maximum - len(result)
		if limit > 500 {
			limit = 500
		}
		items, err := transaction.ListOutbox(ctx, OutboxQuery{
			ProjectID: request.ProjectID, OperationID: request.OperationID, AfterEventID: after, Limit: limit,
		})
		if err != nil {
			return nil, false, err
		}
		if len(items) == 0 {
			return result, false, nil
		}
		if len(items) > limit {
			return nil, false, ErrInvalidEvent
		}
		for _, item := range items {
			if item.Validate() != nil || !item.Persistent() || item.EventID == nil || item.ProjectRevision == nil || item.ProjectID != request.ProjectID {
				return nil, false, ErrInvalidEvent
			}
			eventID := cursorValue(*item.EventID)
			if eventID <= previous {
				return nil, false, ErrInvalidEvent
			}
			previous = eventID
			result = append(result, item)
		}
		after = *items[len(items)-1].EventID
		if len(items) < limit {
			return result, false, nil
		}
	}
	more, err := transaction.ListOutbox(ctx, OutboxQuery{
		ProjectID: request.ProjectID, OperationID: request.OperationID, AfterEventID: after, Limit: 1,
	})
	if err != nil {
		return nil, false, err
	}
	return result, len(more) > 0, nil
}

func latestEventID(ctx context.Context, transaction Transaction, projectID int64) (int64, bool, error) {
	after := ""
	var latest int64
	found := false
	for {
		items, err := transaction.ListOutbox(ctx, OutboxQuery{ProjectID: projectID, AfterEventID: after, Limit: 500})
		if err != nil {
			return 0, false, err
		}
		if len(items) == 0 {
			return latest, found, nil
		}
		if len(items) > 500 {
			return 0, false, ErrInvalidEvent
		}
		for _, item := range items {
			if item.Validate() != nil || !item.Persistent() || item.EventID == nil || item.ProjectID != projectID {
				return 0, false, ErrInvalidEvent
			}
			next := cursorValue(*item.EventID)
			if next <= latest {
				return 0, false, ErrInvalidEvent
			}
			latest = next
			found = true
		}
		after = *items[len(items)-1].EventID
		if len(items) < 500 {
			return latest, found, nil
		}
	}
}
