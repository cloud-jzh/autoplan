package scheduler

import (
	"context"
	"sync"
	"time"
)

// Config controls bounded scheduling mechanics. WorkerLimit and both queue
// capacities are mandatory: zero never means unbounded or disabled.
type Config struct {
	WorkerLimit        int
	QueueCapacity      int
	ActorQueueCapacity int
}

func DefaultConfig() Config {
	return Config{WorkerLimit: 4, QueueCapacity: 32, ActorQueueCapacity: 64}
}

func (config Config) valid() bool {
	return config.WorkerLimit > 0 && config.QueueCapacity >= 0 && config.ActorQueueCapacity > 0
}

type Dependencies struct {
	Config          Config
	Clock           Clock
	ProcessLauncher ProcessLauncher
	EventBus        EventBus
}

// Manager owns all project actors and exactly one global worker pool. It is
// the only scheduler object assembled by bootstrap; callers never create a
// second pool for an individual transport or project.
type Manager struct {
	config   Config
	clock    Clock
	launcher ProcessLauncher
	events   EventBus
	pool     *WorkerPool

	mu      sync.Mutex
	actors  map[int64]*Actor
	closed  bool
	closedAt time.Time
}

func NewManager(dependencies Dependencies) (*Manager, error) {
	if !dependencies.Config.valid() || dependencies.Clock == nil {
		return nil, ErrInvalidConfig
	}
	launcher := dependencies.ProcessLauncher
	if launcher == nil {
		launcher = unavailableProcessLauncher{}
	}
	events := dependencies.EventBus
	if events == nil {
		events = discardEventBus{}
	}
	pool, err := NewWorkerPool(dependencies.Clock, WorkPoolConfig{
		WorkerLimit: dependencies.Config.WorkerLimit, QueueCapacity: dependencies.Config.QueueCapacity,
	})
	if err != nil {
		return nil, err
	}
	return &Manager{
		config: dependencies.Config, clock: dependencies.Clock, launcher: launcher, events: events,
		pool: pool, actors: make(map[int64]*Actor),
	}, nil
}

// Submit routes a command to its project actor. Admission failure occurs
// before Start, so queue pressure cannot create an unclaimed operation.
func (manager *Manager) Submit(ctx context.Context, projectID int64, command Command) (*Submission, error) {
	if manager == nil || projectID <= 0 {
		return nil, ErrInvalidCommand
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	actor, err := manager.actor(projectID)
	if err != nil {
		return nil, err
	}
	return actor.Submit(ctx, command)
}

func (manager *Manager) actor(projectID int64) (*Actor, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.closed {
		return nil, ErrManagerClosed
	}
	if actor := manager.actors[projectID]; actor != nil {
		return actor, nil
	}
	actor, err := NewActor(projectID, manager.pool, manager.clock, manager.config.ActorQueueCapacity)
	if err != nil {
		return nil, err
	}
	manager.actors[projectID] = actor
	return actor, nil
}

// StartProcess is a narrow delegation point for the future P003 runner. The
// manager never falls back to os/exec and never changes command boundaries.
func (manager *Manager) StartProcess(ctx context.Context, spec ProcessSpec) (Process, error) {
	if manager == nil {
		return nil, ErrManagerClosed
	}
	if ctx == nil {
		ctx = context.Background()
	}
	manager.mu.Lock()
	closed := manager.closed
	launcher := manager.launcher
	manager.mu.Unlock()
	if closed {
		return nil, ErrManagerClosed
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return launcher.Start(ctx, spec)
}

// NewTimer exposes the injected scheduler clock to later retry and timeout
// orchestration without reintroducing time.After or real sleep into a use
// case. Timers are rejected once shutdown begins.
func (manager *Manager) NewTimer(wait time.Duration) (Timer, error) {
	if manager == nil {
		return nil, ErrManagerClosed
	}
	if wait < 0 {
		return nil, ErrInvalidCommand
	}
	manager.mu.Lock()
	closed := manager.closed
	clock := manager.clock
	manager.mu.Unlock()
	if closed {
		return nil, ErrManagerClosed
	}
	return clock.NewTimer(wait), nil
}

// Publish exposes the injected event dependency for runtime components. It
// rejects service shutdown rather than publishing a late, unowned event.
func (manager *Manager) Publish(ctx context.Context, event Event) error {
	if manager == nil || event.ProjectID <= 0 || event.Kind == "" {
		return ErrInvalidCommand
	}
	if ctx == nil {
		ctx = context.Background()
	}
	manager.mu.Lock()
	closed := manager.closed
	events := manager.events
	manager.mu.Unlock()
	if closed {
		return ErrManagerClosed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if event.At.IsZero() {
		event.At = manager.clock.Now().UTC()
	}
	return events.Publish(ctx, event)
}

func (manager *Manager) PoolStats() PoolStats {
	if manager == nil {
		return PoolStats{Closed: true}
	}
	return manager.pool.Stats()
}

func (manager *Manager) ActorStats(projectID int64) (ActorStats, bool) {
	if manager == nil || projectID <= 0 {
		return ActorStats{}, false
	}
	manager.mu.Lock()
	actor := manager.actors[projectID]
	manager.mu.Unlock()
	if actor == nil {
		return ActorStats{}, false
	}
	return actor.Stats(), true
}

// RemoveProject is the lifecycle boundary used when a project is deleted.
// It first removes the actor from admission, then cancels its queued/running
// work. A deleted project therefore cannot retain a worker slot or post a
// late completion into a replacement actor.
func (manager *Manager) RemoveProject(ctx context.Context, projectID int64) error {
	if manager == nil || projectID <= 0 {
		return ErrInvalidCommand
	}
	if ctx == nil {
		ctx = context.Background()
	}
	manager.mu.Lock()
	if manager.closed {
		manager.mu.Unlock()
		return ErrManagerClosed
	}
	actor := manager.actors[projectID]
	delete(manager.actors, projectID)
	manager.mu.Unlock()
	if actor == nil {
		return nil
	}
	return actor.Close(ctx)
}

// Close stops actor admission first, cancels all accepted work, then waits for
// actors and workers using the caller's deadline. It is safe to invoke more
// than once and never creates replacement actors during shutdown.
func (manager *Manager) Close(ctx context.Context) error {
	if manager == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	manager.mu.Lock()
	if !manager.closed {
		manager.closed = true
		manager.closedAt = manager.clock.Now().UTC()
	}
	actors := make([]*Actor, 0, len(manager.actors))
	for _, actor := range manager.actors {
		actors = append(actors, actor)
	}
	manager.mu.Unlock()
	var first error
	for _, actor := range actors {
		if err := actor.Close(ctx); err != nil && first == nil {
			first = err
		}
	}
	if err := manager.pool.Close(ctx); err != nil && first == nil {
		first = err
	}
	return first
}
