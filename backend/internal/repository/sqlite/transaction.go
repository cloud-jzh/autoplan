package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"strings"
	"sync"

	"github.com/lyming99/autoplan/backend/internal/repository"
)

type transactionConnection interface {
	BeginTx(context.Context, *sql.TxOptions) (*sql.Tx, error)
}

type WriterOptions struct {
	Connection     *Connection
	Readiness      repository.Readiness
	Owner          repository.DatabaseOwnerProof
	AuthorizedCopy bool
	SchemaVersion  int
}

// Writer serializes top-level write transactions over the already-open,
// policy-configured connection. It never opens a path or owns the database
// owner lock; the bootstrap runtime closes the connection before that lock.
type Writer struct {
	mu             sync.Mutex
	connection     transactionConnection
	readiness      repository.Readiness
	owner          repository.DatabaseOwnerProof
	authorizedCopy bool
	schemaVersion  int
	closed         bool
	faults         transactionFaults
}

type transactionFaults struct {
	afterWrite   func(string) error
	beforeCommit func() error
}

type writeTransaction struct {
	tx     *sql.Tx
	faults *transactionFaults
}

func NewWriter(options WriterOptions) (*Writer, error) {
	if options.Connection == nil {
		return nil, repository.ErrWriterUnauthorized
	}
	return newWriter(options.Connection, options.Readiness, options.Owner, options.AuthorizedCopy, options.SchemaVersion)
}

func newWriter(
	connection transactionConnection,
	readiness repository.Readiness,
	owner repository.DatabaseOwnerProof,
	authorizedCopy bool,
	schemaVersion int,
) (*Writer, error) {
	if !interfacePresent(connection) || !interfacePresent(readiness) || !interfacePresent(owner) || databaseOwnerID(owner) == "" ||
		!authorizedCopy || schemaVersion != SchemaVersion {
		return nil, repository.ErrWriterUnauthorized
	}
	return &Writer{
		connection: connection, readiness: readiness, owner: owner,
		authorizedCopy: authorizedCopy, schemaVersion: schemaVersion,
	}, nil
}

func (writer *Writer) Check(ctx context.Context) error {
	if writer == nil {
		return repository.ErrNotConfigured
	}
	writer.mu.Lock()
	defer writer.mu.Unlock()
	return writer.guard(ctx)
}

func (writer *Writer) guard(ctx context.Context) error {
	if writer.closed {
		return repository.ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !interfacePresent(writer.connection) || !interfacePresent(writer.readiness) || !interfacePresent(writer.owner) ||
		databaseOwnerID(writer.owner) == "" || !writer.authorizedCopy || writer.schemaVersion != SchemaVersion {
		return repository.ErrWriterUnauthorized
	}
	if err := writer.readiness.Check(ctx); err != nil {
		return err
	}
	return ctx.Err()
}

func interfacePresent(value any) bool {
	if value == nil {
		return false
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return !reflected.IsNil()
	default:
		return true
	}
}

func databaseOwnerID(owner repository.DatabaseOwnerProof) (result string) {
	defer func() {
		if recover() != nil {
			result = ""
		}
	}()
	return strings.TrimSpace(owner.DatabaseID())
}

func (writer *Writer) Transact(ctx context.Context, operation func(repository.WriteTransaction) error) error {
	if writer == nil || operation == nil {
		return repository.ErrTransaction
	}
	writer.mu.Lock()
	defer writer.mu.Unlock()
	if err := writer.guard(ctx); err != nil {
		return err
	}
	tx, err := writer.connection.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return safeSQLError(ctx, err)
	}
	unit := &writeTransaction{tx: tx, faults: &writer.faults}
	if err := callTransactionOperation(operation, unit); err != nil {
		return rollbackTransaction(ctx, tx, err)
	}
	if err := ctx.Err(); err != nil {
		return rollbackTransaction(ctx, tx, err)
	}
	if writer.faults.beforeCommit != nil {
		if err := writer.faults.beforeCommit(); err != nil {
			return rollbackTransaction(ctx, tx, repository.ErrTransaction)
		}
	}
	if err := tx.Commit(); err != nil {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			return repository.ErrRollback
		}
		if contextError := ctx.Err(); contextError != nil {
			return contextError
		}
		return repository.ErrCommit
	}
	return nil
}

func callTransactionOperation(operation func(repository.WriteTransaction) error, tx *writeTransaction) (result error) {
	defer func() {
		if recover() != nil {
			result = repository.ErrTransaction
		}
	}()
	return operation(tx)
}

func rollbackTransaction(ctx context.Context, tx *sql.Tx, cause error) error {
	if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
		return repository.ErrRollback
	}
	if contextError := ctx.Err(); contextError != nil {
		return contextError
	}
	return cause
}

func (writer *Writer) Close() error {
	if writer == nil {
		return nil
	}
	writer.mu.Lock()
	defer writer.mu.Unlock()
	writer.closed = true
	writer.connection = nil
	writer.readiness = nil
	writer.owner = nil
	return nil
}

func (transaction *writeTransaction) wrote(label string) error {
	if transaction.faults != nil && transaction.faults.afterWrite != nil {
		if err := transaction.faults.afterWrite(label); err != nil {
			return repository.ErrTransaction
		}
	}
	return nil
}

func safeSQLError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if contextError := ctx.Err(); contextError != nil {
		return contextError
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "unique constraint") || strings.Contains(message, "constraint unique"):
		return repository.ErrDuplicate
	case strings.Contains(message, "foreign key constraint"):
		return repository.ErrRelationConflict
	case strings.Contains(message, "database is locked") || strings.Contains(message, "database table is locked"):
		return ErrConnectionUnavailable
	default:
		return repository.ErrTransaction
	}
}

var _ repository.Transactional = (*Writer)(nil)
var _ repository.WriteTransaction = (*writeTransaction)(nil)
