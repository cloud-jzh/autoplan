package eventbus

import (
	"context"
	"sort"
	"sync"
	"time"

	domainevents "github.com/lyming99/autoplan/backend/internal/domain/events"
	"github.com/lyming99/autoplan/backend/internal/repository"
	"github.com/lyming99/autoplan/backend/internal/repository/sqlite"
)

type DispatcherOptions struct {
	Store             Store
	Bus               *Bus
	Clock             Clock
	Warn              func(context.Context, string)
	DispatchBatch     int
	DispatchInterval  time.Duration
	RetentionInterval time.Duration
	Retention         RetentionPolicy
}

// Dispatcher reads only committed event_outbox rows. It publishes before its
// idempotent acknowledgement, making delivery at-least-once across failures
// without ever re-invoking the mutation that originally wrote the outbox row.
type Dispatcher struct {
	store             Store
	bus               *Bus
	clock             Clock
	dispatchBatch     int
	dispatchInterval  time.Duration
	retentionInterval time.Duration
	retainer          *Retainer
	warn              func(context.Context, string)
	warnings          map[string]struct{}

	mu      sync.Mutex
	cancel  context.CancelFunc
	done    chan struct{}
	started bool
}

type DispatchResult struct {
	Read         int
	Published    int
	Acknowledged int
}

func NewDispatcher(options DispatcherOptions) *Dispatcher {
	clock := options.Clock
	if clock == nil {
		clock = systemClock{}
	}
	if options.DispatchBatch <= 0 || options.DispatchBatch > 500 {
		options.DispatchBatch = 100
	}
	if options.DispatchInterval <= 0 || options.DispatchInterval > time.Minute {
		options.DispatchInterval = 250 * time.Millisecond
	}
	if options.RetentionInterval <= 0 || options.RetentionInterval > 24*time.Hour {
		options.RetentionInterval = 15 * time.Minute
	}
	retentionValid := validRetention(options.Retention)
	dispatcher := &Dispatcher{
		store: options.Store, bus: options.Bus, clock: clock, dispatchBatch: options.DispatchBatch,
		dispatchInterval: options.DispatchInterval, retentionInterval: options.RetentionInterval,
		retainer: NewRetainer(options.Store, clock, options.Retention), warn: options.Warn,
		warnings: make(map[string]struct{}),
	}
	if !retentionValid {
		dispatcher.warning(context.Background(), "event_retention_config_invalid")
	}
	return dispatcher
}

func (dispatcher *Dispatcher) Configured() bool {
	return dispatcher != nil && dispatcher.store != nil && dispatcher.bus != nil && dispatcher.bus.Configured() &&
		dispatcher.clock != nil && dispatcher.dispatchBatch > 0 && dispatcher.retainer != nil && dispatcher.retainer.Configured()
}

// Start launches one bounded worker. It is safe to call repeatedly; callers
// receive the same worker rather than parallel dispatchers that could reorder
// acknowledgements.
func (dispatcher *Dispatcher) Start(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !dispatcher.Configured() {
		return ErrUnavailable
	}
	if err := dispatcher.store.Check(ctx); err != nil {
		return err
	}
	dispatcher.mu.Lock()
	defer dispatcher.mu.Unlock()
	if dispatcher.started {
		return nil
	}
	workerContext, cancel := context.WithCancel(ctx)
	dispatcher.cancel = cancel
	dispatcher.done = make(chan struct{})
	dispatcher.started = true
	go dispatcher.run(workerContext, dispatcher.done)
	return nil
}

