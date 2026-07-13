package automation

import (
	"context"

	domainautomation "github.com/lyming99/autoplan/backend/internal/domain/automation"
	"github.com/lyming99/autoplan/backend/internal/repository"
)

type ReorderCommand struct {
	ProjectID       int64
	IDs             []int64
	ExpectedVersion map[int64]int64
}

type ImportExecutorsCommand struct {
	ProjectID    int64
	Items        []domainautomation.ExecutorInput
	DedupeLabels *bool
}

func (service *Service) ListExecutors(ctx context.Context, query ListQuery) ([]ExecutorDTO, error) {
	if err := service.ready(ctx); err != nil {
		return nil, err
	}
	if query.ProjectID <= 0 || query.Offset < 0 {
		return nil, ErrInvalidCommand
	}
	var result []ExecutorDTO
	err := service.writer.TransactAutomation(ctx, func(transaction repository.AutomationWriteTransaction) error {
		records, err := transaction.ListExecutors(ctx, domainautomation.ListOptions{ProjectID: query.ProjectID, Limit: query.Limit, Offset: query.Offset})
		if err != nil {
			return err
		}
		result = make([]ExecutorDTO, 0, len(records))
		for _, record := range records {
			result = append(result, executorDTO(record))
		}
		return nil
	})
	return result, mapError(err)
}

func (service *Service) GetExecutor(ctx context.Context, projectID, executorID int64) (ExecutorDTO, error) {
	if err := service.ready(ctx); err != nil {
		return ExecutorDTO{}, err
	}
	if projectID <= 0 || executorID <= 0 {
		return ExecutorDTO{}, ErrInvalidCommand
	}
	var result ExecutorDTO
	err := service.writer.TransactAutomation(ctx, func(transaction repository.AutomationWriteTransaction) error {
		record, found, err := transaction.GetExecutor(ctx, projectID, executorID)
		if err != nil {
			return err
		}
		if !found {
			return repository.ErrNotFound
		}
		result = executorDTO(record)
		return nil
	})
	return result, mapError(err)
}

func (service *Service) CreateExecutor(ctx context.Context, command CreateExecutorCommand) (ExecutorDTO, error) {
	if err := service.ready(ctx); err != nil {
		return ExecutorDTO{}, err
	}
	if command.ProjectID <= 0 {
		return ExecutorDTO{}, ErrInvalidCommand
	}
	var result ExecutorDTO
	err := service.writer.TransactAutomation(ctx, func(transaction repository.AutomationWriteTransaction) error {
		record, err := transaction.CreateExecutor(ctx, domainautomation.ExecutorCreate{ProjectID: command.ProjectID, Input: command.Input, CreatedAt: service.timestamp()})
		if err == nil {
			result = executorDTO(record)
		}
		return err
	})
	return result, mapError(err)
}

func (service *Service) UpdateExecutor(ctx context.Context, command UpdateExecutorCommand) (ExecutorDTO, error) {
	if err := service.ready(ctx); err != nil {
		return ExecutorDTO{}, err
	}
	if command.ProjectID <= 0 || command.ExecutorID <= 0 || command.ExpectedVersion <= 0 {
		return ExecutorDTO{}, ErrInvalidCommand
	}
	var result ExecutorDTO
	err := service.writer.TransactAutomation(ctx, func(transaction repository.AutomationWriteTransaction) error {
		current, found, err := transaction.GetExecutor(ctx, command.ProjectID, command.ExecutorID)
		if err != nil || !found {
			if err != nil {
				return err
			}
			return repository.ErrNotFound
		}
		record, err := transaction.UpdateExecutor(ctx, domainautomation.ExecutorUpdate{ProjectID: command.ProjectID, ExecutorID: command.ExecutorID, Input: command.Input, ExpectedVersion: command.ExpectedVersion, UpdatedAt: service.timestamp(current.UpdatedAt)})
		if err == nil {
			result = executorDTO(record)
		}
		return err
	})
	return result, mapError(err)
}

func (service *Service) DeleteExecutor(ctx context.Context, projectID, executorID, expectedVersion int64) (ExecutorDTO, error) {
	if err := service.ready(ctx); err != nil {
		return ExecutorDTO{}, err
	}
	if projectID <= 0 || executorID <= 0 || expectedVersion <= 0 {
		return ExecutorDTO{}, ErrInvalidCommand
	}
	var result ExecutorDTO
	err := service.writer.TransactAutomation(ctx, func(transaction repository.AutomationWriteTransaction) error {
		record, err := transaction.DeleteExecutor(ctx, domainautomation.Delete{ProjectID: projectID, ID: executorID, ExpectedVersion: expectedVersion})
		if err == nil {
			result = executorDTO(record)
		}
		return err
	})
	return result, mapError(err)
}

