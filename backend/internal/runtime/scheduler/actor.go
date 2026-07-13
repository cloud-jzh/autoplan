package scheduler

import (
	"context"
	"errors"
	"sync"
	"time"
)

// Command contains only orchestration callbacks. Start, Cancel and Complete
// are always invoked by the project actor; Work is the only callback allowed
// to execute on the shared worker pool.
type Command struct {
	Name     string
	Start    func(context.Context) error
	Work     Work
	Cancel   func(context.Context)
	Complete func(context.Context, WorkResult) error
}

type CommandResult struct {
	ProjectID int64
	Sequence  uint64
	StartedAt time.Time
	EndedAt   time.Time
	Started   bool
	Cancelled bool
	Err       error
}

type commandRequest struct {
	command  Command
	context  context.Context
	cancel   context.CancelFunc
	done     chan CommandResult
	sequence uint64

	once   sync.Once
	work   *WorkHandle
	active bool
	cancelCalled bool
}

func (request *commandRequest) finish(result CommandResult) {
	request.once.Do(func() {
		request.cancel()
		request.done <- result
		close(request.done)
	})
}

type actorEntryKind uint8

const (
	actorCommandEntry actorEntryKind = iota + 1
	actorCancelEntry
	actorCompleteEntry
)

type actorEntry struct {
	kind   actorEntryKind
	request *commandRequest
	result WorkResult
}

// Submission represents one accepted actor command. Calling Cancel only
// posts a cancellation request; the actor serializes its state callback with
// all start and completion callbacks for the project.
type Submission struct {
	actor  *Actor
	request *commandRequest
}

func (submission *Submission) Done() <-chan CommandResult {
	if submission == nil || submission.request == nil {
		return nil
	}
	return submission.request.done
}

func (submission *Submission) Cancel() bool {
	if submission == nil || submission.actor == nil || submission.request == nil {
		return false
	}
	return submission.actor.requestCancel(submission.request)
}

func (submission *Submission) Wait(ctx context.Context) (CommandResult, error) {
	if submission == nil || submission.request == nil {
		return CommandResult{}, ErrInvalidCommand
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case result, open := <-submission.request.done:
		if !open {
			return CommandResult{}, ErrActorClosed
		}
		return result, nil
	case <-ctx.Done():
		return CommandResult{}, ctx.Err()
	}
}

type ActorStats struct {
	ProjectID      int64
	QueuedCommands int
	ActiveCommands int
	Closed         bool
}

// Actor is one FIFO command owner for one project. Internal completions use
// the same inbox but do not consume the caller-facing command capacity, so a
// full command queue cannot strand an already accepted worker result.
type Actor struct {
	projectID int64
	pool      *WorkerPool
	clock     Clock
	capacity  int

	mu             sync.Mutex
	cond           *sync.Cond
	inbox          []actorEntry
	queuedCommands int
	active         map[*commandRequest]struct{}
	nextSequence   uint64
	closed         bool
	done           chan struct{}
}

func NewActor(projectID int64, pool *WorkerPool, clock Clock, queueCapacity int) (*Actor, error) {
	if projectID <= 0 || pool == nil || clock == nil || queueCapacity <= 0 {
		return nil, ErrInvalidConfig
	}
	actor := &Actor{
		projectID: projectID, pool: pool, clock: clock, capacity: queueCapacity,
		active: make(map[*commandRequest]struct{}), done: make(chan struct{}),
	}
	actor.cond = sync.NewCond(&actor.mu)
	go actor.run()
	return actor, nil
}

