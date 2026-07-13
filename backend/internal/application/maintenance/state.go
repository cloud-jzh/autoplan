// Package maintenance owns the fail-closed cutover state machine.  It exposes
// only stable operation identifiers, stages, and codes; filesystem paths,
// database details, and underlying errors never leave this package.
package maintenance

import (
	"errors"
	"strings"
	"sync"
)

var (
	ErrOperationInProgress = errors.New("maintenance_operation_in_progress")
	ErrOperationFinal      = errors.New("maintenance_operation_final")
	ErrStateTransition     = errors.New("maintenance_state_transition_invalid")
)

type Mode string

const (
	ModeIdle        Mode = "idle"
	ModeMaintenance Mode = "maintenance"
	ModeReady       Mode = "ready"
)

type Stage string

const (
	StageIdle        Stage = "idle"
	StageFreeze      Stage = "freeze"
	StageDrain       Stage = "drain"
	StageNodeRelease Stage = "node_release"
	StagePreflight   Stage = "preflight"
	StageBackup      Stage = "backup"
	StageAuditBefore Stage = "audit_before"
	StageMigrate     Stage = "migrate"
	StageAuditAfter  Stage = "audit_after"
	StageOwnerLock   Stage = "owner_lock"
	StageSmoke       Stage = "smoke"
	StageReopen      Stage = "reopen"
	StageComplete    Stage = "complete"
	StageFailed      Stage = "failed"
)

// Status is safe to return through CLI and HTTP progress endpoints.
type Status struct {
	OperationID      string `json:"operation_id,omitempty"`
	Mode             Mode   `json:"mode"`
	Stage            Stage  `json:"stage"`
	Code             string `json:"code,omitempty"`
	MutationsBlocked bool   `json:"mutations_blocked"`
}

type State struct {
	mu     sync.RWMutex
	status Status
}

func NewState() *State {
	return &State{status: Status{Mode: ModeIdle, Stage: StageIdle, MutationsBlocked: false}}
}

func (state *State) Snapshot() Status {
	if state == nil {
		return Status{Mode: ModeMaintenance, Stage: StageFailed, Code: "state_unavailable", MutationsBlocked: true}
	}
	state.mu.RLock()
	defer state.mu.RUnlock()
	return state.status
}

func (state *State) Begin(operationID string) error {
	if state == nil || !safeStateLabel(operationID) {
		return ErrStateTransition
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.status.Mode == ModeMaintenance {
		return ErrOperationInProgress
	}
	if state.status.Mode == ModeReady {
		return ErrOperationFinal
	}
	state.status = Status{OperationID: operationID, Mode: ModeMaintenance, Stage: StageFreeze, Code: "maintenance_active", MutationsBlocked: true}
	return nil
}

func (state *State) Advance(stage Stage) error {
	if state == nil {
		return ErrStateTransition
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.status.Mode != ModeMaintenance || !nextStage(state.status.Stage, stage) {
		return ErrStateTransition
	}
	state.status.Stage = stage
	state.status.Code = string(stage) + "_active"
	return nil
}

func (state *State) Fail(code string) {
	if state == nil {
		return
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	state.status.Mode = ModeMaintenance
	state.status.Stage = StageFailed
	state.status.MutationsBlocked = true
	if safeStateLabel(code) {
		state.status.Code = code
	} else {
		state.status.Code = "maintenance_failed"
	}
}

func (state *State) Complete() error {
	if state == nil {
		return ErrStateTransition
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.status.Mode != ModeMaintenance || state.status.Stage != StageReopen {
		return ErrStateTransition
	}
	state.status.Mode = ModeReady
	state.status.Stage = StageComplete
	state.status.Code = "cutover_complete"
	state.status.MutationsBlocked = false
	return nil
}

func nextStage(current, next Stage) bool {
	return (current == StageFreeze && next == StageDrain) ||
		(current == StageDrain && next == StageNodeRelease) ||
		(current == StageNodeRelease && next == StagePreflight) ||
		(current == StagePreflight && next == StageOwnerLock) ||
		(current == StageOwnerLock && next == StageBackup) ||
		(current == StageBackup && next == StageAuditBefore) ||
		(current == StageAuditBefore && next == StageMigrate) ||
		(current == StageMigrate && next == StageAuditAfter) ||
		(current == StageAuditAfter && next == StageSmoke) ||
		(current == StageSmoke && next == StageReopen)
}

func safeStateLabel(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if !((character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') || character == '_' || character == '-') {
			return false
		}
	}
	return true
}