func (dispatcher *Dispatcher) run(ctx context.Context, done chan struct{}) {
	defer func() {
		dispatcher.mu.Lock()
		if dispatcher.done == done {
			dispatcher.started = false
			dispatcher.cancel = nil
			dispatcher.done = nil
		}
		dispatcher.mu.Unlock()
		close(done)
	}()
	dispatchTicker := time.NewTicker(dispatcher.dispatchInterval)
	defer dispatchTicker.Stop()
	retentionTicker := time.NewTicker(dispatcher.retentionInterval)
	defer retentionTicker.Stop()
	for {
		// A best-effort background pass deliberately leaves records unacknowledged
		// on any failure; the next tick or restart replays the durable row.
		if _, err := dispatcher.DispatchOnce(ctx); err != nil && ctx.Err() == nil {
			dispatcher.warning(ctx, "event_outbox_dispatch_failed")
		}
		select {
		case <-ctx.Done():
			return
		case <-dispatchTicker.C:
		case <-retentionTicker.C:
			if _, err := dispatcher.retainer.PruneOnce(ctx); err != nil && ctx.Err() == nil {
				dispatcher.warning(ctx, "event_retention_failed")
			}
		}
	}
}

func (dispatcher *Dispatcher) warning(ctx context.Context, code string) {
	if dispatcher == nil || code == "" {
		return
	}
	dispatcher.mu.Lock()
	if _, reported := dispatcher.warnings[code]; reported || dispatcher.warn == nil {
		dispatcher.mu.Unlock()
		return
	}
	dispatcher.warnings[code] = struct{}{}
	warn := dispatcher.warn
	dispatcher.mu.Unlock()
	defer func() { _ = recover() }()
	warn(ctx, code)
}

