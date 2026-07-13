package maintenance

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"

	"github.com/lyming99/autoplan/backend/internal/migration"
	"github.com/lyming99/autoplan/backend/internal/migration/audit"
)

var (
	ErrDependencyInvalid = errors.New("maintenance_dependency_invalid")
	ErrCutoverFailed     = errors.New("maintenance_cutover_failed")
)

type MutationGate interface {
	FreezeMutations(context.Context) error
	ReopenMutations(context.Context) error
}

type NodeHandoff interface {
	PersistAndRelease(context.Context) error
}

type Preflighter interface {
	Preflight(context.Context) (migration.PreflightReport, error)
}

type Backuper interface {
	Backup(context.Context, string, migration.PreflightReport) (migration.BackupSet, error)
}

type Auditor interface {
	Audit(context.Context, string) (audit.Report, error)
}

type Migrator interface {
	Migrate(context.Context) error
}

type OwnerLease interface {
	Close(context.Context) error
}

type OwnerAcquirer interface {
	Acquire(context.Context) (OwnerLease, error)
}

type Dependencies struct {
	Gate        MutationGate
	Drainer     Drainer
	Node        NodeHandoff
	Preflight   Preflighter
	Backup      Backuper
	Audit       Auditor
	Migrator    Migrator
	Owner       OwnerAcquirer
	Smoke       Smoker
	OperationID func() string
}

type Service struct {
	dependencies Dependencies
	state        *State
	mu           sync.Mutex
	running      bool
	owner        OwnerLease
}

func NewService(dependencies Dependencies) (*Service, error) {
	if dependencies.Gate == nil || dependencies.Drainer == nil || dependencies.Node == nil ||
		dependencies.Preflight == nil || dependencies.Backup == nil || dependencies.Audit == nil ||
		dependencies.Migrator == nil || dependencies.Owner == nil || dependencies.Smoke == nil {
		return nil, ErrDependencyInvalid
	}
	if dependencies.OperationID == nil {
		dependencies.OperationID = newOperationID
	}
	return &Service{dependencies: dependencies, state: NewState()}, nil
}

func (service *Service) Status() Status {
	if service == nil || service.state == nil {
		return Status{Mode: ModeMaintenance, Stage: StageFailed, Code: "service_unavailable", MutationsBlocked: true}
	}
	return service.state.Snapshot()
}

// Cutover is intentionally one-way.  Once maintenance starts, every failure
// remains fail-closed; the previous Node writer is never reopened by this
// method and a successful Go ownership lease remains held by the service.
func (service *Service) Cutover(ctx context.Context) (Status, error) {
	if service == nil || service.state == nil {
		return Status{Mode: ModeMaintenance, Stage: StageFailed, Code: "service_unavailable", MutationsBlocked: true}, ErrDependencyInvalid
	}
	service.mu.Lock()
	if service.running {
		status := service.Status()
		service.mu.Unlock()
		return status, ErrOperationInProgress
	}
	operationID := service.dependencies.OperationID()
	if err := service.state.Begin(operationID); err != nil {
		status := service.Status()
		service.mu.Unlock()
		return status, err
	}
	service.running = true
	service.mu.Unlock()
	defer func() {
		service.mu.Lock()
		service.running = false
		service.mu.Unlock()
	}()

	if err := service.dependencies.Gate.FreezeMutations(ctx); err != nil {
		return service.fail(err, "freeze_failed")
	}
	if err := service.advance(StageDrain); err != nil {
		return service.fail(err, "state_transition_failed")
	}
	drain, err := service.dependencies.Drainer.Drain(ctx)
	if err != nil {
		return service.fail(err, "drain_failed")
	}
	if !drain.Complete() {
		return service.fail(ErrCutoverFailed, "drain_incomplete")
	}
	if err := service.advance(StageNodeRelease); err != nil {
		return service.fail(err, "state_transition_failed")
	}
	if err := service.dependencies.Node.PersistAndRelease(ctx); err != nil {
		return service.fail(err, "node_release_failed")
	}
	if err := service.advance(StagePreflight); err != nil {
		return service.fail(err, "state_transition_failed")
	}
	preflight, err := service.dependencies.Preflight.Preflight(ctx)
	if err != nil {
		return service.fail(err, "preflight_failed")
	}
	// Preflight verifies that no legacy owner sidecar exists.  Immediately
	// after it succeeds, hold the same database identity lock that legacy Node
	// writers use before creating backups or touching the migration runner.
	if err := service.advance(StageOwnerLock); err != nil {
		return service.fail(err, "state_transition_failed")
	}
	lease, err := service.dependencies.Owner.Acquire(ctx)
	if err != nil || lease == nil {
		return service.fail(err, "owner_lock_failed")
	}
	service.mu.Lock()
	service.owner = lease
	service.mu.Unlock()
	if err := service.advance(StageBackup); err != nil {
		return service.fail(err, "state_transition_failed")
	}
	if _, err := service.dependencies.Backup.Backup(ctx, operationID, preflight); err != nil {
		return service.fail(err, "backup_failed")
	}
	if err := service.advance(StageAuditBefore); err != nil {
		return service.fail(err, "state_transition_failed")
	}
	before, err := service.dependencies.Audit.Audit(ctx, "before_migration")
	if err != nil || !before.OK {
		return service.fail(err, "audit_before_failed")
	}
	if err := service.advance(StageMigrate); err != nil {
		return service.fail(err, "state_transition_failed")
	}
	if err := service.dependencies.Migrator.Migrate(ctx); err != nil {
		return service.fail(err, "migration_failed")
	}
	if err := service.advance(StageAuditAfter); err != nil {
		return service.fail(err, "state_transition_failed")
	}
	after, err := service.dependencies.Audit.Audit(ctx, "after_migration")
	if err != nil || !after.OK {
		return service.fail(err, "audit_after_failed")
	}
	if comparison := audit.Compare(before, after, nil); !comparison.OK {
		return service.fail(ErrCutoverFailed, "audit_delta_failed")
	}
	if err := service.advance(StageSmoke); err != nil {
		return service.fail(err, "state_transition_failed")
	}
	smoke, err := service.dependencies.Smoke.Smoke(ctx)
	if err != nil || !smoke.Passed() {
		return service.fail(err, "smoke_failed")
	}
	if err := service.advance(StageReopen); err != nil {
		return service.fail(err, "state_transition_failed")
	}
	if err := service.dependencies.Gate.ReopenMutations(ctx); err != nil {
		return service.fail(err, "reopen_failed")
	}
	if err := service.state.Complete(); err != nil {
		return service.fail(err, "state_transition_failed")
	}
	return service.Status(), nil
}

// Close releases only the Go ownership lease during controlled process
// shutdown.  Cutover failures intentionally do not call it.
func (service *Service) Close(ctx context.Context) error {
	if service == nil {
		return nil
	}
	service.mu.Lock()
	lease := service.owner
	service.owner = nil
	service.mu.Unlock()
	if lease == nil {
		return nil
	}
	return lease.Close(ctx)
}

func (service *Service) advance(stage Stage) error { return service.state.Advance(stage) }

func (service *Service) fail(cause error, code string) (Status, error) {
	if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		code = "operation_cancelled"
	}
	service.state.Fail(code)
	return service.Status(), fmt.Errorf("%w: %s", ErrCutoverFailed, code)
}

func newOperationID() string {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "cutover-random-unavailable"
	}
	return "cutover-" + hex.EncodeToString(bytes)
}
