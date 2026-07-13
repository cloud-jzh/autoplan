package loop

import (
	"context"
	"fmt"

	applicationoperations "github.com/lyming99/autoplan/backend/internal/application/operations"
	domainoperation "github.com/lyming99/autoplan/backend/internal/domain/operation"
)

// OperationExecutor binds one persisted operation type to this Loop service.
// Three instances are registered at bootstrap for start, stop and run-once.
type OperationExecutor struct {
	service *Service
	kind    CommandKind
}

func NewOperationExecutor(service *Service, kind CommandKind) *OperationExecutor {
	return &OperationExecutor{service: service, kind: kind}
}

func (executor *OperationExecutor) Type() string { return string(executor.kind) }

func (executor *OperationExecutor) CanRecover(ctx context.Context, operation domainoperation.Operation) (bool, error) {
	if executor == nil || executor.service == nil || executor.service.runtime == nil || operation.Type != string(executor.kind) {
		return false, nil
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	return executor.service.runtime.Configured(), nil
}

func (executor *OperationExecutor) ExecuteRecovered(ctx context.Context, operation domainoperation.Operation) error {
	if executor == nil || executor.service == nil || executor.service.runtime == nil {
		return ErrRuntimeUnavailable
	}
	return executor.service.runtime.submitRecovered(ctx, executor.kind, operation)
}

func (service *runtimeService) Recover(ctx context.Context) error {
	_, _, stateStore, _, err := service.dependencies()
	if err != nil {
		return err
	}
	states, err := stateStore.ListRunning(ctx)
	if err != nil {
		return err
	}
	for _, state := range states {
		if err := state.Validate(); err != nil {
			return ErrStateConflict
		}
		if state.Running {
			service.arm(state.ProjectID, state.IntervalSeconds)
		}
	}
	return nil
}

func (service *runtimeService) submitRecovered(ctx context.Context, kind CommandKind, operation domainoperation.Operation) error {
	_, manager, _, _, err := service.dependencies()
	if err != nil {
		return err
	}
	if operation.Type != string(kind) || operation.Status != domainoperation.StatusQueued {
		// Operations.Recover will have transitioned a queued record exactly
		// once before calling the registry. Its in-memory copy still contains
		// the pre-transition status/version, so the runner uses version+1.
		return ErrStateConflict
	}
	key := fmt.Sprintf("loop-recovery-%d", operation.ProjectID)
	if operation.IdempotencyKey != nil {
		key = *operation.IdempotencyKey
	}
	request := &commandRequest{
		service: service,
		command: Command{Version: ContractVersion, Kind: kind, ProjectID: operation.ProjectID, CallerScope: "loop-recovery", RequestID: operation.RequestID, IdempotencyKey: key},
		digest:  operation.RequestDigest, reply: make(chan commandReply, 1), operation: operation,
		created: true, claimed: true, recovered: true,
	}
	request.operation.Status = domainoperation.StatusRunning
	request.operation.Version++
	submission, err := manager.Submit(ctx, operation.ProjectID, request.schedulerCommand())
	if err != nil {
		return err
	}
	request.submission = submission
	return nil
}

var _ applicationoperations.Executor = (*OperationExecutor)(nil)
