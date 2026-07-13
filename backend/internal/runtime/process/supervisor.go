package process

import (
	"context"
	"sync"

	"github.com/lyming99/autoplan/backend/internal/config"
)

// Supervisor owns finite global and per-project execution leases. It never
// queues callers: an over-limit request fails closed, preventing an unbounded
// backlog from surviving a slow child process or daemon shutdown.
type Supervisor struct {
	mu            sync.Mutex
	maxGlobal     int
	maxPerProject int
	global        int
	projects      map[int64]int
	closed        bool
}

func NewSupervisor(runtime config.ProcessRuntime) (*Supervisor, error) {
	if !runtime.Valid() {
		return nil, ErrRunnerUnavailable
	}
	return &Supervisor{
		maxGlobal: runtime.MaxGlobalConcurrent, maxPerProject: runtime.MaxProjectConcurrent,
		projects: make(map[int64]int),
	}, nil
}

// Acquire reserves a process slot until the returned release function is
// called. Release is idempotent so every error path can safely defer it.
func (supervisor *Supervisor) Acquire(ctx context.Context, projectID int64) (func(), error) {
	if supervisor == nil || projectID <= 0 {
		return nil, ErrRunnerUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if contextError(ctx) != nil {
		return nil, ErrCancelled
	}
	supervisor.mu.Lock()
	defer supervisor.mu.Unlock()
	if supervisor.closed {
		return nil, ErrRunnerUnavailable
	}
	if supervisor.global >= supervisor.maxGlobal {
		return nil, ErrGlobalConcurrencyLimit
	}
	if supervisor.projects[projectID] >= supervisor.maxPerProject {
		return nil, ErrProjectConcurrencyLimit
	}
	supervisor.global++
	supervisor.projects[projectID]++
	var once sync.Once
	return func() {
		once.Do(func() {
			supervisor.mu.Lock()
			defer supervisor.mu.Unlock()
			if supervisor.global > 0 {
				supervisor.global--
			}
			if supervisor.projects[projectID] <= 1 {
				delete(supervisor.projects, projectID)
			} else {
				supervisor.projects[projectID]--
			}
		})
	}, nil
}

// Shutdown blocks new leases. Existing Runner calls observe their shutdown
// channel and terminate through the platform process-tree primitive.
func (supervisor *Supervisor) Shutdown() {
	if supervisor == nil {
		return
	}
	supervisor.mu.Lock()
	supervisor.closed = true
	supervisor.mu.Unlock()
}

func (supervisor *Supervisor) Active() (global int, projects map[int64]int) {
	if supervisor == nil {
		return 0, map[int64]int{}
	}
	supervisor.mu.Lock()
	defer supervisor.mu.Unlock()
	projects = make(map[int64]int, len(supervisor.projects))
	for projectID, count := range supervisor.projects {
		projects[projectID] = count
	}
	return supervisor.global, projects
}
