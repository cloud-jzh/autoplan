// Package automation provides pure-persistence Script and Executor use cases
// plus a separately registered runtime command handler.
package automation

import (
	"context"
	"errors"
	"time"

	domainautomation "github.com/lyming99/autoplan/backend/internal/domain/automation"
	"github.com/lyming99/autoplan/backend/internal/repository"
)

var (
	ErrUnavailable     = errors.New("automation application service unavailable")
	ErrInvalidCommand  = errors.New("automation command is invalid")
	ErrStateConflict   = errors.New("automation state conflicts")
	ErrRuntimeDisabled = errors.New("automation runtime capability is disabled")
)

type Clock interface{ Now() time.Time }

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now().UTC() }

type Dependencies struct {
	Writer repository.AutomationTransactional
	Clock  Clock
}

type Service struct {
	writer repository.AutomationTransactional
	clock  Clock
}

func NewService(dependencies Dependencies) *Service {
	clock := dependencies.Clock
	if clock == nil {
		clock = systemClock{}
	}
	return &Service{writer: dependencies.Writer, clock: clock}
}

func (service *Service) ready(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if service == nil || service.writer == nil || service.clock == nil {
		return ErrUnavailable
	}
	return service.writer.Check(ctx)
}

func (service *Service) timestamp(after ...string) string {
	next := service.clock.Now().UTC().Truncate(time.Millisecond)
	for _, value := range after {
		current, err := time.Parse(time.RFC3339Nano, value)
		if err == nil && !next.After(current) {
			next = current.Add(time.Millisecond)
		}
	}
	return next.UTC().Format("2006-01-02T15:04:05.000Z")
}

func mapError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, repository.ErrVersionConflict) || errors.Is(err, repository.ErrAutomationConflict) ||
		errors.Is(err, repository.ErrDuplicate) {
		return ErrStateConflict
	}
	if errors.Is(err, repository.ErrInvalidAutomation) || errors.Is(err, domainautomation.ErrInvalidScript) ||
		errors.Is(err, domainautomation.ErrInvalidExecutor) || errors.Is(err, domainautomation.ErrInvalidOrder) {
		return ErrInvalidCommand
	}
	return err
}

// Direct runtime methods are explicit negative compatibility capabilities.
// RuntimeHandler is the P002 bridge entry and is the only route that can pass
// typed execution intent to the supervised runtime dispatcher.
func (service *Service) RunScript(context.Context, int64, int64) error   { return ErrRuntimeDisabled }
func (service *Service) StopScript(context.Context, int64, int64) error  { return ErrRuntimeDisabled }
func (service *Service) RunExecutor(context.Context, int64, int64) error { return ErrRuntimeDisabled }
func (service *Service) StopExecutor(context.Context, int64, int64) error {
	return ErrRuntimeDisabled
}
func (service *Service) RunExecutorAction(context.Context, int64, int64, string) error {
	return ErrRuntimeDisabled
}
