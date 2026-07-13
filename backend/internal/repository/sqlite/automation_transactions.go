package sqlite

import (
	"context"

	"github.com/lyming99/autoplan/backend/internal/repository"
)

// TransactAutomation keeps every P002 metadata mutation inside the writer's
// owner-checked serializable transaction. It exposes no arbitrary SQL handle.
func (writer *Writer) TransactAutomation(
	ctx context.Context,
	operation func(repository.AutomationWriteTransaction) error,
) error {
	if operation == nil {
		return repository.ErrTransaction
	}
	return writer.Transact(ctx, func(transaction repository.WriteTransaction) error {
		automation, ok := transaction.(repository.AutomationWriteTransaction)
		if !ok {
			return repository.ErrTransaction
		}
		return operation(automation)
	})
}

var _ repository.AutomationTransactional = (*Writer)(nil)
var _ repository.AutomationWriteTransaction = (*writeTransaction)(nil)
