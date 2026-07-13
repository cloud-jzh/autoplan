package maintenance

import (
	"context"
	"errors"
	"time"

	"github.com/lyming99/autoplan/backend/internal/migration"
	"github.com/lyming99/autoplan/backend/internal/migration/audit"
	"github.com/lyming99/autoplan/backend/internal/platform/instance"
)

// Function adapters keep the orchestrator testable without admitting a raw
// SQL or filesystem API into the maintenance service.
type PreflightFunc func(context.Context) (migration.PreflightReport, error)

func (function PreflightFunc) Preflight(ctx context.Context) (migration.PreflightReport, error) {
	return function(ctx)
}

type BackupFunc func(context.Context, string, migration.PreflightReport) (migration.BackupSet, error)

func (function BackupFunc) Backup(ctx context.Context, operationID string, report migration.PreflightReport) (migration.BackupSet, error) {
	return function(ctx, operationID, report)
}

type AuditFunc func(context.Context, string) (audit.Report, error)

func (function AuditFunc) Audit(ctx context.Context, phase string) (audit.Report, error) {
	return function(ctx, phase)
}

type MigrateFunc func(context.Context) error

func (function MigrateFunc) Migrate(ctx context.Context) error { return function(ctx) }

type SmokeFunc func(context.Context) (SmokeResult, error)

func (function SmokeFunc) Smoke(ctx context.Context) (SmokeResult, error) { return function(ctx) }

type GateFunc struct {
	Freeze func(context.Context) error
	Reopen func(context.Context) error
}

func (gate GateFunc) FreezeMutations(ctx context.Context) error {
	if gate.Freeze == nil {
		return errors.New("maintenance_freeze_unavailable")
	}
	return gate.Freeze(ctx)
}
func (gate GateFunc) ReopenMutations(ctx context.Context) error {
	if gate.Reopen == nil {
		return errors.New("maintenance_reopen_unavailable")
	}
	return gate.Reopen(ctx)
}

type NodeFunc func(context.Context) error

func (function NodeFunc) PersistAndRelease(ctx context.Context) error { return function(ctx) }

type DrainFunc func(context.Context) (DrainResult, error)

func (function DrainFunc) Drain(ctx context.Context) (DrainResult, error) { return function(ctx) }

type DatabaseOwnerAcquirer struct {
	Target  string
	Timeout time.Duration
}

func (acquirer DatabaseOwnerAcquirer) Acquire(ctx context.Context) (OwnerLease, error) {
	return instance.AcquireDatabaseLock(ctx, instance.DatabaseLockOptions{Target: acquirer.Target, AllowCreate: false, Timeout: acquirer.Timeout})
}

// BackupFromPreflight makes the immutable backup stage use exactly the
// verified source report from the immediately preceding preflight stage.
func BackupFromPreflight(clock migration.Clock) BackupFunc {
	return func(ctx context.Context, operationID string, report migration.PreflightReport) (migration.BackupSet, error) {
		return migration.CreateBackup(ctx, migration.BackupOptions{Preflight: report, Clock: clock, RunID: operationID})
	}
}
