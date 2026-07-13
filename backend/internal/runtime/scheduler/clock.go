// Package scheduler provides the bounded, project-serialized execution
// substrate used by future Loop, plan, task, acceptance, and Agent CLI use
// cases. It owns scheduling mechanics only; business state remains in
// application services.
package scheduler

import (
	"context"
	"errors"
	"time"
)

var (
	ErrInvalidConfig    = errors.New("scheduler configuration is invalid")
	ErrManagerClosed    = errors.New("scheduler manager is closed")
	ErrActorClosed      = errors.New("project actor is closed")
	ErrActorQueueFull   = errors.New("project actor queue is full")
	ErrWorkerQueueFull  = errors.New("worker pool queue is full")
	ErrWorkerPoolClosed = errors.New("worker pool is closed")
	ErrInvalidCommand   = errors.New("scheduler command is invalid")
	ErrWorkerPanic      = errors.New("worker panicked")
	ErrProcessUnavailable = errors.New("process launcher is unavailable")
)

// Clock is injectable so retry, timer and timeout behavior can be advanced
// deterministically without sleep. Every timer returned by NewTimer belongs
// to its caller and must be stopped when no longer needed.
type Clock interface {
	Now() time.Time
	NewTimer(time.Duration) Timer
}

// Timer is intentionally smaller than time.Timer. Fake implementations only
// need delivery and cancellation semantics, not access to a concrete clock.
type Timer interface {
	C() <-chan time.Time
	Stop() bool
}

type systemClock struct{}

// NewSystemClock constructs the production clock selected by bootstrap.
func NewSystemClock() Clock { return systemClock{} }

func (systemClock) Now() time.Time { return time.Now().UTC() }

func (systemClock) NewTimer(wait time.Duration) Timer {
	return systemTimer{timer: time.NewTimer(wait)}
}

type systemTimer struct{ timer *time.Timer }

func (timer systemTimer) C() <-chan time.Time { return timer.timer.C }
func (timer systemTimer) Stop() bool           { return timer.timer.Stop() }

// ProcessSpec keeps the P002 process dependency typed but intentionally does
// not launch anything itself. P003 supplies the policy-enforcing runner.
type ProcessSpec struct {
	Executable string
	Args       []string
	WorkDir    string
	Timeout    time.Duration
}

type ProcessResult struct {
	ExitCode  int
	StartedAt time.Time
	EndedAt   time.Time
	Cancelled bool
}

// ProcessLauncher and Process are injected by bootstrap or fake tests. A
// scheduler consumer may not use os/exec directly as a fallback.
type ProcessLauncher interface {
	Start(context.Context, ProcessSpec) (Process, error)
}

type Process interface {
	Wait(context.Context) (ProcessResult, error)
	Cancel(context.Context) error
}

// Event is the bounded scheduling notification surface. It excludes raw
// command arguments, work directories, output, environment and prompt data.
type Event struct {
	ProjectID int64
	Kind      string
	At        time.Time
}

type EventBus interface {
	Publish(context.Context, Event) error
}

type unavailableProcessLauncher struct{}

func (unavailableProcessLauncher) Start(context.Context, ProcessSpec) (Process, error) {
	return nil, ErrProcessUnavailable
}

type discardEventBus struct{}

func (discardEventBus) Publish(context.Context, Event) error { return nil }
