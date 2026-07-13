// Package testing contains deterministic runtime fakes. It intentionally
// contains no real timer, process, filesystem, network or EventBus access.
package testing

import (
	"sort"
	"sync"
	"time"

	"github.com/lyming99/autoplan/backend/internal/runtime/scheduler"
)

type FakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*FakeTimer
}

func NewFakeClock(now time.Time) *FakeClock {
	return &FakeClock{now: now.UTC()}
}

func (clock *FakeClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return clock.now
}

func (clock *FakeClock) NewTimer(wait time.Duration) scheduler.Timer {
	if wait < 0 {
		wait = 0
	}
	clock.mu.Lock()
	defer clock.mu.Unlock()
	timer := &FakeTimer{clock: clock, due: clock.now.Add(wait), channel: make(chan time.Time, 1)}
	clock.timers = append(clock.timers, timer)
	return timer
}

// Advance delivers every timer due at or before the new instant in due-time
// order. Delivery is buffered once, matching time.Timer's one-shot behavior.
func (clock *FakeClock) Advance(wait time.Duration) {
	clock.mu.Lock()
	clock.now = clock.now.Add(wait)
	timers := append([]*FakeTimer(nil), clock.timers...)
	now := clock.now
	clock.mu.Unlock()
	sort.SliceStable(timers, func(left, right int) bool { return timers[left].due.Before(timers[right].due) })
	for _, timer := range timers {
		timer.fire(now)
	}
}

type FakeTimer struct {
	clock   *FakeClock
	due     time.Time
	channel chan time.Time

	mu      sync.Mutex
	stopped bool
	fired   bool
}

func (timer *FakeTimer) C() <-chan time.Time { return timer.channel }

func (timer *FakeTimer) Stop() bool {
	if timer == nil {
		return false
	}
	timer.mu.Lock()
	defer timer.mu.Unlock()
	if timer.stopped || timer.fired {
		return false
	}
	timer.stopped = true
	return true
}

func (timer *FakeTimer) fire(now time.Time) {
	if timer == nil {
		return
	}
	timer.mu.Lock()
	defer timer.mu.Unlock()
	if timer.stopped || timer.fired || timer.due.After(now) {
		return
	}
	timer.fired = true
	timer.channel <- timer.due
}
