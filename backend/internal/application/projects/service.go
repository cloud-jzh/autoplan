// Package projects provides shared Project queries and mutations for every transport.
package projects

import (
	"context"
	"time"

	applicationidempotency "github.com/lyming99/autoplan/backend/internal/application/idempotency"
	applicationsnapshot "github.com/lyming99/autoplan/backend/internal/application/snapshot"
	domainproject "github.com/lyming99/autoplan/backend/internal/domain/project"
	"github.com/lyming99/autoplan/backend/internal/repository"
)

type Clock interface{ Now() time.Time }

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now().UTC() }

type Dependencies struct {
	Assembler   *applicationsnapshot.Assembler
	Writer      repository.Transactional
	Idempotency *applicationidempotency.Service
	Clock       Clock
}

type Service struct {
	assembler   *applicationsnapshot.Assembler
	writer      repository.Transactional
	idempotency *applicationidempotency.Service
	clock       Clock
}

// NewService preserves the P03 read-only constructor.
func NewService(store repository.ReadOnly) *Service {
	return NewServiceWithDependencies(Dependencies{
		Assembler: applicationsnapshot.New(applicationsnapshot.DirectReader(store)),
		Clock:     systemClock{},
	})
}

func NewServiceWithDependencies(dependencies Dependencies) *Service {
	clock := dependencies.Clock
	if clock == nil {
		clock = systemClock{}
	}
	return &Service{
		assembler: dependencies.Assembler, writer: dependencies.Writer,
		idempotency: dependencies.Idempotency, clock: clock,
	}
}

func (service *Service) ready(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if service == nil || service.assembler == nil {
		return domainproject.ErrUnavailable
	}
	return nil
}

func (service *Service) readyMutation(ctx context.Context) error {
	if err := service.ready(ctx); err != nil {
		return err
	}
	if service.writer == nil || service.idempotency == nil || service.clock == nil {
		return domainproject.ErrUnavailable
	}
	return service.writer.Check(ctx)
}

func (service *Service) timestamp() string {
	return service.clock.Now().UTC().Format("2006-01-02T15:04:05.000Z")
}
