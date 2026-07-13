// Package application owns AutoPlan use cases and business orchestration.
// Every inbound adapter calls services exposed through Boundary; adapters must
// not call repositories, runtimes, or platform integrations directly.
package application

import (
	"context"

	"github.com/lyming99/autoplan/backend/internal/domain"
)

// Boundary is the shared application-service surface used by every transport.
// Domain-specific service interfaces will be added here as contracts migrate.
// The skeleton deliberately exposes no production read or mutation operation.
type Boundary interface {
	Capabilities(context.Context) []domain.Service
}

// EventSink is an outbound application port. Runtime implementations may
// publish events, but transports do not become owners of business rules.
type EventSink interface {
	Publish(context.Context, domain.Service) error
}
