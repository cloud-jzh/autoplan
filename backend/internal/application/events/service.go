// Package events provides the shared, project-isolated audit-event query use case.
package events

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/lyming99/autoplan/backend/internal/domain/contracts"
	domainevent "github.com/lyming99/autoplan/backend/internal/domain/event"
	"github.com/lyming99/autoplan/backend/internal/repository"
)

var (
	ErrUnavailable    = errors.New("event application service unavailable")
	ErrInvalidCommand = errors.New("event query is invalid")
)

type ListQuery struct {
	ProjectID int64
	Limit     int
	Offset    int
}

type EventDTO struct {
	ID        int64  `json:"id"`
	ProjectID int64  `json:"project_id"`
	Type      string `json:"type"`
	Message   string `json:"message"`
	Meta      any    `json:"meta"`
	CreatedAt string `json:"created_at"`
}

type Service struct{ writer repository.PlanTransactional }

func NewService(writer repository.PlanTransactional) *Service { return &Service{writer: writer} }

func (service *Service) ready(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if service == nil || service.writer == nil {
		return ErrUnavailable
	}
	return service.writer.Check(ctx)
}

func (service *Service) List(ctx context.Context, query ListQuery) ([]EventDTO, error) {
	if err := service.ready(ctx); err != nil {
		return nil, err
	}
	if query.ProjectID <= 0 || query.Offset < 0 {
		return nil, ErrInvalidCommand
	}
	if query.Limit <= 0 {
		query.Limit = 80
	} else if query.Limit > 200 {
		query.Limit = 200
	}
	var result []EventDTO
	err := service.writer.TransactPlans(ctx, func(transaction repository.PlanWriteTransaction) error {
		records, err := transaction.ListEvents(ctx, domainevent.ListOptions{
			ProjectID: query.ProjectID, Limit: query.Limit, Offset: query.Offset,
		})
		if err != nil {
			return err
		}
		result = make([]EventDTO, 0, len(records))
		for _, record := range records {
			mapped, mapErr := eventDTO(record)
			if mapErr != nil {
				return mapErr
			}
			result = append(result, mapped)
		}
		return nil
	})
	return result, err
}

func EventSnapshot(value domainevent.Event) (contracts.SanitizedObject, error) {
	mapped, err := eventDTO(value)
	if err != nil {
		return nil, err
	}
	return sanitize(map[string]any{
		"id": mapped.ID, "project_id": mapped.ProjectID, "type": mapped.Type,
		"message": mapped.Message, "meta": mapped.Meta, "created_at": mapped.CreatedAt,
	})
}

func eventDTO(value domainevent.Event) (EventDTO, error) {
	var metadata any
	if value.MetaJSON != nil {
		if err := json.Unmarshal([]byte(*value.MetaJSON), &metadata); err != nil {
			return EventDTO{}, ErrInvalidCommand
		}
	}
	return EventDTO{
		ID: value.ID, ProjectID: value.ProjectID, Type: value.Type,
		Message: value.Message, Meta: metadata, CreatedAt: value.CreatedAt,
	}, nil
}

func sanitize(fields map[string]any) (contracts.SanitizedObject, error) {
	result := make(contracts.SanitizedObject, len(fields))
	for name, value := range fields {
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil, ErrInvalidCommand
		}
		result[name] = encoded
	}
	if result.Validate() != nil {
		return nil, ErrInvalidCommand
	}
	return result, nil
}
