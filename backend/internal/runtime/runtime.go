package runtime

import (
	"context"
	"errors"
	"time"
)

var ErrNotReady = errors.New("runtime is not ready")

// SystemClock is the production clock selected by bootstrap. Application tests
// can inject their own implementation without replacing package globals.
type SystemClock struct{}

func (SystemClock) Now() time.Time {
	return time.Now().UTC()
}

// BlockedReadiness is the safe default until lifecycle dependencies are
// implemented and initialized by P004.
type BlockedReadiness struct{}

func (BlockedReadiness) Check(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return ErrNotReady
}
