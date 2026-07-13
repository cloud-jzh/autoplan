package runtime_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lyming99/autoplan/backend/internal/runtime/scheduler"
	runtimetesting "github.com/lyming99/autoplan/backend/internal/runtime/testing"
)

func TestP11ActorSerializesProjectAndBoundsCrossProjectWorkers(t *testing.T) {
	clock := runtimetesting.NewFakeClock(time.Unix(100, 0))
	manager, err := scheduler.NewManager(scheduler.Dependencies{
		Config: scheduler.Config{WorkerLimit: 2, QueueCapacity: 0, ActorQueueCapacity: 8}, Clock: clock,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = manager.Close(ctx)
	})

	var sameProjectCurrent atomic.Int32
	var sameProjectMaximum atomic.Int32
	firstEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	first, err := manager.Submit(context.Background(), 7, scheduler.Command{
		Name: "same-project-first",
		Start: func(context.Context) error {
			value := sameProjectCurrent.Add(1)
			for {
				maximum := sameProjectMaximum.Load()
				if value <= maximum || sameProjectMaximum.CompareAndSwap(maximum, value) {
					break
				}
			}
			close(firstEntered)
			<-releaseFirst
			sameProjectCurrent.Add(-1)
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-firstEntered:
	case <-time.After(time.Second):
		t.Fatal("first actor command did not start")
	}
	secondStarted := atomic.Bool{}
	second, err := manager.Submit(context.Background(), 7, scheduler.Command{
		Name:  "same-project-second",
		Start: func(context.Context) error { secondStarted.Store(true); return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if secondStarted.Load() {
		t.Fatal("same project actor accepted a concurrent state transition")
	}
	close(releaseFirst)
	deadline, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := first.Wait(deadline); err != nil {
		t.Fatal(err)
	}
	if _, err := second.Wait(deadline); err != nil || !secondStarted.Load() || sameProjectMaximum.Load() != 1 {
		t.Fatalf("same project result err=%v started=%t max=%d", err, secondStarted.Load(), sameProjectMaximum.Load())
	}

	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	var running atomic.Int32
	var maximum atomic.Int32
	command := func(name string) scheduler.Command {
		return scheduler.Command{Name: name, Start: func(context.Context) error { return nil }, Work: func(context.Context) error {
			value := running.Add(1)
			for {
				seen := maximum.Load()
				if value <= seen || maximum.CompareAndSwap(seen, value) {
					break
				}
			}
			entered <- struct{}{}
			<-release
			running.Add(-1)
			return nil
		}}
	}
	left, err := manager.Submit(context.Background(), 11, command("project-11"))
	if err != nil {
		t.Fatal(err)
	}
	right, err := manager.Submit(context.Background(), 13, command("project-13"))
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 2; index++ {
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("cross project work did not reach global pool")
		}
	}
	blocked, err := manager.Submit(context.Background(), 17, command("project-17"))
	if err != nil {
		t.Fatal(err)
	}
	result, err := blocked.Wait(deadline)
	if err != nil || !errors.Is(result.Err, scheduler.ErrWorkerQueueFull) {
		t.Fatalf("backpressure result=%#v err=%v", result, err)
	}
	if maximum.Load() != 2 {
		t.Fatalf("global worker maximum=%d want=2", maximum.Load())
	}
	close(release)
	if _, err := left.Wait(deadline); err != nil {
		t.Fatal(err)
	}
	if _, err := right.Wait(deadline); err != nil {
		t.Fatal(err)
	}
}

func TestP11QueuedCancellationReleasesAdmissionWithoutStartingWork(t *testing.T) {
	clock := runtimetesting.NewFakeClock(time.Unix(100, 0))
	manager, err := scheduler.NewManager(scheduler.Dependencies{Config: scheduler.Config{WorkerLimit: 1, QueueCapacity: 1, ActorQueueCapacity: 4}, Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = manager.Close(ctx)
	})
	started := make(chan struct{})
	release := make(chan struct{})
	active, err := manager.Submit(context.Background(), 7, scheduler.Command{Name: "active", Start: func(context.Context) error { return nil }, Work: func(context.Context) error { close(started); <-release; return nil }})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("active work did not start")
	}
	var ran atomic.Bool
	queued, err := manager.Submit(context.Background(), 9, scheduler.Command{Name: "queued", Start: func(context.Context) error { return nil }, Work: func(context.Context) error { ran.Store(true); return nil }})
	if err != nil {
		t.Fatal(err)
	}
	if !queued.Cancel() {
		t.Fatal("queued cancellation was rejected")
	}
	deadline, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result, err := queued.Wait(deadline)
	if err != nil || !result.Cancelled || !errors.Is(result.Err, context.Canceled) || ran.Load() {
		t.Fatalf("queued cancellation result=%#v err=%v ran=%t", result, err, ran.Load())
	}
	close(release)
	if _, err := active.Wait(deadline); err != nil {
		t.Fatal(err)
	}
}

func TestP11WorkerPoolRemainsFairAndReleasesTokensAfterPanic(t *testing.T) {
	clock := runtimetesting.NewFakeClock(time.Unix(100, 0))
	pool, err := scheduler.NewWorkerPool(clock, scheduler.WorkPoolConfig{WorkerLimit: 1, QueueCapacity: 3})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = pool.Close(ctx)
	})
	started := make(chan struct{})
	release := make(chan struct{})
	blocker := startReservedWork(t, pool, 1, func(context.Context) error {
		close(started)
		<-release
		return nil
	})
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("fairness blocker did not start")
	}
	order := make(chan string, 3)
	first := startReservedWork(t, pool, 7, func(context.Context) error { order <- "project-7-first"; return nil })
	second := startReservedWork(t, pool, 7, func(context.Context) error { order <- "project-7-second"; return nil })
	third := startReservedWork(t, pool, 9, func(context.Context) error { order <- "project-9-first"; return nil })
	close(release)
	_ = waitWork(t, blocker)
	_ = waitWork(t, first)
	_ = waitWork(t, second)
	_ = waitWork(t, third)
	got := []string{<-order, <-order, <-order}
	want := []string{"project-7-first", "project-9-first", "project-7-second"}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("fair queue order=%v want=%v", got, want)
		}
	}

	panicPool, err := scheduler.NewWorkerPool(clock, scheduler.WorkPoolConfig{WorkerLimit: 1, QueueCapacity: 0})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = panicPool.Close(ctx)
	})
	panicked := startReservedWork(t, panicPool, 17, func(context.Context) error { panic("p11 fake failure") })
	if result := waitWork(t, panicked); !errors.Is(result.Err, scheduler.ErrWorkerPanic) {
		t.Fatalf("panic result=%#v", result)
	}
	if result := waitWork(t, startReservedWork(t, panicPool, 19, func(context.Context) error { return nil })); result.Err != nil {
		t.Fatalf("worker token leaked after panic: %#v", result)
	}
}

func startReservedWork(t *testing.T, pool *scheduler.WorkerPool, projectID int64, work scheduler.Work) *scheduler.WorkHandle {
	t.Helper()
	reservation, err := pool.Reserve(context.Background(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := reservation.Start(context.Background(), work)
	if err != nil {
		t.Fatal(err)
	}
	return handle
}

func waitWork(t *testing.T, handle *scheduler.WorkHandle) scheduler.WorkResult {
	t.Helper()
	select {
	case result, open := <-handle.Done():
		if !open {
			t.Fatal("work handle closed without a result")
		}
		return result
	case <-time.After(time.Second):
		t.Fatal("work handle did not finish")
		return scheduler.WorkResult{}
	}
}
