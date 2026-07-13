package intake

import (
	"context"

	"github.com/lyming99/autoplan/backend/internal/domain/contracts"
	domainintake "github.com/lyming99/autoplan/backend/internal/domain/intake"
	domainproject "github.com/lyming99/autoplan/backend/internal/domain/project"
	"github.com/lyming99/autoplan/backend/internal/repository"
)

type ListQuery struct {
	ProjectID int64
	Type      domainintake.Type
	Status    *domainintake.Status
	Limit     int
	Offset    int
}

func (service *Service) List(ctx context.Context, query ListQuery) ([]IntakeDTO, error) {
	if err := service.ready(ctx); err != nil {
		return nil, err
	}
	if query.ProjectID <= 0 || !query.Type.Valid() || query.Offset < 0 ||
		(query.Status != nil && !query.Status.Valid()) {
		return nil, ErrInvalidCommand
	}
	if query.Limit <= 0 {
		query.Limit = 100
	} else if query.Limit > 200 {
		query.Limit = 200
	}
	var result []IntakeDTO
	err := service.writer.TransactIntake(ctx, func(transaction repository.IntakeWriteTransaction) error {
		records, err := transaction.ListIntakes(ctx, domainintake.ListOptions{
			ProjectID: query.ProjectID, Type: query.Type, Status: query.Status,
			Limit: query.Limit, Offset: query.Offset,
		})
		if err != nil {
			return err
		}
		result = make([]IntakeDTO, 0, len(records))
		for _, record := range records {
			links, linkErr := transaction.ListPlanLinksForIntake(ctx, record.ProjectID, record.Type, record.ID)
			if linkErr != nil {
				return linkErr
			}
			result = append(result, intakeDTO(record, links))
		}
		return nil
	})
	return result, err
}

func (service *Service) Get(ctx context.Context, projectID int64, intakeType domainintake.Type, intakeID int64) (IntakeDTO, error) {
	if err := service.ready(ctx); err != nil {
		return IntakeDTO{}, err
	}
	if projectID <= 0 || intakeID <= 0 || !intakeType.Valid() {
		return IntakeDTO{}, ErrInvalidCommand
	}
	var result IntakeDTO
	err := service.writer.TransactIntake(ctx, func(transaction repository.IntakeWriteTransaction) error {
		record, found, err := transaction.GetIntake(ctx, projectID, intakeType, intakeID)
		if err != nil {
			return err
		}
		if !found {
			return repository.ErrNotFound
		}
		links, err := transaction.ListPlanLinksForIntake(ctx, projectID, intakeType, intakeID)
		if err != nil {
			return err
		}
		result = intakeDTO(record, links)
		return nil
	})
	return result, err
}

func (service *Service) Snapshot(
	ctx context.Context,
	projectID int64,
	visibility domainproject.Visibility,
) (contracts.AppSnapshot, error) {
	if err := service.ready(ctx); err != nil {
		return contracts.AppSnapshot{}, err
	}
	if projectID <= 0 {
		return contracts.AppSnapshot{}, ErrInvalidCommand
	}
	return service.assembler.Assemble(ctx, &projectID, visibility)
}
