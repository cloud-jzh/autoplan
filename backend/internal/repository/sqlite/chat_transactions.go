package sqlite

import (
	"context"

	"github.com/lyming99/autoplan/backend/internal/repository"
)

// TransactChat serializes P003 static chat/config mutations with the existing
// owner and rollback guard. It intentionally exposes no runtime or SQL escape.
func (writer *Writer) TransactChat(ctx context.Context, operation func(repository.ChatWriteTransaction) error) error {
	if operation == nil {
		return repository.ErrTransaction
	}
	return writer.Transact(ctx, func(transaction repository.WriteTransaction) error {
		chat, ok := transaction.(repository.ChatWriteTransaction)
		if !ok {
			return repository.ErrTransaction
		}
		return operation(chat)
	})
}

var _ repository.ChatTransactional = (*Writer)(nil)
var _ repository.ChatWriteTransaction = (*writeTransaction)(nil)
