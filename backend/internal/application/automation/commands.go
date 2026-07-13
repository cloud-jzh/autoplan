package automation

import (
	"context"

	domainautomation "github.com/lyming99/autoplan/backend/internal/domain/automation"
	"github.com/lyming99/autoplan/backend/internal/repository"
)

type ListQuery struct {
	ProjectID int64
	Limit     int
	Offset    int
}

type CreateScriptCommand struct {
	ProjectID int64
	Input     domainautomation.ScriptInput
}

type UpdateScriptCommand struct {
	ProjectID       int64
	ScriptID        int64
	Input           domainautomation.ScriptInput
	ExpectedVersion int64
}

type CreateExecutorCommand struct {
	ProjectID int64
	Input     domainautomation.ExecutorInput
}

type UpdateExecutorCommand struct {
	ProjectID       int64
	ExecutorID      int64
	Input           domainautomation.ExecutorInput
	ExpectedVersion int64
}

func (service *Service) ListScripts(ctx context.Context, query ListQuery) ([]ScriptDTO, error) {
	if err := service.ready(ctx); err != nil {
		return nil, err
	}
	if query.ProjectID <= 0 || query.Offset < 0 {
		return nil, ErrInvalidCommand
	}
	var result []ScriptDTO
	err := service.writer.TransactAutomation(ctx, func(transaction repository.AutomationWriteTransaction) error {
		records, err := transaction.ListScripts(ctx, domainautomation.ListOptions{ProjectID: query.ProjectID, Limit: query.Limit, Offset: query.Offset})
		if err != nil {
			return err
		}
		result = make([]ScriptDTO, 0, len(records))
		for _, record := range records {
			result = append(result, scriptDTO(record))
		}
		return nil
	})
	return result, mapError(err)
}

func (service *Service) GetScript(ctx context.Context, projectID, scriptID int64) (ScriptDTO, error) {
	if err := service.ready(ctx); err != nil {
		return ScriptDTO{}, err
	}
	if projectID <= 0 || scriptID <= 0 {
		return ScriptDTO{}, ErrInvalidCommand
	}
	var result ScriptDTO
	err := service.writer.TransactAutomation(ctx, func(transaction repository.AutomationWriteTransaction) error {
		record, found, err := transaction.GetScript(ctx, projectID, scriptID)
		if err != nil {
			return err
		}
		if !found {
			return repository.ErrNotFound
		}
		result = scriptDTO(record)
		return nil
	})
	return result, mapError(err)
}

func (service *Service) CreateScript(ctx context.Context, command CreateScriptCommand) (ScriptDTO, error) {
	if err := service.ready(ctx); err != nil {
		return ScriptDTO{}, err
	}
	if command.ProjectID <= 0 {
		return ScriptDTO{}, ErrInvalidCommand
	}
	var result ScriptDTO
	err := service.writer.TransactAutomation(ctx, func(transaction repository.AutomationWriteTransaction) error {
		record, err := transaction.CreateScript(ctx, domainautomation.ScriptCreate{ProjectID: command.ProjectID, Input: command.Input, CreatedAt: service.timestamp()})
		if err == nil {
			result = scriptDTO(record)
		}
		return err
	})
	return result, mapError(err)
}

func (service *Service) UpdateScript(ctx context.Context, command UpdateScriptCommand) (ScriptDTO, error) {
	if err := service.ready(ctx); err != nil {
		return ScriptDTO{}, err
	}
	if command.ProjectID <= 0 || command.ScriptID <= 0 || command.ExpectedVersion <= 0 {
		return ScriptDTO{}, ErrInvalidCommand
	}
	var result ScriptDTO
	err := service.writer.TransactAutomation(ctx, func(transaction repository.AutomationWriteTransaction) error {
		current, found, err := transaction.GetScript(ctx, command.ProjectID, command.ScriptID)
		if err != nil || !found {
			if err != nil {
				return err
			}
			return repository.ErrNotFound
		}
		record, err := transaction.UpdateScript(ctx, domainautomation.ScriptUpdate{ProjectID: command.ProjectID, ScriptID: command.ScriptID, Input: command.Input, ExpectedVersion: command.ExpectedVersion, UpdatedAt: service.timestamp(current.UpdatedAt)})
		if err == nil {
			result = scriptDTO(record)
		}
		return err
	})
	return result, mapError(err)
}

func (service *Service) DeleteScript(ctx context.Context, projectID, scriptID, expectedVersion int64) (ScriptDTO, error) {
	if err := service.ready(ctx); err != nil {
		return ScriptDTO{}, err
	}
	if projectID <= 0 || scriptID <= 0 || expectedVersion <= 0 {
		return ScriptDTO{}, ErrInvalidCommand
	}
	var result ScriptDTO
	err := service.writer.TransactAutomation(ctx, func(transaction repository.AutomationWriteTransaction) error {
		record, err := transaction.DeleteScript(ctx, domainautomation.Delete{ProjectID: projectID, ID: scriptID, ExpectedVersion: expectedVersion})
		if err == nil {
			result = scriptDTO(record)
		}
		return err
	})
	return result, mapError(err)
}

func (service *Service) ToggleScript(ctx context.Context, projectID, scriptID, expectedVersion int64) (ScriptDTO, error) {
	if err := service.ready(ctx); err != nil {
		return ScriptDTO{}, err
	}
	if projectID <= 0 || scriptID <= 0 || expectedVersion <= 0 {
		return ScriptDTO{}, ErrInvalidCommand
	}
	var result ScriptDTO
	err := service.writer.TransactAutomation(ctx, func(transaction repository.AutomationWriteTransaction) error {
		current, found, err := transaction.GetScript(ctx, projectID, scriptID)
		if err != nil || !found {
			if err != nil {
				return err
			}
			return repository.ErrNotFound
		}
		record, err := transaction.ToggleScript(ctx, domainautomation.Toggle{ProjectID: projectID, ID: scriptID, ExpectedVersion: expectedVersion, UpdatedAt: service.timestamp(current.UpdatedAt)})
		if err == nil {
			result = scriptDTO(record)
		}
		return err
	})
	return result, mapError(err)
}
