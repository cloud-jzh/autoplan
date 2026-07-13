package maintenance

import (
	"context"
	"errors"
	"fmt"

	"github.com/lyming99/autoplan/backend/internal/migration"
)

var (
	ErrRollbackDependency = errors.New("rollback_dependency_invalid")
	ErrRollbackBoundary   = errors.New("rollback_boundary_rejected")
	ErrRollbackFailed     = errors.New("rollback_failed")
)

type FirstWriteBoundary string

const (
	BeforeFirstGoWrite FirstWriteBoundary = "before_first_go_write"
	AfterFirstGoWrite  FirstWriteBoundary = "after_first_go_write"
)

type RecoveryStrategy string

const (
	RecoveryRestoreIndependentCopy RecoveryStrategy = "restore_independent_copy"
	RecoveryRestoreTruncating      RecoveryStrategy = "restore_truncating_replace"
	RecoveryRollbackBinary         RecoveryStrategy = "rollback_go_binary"
	RecoveryForwardRepair          RecoveryStrategy = "forward_repair"
)

type GoStopper interface {
	StopGo(context.Context) error
}

type ControlledCopyVerifier interface {
	VerifyControlledCopy(context.Context) error
}

type OwnerReleaser interface {
	ReleaseOwner(context.Context) error
}

// FirstWriteVerifier is backed by the Go owner's append-only mutation record.
// A caller cannot self-declare a pre-write boundary to bypass truncation
// confirmation after an official Go mutation has been committed.
type FirstWriteVerifier interface {
	Boundary(context.Context, string) (FirstWriteBoundary, error)
}

type CopyRestorer interface {
	Restore(context.Context, migration.RestoreOptions) (migration.RestoreResult, error)
}

type BinaryRollbacker interface {
	RollbackBinary(context.Context) error
}

type ForwardRepairer interface {
	RepairForward(context.Context) error
}

// EvidenceRecorder is called on every terminal result.  Its implementation
// may retain redacted hashes and stable codes, but it must not reopen writers
// if evidence storage itself fails.
type EvidenceRecorder interface {
	Preserve(context.Context, RollbackStatus) error
}

type RollbackDependencies struct {
	Gate             MutationGate
	Stopper          GoStopper
	ControlledCopy   ControlledCopyVerifier
	Owner            OwnerReleaser
	FirstWrite       FirstWriteVerifier
	Restorer         CopyRestorer
	BinaryRollbacker BinaryRollbacker
	ForwardRepairer  ForwardRepairer
	Evidence         EvidenceRecorder
}

type RollbackRequest struct {
	OperationID string
	Boundary    FirstWriteBoundary
	Strategy    RecoveryStrategy
	Restore     migration.RestoreOptions
}

// RollbackStatus is transport-safe.  It deliberately omits paths, backup
// contents, session material, and individual affected mutation identifiers.
type RollbackStatus struct {
	OperationID       string `json:"operation_id"`
	Boundary          string `json:"boundary"`
	Strategy          string `json:"strategy"`
	Stage             string `json:"stage"`
	Code              string `json:"code"`
	MutationsBlocked  bool   `json:"mutations_blocked"`
	Restored          bool   `json:"restored"`
	AffectedMutations int    `json:"affected_mutations"`
	AffectedDigest    string `json:"affected_mutations_sha256,omitempty"`
}

type RollbackService struct{ dependencies RollbackDependencies }

func NewRollbackService(dependencies RollbackDependencies) (*RollbackService, error) {
	if dependencies.Gate == nil || dependencies.Stopper == nil || dependencies.ControlledCopy == nil ||
		dependencies.Owner == nil || dependencies.FirstWrite == nil || dependencies.Restorer == nil || dependencies.Evidence == nil {
		return nil, ErrRollbackDependency
	}
	return &RollbackService{dependencies: dependencies}, nil
}

