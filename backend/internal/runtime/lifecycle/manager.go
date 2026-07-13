// Package lifecycle coordinates bounded, idempotent, reverse-order shutdown.
package lifecycle

import (
	"context"
	"errors"
	"sync"
	"time"
)

var (
	ErrInvalidManager     = errors.New("lifecycle manager configuration is invalid")
	ErrShutdownStarted    = errors.New("shutdown has started")
	ErrShutdownIncomplete = errors.New("shutdown did not complete cleanly")
)

type ShutdownMarker interface {
	BeginShutdown()
}

type Resource interface {
	Close(context.Context) error
}

type CloserFunc func(context.Context) error

func (closer CloserFunc) Close(ctx context.Context) error { return closer(ctx) }

type Manager struct {
	mu        sync.Mutex
	marker    ShutdownMarker
	timeout   time.Duration
	resources []Resource
	started   bool
	once      sync.Once
	done      chan struct{}
	result    error
}

func New(marker ShutdownMarker, timeout time.Duration) (*Manager, error) {
	if marker == nil || timeout <= 0 {
		return nil, ErrInvalidManager
	}
	return &Manager{marker: marker, timeout: timeout, done: make(chan struct{})}, nil
}

// Add records acquisition order. Shutdown releases the resulting list in the
// exact reverse order and refuses late registration once closing begins.
func (manager *Manager) Add(resource Resource) error {
	if resource == nil {
		return ErrInvalidManager
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.started {
		return ErrShutdownStarted
	}
	manager.resources = append(manager.resources, resource)
	return nil
}

// Shutdown is idempotent. Each resource receives the same bounded context;
// non-cooperative resources cannot prevent the manager from returning.
func (manager *Manager) Shutdown() error {
	manager.once.Do(manager.execute)
	<-manager.done
	return manager.result
}

func (manager *Manager) Done() <-chan struct{} { return manager.done }

func (manager *Manager) execute() {
	defer close(manager.done)
	manager.mu.Lock()
	manager.started = true
	resources := append([]Resource(nil), manager.resources...)
	manager.mu.Unlock()

	failed := beginShutdown(manager.marker) != nil
	ctx, cancel := context.WithTimeout(context.Background(), manager.timeout)
	defer cancel()
	for index := len(resources) - 1; index >= 0; index-- {
		if err := closeBounded(ctx, resources[index]); err != nil {
			failed = true
		}
	}
	if failed || ctx.Err() != nil {
		manager.result = ErrShutdownIncomplete
	}
}

func beginShutdown(marker ShutdownMarker) (err error) {
	defer func() {
		if recover() != nil {
			err = ErrShutdownIncomplete
		}
	}()
	marker.BeginShutdown()
	return nil
}

func closeBounded(ctx context.Context, resource Resource) error {
	result := make(chan error, 1)
	go func() {
		defer func() {
			if recover() != nil {
				result <- ErrShutdownIncomplete
			}
		}()
		result <- resource.Close(ctx)
	}()
	select {
	case err := <-result:
		return err
	case <-ctx.Done():
		return ErrShutdownIncomplete
	}
}
