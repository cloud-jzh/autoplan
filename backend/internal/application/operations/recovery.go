package operations

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"

	domainoperation "github.com/lyming99/autoplan/backend/internal/domain/operation"
	"github.com/lyming99/autoplan/backend/internal/repository"
)

type RecoveryResult struct {
	OperationID string                 `json:"operation_id"`
	ProjectID   int64                  `json:"project_id"`
	Status      domainoperation.Status `json:"status"`
	Changed     bool                   `json:"changed"`
	Code        string                 `json:"code,omitempty"`
}

// Recover applies startup recovery before any runner or transport begins
// accepting work. It has no execution callback: registered handlers only
// prove a queued Operation can be claimed, and Claim commits that ownership
// before a separate runner can perform a side effect.
func (service *Service) Recover(ctx context.Context) ([]RecoveryResult, error) {
	if err := service.ready(ctx); err != nil {
		return nil, err
	}
	var projects []repository.Project
	var err error
	if service.projects != nil {
		projects, err = service.projects.ListProjects(ctx)
	} else {
		projects, err = service.store.ListProjects(ctx)
	}
	if err != nil {
		return nil, err
	}
	projectIDs := sortProjectIDs(projects)
	if len(projectIDs) == 0 {
		return []RecoveryResult{}, nil
	}
	now := service.clock.Now().UTC()
	results := make([]RecoveryResult, 0)
	claims := make([]struct {
		handler   RecoveryHandler
		operation domainoperation.Operation
	}, 0)
	err = service.store.Transact(ctx, func(transaction Transaction) error {
		for _, projectID := range projectIDs {
			items, listErr := transaction.List(ctx, ListQuery{ProjectID: projectID, Limit: 200})
			if listErr != nil {
				return listErr
			}
			for _, item := range items {
				decision, decisionErr := domainoperation.DecideRecovery(item.Status, item.CreatedAt, now, service.queuedRecoveryMaxAge)
				if decisionErr != nil {
					return ErrStateConflict
				}
				if decision.Action == domainoperation.RecoveryNone {
					continue
				}
				if decision.Action == domainoperation.RecoveryInterrupt {
					updated, changed, interruptErr := transaction.Transition(ctx, Transition{
						ProjectID: item.ProjectID, OperationID: item.OperationID, ExpectedVersion: item.Version,
						Target: domainoperation.StatusInterrupted, RequestID: recoveryRequestID(item.OperationID),
						UpdatedAt: nextTimestamp(service.clock, item.UpdatedAt),
						Error:     &domainoperation.ErrorSummary{Code: decision.Code, Summary: "Operation was interrupted during startup recovery."},
						Payload:   statusPayload(domainoperation.StatusInterrupted),
					})
					if interruptErr != nil {
						return interruptErr
					}
					results = append(results, RecoveryResult{OperationID: updated.OperationID, ProjectID: updated.ProjectID, Status: updated.Status, Changed: changed, Code: decision.Code})
					continue
				}

				handler := service.handlers[item.Type]
				claimable, handlerErr := safeCanRecover(ctx, handler, item)
				if handlerErr != nil || !claimable {
					updated, changed, interruptErr := transaction.Transition(ctx, Transition{
						ProjectID: item.ProjectID, OperationID: item.OperationID, ExpectedVersion: item.Version,
						Target: domainoperation.StatusInterrupted, RequestID: recoveryRequestID(item.OperationID),
						UpdatedAt: nextTimestamp(service.clock, item.UpdatedAt),
						Error:     &domainoperation.ErrorSummary{Code: "RECOVERY_UNCLAIMED", Summary: "Operation could not be safely claimed during recovery."},
						Payload:   statusPayload(domainoperation.StatusInterrupted),
					})
					if interruptErr != nil {
						return interruptErr
					}
					results = append(results, RecoveryResult{OperationID: updated.OperationID, ProjectID: updated.ProjectID, Status: updated.Status, Changed: changed, Code: "RECOVERY_UNCLAIMED"})
					continue
				}
				updated, changed, claimErr := transaction.Transition(ctx, Transition{
					ProjectID: item.ProjectID, OperationID: item.OperationID, ExpectedVersion: item.Version,
					Target: domainoperation.StatusRunning, RequestID: recoveryRequestID(item.OperationID),
					UpdatedAt: nextTimestamp(service.clock, item.UpdatedAt), Payload: statusPayload(domainoperation.StatusRunning),
				})
				if claimErr != nil {
					return claimErr
				}
				results = append(results, RecoveryResult{OperationID: updated.OperationID, ProjectID: updated.ProjectID, Status: updated.Status, Changed: changed})
				if changed {
					claims = append(claims, struct {
						handler   RecoveryHandler
						operation domainoperation.Operation
					}{handler: handler, operation: item})
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, mapStoreError(err)
	}
	for _, claim := range claims {
		if observer, ok := claim.handler.(RecoveryClaimObserver); ok {
			observer.RecoveryClaimed(ctx, claim.operation)
		}
	}
	return results, nil
}

func safeCanRecover(ctx context.Context, handler RecoveryHandler, operation domainoperation.Operation) (claimable bool, err error) {
	if handler == nil || handler.Type() != operation.Type {
		return false, nil
	}
	defer func() {
		if recover() != nil {
			claimable, err = false, errors.New("recovery handler failed")
		}
	}()
	return handler.CanRecover(ctx, operation)
}

func recoveryRequestID(operationID string) string {
	// Operation IDs are opaque and can be longer than request_id. The bounded
	// hexadecimal digest is stable, non-sensitive, and compatible with DTOs.
	sum := sha256.Sum256([]byte(operationID))
	return fmt.Sprintf("recovery-%x", sum[:20])
}
