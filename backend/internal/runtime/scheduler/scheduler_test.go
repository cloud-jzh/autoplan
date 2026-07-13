package scheduler_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lyming99/autoplan/backend/internal/runtime/scheduler"
	runtimetesting "github.com/lyming99/autoplan/backend/internal/runtime/testing"
)

func newManager(t *testing.T, config scheduler.Config) *scheduler.Manager {
	t.Helper()
	clock := runtimetesting.NewFakeClock(time.Unix(100, 0))
	manager, err := scheduler.NewManager(scheduler.Dependencies{Config: config, Clock: clock})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = manager.Close(ctx)
	})
	return manager
}

func TestActorSerializesLifecycleCallbacks(t *testing.T) {
	manager := newManager(t, scheduler.Config{WorkerLimit: 1, QueueCapacity: 1, ActorQueueCapacity: 4})
	var current int32
	var maximum int32
	command := func() scheduler.Command {
		return scheduler.Command{
			Name: "state-transition",
			Start: func(context.Context) error {
				value := atomic.AddInt32(&current, 1)
				for {
					seen := atomic.LoadInt32(&maximum)
					if value <= seen || atomic.CompareAndSwapInt32(&maximum, seen, value) {
						break
					}
				}
				atomic.AddInt32(&current, -1)
				return nil
			},
		}
	}
	first, err := manager.Submit(context.Background(), 7, command())
	if err != nil {
		t.Fatalf("first Submit() error = %v", err)
	}
	second, err := manager.Submit(context.Background(), 7, command())
	if err != nil {
		t.Fatalf("second Submit() error = %v", err)
	}
	deadline, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := first.Wait(deadline); err != nil {
		t.Fatalf("first Wait() error = %v", err)
	}
	if _, err := second.Wait(deadline); err != nil {
		t.Fatalf("second Wait() error = %v", err)
	}
	if maximum != 1 {
		t.Fatalf("maximum concurrent actor callbacks = %d, want 1", maximum)
	}
}

func TestPoolAdmissionPrecedesStartTransition(t *testing.T) {
	manager := newManager(t, scheduler.Config{WorkerLimit: 1, QueueCapacity: 0, ActorQueueCapacity: 4})
	started := make(chan struct{})
	release := make(chan struct{})
	first, err := manager.Submit(context.Background(), 7, scheduler.Command{
		Name: "first",
		Start: func(context.Context) error { return nil },
		Work: func(context.Context) error {
			close(started)
			<-release
			return nil
		},
	})
	if err != nil {
		t.Fatalf("first Submit() error = %v", err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first work did not start")
	}
	var secondStarted atomic.Bool
	second, err := manager.Submit(context.Background(), 7, scheduler.Command{
		Name: "second",
		Start: func(context.Context) error {
			secondStarted.Store(true)
			return nil
		},
		Work: func(context.Context) error { return nil },
	})
	if err != nil {
		t.Fatalf("second Submit() error = %v", err)
	}
	deadline, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	secondResult, err := second.Wait(deadline)
	if err != nil {
		t.Fatalf("second Wait() error = %v", err)
	}
	if !errors.Is(secondResult.Err, scheduler.ErrWorkerQueueFull) {
		t.Fatalf("second error = %v, want ErrWorkerQueueFull", secondResult.Err)
	}
	if secondStarted.Load() {
		t.Fatal("second Start ran despite unavailable worker capacity")
	}
	close(release)
	if _, err := first.Wait(deadline); err != nil {
		t.Fatalf("first Wait() error = %v", err)
	}
}

func TestQueuedCancellationNeverStartsWork(t *testing.T) {
	manager := newManager(t, scheduler.Config{WorkerLimit: 1, QueueCapacity: 1, ActorQueueCapacity: 4})
	started := make(chan struct{})
	release := make(chan struct{})
	first, err := manager.Submit(context.Background(), 7, scheduler.Command{
		Name: "first",
		Start: func(context.Context) error { return nil },
		Work: func(context.Context) error {
			close(started)
			<-release
			return nil
		},
	})
	if err != nil {
		t.Fatalf("first Submit() error = %v", err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first work did not start")
	}
	var secondRan atomic.Bool
	second, err := manager.Submit(context.Background(), 9, scheduler.Command{
		Name: "queued",
		Start: func(context.Context) error { return nil },
		Work: func(context.Context) error {
			secondRan.Store(true)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("second Submit() error = %v", err)
	}
	if !second.Cancel() {
		t.Fatal("second Cancel() = false, want true")
	}
	deadline, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	secondResult, err := second.Wait(deadline)
	if err != nil {
		t.Fatalf("second Wait() error = %v", err)
	}
	if !errors.Is(secondResult.Err, context.Canceled) || !secondResult.Cancelled {
		t.Fatalf("second result = %#v, want cancelled", secondResult)
	}
	if secondRan.Load() {
		t.Fatal("cancelled queued work ran")
	}
	close(release)
	if _, err := first.Wait(deadline); err != nil {
		t.Fatalf("first Wait() error = %v", err)
	}
}
