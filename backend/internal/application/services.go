package application

import (
	"context"
	"errors"
	"time"

	"github.com/lyming99/autoplan/backend/internal/domain"
	"github.com/lyming99/autoplan/backend/internal/repository"
)

var ErrMissingDependency = errors.New("application dependency is missing")

// Clock is owned by the application boundary so fake clocks do not require a
// concrete runtime implementation.
type Clock interface {
	Now() time.Time
}

// ReadinessGate prevents application use before all required dependencies are
// ready. P004 will provide the lifecycle-aware aggregate implementation.
type ReadinessGate interface {
	Check(context.Context) error
}

// LogEntry is deliberately limited to non-sensitive infrastructure metadata.
// Arbitrary fields, request bodies, paths, and errors are not accepted here.
type LogEntry struct {
	Level string
	Code  string
}

type Logger interface {
	Log(context.Context, LogEntry)
}

// ServiceDependencies are explicit outbound ports. Tests can replace every
// dependency without process globals or a production database.
type ServiceDependencies struct {
	Clock      Clock
	Readiness  ReadinessGate
	Repository repository.Readiness
	Events     EventSink
	Logger     Logger
}

// Services is the single application facade shared by REST, streaming, MCP,
// background runtime, and future Electron compatibility adapters.
type Services struct {
	clock      Clock
	readiness  ReadinessGate
	repository repository.Readiness
	events     EventSink
	logger     Logger
}

var _ Boundary = (*Services)(nil)

func NewServices(dependencies ServiceDependencies) (*Services, error) {
	if dependencies.Clock == nil || dependencies.Readiness == nil || dependencies.Repository == nil ||
		dependencies.Events == nil || dependencies.Logger == nil {
		return nil, ErrMissingDependency
	}
	return &Services{
		clock:      dependencies.Clock,
		readiness:  dependencies.Readiness,
		repository: dependencies.Repository,
		events:     dependencies.Events,
		logger:     dependencies.Logger,
	}, nil
}

// Capabilities implements Boundary without exposing an adapter-specific API.
// Returning a new slice prevents callers from mutating application state.
func (services *Services) Capabilities(context.Context) []domain.Service {
	return []domain.Service{
		domain.ServiceProjects,
		domain.ServiceSnapshots,
		domain.ServiceOperations,
		domain.ServiceEvents,
	}
}

// Ready performs real dependency checks; it never turns an unavailable
// repository into a successful readiness result.
func (services *Services) Ready(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := services.readiness.Check(ctx); err != nil {
		return err
	}
	return services.repository.Check(ctx)
}
