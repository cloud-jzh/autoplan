package bootstrap

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lyming99/autoplan/backend/internal/repository"
	storesqlite "github.com/lyming99/autoplan/backend/internal/repository/sqlite"
	"github.com/lyming99/autoplan/backend/migrations"
)

func TestStartDatabaseMigratesEmptyTemporaryDatabase(t *testing.T) {
	readiness, err := NewDatabaseReadiness()
	if err != nil {
		t.Fatal(err)
	}
	root := canonicalTemporaryDirectory(t)
	for range 2 {
		runtime, err := StartDatabase(context.Background(), DatabaseStartupOptions{
			Target: filepath.Join(root, "autoplan.sqlite"), DriverName: "sqlite", AllowCreate: true,
			LockTimeout: time.Second, AuthorizedRoots: []string{root}, Readiness: readiness,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := runtime.Close(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
}

func TestStartDatabaseRepairsV2OperationsAndRestartsWithStoredWorkspace(t *testing.T) {
	ctx := context.Background()
	root := canonicalTemporaryDirectory(t)
	workspace := canonicalTemporaryDirectory(t)
	target := filepath.Join(root, "autoplan.sqlite")
	seedVersionTwoRestartFixture(t, target, workspace)

	readiness, err := NewDatabaseReadiness()
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := StartDatabase(ctx, DatabaseStartupOptions{
		Target: target, DriverName: "sqlite", AllowCreate: true, LockTimeout: time.Second,
		AuthorizedRoots: []string{root}, AuthorizeStoredProjectWorkspaces: true, Readiness: readiness,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close(ctx)
	connection, ok := runtime.Connection().(*storesqlite.Connection)
	if !ok {
		t.Fatal("startup connection type drifted")
	}
	gate, err := readiness.Gate("configuration", "prerequisites", "application", "listener")
	if err != nil {
		t.Fatal(err)
	}
	writer, err := storesqlite.NewWriter(storesqlite.WriterOptions{
		Connection: connection, Readiness: gate, Owner: runtime,
		AuthorizedCopy: true, SchemaVersion: storesqlite.SchemaVersion,
	})
	if err != nil {
		t.Fatal(err)
	}
	resultJSON := `{"kind":"active-project","project_id":1}`
	err = writer.Transact(ctx, func(transaction repository.WriteTransaction) error {
		projectID := int64(1)
		if reserveErr := transaction.ReserveIdempotency(ctx, repository.IdempotencyRecord{
			OperationID: "current-operation", ProjectID: &projectID, Route: "projects:update",
			Status: "running", RequestID: "current-request", Scope: "current-scope", Key: "current-key",
			RequestHash: strings.Repeat("b", 64), CreatedAt: "2026-07-13T00:00:01.000Z",
			UpdatedAt: "2026-07-13T00:00:01.000Z",
		}); reserveErr != nil {
			return reserveErr
		}
		return transaction.CompleteIdempotency(
			ctx, "current-scope", "current-key", "succeeded", &resultJSON, nil,
			"2026-07-13T00:00:02.000Z",
		)
	})
	if err != nil {
		t.Fatal(err)
	}
	for operation, expected := range map[string]string{
		"legacy-operation":  "2026-07-13T00:00:00.000Z",
		"current-operation": "2026-07-13T00:00:01.000Z",
	} {
		var startedAt string
		if err := connection.QueryRowContext(ctx,
			"SELECT started_at FROM operations WHERE operation_id = ?", operation,
		).Scan(&startedAt); err != nil || startedAt != expected {
			t.Fatalf("%s started_at = %q, %v", operation, startedAt, err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Close(ctx); err != nil {
		t.Fatal(err)
	}

	secondReadiness, err := NewDatabaseReadiness()
	if err != nil {
		t.Fatal(err)
	}
	second, err := StartDatabase(ctx, DatabaseStartupOptions{
		Target: target, DriverName: "sqlite", AllowCreate: true, LockTimeout: time.Second,
		AuthorizedRoots: []string{root}, AuthorizeStoredProjectWorkspaces: true, Readiness: secondReadiness,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close(ctx)
	if err := second.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

func canonicalTemporaryDirectory(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Clean(root)
}

func seedVersionTwoRestartFixture(t *testing.T, target, workspace string) {
	t.Helper()
	ctx := context.Background()
	connection, err := storesqlite.OpenConnection(ctx, storesqlite.ConnectionOptions{
		DriverName: "sqlite", DataSourceName: target,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	entries := migrations.NewRegistry(migrations.NewCatalog()).Migrations()
	if len(entries) < migrations.SchemaV2Version {
		t.Fatal("v2 migration history missing")
	}
	for _, entry := range entries[:migrations.SchemaV2Version] {
		if _, err := connection.ExecContext(ctx, entry.SQL); err != nil {
			t.Fatal(err)
		}
		if _, err := connection.ExecContext(ctx,
			"INSERT INTO schema_migrations (version, name, checksum, applied_at) VALUES (?, ?, ?, ?)",
			entry.Version, entry.Name, entry.Checksum, "2026-07-13T00:00:00Z",
		); err != nil {
			t.Fatal(err)
		}
		if _, err := connection.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", entry.TargetUserVersion)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := connection.ExecContext(ctx,
		`INSERT INTO projects (id, name, workspace_path, description, created_at, updated_at)
		 VALUES (1, 'restart fixture', ?, '', '2026-07-13T00:00:00.000Z', '2026-07-13T00:00:00.000Z')`,
		workspace,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := connection.ExecContext(ctx,
		`INSERT INTO project_states (project_id, updated_at) VALUES (1, '2026-07-13T00:00:00.000Z')`,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := connection.ExecContext(ctx,
		`INSERT INTO operations (
		 operation_id, project_id, type, status, request_id, idempotency_scope, idempotency_key,
		 request_hash, created_at, updated_at, finished_at, result_json, version
		) VALUES (?, 1, ?, 'succeeded', ?, ?, ?, ?, ?, ?, ?, ?, 2)`,
		"legacy-operation", "projects:create", "legacy-request", "legacy-scope", "legacy-key",
		strings.Repeat("a", 64), "2026-07-13T00:00:00.000Z", "2026-07-13T00:00:00.000Z",
		"2026-07-13T00:00:00.000Z", `{"kind":"active-project","project_id":1}`,
	); err != nil {
		t.Fatal(err)
	}
}

func TestDaemonAllowedOriginsAddsOnlyLoopbackDevelopmentRenderer(t *testing.T) {
	t.Setenv("AUTOPLAN_SIDECAR_RENDERER_ORIGIN", "http://127.0.0.1:5173")
	origins := daemonAllowedOrigins()
	if len(origins) != 2 || origins[0] != daemonOrigin || origins[1] != "http://127.0.0.1:5173" {
		t.Fatalf("allowed origins = %#v", origins)
	}
	t.Setenv("AUTOPLAN_SIDECAR_RENDERER_ORIGIN", "https://example.test")
	origins = daemonAllowedOrigins()
	if len(origins) != 1 || origins[0] != daemonOrigin {
		t.Fatalf("non-loopback renderer origin was accepted: %#v", origins)
	}
}