func (dispatcher *Dispatcher) Close(ctx context.Context) error {
	if dispatcher == nil {
		return nil
	}
	dispatcher.mu.Lock()
	if !dispatcher.started {
		dispatcher.mu.Unlock()
		return nil
	}
	cancel := dispatcher.cancel
	done := dispatcher.done
	dispatcher.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// DispatchOnce is exposed for deterministic lifecycle and fault-injection
// tests. It has no business-write path: the only write is MarkPublished after
// a committed record has been offered to the local bus.
func (dispatcher *Dispatcher) DispatchOnce(ctx context.Context) (DispatchResult, error) {
	if err := ctx.Err(); err != nil {
		return DispatchResult{}, err
	}
	if !dispatcher.Configured() {
		return DispatchResult{}, ErrUnavailable
	}
	if err := dispatcher.store.Check(ctx); err != nil {
		return DispatchResult{}, err
	}
	projects, err := dispatcher.store.ListProjects(ctx)
	if err != nil {
		return DispatchResult{}, err
	}
	candidates := make([]dispatchCandidate, 0, len(projects))
	for _, projectID := range projects {
		if projectID <= 0 {
			return DispatchResult{}, ErrInvalidEvent
		}
		candidate, found, readErr := dispatcher.nextPending(ctx, projectID, "")
		if readErr != nil {
			return DispatchResult{}, readErr
		}
		if found {
			candidates = append(candidates, candidate)
		}
	}

	result := DispatchResult{Read: len(candidates)}
	for len(candidates) > 0 && result.Published < dispatcher.dispatchBatch {
		sort.Slice(candidates, func(left, right int) bool {
			return cursorValue(*candidates[left].envelope.EventID) < cursorValue(*candidates[right].envelope.EventID)
		})
		candidate := candidates[0]
		candidates = candidates[1:]
		if err := dispatcher.bus.publishCommitted(candidate.envelope); err != nil {
			return result, err
		}
		result.Published++
		publishedAt := dispatcher.clock.Now().UTC().Format(time.RFC3339Nano)
		acknowledged := false
		err := dispatcher.store.Transact(ctx, func(transaction Transaction) error {
			value, markErr := transaction.MarkPublished(ctx, *candidate.envelope.EventID, publishedAt)
			if markErr != nil {
				return markErr
			}
			acknowledged = value
			return nil
		})
		if err != nil {
			return result, err
		}
		if acknowledged {
			result.Acknowledged++
		}
		next, found, nextErr := dispatcher.nextPending(ctx, candidate.projectID, *candidate.envelope.EventID)
		if nextErr != nil {
			return result, nextErr
		}
		if found {
			candidates = append(candidates, next)
			result.Read++
		}
	}
	return result, nil
}

type dispatchCandidate struct {
	projectID int64
	envelope  domainevents.Envelope
}

func (dispatcher *Dispatcher) nextPending(ctx context.Context, projectID int64, after string) (dispatchCandidate, bool, error) {
	var items []domainevents.Envelope
	err := dispatcher.store.Transact(ctx, func(transaction Transaction) error {
		value, err := transaction.ListOutbox(ctx, OutboxQuery{
			ProjectID: projectID, AfterEventID: after, Limit: 1, PendingOnly: true,
		})
		if err != nil {
			return err
		}
		items = value
		return nil
	})
	if err != nil {
		return dispatchCandidate{}, false, err
	}
	if len(items) == 0 {
		return dispatchCandidate{}, false, nil
	}
	if len(items) != 1 {
		return dispatchCandidate{}, false, ErrInvalidEvent
	}
	item := items[0]
	if item.Validate() != nil || !item.Persistent() || item.EventID == nil || item.ProjectRevision == nil || item.ProjectID != projectID {
		return dispatchCandidate{}, false, ErrInvalidEvent
	}
	return dispatchCandidate{projectID: projectID, envelope: item}, true, nil
}

// NewSQLiteStore adapts the narrow P10 repository facade for runtime use. The
// adapter exposes only committed outbox operations; transports never receive a
// SQLite Writer or raw transaction.
func NewSQLiteStore(writer *sqlite.Writer) Store {
	if writer == nil {
		return nil
	}
	return sqliteStore{writer: writer}
}

type sqliteStore struct{ writer *sqlite.Writer }

func (store sqliteStore) Check(ctx context.Context) error { return store.writer.Check(ctx) }

func (store sqliteStore) ListProjects(ctx context.Context) ([]int64, error) {
	var result []int64
	err := store.writer.Transact(ctx, func(transaction repository.WriteTransaction) error {
		projects, err := transaction.ListProjects(ctx)
		if err != nil {
			return err
		}
		result = make([]int64, 0, len(projects))
		for _, project := range projects {
			if project.ID <= 0 {
				return ErrInvalidEvent
			}
			result = append(result, project.ID)
		}
		return nil
	})
	return result, err
}

func (store sqliteStore) Transact(ctx context.Context, operation func(Transaction) error) error {
	if operation == nil {
		return ErrUnavailable
	}
	return store.writer.TransactOperations(ctx, func(transaction *sqlite.OperationTransaction) error {
		return operation(sqliteTransaction{transaction: transaction})
	})
}

type sqliteTransaction struct{ transaction *sqlite.OperationTransaction }

func (transaction sqliteTransaction) ListOutbox(ctx context.Context, query OutboxQuery) ([]domainevents.Envelope, error) {
	items, err := transaction.transaction.ListOutbox(ctx, sqlite.OutboxQuery{
		ProjectID: query.ProjectID, OperationID: query.OperationID, AfterEventID: query.AfterEventID,
		Limit: query.Limit, PendingOnly: query.PendingOnly,
	})
	if err != nil {
		return nil, err
	}
	result := make([]domainevents.Envelope, 0, len(items))
	for _, item := range items {
		result = append(result, item.Envelope)
	}
	return result, nil
}

func (transaction sqliteTransaction) RequiresResync(ctx context.Context, projectID int64, lastEventID string) (bool, error) {
	return transaction.transaction.RequiresResync(ctx, projectID, lastEventID)
}

func (transaction sqliteTransaction) MarkPublished(ctx context.Context, eventID, publishedAt string) (bool, error) {
	return transaction.transaction.MarkOutboxPublished(ctx, eventID, publishedAt)
}

func (transaction sqliteTransaction) Prune(ctx context.Context, policy RetentionPolicy) (RetentionResult, error) {
	result, err := transaction.transaction.PruneOutbox(ctx, sqlite.EventRetentionPolicy{
		Now: policy.Now, MaximumAge: policy.MaximumAge, GlobalLimit: policy.GlobalLimit,
		PerProjectLimit: policy.PerProjectLimit, BatchLimit: policy.BatchLimit,
	})
	if err != nil {
		return RetentionResult{}, err
	}
	return RetentionResult{Deleted: result.Deleted, DeletedThrough: result.DeletedThrough}, nil
}
