package testing

import (
	"context"
	"sync"
	"time"

	"github.com/lyming99/autoplan/backend/internal/runtime/scheduler"
)

type FakeProcessLauncher struct {
	mu         sync.Mutex
	Starts     []scheduler.ProcessSpec
	StartError error
	Processes  []*FakeProcess
}

func (launcher *FakeProcessLauncher) Start(ctx context.Context, spec scheduler.ProcessSpec) (scheduler.Process, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	launcher.mu.Lock()
	defer launcher.mu.Unlock()
	if launcher.StartError != nil {
		return nil, launcher.StartError
	}
	process := NewFakeProcess()
	launcher.Starts = append(launcher.Starts, scheduler.ProcessSpec{
		Executable: spec.Executable, Args: append([]string(nil), spec.Args...), WorkDir: spec.WorkDir, Timeout: spec.Timeout,
	})
	launcher.Processes = append(launcher.Processes, process)
	return process, nil
}

type FakeProcess struct {
	mu        sync.Mutex
	done      chan struct{}
	result    scheduler.ProcessResult
	waitError error
	cancelErr error
	completed bool
	cancelled bool
}

func NewFakeProcess() *FakeProcess {
	return &FakeProcess{done: make(chan struct{})}
}

func (process *FakeProcess) Wait(ctx context.Context) (scheduler.ProcessResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-process.done:
		process.mu.Lock()
		defer process.mu.Unlock()
		return process.result, process.waitError
	case <-ctx.Done():
		return scheduler.ProcessResult{}, ctx.Err()
	}
}

func (process *FakeProcess) Cancel(ctx context.Context) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	process.mu.Lock()
	defer process.mu.Unlock()
	if process.completed {
		return nil
	}
	process.cancelled = true
	return process.cancelErr
}

func (process *FakeProcess) Complete(result scheduler.ProcessResult, waitError error) {
	process.mu.Lock()
	defer process.mu.Unlock()
	if process.completed {
		return
	}
	if result.EndedAt.IsZero() {
		result.EndedAt = time.Unix(0, 0).UTC()
	}
	result.Cancelled = result.Cancelled || process.cancelled
	process.result = result
	process.waitError = waitError
	process.completed = true
	close(process.done)
}

func (process *FakeProcess) SetCancelError(err error) {
	process.mu.Lock()
	defer process.mu.Unlock()
	process.cancelErr = err
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}
