package eventbus

import (
	"context"
	"time"
)

// RetentionPolicy describes only deletion of delivery-confirmed durable
// records. Pending records are intentionally not candidates, so a dispatcher
// outage cannot turn into silent event loss.
type RetentionPolicy struct {
	Now             string
	MaximumAge      time.Duration
	GlobalLimit     int
	PerProjectLimit int
	BatchLimit      int
}

type RetentionResult struct {
	Deleted        int
	DeletedThrough map[int64]string
}

type Retainer struct {
	store  Store
	clock  Clock
	policy RetentionPolicy
}

func NewRetainer(store Store, clock Clock, policy RetentionPolicy) *Retainer {
	if clock == nil {
		clock = systemClock{}
	}
	return &Retainer{store: store, clock: clock, policy: normalizeRetention(policy)}
}

func (retainer *Retainer) Configured() bool {
	return retainer != nil && retainer.store != nil && retainer.clock != nil && validRetention(retainer.policy)
}

// PruneOnce advances retention watermarks in the same SQLite transaction as
// deletion. Reconnects that predate an advanced watermark therefore receive a
// resync_required signal rather than a guessed partial history.
func (retainer *Retainer) PruneOnce(ctx context.Context) (RetentionResult, error) {
	if err := ctx.Err(); err != nil {
		return RetentionResult{}, err
	}
	if !retainer.Configured() {
		return RetentionResult{}, ErrUnavailable
	}
	if err := retainer.store.Check(ctx); err != nil {
		return RetentionResult{}, err
	}
	policy := retainer.policy
	policy.Now = retainer.clock.Now().UTC().Format(time.RFC3339Nano)
	var result RetentionResult
	err := retainer.store.Transact(ctx, func(transaction Transaction) error {
		value, err := transaction.Prune(ctx, policy)
		if err != nil {
			return err
		}
		result = value
		return nil
	})
	return result, err
}

func normalizeRetention(policy RetentionPolicy) RetentionPolicy {
	if !validRetention(policy) {
		return RetentionPolicy{
			MaximumAge: 30 * 24 * time.Hour, GlobalLimit: 10000, PerProjectLimit: 2000, BatchLimit: 200,
		}
	}
	return policy
}

func validRetention(policy RetentionPolicy) bool {
	return policy.MaximumAge > 0 && policy.MaximumAge <= 365*24*time.Hour &&
		policy.GlobalLimit > 0 && policy.GlobalLimit <= 100000 &&
		policy.PerProjectLimit > 0 && policy.PerProjectLimit <= 20000 &&
		policy.PerProjectLimit <= policy.GlobalLimit && policy.BatchLimit > 0 && policy.BatchLimit <= 1000
}
