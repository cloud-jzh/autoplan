package projects

import (
	"context"

	"github.com/lyming99/autoplan/backend/internal/domain/contracts"
	domainproject "github.com/lyming99/autoplan/backend/internal/domain/project"
)

func (service *Service) List(ctx context.Context, visibility domainproject.Visibility) ([]contracts.Project, error) {
	if err := service.ready(ctx); err != nil {
		return nil, err
	}
	return service.assembler.List(ctx, visibility)
}

func (service *Service) Get(ctx context.Context, projectID int64, visibility domainproject.Visibility) (contracts.Project, error) {
	if err := service.ready(ctx); err != nil {
		return contracts.Project{}, err
	}
	return service.assembler.Get(ctx, projectID, visibility)
}

// Snapshot returns the full compatibility shape. A nil project id is the
// project-list snapshot; an explicit missing id remains a domain error.
func (service *Service) Snapshot(
	ctx context.Context,
	projectID *int64,
	visibility domainproject.Visibility,
) (contracts.AppSnapshot, error) {
	if err := service.ready(ctx); err != nil {
		return contracts.AppSnapshot{}, err
	}
	return service.assembler.Assemble(ctx, projectID, visibility)
}
