package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
)

const (
	RequiredJournalMode = "wal"
	RequiredBusyTimeout = 5000
	RequiredSynchronous = 1 // SQLite numeric value for NORMAL.
)

var (
	ErrInvalidConnectionOptions = errors.New("sqlite_connection_options_invalid")
	ErrReadOnlyUnsupported      = errors.New("sqlite_read_only_policy_unsupported")
	ErrConnectionUnavailable    = errors.New("sqlite_connection_unavailable")
	ErrConnectionPolicy         = errors.New("sqlite_connection_policy_failed")
)

type ConnectionOptions struct {
	DriverName     string
	DataSourceName string
	ReadOnly       bool
}

// Connection pins all work to the one physical database/sql connection on
// which the required PRAGMAs were set and verified. Callers cannot obtain an
// unconfigured replacement connection from the pool.
type Connection struct {
	mu     sync.Mutex
	db     *sql.DB
	conn   *sql.Conn
	closed bool
}

func OpenConnection(ctx context.Context, options ConnectionOptions) (*Connection, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(options.DriverName) == "" || strings.TrimSpace(options.DataSourceName) == "" {
		return nil, ErrInvalidConnectionOptions
	}
	if options.ReadOnly {
		return nil, ErrReadOnlyUnsupported
	}
	database, err := sql.Open(options.DriverName, options.DataSourceName)
	if err != nil {
		return nil, ErrConnectionUnavailable
	}
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)
	connection, err := database.Conn(ctx)
	if err != nil {
		_ = database.Close()
		return nil, ErrConnectionUnavailable
	}
	result := &Connection{db: database, conn: connection}
	if err := result.configure(ctx); err != nil {
		_ = result.Close()
		return nil, err
	}
	return result, nil
}

func (connection *Connection) configure(ctx context.Context) error {
	if _, err := connection.conn.ExecContext(ctx, "PRAGMA foreign_keys = ON"); err != nil {
		return ErrConnectionPolicy
	}
	var foreignKeys int
	if err := connection.conn.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil || foreignKeys != 1 {
		return ErrConnectionPolicy
	}
	var journalMode string
	if err := connection.conn.QueryRowContext(ctx, "PRAGMA journal_mode = WAL").Scan(&journalMode); err != nil ||
		strings.ToLower(strings.TrimSpace(journalMode)) != RequiredJournalMode {
		return ErrConnectionPolicy
	}
	if _, err := connection.conn.ExecContext(ctx, "PRAGMA busy_timeout = 5000"); err != nil {
		return ErrConnectionPolicy
	}
	var busyTimeout int
	if err := connection.conn.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&busyTimeout); err != nil || busyTimeout != RequiredBusyTimeout {
		return ErrConnectionPolicy
	}
	if _, err := connection.conn.ExecContext(ctx, "PRAGMA synchronous = NORMAL"); err != nil {
		return ErrConnectionPolicy
	}
	var synchronous int
	if err := connection.conn.QueryRowContext(ctx, "PRAGMA synchronous").Scan(&synchronous); err != nil || synchronous != RequiredSynchronous {
		return ErrConnectionPolicy
	}
	if err := connection.conn.PingContext(ctx); err != nil {
		return ErrConnectionPolicy
	}
	return nil
}

func (connection *Connection) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return connection.conn.ExecContext(ctx, query, args...)
}

func (connection *Connection) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return connection.conn.QueryContext(ctx, query, args...)
}

func (connection *Connection) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return connection.conn.QueryRowContext(ctx, query, args...)
}

func (connection *Connection) BeginTx(ctx context.Context, options *sql.TxOptions) (*sql.Tx, error) {
	return connection.conn.BeginTx(ctx, options)
}

func (connection *Connection) Close() error {
	connection.mu.Lock()
	defer connection.mu.Unlock()
	if connection.closed {
		return nil
	}
	connection.closed = true
	var first error
	if connection.conn != nil {
		first = connection.conn.Close()
		connection.conn = nil
	}
	if connection.db != nil {
		if err := connection.db.Close(); first == nil {
			first = err
		}
		connection.db = nil
	}
	return first
}
