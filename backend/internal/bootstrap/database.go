package bootstrap

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"time"

	coremigration "github.com/lyming99/autoplan/backend/internal/migration"
	"github.com/lyming99/autoplan/backend/internal/migration/audit"
	"github.com/lyming99/autoplan/backend/internal/platform/instance"
	"github.com/lyming99/autoplan/backend/internal/repository/sqlite"
	"github.com/lyming99/autoplan/backend/migrations"
)

const (
	ReadinessDatabaseOwner = "database_owner"
	ReadinessMigrations    = "migrations"
	ReadinessDatabase      = "database"
)

var (
	ErrDatabaseStartupInvalid = errors.New("database_startup_invalid")
	ErrDatabaseOpen           = errors.New("database_open_failed")
	ErrDatabaseMigration      = errors.New("database_migration_failed")
	ErrDatabaseSchema         = errors.New("database_schema_failed")
	ErrDatabaseAudit          = errors.New("database_audit_failed")
	ErrDatabaseCleanup        = errors.New("database_cleanup_failed")
)

type DatabaseOwner interface {
	DatabaseID() string
	Close(context.Context) error
}

type StartupConnection interface {
	Close() error
}

type AcquireDatabaseOwnerFunc func(context.Context, instance.DatabaseLockOptions) (DatabaseOwner, error)
type OpenStartupDatabaseFunc func(context.Context, string, string) (StartupConnection, error)
type CheckStartupDatabaseFunc func(context.Context, StartupConnection) error

type DatabaseStartupDependencies struct {
	Acquire  AcquireDatabaseOwnerFunc
	Open     OpenStartupDatabaseFunc
	Migrate  CheckStartupDatabaseFunc
	Validate CheckStartupDatabaseFunc
	Audit    CheckStartupDatabaseFunc
}

type DatabaseStartupOptions struct {
	Target                           string
	DriverName                       string
	AllowCreate                      bool
	LockTimeout                      time.Duration
	AuthorizedRoots                  []string
	AuthorizeStoredProjectWorkspaces bool
	Readiness                        *Readiness
	Dependencies                     DatabaseStartupDependencies
}

// DatabaseRuntime owns the connection and owner lock as one lifecycle
// resource. Close always releases the connection before the process-wide lock.
type DatabaseRuntime struct {
	connection StartupConnection
	owner      DatabaseOwner
	databaseID string
	once       sync.Once
	result     error
}