func (actor *Actor) Submit(ctx context.Context, command Command) (*Submission, error) {
	if actor == nil || command.Start == nil || command.Name == "" {
		return nil, ErrInvalidCommand
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	commandContext, cancel := context.WithCancel(ctx)
	actor.mu.Lock()
	defer actor.mu.Unlock()
	if actor.closed {
		cancel()
		return nil, ErrActorClosed
	}
	if actor.queuedCommands >= actor.capacity {
		cancel()
		return nil, ErrActorQueueFull
	}
	actor.nextSequence++
	request := &commandRequest{
		command: command, context: commandContext, cancel: cancel,
		done: make(chan CommandResult, 1), sequence: actor.nextSequence,
	}
	actor.inbox = append(actor.inbox, actorEntry{kind: actorCommandEntry, request: request})
	actor.queuedCommands++
	actor.cond.Signal()
	return &Submission{actor: actor, request: request}, nil
}

func (actor *Actor) requestCancel(request *commandRequest) bool {
	if actor == nil || request == nil {
		return false
	}
	actor.mu.Lock()
	defer actor.mu.Unlock()
	if actor.closed {
		return false
	}
	// A command still waiting in the mailbox is cancelled before the actor can
	// enter Start, so queued cancellation never starts external work.
	request.cancel()
	actor.inbox = append(actor.inbox, actorEntry{kind: actorCancelEntry, request: request})
	actor.cond.Signal()
	return true
}

func (actor *Actor) postCompletion(request *commandRequest, result WorkResult) {
	actor.mu.Lock()
	actor.inbox = append(actor.inbox, actorEntry{kind: actorCompleteEntry, request: request, result: result})
	actor.cond.Signal()
	actor.mu.Unlock()
}

func (actor *Actor) run() {
	defer close(actor.done)
	for {
		entry, open := actor.nextEntry()
		if !open {
			return
		}
		switch entry.kind {
		case actorCommandEntry:
			actor.execute(entry.request)
		case actorCancelEntry:
			actor.cancel(entry.request)
		case actorCompleteEntry:
			actor.complete(entry.request, entry.result)
		}
	}
}

func (actor *Actor) nextEntry() (actorEntry, bool) {
	actor.mu.Lock()
	defer actor.mu.Unlock()
	for len(actor.inbox) == 0 && !(actor.closed && len(actor.active) == 0) {
		actor.cond.Wait()
	}
	if len(actor.inbox) == 0 && actor.closed && len(actor.active) == 0 {
		return actorEntry{}, false
	}
	entry := actor.inbox[0]
	actor.inbox = actor.inbox[1:]
	if entry.kind == actorCommandEntry && actor.queuedCommands > 0 {
		actor.queuedCommands--
	}
	return entry, true
}

func (actor *Actor) execute(request *commandRequest) {
	if request == nil {
		return
	}
	if err := request.context.Err(); err != nil {
		actor.finish(request, actor.resultFor(request, false, err))
		return
	}
	actor.mu.Lock()
	if actor.closed {
		actor.mu.Unlock()
		actor.finish(request, actor.resultFor(request, false, ErrActorClosed))
		return
	}
	request.active = true
	actor.active[request] = struct{}{}
	actor.mu.Unlock()

	var reservation *Reservation
	if request.command.Work != nil {
		var err error
		reservation, err = actor.pool.Reserve(request.context, actor.projectID)
		if err != nil {
			actor.finish(request, actor.resultFor(request, false, err))
			return
		}
	}
	if err := request.command.Start(request.context); err != nil {
		if reservation != nil {
			reservation.Release()
		}
		actor.finish(request, actor.resultFor(request, false, err))
		return
	}
	if request.command.Work == nil {
		actor.finish(request, actor.resultFor(request, true, nil))
		return
	}
	if err := request.context.Err(); err != nil {
		reservation.Release()
		actor.finish(request, actor.resultFor(request, true, err))
		return
	}
	handle, err := reservation.Start(request.context, request.command.Work)
	if err != nil {
		actor.finish(request, actor.resultFor(request, true, err))
		return
	}
	actor.mu.Lock()
	request.work = handle
	actor.mu.Unlock()
	go func() {
		result, open := <-handle.Done()
		if !open {
			result = WorkResult{ProjectID: actor.projectID, EndedAt: actor.clock.Now().UTC(), Cancelled: true, Err: context.Canceled}
		}
		actor.postCompletion(request, result)
	}()
}

func (actor *Actor) cancel(request *commandRequest) {
	if request == nil {
		return
	}
	actor.mu.Lock()
	_, active := actor.active[request]
	handle := request.work
	actor.mu.Unlock()
	if !active {
		return
	}
	if request.command.Cancel != nil && !request.cancelCalled {
		request.cancelCalled = true
		request.command.Cancel(context.WithoutCancel(request.context))
	}
	if handle != nil {
		handle.Cancel()
	}
}

func (actor *Actor) complete(request *commandRequest, workResult WorkResult) {
	if request == nil {
		return
	}
	result := actor.resultFor(request, true, workResult.Err)
	result.Started = workResult.Started || result.Started
	result.Cancelled = workResult.Cancelled || errors.Is(workResult.Err, context.Canceled)
	if workResult.StartedAt.IsZero() {
		result.StartedAt = result.EndedAt
	} else {
		result.StartedAt = workResult.StartedAt
	}
	if !workResult.EndedAt.IsZero() {
		result.EndedAt = workResult.EndedAt
	}
	if request.command.Complete != nil {
		if err := request.command.Complete(context.WithoutCancel(request.context), workResult); err != nil {
			result.Err = err
		}
	}
	actor.finish(request, result)
}

func (actor *Actor) resultFor(request *commandRequest, started bool, err error) CommandResult {
	now := actor.clock.Now().UTC()
	return CommandResult{
		ProjectID: actor.projectID, Sequence: request.sequence, StartedAt: now, EndedAt: now,
		Started: started, Cancelled: err == context.Canceled, Err: err,
	}
}

func (actor *Actor) finish(request *commandRequest, result CommandResult) {
	if request == nil {
		return
	}
	actor.mu.Lock()
	delete(actor.active, request)
	request.active = false
	actor.cond.Broadcast()
	actor.mu.Unlock()
	request.finish(result)
}

func (actor *Actor) Stats() ActorStats {
	if actor == nil {
		return ActorStats{Closed: true}
	}
	actor.mu.Lock()
	defer actor.mu.Unlock()
	return ActorStats{
		ProjectID: actor.projectID, QueuedCommands: actor.queuedCommands,
		ActiveCommands: len(actor.active), Closed: actor.closed,
	}
}

func (actor *Actor) Close(ctx context.Context) error {
	if actor == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	actor.mu.Lock()
	if !actor.closed {
		actor.closed = true
		pending := make([]*commandRequest, 0, len(actor.inbox))
		retained := make([]actorEntry, 0, len(actor.inbox)+len(actor.active))
		for _, entry := range actor.inbox {
			if entry.kind == actorCommandEntry && entry.request != nil {
				pending = append(pending, entry.request)
			} else {
				retained = append(retained, entry)
			}
		}
		actor.inbox = retained
		actor.queuedCommands = 0
		active := make([]*commandRequest, 0, len(actor.active))
		for request := range actor.active {
			active = append(active, request)
			request.cancel()
			actor.inbox = append(actor.inbox, actorEntry{kind: actorCancelEntry, request: request})
		}
		actor.cond.Broadcast()
		actor.mu.Unlock()
		for _, request := range pending {
			request.cancel()
			actor.finish(request, actor.resultFor(request, false, context.Canceled))
		}
		for _, request := range active {
			if request.work != nil {
				request.work.Cancel()
			}
		}
	} else {
		actor.mu.Unlock()
	}
	select {
	case <-actor.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
