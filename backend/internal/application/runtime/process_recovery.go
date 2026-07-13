package runtime

import (
	"context"
	"errors"
	"time"

	"github.com/lyming99/autoplan/backend/internal/config"
)

const (
	DefaultProcessRecoveryMaximumRecords = config.DefaultProcessRecoveryRecords
	MaximumProcessRecoveryRecords        = config.MaximumProcessRecoveryRecords
)

var ErrProcessRecovery = errors.New("runtime process recovery is invalid")

// ProcessRecoveryInput is intentionally bounded. A database with more stale
// records than the configured safe batch fails closed during startup instead
// of partially recovering it and then accepting new process work.
type ProcessRecoveryInput struct {
	OccurredAt     string
	RequestID      string
	MaximumRecords int
}

type ProcessRecoveryResult struct {
	InterruptedOperations int
	InterruptedScripts    int
	InterruptedExecutors  int
}

// ProcessRecoveryStore is implemented by the runtime persistence adapter.
// Its one method must update Operation state, resource last_* state and all
// durable events in the same database transaction.
type ProcessRecoveryStore interface {
	RecoverProcessOwnership(context.Context, ProcessRecoveryInput) (ProcessRecoveryResult, error)
}

func DefaultProcessRecoveryInput(now time.Time) ProcessRecoveryInput {
	return ProcessRecoveryInput{
		OccurredAt:     now.UTC().Format(time.RFC3339Nano),
		RequestID:      "runtime-recovery",
		MaximumRecords: DefaultProcessRecoveryMaximumRecords,
	}
}

func (value ProcessRecoveryInput) Valid() bool {
	if value.MaximumRecords <= 0 || value.MaximumRecords > MaximumProcessRecoveryRecords || !validOwnerID(value.RequestID) {
		return false
	}
	if len(value.OccurredAt) == 0 || value.OccurredAt[len(value.OccurredAt)-1] != 'Z' {
		return false
	}
	parsed, err := time.Parse(time.RFC3339Nano, value.OccurredAt)
	return err == nil && parsed.Location() == time.UTC
}

// RecoverProcessOwnership first clears any local registrations, then asks the
// durable adapter to interrupt stale process work. It has no restart callback
// and no PID parameter by design: an earlier runtime's process tree is never
// adopted or relaunched.
func RecoverProcessOwnership(
	ctx context.Context,
	registry *OwnershipRegistry,
	store ProcessRecoveryStore,
	input ProcessRecoveryInput,
) (ProcessRecoveryResult, error) {
	if ctx == nil || store == nil || !input.Valid() {
		return ProcessRecoveryResult{}, ErrProcessRecovery
	}
	if err := ctx.Err(); err != nil {
		return ProcessRecoveryResult{}, err
	}
	if registry != nil {
		registry.Reset()
	}
	return store.RecoverProcessOwnership(ctx, input)
}
