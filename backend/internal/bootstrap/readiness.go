package bootstrap

import (
	"context"
	"errors"
	"regexp"
	"sync"
)

type DependencyState string

const (
	DependencyPending DependencyState = "pending"
	DependencyReady   DependencyState = "ready"
	DependencyFailed  DependencyState = "failed"
)

var (
	ErrReadinessDependency = errors.New("readiness dependency is invalid")
	ErrNotReady            = errors.New("application is not ready")
	ErrReadinessShutting   = errors.New("readiness is shutting down")
	readinessToken         = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
)

type DependencyStatus struct {
	Name   string          `json:"name"`
	State  DependencyState `json:"state"`
	Reason string          `json:"reason,omitempty"`
}

type ReadinessSnapshot struct {
	Ready        bool               `json:"ready"`
	State        string             `json:"state"`
	Dependencies []DependencyStatus `json:"dependencies"`
}

var databaseReadinessDependencies = []string{
	"configuration",
	"prerequisites",
	ReadinessDatabaseOwner,
	ReadinessMigrations,
	ReadinessDatabase,
	"application",
	"listener",
}

// NewDatabaseReadiness returns the fixed startup gate order used by a process
// that owns SQLite. Callers cannot omit the owner, migration, or audit-backed
// database gate and accidentally expose /readyz early.
func NewDatabaseReadiness() (*Readiness, error) {
	return NewReadiness(databaseReadinessDependencies...)
}

func DatabaseReadinessDependencies() []string {
	return append([]string(nil), databaseReadinessDependencies...)
}

// Readiness is a concurrency-safe aggregate. Shutdown is terminal; dependency
// failures may recover to ready before shutdown without bypassing other gates.
type Readiness struct {
	mu           sync.RWMutex
	order        []string
	dependencies map[string]DependencyStatus
	shuttingDown bool
}

func NewReadiness(names ...string) (*Readiness, error) {
	if len(names) == 0 {
		return nil, ErrReadinessDependency
	}
	value := &Readiness{
		order:        make([]string, 0, len(names)),
		dependencies: make(map[string]DependencyStatus, len(names)),
	}
	for _, name := range names {
		if !readinessToken.MatchString(name) {
			return nil, ErrReadinessDependency
		}
		if _, duplicate := value.dependencies[name]; duplicate {
			return nil, ErrReadinessDependency
		}
		value.order = append(value.order, name)
		value.dependencies[name] = DependencyStatus{Name: name, State: DependencyPending, Reason: "initializing"}
	}
	return value, nil
}

func (readiness *Readiness) MarkReady(name string) error {
	return readiness.update(name, DependencyReady, "")
}

func (readiness *Readiness) MarkPending(name, reason string) error {
	if !readinessToken.MatchString(reason) {
		return ErrReadinessDependency
	}
	return readiness.update(name, DependencyPending, reason)
}

func (readiness *Readiness) MarkFailed(name, reason string) error {
	if !readinessToken.MatchString(reason) {
		return ErrReadinessDependency
	}
	return readiness.update(name, DependencyFailed, reason)
}

func (readiness *Readiness) update(name string, state DependencyState, reason string) error {
	readiness.mu.Lock()
	defer readiness.mu.Unlock()
	if readiness.shuttingDown {
		return ErrReadinessShutting
	}
	status, exists := readiness.dependencies[name]
	if !exists {
		return ErrReadinessDependency
	}
	status.State = state
	status.Reason = reason
	readiness.dependencies[name] = status
	return nil
}

// BeginShutdown is idempotent and permanently forces aggregate readiness off.
func (readiness *Readiness) BeginShutdown() {
	readiness.mu.Lock()
	readiness.shuttingDown = true
	readiness.mu.Unlock()
}

func (readiness *Readiness) Ready() bool {
	readiness.mu.RLock()
	defer readiness.mu.RUnlock()
	return readiness.readyLocked(nil)
}

func (readiness *Readiness) ShuttingDown() bool {
	readiness.mu.RLock()
	defer readiness.mu.RUnlock()
	return readiness.shuttingDown
}

// State returns the aggregate state without exposing dependency names or
// failure reasons to a transport. Daemon readiness probes need this bounded
// status to distinguish a healthy process from one that is draining.
func (readiness *Readiness) State() string {
	if readiness == nil {
		return "failed"
	}
	return readiness.Snapshot().State
}

func (readiness *Readiness) Snapshot() ReadinessSnapshot {
	readiness.mu.RLock()
	defer readiness.mu.RUnlock()
	snapshot := ReadinessSnapshot{
		Ready:        readiness.readyLocked(nil),
		State:        readiness.aggregateStateLocked(),
		Dependencies: make([]DependencyStatus, 0, len(readiness.order)),
	}
	for _, name := range readiness.order {
		snapshot.Dependencies = append(snapshot.Dependencies, readiness.dependencies[name])
	}
	return snapshot
}

func (readiness *Readiness) aggregateStateLocked() string {
	if readiness.shuttingDown {
		return "shutting_down"
	}
	hasPending := false
	for _, status := range readiness.dependencies {
		if status.State == DependencyFailed {
			return "failed"
		}
		if status.State == DependencyPending {
			hasPending = true
		}
	}
	if hasPending {
		return "pending"
	}
	return "ready"
}

// Gate returns an application readiness port that ignores only explicitly
// downstream dependencies, preventing circular initialization.
func (readiness *Readiness) Gate(excluded ...string) (*ReadinessGate, error) {
	ignored := make(map[string]struct{}, len(excluded))
	readiness.mu.RLock()
	defer readiness.mu.RUnlock()
	for _, name := range excluded {
		if _, exists := readiness.dependencies[name]; !exists {
			return nil, ErrReadinessDependency
		}
		ignored[name] = struct{}{}
	}
	return &ReadinessGate{readiness: readiness, ignored: ignored}, nil
}

type ReadinessGate struct {
	readiness *Readiness
	ignored   map[string]struct{}
}

func (gate *ReadinessGate) Check(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	gate.readiness.mu.RLock()
	defer gate.readiness.mu.RUnlock()
	if gate.readiness.shuttingDown {
		return ErrReadinessShutting
	}
	if !gate.readiness.readyLocked(gate.ignored) {
		return ErrNotReady
	}
	return nil
}

func (readiness *Readiness) readyLocked(ignored map[string]struct{}) bool {
	if readiness.shuttingDown {
		return false
	}
	for name, status := range readiness.dependencies {
		if _, skip := ignored[name]; skip {
			continue
		}
		if status.State != DependencyReady {
			return false
		}
	}
	return true
}
