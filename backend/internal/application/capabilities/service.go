// Package capabilities declares the fixed public capability surface.
//
// The P07 migration deliberately exposes long-running actions as disabled.
// This package is data-only: it has no configuration, repository, runtime,
// filesystem, process, or credential dependency that could enable an action
// implicitly.
package capabilities

import "errors"

const ContractVersion = "v1"

type ID string

const (
	PlansQuery    ID = "plans.query"
	PlansReorder  ID = "plans.reorder"
	PlansDelete   ID = "plans.delete"
	PlansAccept   ID = "plans.accept"
	PlansUnaccept ID = "plans.unaccept"
	PlansRedo     ID = "plans.redo"
	TasksQuery    ID = "tasks.query"
	TasksAccept   ID = "tasks.accept"
	TasksUnaccept ID = "tasks.unaccept"
	TasksRedo     ID = "tasks.redo"
	EventsQuery   ID = "events.query"

	PlansRun        ID = "plans.run"
	PlansStop       ID = "plans.stop"
	PlansResume     ID = "plans.resume"
	PlansReexecute  ID = "plans.reexecute"
	PlansRecreate   ID = "plans.recreate"
	TasksRun        ID = "tasks.run"
	TasksRunBatches ID = "tasks.run_batches"
	TasksStop       ID = "tasks.stop"
)

var ErrNotImplemented = errors.New("long-running action is not implemented")

// Capability is intentionally limited to an opaque machine-readable ID and
// its state. It never describes a command, session, filesystem path, or a
// particular resource.
type Capability struct {
	ID      ID   `json:"id"`
	Enabled bool `json:"enabled"`
}

// OperationReference is the success shape reserved for a later, separately
// gated action implementation. Disabled actions never construct one.
type OperationReference struct {
	OperationID string `json:"operation_id"`
	Type        string `json:"type"`
	Status      string `json:"status"`
	RequestID   string `json:"request_id"`
	AcceptedAt  string `json:"accepted_at"`
}

// DisabledActionError retains only the advertised capability. Its text is
// deliberately target-independent so it cannot disclose resource existence,
// project ownership, commands, sessions, or paths.
type DisabledActionError struct{ capability ID }

func (err DisabledActionError) Error() string { return "action capability is disabled" }

func (err DisabledActionError) Unwrap() error { return ErrNotImplemented }

func (err DisabledActionError) Capability() ID { return err.capability }

// Service owns an immutable capability catalog. There is intentionally no
// options object, feature flag, request override, or development-mode switch.
type Service struct{}

func NewService() *Service { return &Service{} }

func (service *Service) List() []Capability {
	return append([]Capability(nil), catalog...)
}

// DisabledAction always rejects the supplied long-running action before a
// caller can inspect or mutate a target.
func (service *Service) DisabledAction(capability ID) error {
	return &DisabledActionError{capability: capability}
}

var catalog = []Capability{
	{ID: PlansQuery, Enabled: true},
	{ID: PlansReorder, Enabled: true},
	{ID: PlansDelete, Enabled: true},
	{ID: PlansAccept, Enabled: true},
	{ID: PlansUnaccept, Enabled: true},
	{ID: PlansRedo, Enabled: true},
	{ID: TasksQuery, Enabled: true},
	{ID: TasksAccept, Enabled: true},
	{ID: TasksUnaccept, Enabled: true},
	{ID: TasksRedo, Enabled: true},
	{ID: EventsQuery, Enabled: true},
	{ID: PlansRun, Enabled: false},
	{ID: PlansStop, Enabled: false},
	{ID: PlansResume, Enabled: false},
	{ID: PlansReexecute, Enabled: false},
	{ID: PlansRecreate, Enabled: false},
	{ID: TasksRun, Enabled: false},
	{ID: TasksRunBatches, Enabled: false},
	{ID: TasksStop, Enabled: false},
}
