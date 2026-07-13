package attachments

import (
	"context"
	"errors"
	"io/fs"

	domainfiles "github.com/lyming99/autoplan/backend/internal/domain/files"
	"github.com/lyming99/autoplan/backend/internal/repository"
)

type RecoveryReport struct {
	Recovered int64 `json:"recovered"`
	Complete  int64 `json:"complete"`
	Pending   int64 `json:"pending"`
	Failed    int64 `json:"failed"`
}

// Recover is deliberately all-or-report: callers must keep readiness closed
// when Pending is non-zero instead of silently accepting an unknown file state.
func (service *Service) Recover(ctx context.Context) (RecoveryReport, error) {
	if err := service.ready(ctx); err != nil {
		return RecoveryReport{}, err
	}
	var projects []repository.Project
	err := service.transact(ctx, func(transaction attachmentTransaction) error {
		var listErr error
		projects, listErr = transaction.ListProjects(ctx)
		return listErr
	})
	if err != nil {
		return RecoveryReport{}, err
	}
	report := RecoveryReport{}
	for _, project := range projects {
		operations, listErr := service.operations(ctx, project.ID, false)
		if listErr != nil {
			return report, listErr
		}
		for _, operation := range operations {
			before := operation.State
			after, recoverErr := service.recoverOperation(ctx, operation)
			if recoverErr != nil || after.State != domainfiles.StateComplete && after.State != domainfiles.StateReady {
				report.Pending++
				if after.State == domainfiles.StateFailed {
					report.Failed++
				}
				continue
			}
			if before != after.State {
				report.Recovered++
			}
			if after.State == domainfiles.StateComplete || after.State == domainfiles.StateReady {
				report.Complete++
			}
		}
	}
	if report.Pending != 0 {
		return report, domainfiles.ErrAttachmentRecovery
	}
	return report, nil
}

func (service *Service) recoverOperation(ctx context.Context, operation domainfiles.AttachmentOperation) (domainfiles.AttachmentOperation, error) {
	if operation.Kind == domainfiles.OperationUpload {
		return service.recoverUpload(ctx, operation)
	}
	if operation.Kind == domainfiles.OperationDelete {
		return service.recoverDelete(ctx, operation)
	}
	return operation, domainfiles.ErrAttachmentRecovery
}

func (service *Service) recoverUpload(ctx context.Context, operation domainfiles.AttachmentOperation) (domainfiles.AttachmentOperation, error) {
	switch operation.State {
	case domainfiles.StateReady:
		return service.verifyReady(ctx, operation)
	case domainfiles.StateFailed:
		if operation.AttachmentID == nil {
			if operation.StageKey == "" {
				return service.markFailed(ctx, operation)
			}
			if err := service.store.Remove(ctx, operation.StageKey); err != nil {
				return service.markFailed(ctx, operation)
			}
			operation.State = domainfiles.StateComplete
			operation.UpdatedAt = service.timestamp()
			if err := service.updateOperation(ctx, operation); err != nil {
				return operation, err
			}
			return operation, nil
		}
		fallthrough
	case domainfiles.StateStaged:
		if operation.StageKey == "" || operation.StoredKey == "" {
			return service.markFailed(ctx, operation)
		}
		if err := service.store.Promote(ctx, operation.StageKey, operation.StoredKey); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				if _, _, inspectErr := service.store.Inspect(ctx, operation.StoredKey); inspectErr != nil {
					return service.markFailed(ctx, operation)
				}
			} else {
				return service.markFailed(ctx, operation)
			}
		}
		operation.State = domainfiles.StateReady
		operation.UpdatedAt = service.timestamp()
		if err := service.updateOperation(ctx, operation); err != nil {
			return operation, err
		}
		return service.verifyReady(ctx, operation)
	default:
		return operation, domainfiles.ErrAttachmentRecovery
	}
}

func (service *Service) verifyReady(ctx context.Context, operation domainfiles.AttachmentOperation) (domainfiles.AttachmentOperation, error) {
	file, digest, err := service.store.Inspect(ctx, operation.StoredKey)
	if err != nil || file.Size != operation.Size || (operation.SHA256 != "" && digest != operation.SHA256) {
		return service.markFailed(ctx, operation)
	}
	return operation, nil
}

func (service *Service) recoverDelete(ctx context.Context, operation domainfiles.AttachmentOperation) (domainfiles.AttachmentOperation, error) {
	switch operation.State {
	case domainfiles.StateComplete:
		return operation, nil
	case domainfiles.StateDeleting, domainfiles.StateFailed:
		if operation.StoredKey == "" || operation.QuarantineKey == "" {
			return service.markFailed(ctx, operation)
		}
		if err := service.store.Quarantine(ctx, operation.StoredKey, operation.QuarantineKey); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return service.markFailed(ctx, operation)
		}
		operation.State = domainfiles.StateQuarantined
		operation.UpdatedAt = service.timestamp()
		if err := service.updateOperation(ctx, operation); err != nil {
			return operation, err
		}
		fallthrough
	case domainfiles.StateQuarantined:
		if operation.QuarantineKey == "" {
			return service.markFailed(ctx, operation)
		}
		if err := service.store.Remove(ctx, operation.QuarantineKey); err != nil {
			return service.markFailed(ctx, operation)
		}
		operation.State = domainfiles.StateComplete
		operation.UpdatedAt = service.timestamp()
		if err := service.updateOperation(ctx, operation); err != nil {
			return operation, err
		}
		return operation, nil
	default:
		return operation, domainfiles.ErrAttachmentRecovery
	}
}

func (service *Service) markFailed(ctx context.Context, operation domainfiles.AttachmentOperation) (domainfiles.AttachmentOperation, error) {
	operation.State = domainfiles.StateFailed
	operation.UpdatedAt = service.timestamp()
	if err := service.updateOperation(ctx, operation); err != nil {
		return operation, err
	}
	return operation, domainfiles.ErrAttachmentRecovery
}

func (service *Service) operations(ctx context.Context, projectID int64, includeComplete bool) ([]domainfiles.AttachmentOperation, error) {
	var result []domainfiles.AttachmentOperation
	err := service.transact(ctx, func(transaction attachmentTransaction) error {
		var listErr error
		result, listErr = transaction.ListAttachmentOperations(ctx, projectID, includeComplete)
		return listErr
	})
	return result, err
}
