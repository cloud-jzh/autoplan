// Package runtime contains lifecycle and background-runtime adapters. Runtime
// code may invoke application services but must not implement business rules.
package runtime

import (
	"context"
	"time"
)

// Clock is an injectable application/runtime time source.
type Clock interface {
	Now() time.Time
}

// Component is a lifecycle-managed runtime dependency assembled by bootstrap.
type Component interface {
	Start(context.Context) error
	Close(context.Context) error
}
