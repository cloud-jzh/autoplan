package testing

import (
	"context"
	"sync"

	"github.com/lyming99/autoplan/backend/internal/runtime/scheduler"
)

type FakeEventBus struct {
	mu     sync.Mutex
	Events []scheduler.Event
	Err    error
}

func (bus *FakeEventBus) Publish(ctx context.Context, event scheduler.Event) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	bus.mu.Lock()
	defer bus.mu.Unlock()
	if bus.Err != nil {
		return bus.Err
	}
	bus.Events = append(bus.Events, event)
	return nil
}

func (bus *FakeEventBus) Snapshot() []scheduler.Event {
	bus.mu.Lock()
	defer bus.mu.Unlock()
	return append([]scheduler.Event(nil), bus.Events...)
}