func (service *Service) ToggleExecutor(ctx context.Context, projectID, executorID, expectedVersion int64) (ExecutorDTO, error) {
	if err := service.ready(ctx); err != nil {
		return ExecutorDTO{}, err
	}
	if projectID <= 0 || executorID <= 0 || expectedVersion <= 0 {
		return ExecutorDTO{}, ErrInvalidCommand
	}
	var result ExecutorDTO
	err := service.writer.TransactAutomation(ctx, func(transaction repository.AutomationWriteTransaction) error {
		current, found, err := transaction.GetExecutor(ctx, projectID, executorID)
		if err != nil || !found {
			if err != nil {
				return err
			}
			return repository.ErrNotFound
		}
		record, err := transaction.ToggleExecutor(ctx, domainautomation.Toggle{ProjectID: projectID, ID: executorID, ExpectedVersion: expectedVersion, UpdatedAt: service.timestamp(current.UpdatedAt)})
		if err == nil {
			result = executorDTO(record)
		}
		return err
	})
	return result, mapError(err)
}

func (service *Service) ReorderScripts(ctx context.Context, command ReorderCommand) ([]ScriptDTO, error) {
	if err := service.ready(ctx); err != nil {
		return nil, err
	}
	if command.ProjectID <= 0 || len(command.IDs) == 0 || len(command.IDs) != len(command.ExpectedVersion) {
		return nil, ErrInvalidCommand
	}
	var result []ScriptDTO
	err := service.writer.TransactAutomation(ctx, func(transaction repository.AutomationWriteTransaction) error {
		times := make([]string, 0, len(command.IDs))
		for _, id := range command.IDs {
			record, found, err := transaction.GetScript(ctx, command.ProjectID, id)
			if err != nil {
				return err
			}
			if !found {
				return repository.ErrNotFound
			}
			times = append(times, record.UpdatedAt)
		}
		updated, err := transaction.ReorderScripts(ctx, domainautomation.Reorder{ProjectID: command.ProjectID, IDs: append([]int64(nil), command.IDs...), ExpectedVersion: copyVersions(command.ExpectedVersion), UpdatedAt: service.timestamp(times...)})
		if err != nil {
			return err
		}
		result = make([]ScriptDTO, 0, len(updated))
		for _, record := range updated {
			result = append(result, scriptDTO(record))
		}
		return nil
	})
	return result, mapError(err)
}

func (service *Service) ReorderExecutors(ctx context.Context, command ReorderCommand) ([]ExecutorDTO, error) {
	if err := service.ready(ctx); err != nil {
		return nil, err
	}
	if command.ProjectID <= 0 || len(command.IDs) == 0 || len(command.IDs) != len(command.ExpectedVersion) {
		return nil, ErrInvalidCommand
	}
	var result []ExecutorDTO
	err := service.writer.TransactAutomation(ctx, func(transaction repository.AutomationWriteTransaction) error {
		times := make([]string, 0, len(command.IDs))
		for _, id := range command.IDs {
			record, found, err := transaction.GetExecutor(ctx, command.ProjectID, id)
			if err != nil {
				return err
			}
			if !found {
				return repository.ErrNotFound
			}
			times = append(times, record.UpdatedAt)
		}
		updated, err := transaction.ReorderExecutors(ctx, domainautomation.Reorder{ProjectID: command.ProjectID, IDs: append([]int64(nil), command.IDs...), ExpectedVersion: copyVersions(command.ExpectedVersion), UpdatedAt: service.timestamp(times...)})
		if err != nil {
			return err
		}
		result = make([]ExecutorDTO, 0, len(updated))
		for _, record := range updated {
			result = append(result, executorDTO(record))
		}
		return nil
	})
	return result, mapError(err)
}

func (service *Service) ImportExecutors(ctx context.Context, command ImportExecutorsCommand) ([]ExecutorDTO, error) {
	if err := service.ready(ctx); err != nil {
		return nil, err
	}
	if command.ProjectID <= 0 || len(command.Items) == 0 {
		return nil, ErrInvalidCommand
	}
	var result []ExecutorDTO
	err := service.writer.TransactAutomation(ctx, func(transaction repository.AutomationWriteTransaction) error {
		records, err := transaction.ImportExecutors(ctx, domainautomation.Import{ProjectID: command.ProjectID, Items: append([]domainautomation.ExecutorInput(nil), command.Items...), DedupeLabels: copyBool(command.DedupeLabels), UpdatedAt: service.timestamp()})
		if err != nil {
			return err
		}
		result = make([]ExecutorDTO, 0, len(records))
		for _, record := range records {
			result = append(result, executorDTO(record))
		}
		return nil
	})
	return result, mapError(err)
}

func copyVersions(values map[int64]int64) map[int64]int64 {
	result := make(map[int64]int64, len(values))
	for id, version := range values {
		result[id] = version
	}
	return result
}

func copyBool(value *bool) *bool {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}
