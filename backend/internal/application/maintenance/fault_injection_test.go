package maintenance

import (
	"context"
	"errors"
	"testing"

	"github.com/lyming99/autoplan/backend/internal/migration"
	"github.com/lyming99/autoplan/backend/internal/migration/audit"
)

type faultOwner struct{ acquired bool }

func (owner *faultOwner) Acquire(context.Context) (OwnerLease, error) {
	owner.acquired = true
	return faultLease{}, nil
}

type faultLease struct{}

func (faultLease) Close(context.Context) error { return nil }

type faultRecorder struct{ statuses []RollbackStatus }

func (recorder *faultRecorder) Preserve(_ context.Context, status RollbackStatus) error {
	recorder.statuses = append(recorder.statuses, status)
	return nil
}

func TestCutoverFaultMatrixRemainsFailClosed(t *testing.T) {
	faults := []struct {
		name  string
		apply func(*Dependencies)
	}{
		{name: "freeze", apply: func(dependencies *Dependencies) {
			dependencies.Gate = GateFunc{Freeze: func(context.Context) error { return errors.New("fault") }, Reopen: func(context.Context) error { return nil }}
		}},
		{name: "process-stop-timeout", apply: func(dependencies *Dependencies) {
			dependencies.Drainer = DrainFunc(func(context.Context) (DrainResult, error) { return DrainResult{}, errors.New("timeout") })
		}},
		{name: "final-node-persist", apply: func(dependencies *Dependencies) {
			dependencies.Node = NodeFunc(func(context.Context) error { return errors.New("persist") })
		}},
		{name: "preflight", apply: func(dependencies *Dependencies) {
			dependencies.Preflight = PreflightFunc(func(context.Context) (migration.PreflightReport, error) {
				return migration.PreflightReport{}, errors.New("preflight")
			})
		}},
		{name: "backup-hash", apply: func(dependencies *Dependencies) {
			dependencies.Backup = BackupFunc(func(context.Context, string, migration.PreflightReport) (migration.BackupSet, error) {
				return migration.BackupSet{}, errors.New("hash")
			})
		}},
		{name: "backup-interruption", apply: func(dependencies *Dependencies) {
			dependencies.Backup = BackupFunc(func(context.Context, string, migration.PreflightReport) (migration.BackupSet, error) {
				return migration.BackupSet{}, context.Canceled
			})
		}},
		{name: "audit-before", apply: func(dependencies *Dependencies) {
			dependencies.Audit = AuditFunc(func(context.Context, string) (audit.Report, error) { return audit.Report{OK: false}, nil })
		}},
		{name: "audit-after", apply: func(dependencies *Dependencies) {
			dependencies.Audit = AuditFunc(func(_ context.Context, phase string) (audit.Report, error) {
				if phase == "after_migration" {
					return audit.Report{OK: false}, nil
				}
				return audit.Report{OK: true}, nil
			})
		}},
		{name: "migration-write", apply: func(dependencies *Dependencies) {
			dependencies.Migrator = MigrateFunc(func(context.Context) error { return errors.New("write-point") })
		}},
		{name: "owner-lock-conflict", apply: func(dependencies *Dependencies) {
			dependencies.Owner = ownerFunc(func(context.Context) (OwnerLease, error) { return nil, errors.New("locked") })
		}},
		{name: "readiness", apply: func(dependencies *Dependencies) {
			dependencies.Smoke = SmokeFunc(func(context.Context) (SmokeResult, error) { return SmokeResult{}, errors.New("readiness") })
		}},
		{name: "readyz", apply: func(dependencies *Dependencies) {
			dependencies.Smoke = SmokeFunc(func(context.Context) (SmokeResult, error) { return SmokeResult{}, errors.New("readyz") })
		}},
		{name: "read-write-smoke", apply: func(dependencies *Dependencies) {
			dependencies.Smoke = SmokeFunc(func(context.Context) (SmokeResult, error) { return SmokeResult{}, nil })
		}},
		{name: "ui-open", apply: func(dependencies *Dependencies) {
			dependencies.Gate = GateFunc{Freeze: func(context.Context) error { return nil }, Reopen: func(context.Context) error { return errors.New("open") }}
		}},
		{name: "process-tree-cleanup", apply: func(dependencies *Dependencies) {
			dependencies.Drainer = DrainFunc(func(context.Context) (DrainResult, error) { return DrainResult{}, errors.New("cleanup") })
		}},
	}
	for _, fault := range faults {
		t.Run(fault.name, func(t *testing.T) {
			dependencies := healthyCutoverDependencies()
			fault.apply(&dependencies)
			service, err := NewService(dependencies)
			if err != nil {
				t.Fatal(err)
			}
			status, err := service.Cutover(context.Background())
			if !errors.Is(err, ErrCutoverFailed) || status.Mode != ModeMaintenance || status.Stage != StageFailed || !status.MutationsBlocked {
				t.Fatalf("fault=%s status=%#v error=%v", fault.name, status, err)
			}
		})
	}
}

