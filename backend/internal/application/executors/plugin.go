package executors

import (
	"errors"

	applicationoperations "github.com/lyming99/autoplan/backend/internal/application/operations"
	"github.com/lyming99/autoplan/backend/internal/runtime/scheduler"
)

// plugin.go contains the lifecycle policy shared by Run, RunAction and Stop.
// The process itself is never exposed: P11 runner cancellation reaps the
// registered tree, and reload input actions fail closed because the runner
// intentionally has no writeable child-stdin escape hatch.
func (service *Service) pluginActive(projectID, executorID int64) bool {
	active, found := service.activeFor(projectID, executorID, "")
	return found && active.plugin
}

func schedulerError(err error) error {
	switch {
	case errors.Is(err, scheduler.ErrActorQueueFull), errors.Is(err, scheduler.ErrWorkerQueueFull):
		return ErrQueueFull
	case errors.Is(err, scheduler.ErrManagerClosed), errors.Is(err, scheduler.ErrActorClosed), errors.Is(err, scheduler.ErrWorkerPoolClosed):
		return ErrUnavailable
	default:
		return ErrStateConflict
	}
}

func operationError(err error) error {
	switch {
	case errors.Is(err, applicationoperations.ErrUnavailable), errors.Is(err, applicationoperations.ErrHandlerUnavailable):
		return ErrUnavailable
	case errors.Is(err, applicationoperations.ErrUnauthorized):
		return ErrUnauthorized
	case errors.Is(err, applicationoperations.ErrNotFound):
		return ErrNotFound
	case errors.Is(err, applicationoperations.ErrIdempotencyConflict), errors.Is(err, applicationoperations.ErrStateConflict), errors.Is(err, applicationoperations.ErrVersionConflict):
		return ErrStateConflict
	default:
		return ErrInvalidCommand
	}
}
