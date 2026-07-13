package migration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/lyming99/autoplan/backend/internal/repository/sqlite"
	"github.com/lyming99/autoplan/backend/migrations"
)

var (
	ErrInvalidRunner      = errors.New("migration_runner_invalid")
	ErrFutureVersion      = errors.New("migration_future_version")
	ErrDirtyHistory       = errors.New("migration_history_dirty")
	ErrChecksumDrift      = errors.New("migration_checksum_drift")
	ErrMigrationFailed    = errors.New("migration_apply_failed")
	ErrMigrationPanic     = errors.New("migration_apply_panic")
	ErrSchemaVerification = errors.New("migration_schema_verification_failed")
)

type Row interface {
	Scan(...any) error
}

type Rows interface {
	Next() bool
	Scan(...any) error
	Err() error
	Close() error
}

type Queryer interface {
	QueryContext(context.Context, string, ...any) (Rows, error)
	QueryRowContext(context.Context, string, ...any) Row
}

type Transaction interface {
	Queryer
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	Commit() error
	Rollback() error
}

type Database interface {
	Queryer
	Begin(context.Context) (Transaction, error)
}

type SQLHandle interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
	BeginTx(context.Context, *sql.TxOptions) (*sql.Tx, error)
}

type sqlDatabase struct {
	handle SQLHandle
}

type sqlTransaction struct {
	tx *sql.Tx
}

func WrapSQL(handle SQLHandle) Database {
	return &sqlDatabase{handle: handle}
}

func (database *sqlDatabase) QueryContext(ctx context.Context, query string, args ...any) (Rows, error) {
	return database.handle.QueryContext(ctx, query, args...)
}

func (database *sqlDatabase) QueryRowContext(ctx context.Context, query string, args ...any) Row {
	return database.handle.QueryRowContext(ctx, query, args...)
}

func (database *sqlDatabase) Begin(ctx context.Context) (Transaction, error) {
	transaction, err := database.handle.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	return &sqlTransaction{tx: transaction}, nil
}

func (transaction *sqlTransaction) QueryContext(ctx context.Context, query string, args ...any) (Rows, error) {
	return transaction.tx.QueryContext(ctx, query, args...)
}

func (transaction *sqlTransaction) QueryRowContext(ctx context.Context, query string, args ...any) Row {
	return transaction.tx.QueryRowContext(ctx, query, args...)
}

func (transaction *sqlTransaction) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return transaction.tx.ExecContext(ctx, query, args...)
}

func (transaction *sqlTransaction) Commit() error {
	return transaction.tx.Commit()
}

func (transaction *sqlTransaction) Rollback() error {
	return transaction.tx.Rollback()
}

type Clock interface {
	Now() time.Time
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now().UTC() }

type VerifyFunc func(context.Context, Queryer) error

type Option func(*Runner)

func WithClock(clock Clock) Option {
	return func(runner *Runner) { runner.clock = clock }
}

func WithVerifier(verifier VerifyFunc) Option {
	return func(runner *Runner) { runner.verify = verifier }
}

type Runner struct {
	database Database
	registry migrations.Registry
	clock    Clock
	verify   VerifyFunc
}

type AppliedMigration struct {
	Version  int
	Name     string
	Checksum string
}

type Result struct {
	FromVersion int
	ToVersion   int
	Applied     []AppliedMigration
	NoOp        bool
}

type historyRow struct {
	version   int
	name      string
	checksum  string
	appliedAt string
}

type databaseState struct {
	userVersion int
	history     []historyRow
}

func NewRunner(database Database, registry migrations.Registry, options ...Option) *Runner {
	runner := &Runner{database: database, registry: registry, clock: systemClock{}, verify: verifySchemaV1}
	for _, option := range options {
		if option != nil {
			option(runner)
		}
	}
	return runner
}

func (runner *Runner) Run(ctx context.Context) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if runner == nil || runner.database == nil || runner.clock == nil || runner.verify == nil {
		return Result{}, ErrInvalidRunner
	}
	if err := runner.registry.Validate(); err != nil {
		return Result{}, ErrInvalidRunner
	}
	entries := runner.registry.Migrations()
	state, err := loadState(ctx, runner.database)
	if err != nil {
		return Result{}, err
	}
	if err := validateState(state, entries); err != nil {
		return Result{}, err
	}
	result := Result{FromVersion: state.userVersion, ToVersion: state.userVersion, Applied: []AppliedMigration{}}
	for _, entry := range entries {
		if entry.Version <= state.userVersion {
			continue
		}
		if err := runner.apply(ctx, entry, entry.TargetUserVersion == runner.registry.LatestVersion()); err != nil {
			return Result{}, err
		}
		result.Applied = append(result.Applied, AppliedMigration{
			Version: entry.Version, Name: entry.Name, Checksum: entry.Checksum,
		})
		result.ToVersion = entry.TargetUserVersion
		state.userVersion = entry.TargetUserVersion
	}
	if len(result.Applied) == 0 {
		if state.userVersion == runner.registry.LatestVersion() {
			if err := runner.verify(ctx, runner.database); err != nil {
				return Result{}, fmt.Errorf("%w", ErrSchemaVerification)
			}
		}
		result.NoOp = true
	}
	finalState, err := loadState(ctx, runner.database)
	if err != nil {
		return Result{}, err
	}
	if err := validateState(finalState, entries); err != nil || finalState.userVersion != result.ToVersion {
		return Result{}, ErrDirtyHistory
	}
	return result, nil
}

