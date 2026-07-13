package terminal

import (
	"context"
	"sync"
)

// Supervisor provides finite global and per-project session ownership. It
// never queues new PTY requests: overload and daemon shutdown fail closed.
type Supervisor struct {
	mu            sync.Mutex
	maxGlobal     int
	maxPerProject int
	global        int
	projects      map[int64]int
	leases        map[*Lease]*Session
	closed        bool
}

type Lease struct {
	supervisor *Supervisor
	projectID  int64
	once       sync.Once
}

func NewSupervisor(limits Limits) (*Supervisor, error) {
	if limits.MaxSessionsGlobal <= 0 || limits.MaxSessionsPerProject <= 0 || limits.MaxSessionsPerProject > limits.MaxSessionsGlobal {
		return nil, ErrConfiguration
	}
	return &Supervisor{
		maxGlobal: limits.MaxSessionsGlobal, maxPerProject: limits.MaxSessionsPerProject,
		projects: make(map[int64]int), leases: make(map[*Lease]*Session),
	}, nil
}

func (supervisor *Supervisor) Acquire(ctx context.Context, projectID int64) (*Lease, error) {
	if supervisor == nil || projectID <= 0 {
		return nil, ErrConfiguration
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if ctx.Err() != nil {
		return nil, ErrSessionClosed
	}
	supervisor.mu.Lock()
	defer supervisor.mu.Unlock()
	if supervisor.closed {
		return nil, ErrSessionClosed
	}
	if supervisor.global >= supervisor.maxGlobal || supervisor.projects[projectID] >= supervisor.maxPerProject {
		return nil, ErrSessionLimit
	}
	lease := &Lease{supervisor: supervisor, projectID: projectID}
	supervisor.global++
	supervisor.projects[projectID]++
	return lease, nil
}

func (lease *Lease) Bind(session *Session) {
	if lease == nil || lease.supervisor == nil || session == nil {
		return
	}
	var stop bool
	lease.supervisor.mu.Lock()
	if lease.supervisor.closed {
		stop = true
	} else {
		lease.supervisor.leases[lease] = session
	}
	lease.supervisor.mu.Unlock()
	if stop {
		_ = session.Close()
	}
}

func (lease *Lease) Release() {
	if lease == nil || lease.supervisor == nil {
		return
	}
	lease.once.Do(func() {
		supervisor := lease.supervisor
		supervisor.mu.Lock()
		delete(supervisor.leases, lease)
		if supervisor.global > 0 {
			supervisor.global--
		}
		if supervisor.projects[lease.projectID] <= 1 {
			delete(supervisor.projects, lease.projectID)
		} else {
			supervisor.projects[lease.projectID]--
		}
		supervisor.mu.Unlock()
	})
}

func (supervisor *Supervisor) CloseProject(projectID int64) int {
	if supervisor == nil || projectID <= 0 {
		return 0
	}
	supervisor.mu.Lock()
	sessions := make([]*Session, 0)
	for lease, session := range supervisor.leases {
		if lease.projectID == projectID {
			sessions = append(sessions, session)
		}
	}
	supervisor.mu.Unlock()
	for _, session := range sessions {
		_ = session.Close()
	}
	return len(sessions)
}

func (supervisor *Supervisor) Shutdown() {
	if supervisor == nil {
		return
	}
	supervisor.mu.Lock()
	if supervisor.closed {
		supervisor.mu.Unlock()
		return
	}
	supervisor.closed = true
	sessions := make([]*Session, 0, len(supervisor.leases))
	for _, session := range supervisor.leases {
		sessions = append(sessions, session)
	}
	supervisor.mu.Unlock()
	for _, session := range sessions {
		_ = session.Close()
	}
}

func (supervisor *Supervisor) Active() (int, map[int64]int) {
	if supervisor == nil {
		return 0, map[int64]int{}
	}
	supervisor.mu.Lock()
	defer supervisor.mu.Unlock()
	projects := make(map[int64]int, len(supervisor.projects))
	for projectID, count := range supervisor.projects {
		projects[projectID] = count
	}
	return supervisor.global, projects
}