func StartDatabase(ctx context.Context, options DatabaseStartupOptions) (*DatabaseRuntime, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(options.Target) == "" || !filepath.IsAbs(options.Target) ||
		strings.TrimSpace(options.DriverName) == "" || strings.TrimSpace(options.DriverName) != options.DriverName ||
		options.Readiness == nil {
		return nil, ErrDatabaseStartupInvalid
	}
	options.Target = filepath.Clean(options.Target)
	dependencies := withDatabaseStartupDefaults(options)
	if dependencies.Acquire == nil || dependencies.Open == nil || dependencies.Migrate == nil ||
		dependencies.Validate == nil || dependencies.Audit == nil {
		return nil, ErrDatabaseStartupInvalid
	}
	for _, dependency := range []string{ReadinessDatabaseOwner, ReadinessMigrations, ReadinessDatabase} {
		if err := options.Readiness.MarkPending(dependency, "initializing"); err != nil {
			return nil, ErrDatabaseStartupInvalid
		}
	}

	owner, err := callAcquireDatabaseOwner(dependencies.Acquire, ctx, instance.DatabaseLockOptions{
		Target: options.Target, AllowCreate: options.AllowCreate, Timeout: options.LockTimeout,
	})
	if err != nil || owner == nil {
		_ = options.Readiness.MarkFailed(ReadinessDatabaseOwner, databaseOwnerReason(err))
		if err == nil {
			err = ErrDatabaseStartupInvalid
		}
		return nil, err
	}
	databaseID, idErr := databaseOwnerID(owner)
	if idErr != nil || databaseID == "" || options.Readiness.MarkReady(ReadinessDatabaseOwner) != nil {
		return nil, cleanupDatabaseStartup(nil, owner, ErrDatabaseStartupInvalid)
	}
	if err := ctx.Err(); err != nil {
		return nil, cleanupDatabaseStartup(nil, owner, err)
	}

	connection, err := callOpenStartupDatabase(dependencies.Open, ctx, options.DriverName, options.Target)
	if err != nil || connection == nil {
		_ = options.Readiness.MarkFailed(ReadinessDatabase, "open_failed")
		return nil, cleanupDatabaseStartup(nil, owner, ErrDatabaseOpen)
	}
	if err := callCheckStartupDatabase(dependencies.Migrate, ctx, connection); err != nil {
		_ = options.Readiness.MarkFailed(ReadinessMigrations, "migration_failed")
		return nil, cleanupDatabaseStartup(connection, owner, ErrDatabaseMigration)
	}
	if err := options.Readiness.MarkReady(ReadinessMigrations); err != nil {
		return nil, cleanupDatabaseStartup(connection, owner, ErrDatabaseStartupInvalid)
	}
	if err := callCheckStartupDatabase(dependencies.Validate, ctx, connection); err != nil {
		_ = options.Readiness.MarkFailed(ReadinessDatabase, "schema_failed")
		return nil, cleanupDatabaseStartup(connection, owner, ErrDatabaseSchema)
	}
	if err := callCheckStartupDatabase(dependencies.Audit, ctx, connection); err != nil {
		_ = options.Readiness.MarkFailed(ReadinessDatabase, "audit_failed")
		return nil, cleanupDatabaseStartup(connection, owner, ErrDatabaseAudit)
	}
	if err := ctx.Err(); err != nil {
		return nil, cleanupDatabaseStartup(connection, owner, err)
	}
	if err := options.Readiness.MarkReady(ReadinessDatabase); err != nil {
		return nil, cleanupDatabaseStartup(connection, owner, ErrDatabaseStartupInvalid)
	}
	return &DatabaseRuntime{
		connection: connection, owner: owner, databaseID: databaseID,
	}, nil
}

func (runtime *DatabaseRuntime) DatabaseID() string {
	if runtime == nil {
		return ""
	}
	return runtime.databaseID
}

func (runtime *DatabaseRuntime) Connection() StartupConnection {
	if runtime == nil {
		return nil
	}
	return runtime.connection
}

func (runtime *DatabaseRuntime) Close(ctx context.Context) error {
	if runtime == nil {
		return nil
	}
	runtime.once.Do(func() {
		failed := false
		if runtime.connection != nil && closeStartupConnection(runtime.connection) != nil {
			failed = true
		}
		runtime.connection = nil
		if runtime.owner != nil && closeDatabaseOwner(runtime.owner, ctx) != nil {
			failed = true
		}
		runtime.owner = nil
		if failed {
			runtime.result = ErrDatabaseCleanup
		}
	})
	return runtime.result
}

