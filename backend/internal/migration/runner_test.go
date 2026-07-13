package migration

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/lyming99/autoplan/backend/internal/repository/sqlite"
	"github.com/lyming99/autoplan/backend/migrations"
)

type fixedClock struct{ value time.Time }

func (clock fixedClock) Now() time.Time { return clock.value }

type fakeRow struct {
	values []any
	err    error
}

func (row fakeRow) Scan(destinations ...any) error {
	if row.err != nil {
		return row.err
	}
	if len(destinations) != len(row.values) {
		return errors.New("scan_count")
	}
	for index, value := range row.values {
		switch target := destinations[index].(type) {
		case *int:
			*target = value.(int)
		case *string:
			*target = value.(string)
		default:
			return errors.New("scan_type")
		}
	}
	return nil
}

type fakeRows struct {
	values [][]any
	index  int
	err    error
	closed bool
}

func (rows *fakeRows) Next() bool {
	if rows.closed || rows.index >= len(rows.values) {
		return false
	}
	rows.index++
	return true
}

func (rows *fakeRows) Scan(destinations ...any) error {
	return fakeRow{values: rows.values[rows.index-1]}.Scan(destinations...)
}

func (rows *fakeRows) Err() error { return rows.err }

func (rows *fakeRows) Close() error {
	rows.closed = true
	return nil
}

type fakeResult struct{}

func (fakeResult) LastInsertId() (int64, error) { return 0, nil }
func (fakeResult) RowsAffected() (int64, error) { return 1, nil }

type fakeDatabase struct {
	userVersion int
	ledger      bool
	history     []historyRow
	tables      map[string]bool
	failSQL     bool
	failCommit  bool
	lastTx      *fakeTransaction
}

func newFakeDatabase() *fakeDatabase {
	return &fakeDatabase{tables: map[string]bool{}}
}

func (database *fakeDatabase) QueryRowContext(_ context.Context, query string, _ ...any) Row {
	return fakeQueryRow(database.userVersion, database.ledger, query)
}

func (database *fakeDatabase) QueryContext(_ context.Context, query string, _ ...any) (Rows, error) {
	return fakeQueryRows(database.history, database.tables, query), nil
}

func (database *fakeDatabase) Begin(_ context.Context) (Transaction, error) {
	transaction := &fakeTransaction{
		parent: database, userVersion: database.userVersion, ledger: database.ledger,
		history: append([]historyRow(nil), database.history...), tables: cloneTables(database.tables),
	}
	database.lastTx = transaction
	return transaction, nil
}

type fakeTransaction struct {
	parent      *fakeDatabase
	userVersion int
	ledger      bool
	history     []historyRow
	tables      map[string]bool
	committed   bool
	rolledBack  bool
}

func (transaction *fakeTransaction) QueryRowContext(_ context.Context, query string, _ ...any) Row {
	return fakeQueryRow(transaction.userVersion, transaction.ledger, query)
}

func (transaction *fakeTransaction) QueryContext(_ context.Context, query string, _ ...any) (Rows, error) {
	return fakeQueryRows(transaction.history, transaction.tables, query), nil
}

func (transaction *fakeTransaction) ExecContext(_ context.Context, query string, args ...any) (sql.Result, error) {
	if transaction.parent.failSQL && strings.Contains(query, "CREATE TABLE") {
		return nil, errors.New("synthetic_sql_failure")
	}
	if strings.Contains(query, "CREATE TABLE") {
		transaction.ledger = true
		for _, table := range sqlite.RequiredTables {
			transaction.tables[table] = true
		}
	}
	if strings.HasPrefix(query, "INSERT INTO schema_migrations") {
		transaction.history = append(transaction.history, historyRow{
			version: args[0].(int), name: args[1].(string), checksum: args[2].(string), appliedAt: args[3].(string),
		})
	}
	if strings.HasPrefix(query, "PRAGMA user_version = ") {
		_, _ = fmtSscanf(query, "PRAGMA user_version = %d", &transaction.userVersion)
	}
	return fakeResult{}, nil
}