func (service *RollbackService) Recover(ctx context.Context, request RollbackRequest) (RollbackStatus, error) {
	status, err := validateRollbackRequest(request)
	if err != nil {
		return status, err
	}
	if service == nil {
		return status, ErrRollbackDependency
	}
	boundary, err := service.dependencies.FirstWrite.Boundary(ctx, request.OperationID)
	if err != nil {
		return service.fail(ctx, status, "first_write_state_failed", err)
	}
	if boundary != request.Boundary {
		return service.fail(ctx, status, "first_write_boundary_mismatch", ErrRollbackBoundary)
	}
	if err := service.dependencies.Gate.FreezeMutations(ctx); err != nil {
		return service.fail(ctx, status, "freeze_failed", err)
	}
	status.Stage = "stop_go"
	if err := service.dependencies.Stopper.StopGo(ctx); err != nil {
		return service.fail(ctx, status, "go_stop_failed", err)
	}
	status.Stage = "verify_controlled_copy"
	if err := service.dependencies.ControlledCopy.VerifyControlledCopy(ctx); err != nil {
		return service.fail(ctx, status, "controlled_copy_failed", err)
	}
	status.Stage = "release_owner"
	if err := service.dependencies.Owner.ReleaseOwner(ctx); err != nil {
		return service.fail(ctx, status, "owner_release_failed", err)
	}
	switch request.Strategy {
	case RecoveryRestoreIndependentCopy, RecoveryRestoreTruncating:
		status.Stage = "restore_copy"
		result, err := service.dependencies.Restorer.Restore(ctx, request.Restore)
		if err != nil {
			return service.fail(ctx, status, "restore_failed", err)
		}
		status.Stage = "complete"
		status.Code = "restore_complete"
		status.Restored = true
		status.AffectedMutations = result.AffectedMutationCount
		status.AffectedDigest = result.AffectedMutationSHA256
	case RecoveryRollbackBinary:
		if service.dependencies.BinaryRollbacker == nil {
			return service.fail(ctx, status, "binary_rollback_unavailable", ErrRollbackBoundary)
		}
		status.Stage = "rollback_binary"
		if err := service.dependencies.BinaryRollbacker.RollbackBinary(ctx); err != nil {
			return service.fail(ctx, status, "binary_rollback_failed", err)
		}
		status.Stage, status.Code = "complete", "binary_rollback_complete"
	case RecoveryForwardRepair:
		if service.dependencies.ForwardRepairer == nil {
			return service.fail(ctx, status, "forward_repair_unavailable", ErrRollbackBoundary)
		}
		status.Stage = "forward_repair"
		if err := service.dependencies.ForwardRepairer.RepairForward(ctx); err != nil {
			return service.fail(ctx, status, "forward_repair_failed", err)
		}
		status.Stage, status.Code = "complete", "forward_repair_complete"
	default:
		return service.fail(ctx, status, "strategy_invalid", ErrRollbackBoundary)
	}
	_ = service.dependencies.Evidence.Preserve(ctx, status)
	return status, nil
}

func validateRollbackRequest(request RollbackRequest) (RollbackStatus, error) {
	status := RollbackStatus{
		OperationID: request.OperationID, Boundary: string(request.Boundary), Strategy: string(request.Strategy),
		Stage: "rejected", Code: "rollback_rejected", MutationsBlocked: true,
	}
	if !safeStateLabel(request.OperationID) || (request.Boundary != BeforeFirstGoWrite && request.Boundary != AfterFirstGoWrite) {
		return status, ErrRollbackBoundary
	}
	switch request.Boundary {
	case BeforeFirstGoWrite:
		if request.Strategy != RecoveryRestoreIndependentCopy || request.Restore.Mode != migration.RestoreModeIndependentCopy {
			return status, ErrRollbackBoundary
		}
	case AfterFirstGoWrite:
		switch request.Strategy {
		case RecoveryRollbackBinary, RecoveryForwardRepair:
			if request.Restore.Mode != "" {
				return status, ErrRollbackBoundary
			}
		case RecoveryRestoreTruncating:
			if request.Restore.Mode != migration.RestoreModeTruncatingReplace || !request.Restore.Truncation.Confirmed {
				return status, ErrRollbackBoundary
			}
		default:
			return status, ErrRollbackBoundary
		}
	}
	status.Stage, status.Code = "freeze", "rollback_active"
	return status, nil
}

func (service *RollbackService) fail(ctx context.Context, status RollbackStatus, code string, cause error) (RollbackStatus, error) {
	if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		code = "rollback_cancelled"
	}
	status.Stage, status.Code, status.MutationsBlocked = "failed", code, true
	_ = service.dependencies.Evidence.Preserve(ctx, status)
	return status, fmt.Errorf("%w: %s", ErrRollbackFailed, code)
}

type RestoreFunc func(context.Context, migration.RestoreOptions) (migration.RestoreResult, error)

func (function RestoreFunc) Restore(ctx context.Context, options migration.RestoreOptions) (migration.RestoreResult, error) {
	return function(ctx, options)
}