func withDatabaseStartupDefaults(options DatabaseStartupOptions) DatabaseStartupDependencies {
	dependencies := options.Dependencies
	if dependencies.Acquire == nil {
		dependencies.Acquire = func(ctx context.Context, lockOptions instance.DatabaseLockOptions) (DatabaseOwner, error) {
			return instance.AcquireDatabaseLock(ctx, lockOptions)
		}
	}
	if dependencies.Open == nil {
		dependencies.Open = func(ctx context.Context, driverName, target string) (StartupConnection, error) {
			return sqlite.OpenConnection(ctx, sqlite.ConnectionOptions{DriverName: driverName, DataSourceName: target})
		}
	}
	if dependencies.Migrate == nil {
		dependencies.Migrate = func(ctx context.Context, connection StartupConnection) error {
			handle, ok := connection.(startupSQLHandle)
			if !ok {
				return ErrDatabaseStartupInvalid
			}
			runner := coremigration.NewRunner(coremigration.WrapSQL(handle), migrations.NewRegistry(migrations.NewCatalog()))
			_, err := runner.Run(ctx)
			return err
		}
	}
	if dependencies.Validate == nil {
		dependencies.Validate = func(ctx context.Context, connection StartupConnection) error {
			handle, ok := connection.(startupSQLHandle)
			if !ok {
				return ErrDatabaseStartupInvalid
			}
			return sqlite.ValidateSchemaV1(ctx, handle)
		}
	}
	if dependencies.Audit == nil {
		dependencies.Audit = func(ctx context.Context, connection StartupConnection) error {
			handle, ok := connection.(startupSQLHandle)
			if !ok {
				return ErrDatabaseStartupInvalid
			}
			roots := append([]string(nil), options.AuthorizedRoots...)
			if len(roots) == 0 {
				roots = []string{filepath.Dir(options.Target)}
			}
			auditor, err := audit.New(audit.WrapSQL(handle), audit.Options{
				Phase: "startup", MigrationVersion: sqlite.SchemaVersion,
				AuthorizedRoots: roots, ProjectWorkspacesAreRoots: options.AuthorizeStoredProjectWorkspaces,
			})
			if err != nil {
				return err
			}
			report, err := auditor.Audit(ctx)
			if err != nil || !report.OK {
				return ErrDatabaseAudit
			}
			return nil
		}
	}
	return dependencies
}

type startupSQLHandle interface {
	StartupConnection
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
	BeginTx(context.Context, *sql.TxOptions) (*sql.Tx, error)
}

func cleanupDatabaseStartup(connection StartupConnection, owner DatabaseOwner, cause error) error {
	failed := false
	if connection != nil && closeStartupConnection(connection) != nil {
		failed = true
	}
	if owner != nil && closeDatabaseOwner(owner, context.Background()) != nil {
		failed = true
	}
	if failed {
		return ErrDatabaseCleanup
	}
	return cause
}

func callAcquireDatabaseOwner(
	acquire AcquireDatabaseOwnerFunc,
	ctx context.Context,
	options instance.DatabaseLockOptions,
) (owner DatabaseOwner, err error) {
	defer func() {
		if recover() != nil {
			owner, err = nil, ErrDatabaseStartupInvalid
		}
	}()
	return acquire(ctx, options)
}

func callOpenStartupDatabase(
	open OpenStartupDatabaseFunc,
	ctx context.Context,
	driverName, target string,
) (connection StartupConnection, err error) {
	defer func() {
		if recover() != nil {
			connection, err = nil, ErrDatabaseOpen
		}
	}()
	return open(ctx, driverName, target)
}

func callCheckStartupDatabase(
	check CheckStartupDatabaseFunc,
	ctx context.Context,
	connection StartupConnection,
) (err error) {
	defer func() {
		if recover() != nil {
			err = ErrDatabaseStartupInvalid
		}
	}()
	return check(ctx, connection)
}

func closeStartupConnection(connection StartupConnection) (err error) {
	defer func() {
		if recover() != nil {
			err = ErrDatabaseCleanup
		}
	}()
	return connection.Close()
}

func closeDatabaseOwner(owner DatabaseOwner, ctx context.Context) (err error) {
	defer func() {
		if recover() != nil {
			err = ErrDatabaseCleanup
		}
	}()
	return owner.Close(ctx)
}

func databaseOwnerID(owner DatabaseOwner) (id string, err error) {
	defer func() {
		if recover() != nil {
			id, err = "", ErrDatabaseStartupInvalid
		}
	}()
	return owner.DatabaseID(), nil
}

func databaseOwnerReason(err error) string {
	switch {
	case errors.Is(err, instance.ErrDatabaseOwnerLocked):
		return "owner_locked"
	case errors.Is(err, instance.ErrDatabaseIdentityInvalid):
		return "identity_invalid"
	default:
		return "owner_unavailable"
	}
}