func (transaction *fakeTransaction) Commit() error {
	if transaction.parent.failCommit {
		return errors.New("synthetic_commit_failure")
	}
	transaction.parent.userVersion = transaction.userVersion
	transaction.parent.ledger = transaction.ledger
	transaction.parent.history = append([]historyRow(nil), transaction.history...)
	transaction.parent.tables = cloneTables(transaction.tables)
	transaction.committed = true
	return nil
}

func (transaction *fakeTransaction) Rollback() error {
	transaction.rolledBack = true
	return nil
}

func fakeQueryRow(userVersion int, ledger bool, query string) Row {
	switch {
	case query == "PRAGMA user_version":
		return fakeRow{values: []any{userVersion}}
	case strings.Contains(query, "name = 'schema_migrations'"):
		if ledger {
			return fakeRow{values: []any{1}}
		}
		return fakeRow{values: []any{0}}
	default:
		return fakeRow{err: errors.New("unexpected_query")}
	}
}

func fakeQueryRows(history []historyRow, tables map[string]bool, query string) Rows {
	rows := &fakeRows{}
	switch {
	case strings.Contains(query, "FROM schema_migrations"):
		for _, item := range history {
			rows.values = append(rows.values, []any{item.version, item.name, item.checksum, item.appliedAt})
		}
	case strings.Contains(query, "FROM sqlite_schema"):
		for _, table := range sqlite.RequiredTables {
			if tables[table] {
				rows.values = append(rows.values, []any{table})
			}
		}
	default:
		rows.err = errors.New("unexpected_query")
	}
	return rows
}

func cloneTables(source map[string]bool) map[string]bool {
	result := make(map[string]bool, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

// Kept local so the fake does not need a production SQL parser.
func fmtSscanf(value, format string, destination *int) (int, error) {
	prefix := strings.TrimSuffix(format, "%d")
	if !strings.HasPrefix(value, prefix) {
		return 0, errors.New("scan_prefix")
	}
	parsed := 0
	for _, char := range strings.TrimSpace(strings.TrimPrefix(value, prefix)) {
		if char < '0' || char > '9' {
			return 0, errors.New("scan_integer")
		}
		parsed = parsed*10 + int(char-'0')
	}
	*destination = parsed
	return 1, nil
}

func TestRunnerAppliesSchemaAndLedgerAtomicallyThenReturnsNoOp(t *testing.T) {
	database := newFakeDatabase()
	registry := migrations.NewRegistry(migrations.NewCatalog())
	clock := fixedClock{value: time.Date(2026, 7, 11, 4, 0, 0, 0, time.UTC)}
	runner := NewRunner(database, registry, WithClock(clock))

	result, err := runner.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.FromVersion != 0 || result.ToVersion != 1 || result.NoOp || len(result.Applied) != 1 {
		t.Fatalf("unexpected result: %#v", result)
	}
	if !database.lastTx.committed || database.lastTx.rolledBack || len(database.history) != 1 {
		t.Fatalf("migration was not committed atomically: %#v", database.lastTx)
	}
	if database.history[0].checksum != migrations.SchemaV1Checksum || database.userVersion != 1 {
		t.Fatalf("ledger/version mismatch: %#v version=%d", database.history, database.userVersion)
	}

	second, err := runner.Run(context.Background())
	if err != nil || !second.NoOp || len(second.Applied) != 0 || len(database.history) != 1 {
		t.Fatalf("second Run() = %#v, %v", second, err)
	}
}

func TestRunnerRollsBackSQLVerificationCommitAndPanicFailures(t *testing.T) {
	cases := []struct {
		name     string
		prepare  func(*fakeDatabase) Option
		expected error
	}{
		{name: "sql", prepare: func(database *fakeDatabase) Option { database.failSQL = true; return nil }, expected: ErrMigrationFailed},
		{name: "verify", prepare: func(_ *fakeDatabase) Option {
			return WithVerifier(func(context.Context, Queryer) error { return errors.New("verify") })
		}, expected: ErrSchemaVerification},
		{name: "commit", prepare: func(database *fakeDatabase) Option { database.failCommit = true; return nil }, expected: ErrMigrationFailed},
		{name: "panic", prepare: func(_ *fakeDatabase) Option {
			return WithVerifier(func(context.Context, Queryer) error { panic("synthetic") })
		}, expected: ErrMigrationPanic},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			database := newFakeDatabase()
			options := []Option{WithClock(fixedClock{value: time.Date(2026, 7, 11, 4, 0, 0, 0, time.UTC)})}
			if option := item.prepare(database); option != nil {
				options = append(options, option)
			}
			runner := NewRunner(database, migrations.NewRegistry(migrations.NewCatalog()), options...)
			_, err := runner.Run(context.Background())
			if !errors.Is(err, item.expected) {
				t.Fatalf("Run() error = %v, want %v", err, item.expected)
			}
			if database.userVersion != 0 || database.ledger || len(database.history) != 0 || !database.lastTx.rolledBack {
				t.Fatalf("failed transaction leaked state: %#v", database)
			}
		})
	}
}