func TestRollbackBoundaryRejectsImplicitDataTruncationAndNeverReopensMutations(t *testing.T) {
	service, err := NewRollbackService(RollbackDependencies{
		Gate:           GateFunc{Freeze: func(context.Context) error { return nil }, Reopen: func(context.Context) error { t.Fatal("rollback must not reopen mutations"); return nil }},
		Stopper:        rollbackStopperFunc(func(context.Context) error { return nil }),
		ControlledCopy: rollbackVerifierFunc(func(context.Context) error { return nil }),
		Owner:          rollbackOwnerFunc(func(context.Context) error { return nil }),
		FirstWrite:     firstWriteFunc(func(context.Context, string) (FirstWriteBoundary, error) { return BeforeFirstGoWrite, nil }),
		Restorer: RestoreFunc(func(context.Context, migration.RestoreOptions) (migration.RestoreResult, error) {
			return migration.RestoreResult{}, nil
		}),
		Evidence: &faultRecorder{},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Recover(context.Background(), RollbackRequest{
		OperationID: "after-write", Boundary: AfterFirstGoWrite, Strategy: RecoveryRestoreIndependentCopy,
		Restore: migration.RestoreOptions{Mode: migration.RestoreModeIndependentCopy},
	})
	if !errors.Is(err, ErrRollbackBoundary) {
		t.Fatalf("implicit post-write restore error = %v", err)
	}
	status, err := service.Recover(context.Background(), RollbackRequest{
		OperationID: "before-write", Boundary: BeforeFirstGoWrite, Strategy: RecoveryRestoreIndependentCopy,
		Restore: migration.RestoreOptions{Mode: migration.RestoreModeIndependentCopy},
	})
	if err != nil || !status.Restored || !status.MutationsBlocked || status.Code != "restore_complete" {
		t.Fatalf("pre-write restore status=%#v error=%v", status, err)
	}
}

func healthyCutoverDependencies() Dependencies {
	return Dependencies{
		Gate:    GateFunc{Freeze: func(context.Context) error { return nil }, Reopen: func(context.Context) error { return nil }},
		Drainer: DrainFunc(func(context.Context) (DrainResult, error) { return DrainResult{}, nil }),
		Node:    NodeFunc(func(context.Context) error { return nil }),
		Preflight: PreflightFunc(func(context.Context) (migration.PreflightReport, error) {
			return migration.PreflightReport{StableDatabaseID: "0123456789abcdef"}, nil
		}),
		Backup: BackupFunc(func(context.Context, string, migration.PreflightReport) (migration.BackupSet, error) {
			return migration.BackupSet{}, nil
		}),
		Audit: AuditFunc(func(_ context.Context, phase string) (audit.Report, error) {
			return audit.Report{Phase: phase, OK: true}, nil
		}),
		Migrator: MigrateFunc(func(context.Context) error { return nil }),
		Owner:    &faultOwner{},
		Smoke: SmokeFunc(func(context.Context) (SmokeResult, error) {
			return SmokeResult{ApplicationReadWrite: true, RESTReadWrite: true, SnapshotVisible: true, MarkerRolledBack: true, NoDuplicateEvents: true}, nil
		}),
		OperationID: func() string { return "fault-matrix" },
	}
}

type ownerFunc func(context.Context) (OwnerLease, error)

func (function ownerFunc) Acquire(ctx context.Context) (OwnerLease, error) { return function(ctx) }

type rollbackStopperFunc func(context.Context) error

func (function rollbackStopperFunc) StopGo(ctx context.Context) error { return function(ctx) }

type rollbackVerifierFunc func(context.Context) error

func (function rollbackVerifierFunc) VerifyControlledCopy(ctx context.Context) error {
	return function(ctx)
}

type rollbackOwnerFunc func(context.Context) error

func (function rollbackOwnerFunc) ReleaseOwner(ctx context.Context) error { return function(ctx) }

type firstWriteFunc func(context.Context, string) (FirstWriteBoundary, error)

func (function firstWriteFunc) Boundary(ctx context.Context, operationID string) (FirstWriteBoundary, error) {
	return function(ctx, operationID)
}