func (runner *Runner) apply(ctx context.Context, entry migrations.Migration, verify bool) (returnErr error) {
	transaction, err := runner.database.Begin(ctx)
	if err != nil {
		return ErrMigrationFailed
	}
	committed := false
	defer func() {
		if !committed {
			_ = transaction.Rollback()
		}
		if recovered := recover(); recovered != nil {
			returnErr = ErrMigrationPanic
		}
	}()
	if _, err := transaction.ExecContext(ctx, entry.SQL); err != nil {
		return ErrMigrationFailed
	}
	appliedAt := runner.clock.Now().UTC().Format(time.RFC3339Nano)
	if _, err := transaction.ExecContext(ctx,
		"INSERT INTO schema_migrations (version, name, checksum, applied_at) VALUES (?, ?, ?, ?)",
		entry.Version, entry.Name, entry.Checksum, appliedAt); err != nil {
		return ErrMigrationFailed
	}
	if _, err := transaction.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", entry.TargetUserVersion)); err != nil {
		return ErrMigrationFailed
	}
	if verify {
		if err := runner.verify(ctx, transaction); err != nil {
			return ErrSchemaVerification
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := transaction.Commit(); err != nil {
		return ErrMigrationFailed
	}
	committed = true
	return nil
}

func loadState(ctx context.Context, database Queryer) (databaseState, error) {
	var state databaseState
	if err := database.QueryRowContext(ctx, "PRAGMA user_version").Scan(&state.userVersion); err != nil {
		return databaseState{}, ErrDirtyHistory
	}
	var ledgerExists int
	if err := database.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM sqlite_schema WHERE type = 'table' AND name = 'schema_migrations'").Scan(&ledgerExists); err != nil {
		return databaseState{}, ErrDirtyHistory
	}
	if ledgerExists == 0 {
		if state.userVersion != 0 {
			return databaseState{}, ErrDirtyHistory
		}
		return state, nil
	}
	if ledgerExists != 1 {
		return databaseState{}, ErrDirtyHistory
	}
	rows, err := database.QueryContext(ctx,
		"SELECT version, name, checksum, applied_at FROM schema_migrations ORDER BY version")
	if err != nil {
		return databaseState{}, ErrDirtyHistory
	}
	defer rows.Close()
	for rows.Next() {
		var row historyRow
		if err := rows.Scan(&row.version, &row.name, &row.checksum, &row.appliedAt); err != nil {
			return databaseState{}, ErrDirtyHistory
		}
		state.history = append(state.history, row)
	}
	if err := rows.Err(); err != nil {
		return databaseState{}, ErrDirtyHistory
	}
	return state, nil
}

func validateState(state databaseState, entries []migrations.Migration) error {
	if state.userVersion < 0 {
		return ErrDirtyHistory
	}
	if len(entries) == 0 || state.userVersion > entries[len(entries)-1].TargetUserVersion {
		return ErrFutureVersion
	}
	byVersion := make(map[int]migrations.Migration, len(entries))
	for _, entry := range entries {
		if _, duplicate := byVersion[entry.Version]; duplicate {
			return ErrDirtyHistory
		}
		byVersion[entry.Version] = entry
	}
	sort.Slice(state.history, func(i, j int) bool { return state.history[i].version < state.history[j].version })
	seen := make(map[int]struct{}, len(state.history))
	for index, applied := range state.history {
		if applied.version != index+1 || applied.version > state.userVersion ||
			applied.name == "" || applied.checksum == "" || applied.appliedAt == "" {
			return ErrDirtyHistory
		}
		if _, duplicate := seen[applied.version]; duplicate {
			return ErrDirtyHistory
		}
		seen[applied.version] = struct{}{}
		expected, ok := byVersion[applied.version]
		if !ok {
			return ErrFutureVersion
		}
		if applied.name != expected.Name || applied.checksum != expected.Checksum {
			return ErrChecksumDrift
		}
		if !strings.HasSuffix(applied.appliedAt, "Z") {
			return ErrDirtyHistory
		}
		if _, err := time.Parse(time.RFC3339Nano, applied.appliedAt); err != nil {
			return ErrDirtyHistory
		}
	}
	if len(state.history) != state.userVersion {
		return ErrDirtyHistory
	}
	return nil
}

func verifySchemaV1(ctx context.Context, database Queryer) error {
	var userVersion int
	if err := database.QueryRowContext(ctx, "PRAGMA user_version").Scan(&userVersion); err != nil || userVersion != sqlite.SchemaVersion {
		return ErrSchemaVerification
	}
	rows, err := database.QueryContext(ctx,
		"SELECT name FROM sqlite_schema WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name")
	if err != nil {
		return ErrSchemaVerification
	}
	defer rows.Close()
	actual := make(map[string]struct{})
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return ErrSchemaVerification
		}
		actual[name] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return ErrSchemaVerification
	}
	for _, table := range sqlite.RequiredTables {
		if _, ok := actual[table]; !ok {
			return ErrSchemaVerification
		}
	}
	return nil
}
