package scheduler

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Work is external, potentially slow work. It receives a cancellation-aware
// context and must not perform application state transitions itself.
type Work func(context.Context) error

type WorkResult struct {
	ProjectID int64
	StartedAt time.Time
	EndedAt   time.Time
	Started   bool
	Cancelled bool
	Err       error
}

type WorkPoolConfig struct {
	WorkerLimit   int
	QueueCapacity int
}

func (config WorkPoolConfig) valid() bool {
	return config.WorkerLimit > 0 && config.QueueCapacity >= 0
}

// PoolStats is a point-in-time bounded-resource report. It contains counts
// only, never command or process details.
type PoolStats struct {
	WorkerLimit int
	Capacity    int
	Reserved    int
	Queued      int
	Running     int
	Closed      bool
}

type workState uint8

const (
	workQueued workState = iota + 1
	workRunning
	workCancelling
	workFinished
)

type workItem struct {
	projectID int64
	ctx       context.Context
	cancel    context.CancelFunc
	work      Work
	done      chan WorkResult
	state     workState
}

// WorkHandle permits an explicit cancellation request and exposes one
// immutable terminal result. Cancelled queued work never invokes Work.
type WorkHandle struct {
	pool *WorkerPool
	item *workItem
}

func (handle *WorkHandle) Done() <-chan WorkResult {
	if handle == nil || handle.item == nil {
		return nil
	}
	return handle.item.done
}

func (handle *WorkHandle) Cancel() bool {
	if handle == nil || handle.pool == nil || handle.item == nil {
		return false
	}
	return handle.pool.cancel(handle.item)
}

// Reservation is acquired before an actor commits a start transition. This
// prevents an accepted state mutation from being left without worker capacity
// when the global queue is full.
type Reservation struct {
	pool     *WorkerPool
	projectID int64
	once     sync.Once
}

func (reservation *Reservation) Start(ctx context.Context, work Work) (*WorkHandle, error) {
	if reservation == nil || reservation.pool == nil || work == nil {
		return nil, ErrInvalidCommand
	}
	var result *WorkHandle
	var startErr error
	called := false
	reservation.once.Do(func() {
		called = true
		result, startErr = reservation.pool.startReserved(ctx, reservation.projectID, work)
	})
	if !called {
		return nil, ErrInvalidCommand
	}
	return result, startErr
}

func (reservation *Reservation) Release() {
	if reservation == nil || reservation.pool == nil {
		return
	}
	reservation.once.Do(func() {
		reservation.pool.releaseReservation()
	})
}

// WorkerPool provides global bounded capacity. Queue admission uses a
// reservation; queued work is then dispatched fairly round-robin by project.
type WorkerPool struct {
	clock  Clock
	config WorkPoolConfig

	mu          sync.Mutex
	cond        *sync.Cond
	queue       fairQueue
	reserved    int
	running     int
	inFlight    int
	active      map[*workItem]struct{}
	closed      bool
	workersDone chan struct{}
	workers     sync.WaitGroup
}

func NewWorkerPool(clock Clock, config WorkPoolConfig) (*WorkerPool, error) {
	if clock == nil || !config.valid() {
		return nil, ErrInvalidConfig
	}
	pool := &WorkerPool{
		clock: clock, config: config, active: make(map[*workItem]struct{}),
		workersDone: make(chan struct{}),
	}
	pool.cond = sync.NewCond(&pool.mu)
	pool.workers.Add(config.WorkerLimit)
	for index := 0; index < config.WorkerLimit; index++ {
		go pool.worker()
	}
	go func() {
		pool.workers.Wait()
		close(pool.workersDone)
	}()
	return pool, nil
}

// Reserve fails closed when the pool is full or closed. It does not start
// Work and is safe to call from a project actor before its start transition.
func (pool *WorkerPool) Reserve(ctx context.Context, projectID int64) (*Reservation, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	if pool == nil || projectID <= 0 {
		return nil, ErrInvalidCommand
	}
	pool.mu.Lock()
	defer pool.mu.Unlock()
	if pool.closed {
		return nil, ErrWorkerPoolClosed
	}
	if pool.inFlight >= pool.config.WorkerLimit+pool.config.QueueCapacity {
		return nil, ErrWorkerQueueFull
	}
	pool.inFlight++
	pool.reserved++
	return &Reservation{pool: pool, projectID: projectID}, nil
}

func (pool *WorkerPool) startReserved(ctx context.Context, projectID int64, work Work) (*WorkHandle, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := contextError(ctx); err != nil {
		pool.releaseReservation()
		return nil, err
	}
	pool.mu.Lock()
	defer pool.mu.Unlock()
	if pool.reserved <= 0 {
		return nil, ErrInvalidCommand
	}
	pool.reserved--
	if pool.closed {
		pool.inFlight--
		return nil, ErrWorkerPoolClosed
	}
	workContext, cancel := context.WithCancel(ctx)
	item := &workItem{
		projectID: projectID, ctx: workContext, cancel: cancel, work: work,
		done: make(chan WorkResult, 1), state: workQueued,
	}
	pool.queue.Push(item)
	pool.active[item] = struct{}{}
	pool.cond.Signal()
	return &WorkHandle{pool: pool, item: item}, nil
}