func TestValidateStateRejectsFutureGapDirtyAndChecksumDrift(t *testing.T) {
	entry := migrations.NewRegistry(migrations.NewCatalog()).Migrations()[0]
	validTime := "2026-07-11T04:00:00Z"
	cases := []struct {
		name     string
		state    databaseState
		expected error
	}{
		{name: "future", state: databaseState{userVersion: 2}, expected: ErrFutureVersion},
		{name: "missing ledger", state: databaseState{userVersion: 1}, expected: ErrDirtyHistory},
		{name: "gap", state: databaseState{userVersion: 1, history: []historyRow{{version: 2, name: entry.Name, checksum: entry.Checksum, appliedAt: validTime}}}, expected: ErrDirtyHistory},
		{name: "checksum", state: databaseState{userVersion: 1, history: []historyRow{{version: 1, name: entry.Name, checksum: "drift", appliedAt: validTime}}}, expected: ErrChecksumDrift},
		{name: "dirty time", state: databaseState{userVersion: 1, history: []historyRow{{version: 1, name: entry.Name, checksum: entry.Checksum, appliedAt: "not-utc"}}}, expected: ErrDirtyHistory},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			if err := validateState(item.state, []migrations.Migration{entry}); !errors.Is(err, item.expected) {
				t.Fatalf("validateState() = %v, want %v", err, item.expected)
			}
		})
	}
}

func TestRunnerHonorsCancellationBeforeOpeningTransaction(t *testing.T) {
	database := newFakeDatabase()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := NewRunner(database, migrations.NewRegistry(migrations.NewCatalog())).Run(ctx)
	if !errors.Is(err, context.Canceled) || database.lastTx != nil {
		t.Fatalf("Run() = %v, transaction=%#v", err, database.lastTx)
	}
}

func TestConnectionRejectsInvalidAndReadOnlyPoliciesBeforeDriverAccess(t *testing.T) {
	if _, err := sqlite.OpenConnection(context.Background(), sqlite.ConnectionOptions{}); !errors.Is(err, sqlite.ErrInvalidConnectionOptions) {
		t.Fatalf("empty OpenConnection() error = %v", err)
	}
	_, err := sqlite.OpenConnection(context.Background(), sqlite.ConnectionOptions{
		DriverName: "unregistered-driver", DataSourceName: "sanitized-copy.sqlite", ReadOnly: true,
	})
	if !errors.Is(err, sqlite.ErrReadOnlyUnsupported) {
		t.Fatalf("read-only OpenConnection() error = %v", err)
	}
}
