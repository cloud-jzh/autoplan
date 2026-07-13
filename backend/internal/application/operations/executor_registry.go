package operations

import (
	"context"
	"errors"
	"sort"
	"sync"

	domainoperation "github.com/lyming99/autoplan/backend/internal/domain/operation"
)

var ErrExecutorRegistry = errors.New("operation executor registry is invalid")

// Executor is the application-owned continuation for one Operation type.
// It never receives a transport request or raw process data. Recovery is
// split in two: CanRecover merely proves ownership; ExecuteRecovered runs
// only after Operations.Recover committed the running transition.
type Executor interface {
	Type() string
	CanRecover(context.Context, domainoperation.Operation) (bool, error)
	ExecuteRecovered(context.Context, domainoperation.Operation) error
}

// RecoveryClaimObserver is notified only after the generic Operation recovery
// transaction committed its queued-to-running transition. This prevents an
// executor from running an item that was merely inspected in a transaction
// later rolled back because another project's recovery failed.
type RecoveryClaimObserver interface {
	RecoveryClaimed(context.Context, domainoperation.Operation)
}

// ExecutorRegistry freezes the operation-to-executor mapping during
// bootstrap. It also remembers successfully claimed queued Operations so the
// lifecycle can resume them only after the generic Operation recovery
// transaction has completed.
type ExecutorRegistry struct {
	mu        sync.Mutex
	executors map[string]Executor
	claimed   []domainoperation.Operation
	frozen    bool
}

func NewExecutorRegistry() *ExecutorRegistry {
	return &ExecutorRegistry{executors: make(map[string]Executor)}
}

func (registry *ExecutorRegistry) Register(executor Executor) error {
	if registry == nil || executor == nil || !validOperationType(executor.Type()) {
		return ErrExecutorRegistry
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if registry.frozen {
		return ErrExecutorRegistry
	}
	if _, exists := registry.executors[executor.Type()]; exists {
		return ErrExecutorRegistry
	}
	registry.executors[executor.Type()] = executor
	return nil
}

// RecoveryHandlers returns a stable copy suitable for Service construction.
// Registration is closed afterwards so a recovery pass cannot observe a
// partial executor catalogue.
func (registry *ExecutorRegistry) RecoveryHandlers() []RecoveryHandler {
	if registry == nil {
		return nil
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	registry.frozen = true
	types := make([]string, 0, len(registry.executors))
	for operationType := range registry.executors {
		types = append(types, operationType)
	}
	sort.Strings(types)
	values := make([]RecoveryHandler, 0, len(types))
	for _, operationType := range types {
		executor := registry.executors[operationType]
		values = append(values, registryRecoveryHandler{registry: registry, operationType: operationType, executor: executor})
	}
	return values
}

// ResumeClaimed is called after Service.Recover. It drains every remembered
// claim exactly once. A failure is retained for the next supervised recovery
// attempt; it is never silently discarded.
func (registry *ExecutorRegistry) ResumeClaimed(ctx context.Context) error {
	if registry == nil {
		return nil
	}
	for {
		registry.mu.Lock()
		if len(registry.claimed) == 0 {
			registry.mu.Unlock()
			return nil
		}
		operation := registry.claimed[0]
		registry.claimed = registry.claimed[1:]
		executor := registry.executors[operation.Type]
		registry.mu.Unlock()
		if executor == nil {
			return ErrExecutorRegistry
		}
		if err := executor.ExecuteRecovered(ctx, operation); err != nil {
			registry.mu.Lock()
			registry.claimed = append([]domainoperation.Operation{operation}, registry.claimed...)
			registry.mu.Unlock()
			return err
		}
	}
}

type registryRecoveryHandler struct {
	registry      *ExecutorRegistry
	operationType string
	executor      Executor
}

func (handler registryRecoveryHandler) Type() string { return handler.operationType }

func (handler registryRecoveryHandler) CanRecover(ctx context.Context, operation domainoperation.Operation) (bool, error) {
	if handler.registry == nil || handler.executor == nil || operation.Type != handler.operationType {
		return false, nil
	}
	claimable, err := handler.executor.CanRecover(ctx, operation)
	if err != nil || !claimable {
		return claimable, err
	}
	return true, nil
}

func (handler registryRecoveryHandler) RecoveryClaimed(_ context.Context, operation domainoperation.Operation) {
	if handler.registry == nil || operation.Type != handler.operationType {
		return
	}
	handler.registry.mu.Lock()
	defer handler.registry.mu.Unlock()
	for _, claimed := range handler.registry.claimed {
		if claimed.OperationID == operation.OperationID {
			return
		}
	}
	handler.registry.claimed = append(handler.registry.claimed, operation)
}