func (pool *WorkerPool) releaseReservation() {
	if pool == nil {
		return
	}
	pool.mu.Lock()
	defer pool.mu.Unlock()
	if pool.reserved <= 0 || pool.inFlight <= 0 {
		return
	}
	pool.reserved--
	pool.inFlight--
}

func (pool *WorkerPool) worker() {
	defer pool.workers.Done()
	for {
		item := pool.next()
		if item == nil {
			return
		}
		result := pool.execute(item)
		pool.finish(item, result)
	}
}

func (pool *WorkerPool) next() *workItem {
	pool.mu.Lock()
	defer pool.mu.Unlock()
	for pool.queue.Len() == 0 && !pool.closed {
		pool.cond.Wait()
	}
	if pool.queue.Len() == 0 && pool.closed {
		return nil
	}
	item := pool.queue.Pop()
	if item == nil {
		return nil
	}
	if item.state == workQueued {
		if item.ctx.Err() != nil {
			item.state = workFinished
			return item
		}
		item.state = workRunning
		pool.running++
	}
	return item
}

func (pool *WorkerPool) execute(item *workItem) (result WorkResult) {
	result = WorkResult{ProjectID: item.projectID, StartedAt: pool.clock.Now().UTC()}
	pool.mu.Lock()
	state := item.state
	pool.mu.Unlock()
	if state == workFinished || item.ctx.Err() != nil {
		result.Cancelled = true
		result.Err = context.Canceled
		result.EndedAt = pool.clock.Now().UTC()
		return result
	}
	result.Started = true
	defer func() {
		if recovered := recover(); recovered != nil {
			result.Err = fmt.Errorf("%w: %v", ErrWorkerPanic, recovered)
		}
		pool.mu.Lock()
		cancelled := item.state == workCancelling
		pool.mu.Unlock()
		if cancelled {
			result.Cancelled = true
			result.Err = context.Canceled
		}
		result.EndedAt = pool.clock.Now().UTC()
	}()
	result.Err = item.work(item.ctx)
	return result
}

func (pool *WorkerPool) finish(item *workItem, result WorkResult) {
	pool.mu.Lock()
	defer pool.mu.Unlock()
	if item.state == workFinished {
		pool.finishLocked(item, result)
		return
	}
	if item.state == workRunning || item.state == workCancelling {
		pool.running--
	}
	item.state = workFinished
	pool.finishLocked(item, result)
}

func (pool *WorkerPool) finishLocked(item *workItem, result WorkResult) {
	if _, exists := pool.active[item]; !exists {
		return
	}
	delete(pool.active, item)
	if pool.inFlight > 0 {
		pool.inFlight--
	}
	item.cancel()
	item.done <- result
	close(item.done)
}

func (pool *WorkerPool) cancel(item *workItem) bool {
	if pool == nil || item == nil {
		return false
	}
	pool.mu.Lock()
	defer pool.mu.Unlock()
	switch item.state {
	case workQueued:
		if !pool.queue.Remove(item) {
			return false
		}
		item.state = workFinished
		item.cancel()
		pool.finishLocked(item, WorkResult{
			ProjectID: item.projectID, EndedAt: pool.clock.Now().UTC(), Cancelled: true, Err: context.Canceled,
		})
		return true
	case workRunning:
		item.state = workCancelling
		item.cancel()
		return true
	case workCancelling:
		return true
	default:
		return false
	}
}

func (pool *WorkerPool) Stats() PoolStats {
	if pool == nil {
		return PoolStats{Closed: true}
	}
	pool.mu.Lock()
	defer pool.mu.Unlock()
	return PoolStats{
		WorkerLimit: pool.config.WorkerLimit, Capacity: pool.config.WorkerLimit + pool.config.QueueCapacity,
		Reserved: pool.reserved, Queued: pool.queue.Len(), Running: pool.running, Closed: pool.closed,
	}
}

// Close cancels queued and running work. Queued callbacks receive a single
// cancelled result without invoking Work; running Work receives cancellation
// and Close waits only until the caller's context expires.
func (pool *WorkerPool) Close(ctx context.Context) error {
	if pool == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	pool.mu.Lock()
	if !pool.closed {
		pool.closed = true
		for _, item := range pool.queue.Drain() {
			item.state = workFinished
			item.cancel()
			pool.finishLocked(item, WorkResult{
				ProjectID: item.projectID, EndedAt: pool.clock.Now().UTC(), Cancelled: true, Err: context.Canceled,
			})
		}
		for item := range pool.active {
			if item.state == workRunning {
				item.state = workCancelling
				item.cancel()
			}
		}
		pool.cond.Broadcast()
	}
	pool.mu.Unlock()
	select {
	case <-pool.workersDone:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}
