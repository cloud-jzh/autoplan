package sqlite

import (
	"context"
	"database/sql/driver"
	"errors"
	"testing"

	domainevent "github.com/lyming99/autoplan/backend/internal/domain/event"
	domainplan "github.com/lyming99/autoplan/backend/internal/domain/plan"
	"github.com/lyming99/autoplan/backend/internal/repository"
)

func TestPlanTransactionRollsBackQueuedEventWithBusinessWork(t *testing.T) {
	metadata := `{}`
	backend := &scriptBackend{steps: []scriptStep{
		queryStep("SELECT 1 FROM projects", []string{"1"}, []driver.Value{int64(1)}),
		execStep("INSERT INTO events", 1, 1),
		execStep("INSERT INTO event_outbox", 1, 1),
	}}
	writer, cleanup := newTestWriter(t, backend)
	defer cleanup()

	err := writer.TransactPlans(context.Background(), func(transaction repository.PlanWriteTransaction) error {
		if err := transaction.AppendEvent(context.Background(), domainevent.PendingEvent{
			EventID: "event-plan-1", StreamKey: "project:1", Sequence: 1,
			Type: "plan.accepted", RequestID: "request-plan-1", ProjectID: 1,
			Message: "plan accepted", MetaJSON: &metadata,
			OccurredAt: intakeTestTime, CreatedAt: intakeTestTime,
		}); err != nil {
			return err
		}
		return errors.New("synthetic failure after queued event")
	})
	if err == nil || err.Error() != "synthetic failure after queued event" {
		t.Fatalf("transaction error = %v", err)
	}
	backend.assertFinished(t, 0, 1)
}

func TestPlanAcceptanceUsesProjectScopedUpdatedAtCAS(t *testing.T) {
	acceptedAt := "2026-07-11T00:00:01.000Z"
	backend := &scriptBackend{steps: []scriptStep{
		queryStep("FROM plans WHERE project_id", planTestColumns(), planTestValues(8, 3, "completed", intakeTestTime, nil)),
		execStep("UPDATE plans SET accepted_at", 1, 0),
		queryStep("FROM plans WHERE project_id", planTestColumns(), planTestValues(8, 3, "completed", acceptedAt, &acceptedAt)),
	}}
	writer, cleanup := newTestWriter(t, backend)
	defer cleanup()

	var updatedAt string
	err := writer.TransactPlans(context.Background(), func(transaction repository.PlanWriteTransaction) error {
		updated, err := transaction.SetPlanAcceptance(context.Background(), domainplan.AcceptanceUpdate{
			ProjectID: 3, ID: 8, AcceptedAt: &acceptedAt,
			ExpectedUpdatedAt: intakeTestTime, UpdatedAt: acceptedAt,
		})
		updatedAt = updated.UpdatedAt
		return err
	})
	if err != nil || updatedAt != acceptedAt {
		t.Fatalf("acceptance update=%q error=%v", updatedAt, err)
	}
	backend.assertFinished(t, 1, 0)
}

func TestPlanAcceptanceRejectsStaleSnapshotWithoutWriting(t *testing.T) {
	staleAt := "2026-07-11T00:00:00.000Z"
	currentAt := "2026-07-11T00:00:01.000Z"
	nextAt := "2026-07-11T00:00:02.000Z"
	backend := &scriptBackend{steps: []scriptStep{
		queryStep("FROM plans WHERE project_id", planTestColumns(), planTestValues(8, 3, "completed", currentAt, nil)),
	}}
	writer, cleanup := newTestWriter(t, backend)
	defer cleanup()

	err := writer.TransactPlans(context.Background(), func(transaction repository.PlanWriteTransaction) error {
		_, err := transaction.SetPlanAcceptance(context.Background(), domainplan.AcceptanceUpdate{
			ProjectID: 3, ID: 8, ExpectedUpdatedAt: staleAt, UpdatedAt: nextAt,
		})
		return err
	})
	if !errors.Is(err, repository.ErrVersionConflict) {
		t.Fatalf("stale acceptance error=%v", err)
	}
	backend.assertFinished(t, 0, 1)
}

func planTestColumns() []string {
	return []string{
		"id", "project_id", "issue_hash", "file_path", "hash", "status", "sort_order",
		"total_tasks", "completed_tasks", "validation_passed", "agent_cli_provider", "agent_cli_command",
		"codex_reasoning_effort", "plan_generation_strategy", "plan_generation_provider",
		"plan_generation_command", "plan_generation_model", "plan_generation_codex_reasoning_effort",
		"plan_generation_claude_config_id", "plan_execution_strategy", "plan_execution_provider",
		"plan_execution_command", "plan_execution_model", "plan_execution_codex_reasoning_effort",
		"plan_execution_claude_config_id", "plan_generation_duration_ms", "created_at", "updated_at", "accepted_at",
	}
}

func planTestValues(id, projectID int64, status, updatedAt string, acceptedAt *string) []driver.Value {
	var accepted driver.Value
	if acceptedAt != nil {
		accepted = *acceptedAt
	}
	return []driver.Value{
		id, projectID, "issue-digest", "docs/plan/fixture.md", "plan-digest", status, int64(1),
		int64(1), int64(1), int64(1), nil, "", nil,
		"external-cli-markdown", nil, "", "", nil, int64(0),
		"external-cli", nil, "", "", nil, int64(0), int64(0),
		intakeTestTime, updatedAt, accepted,
	}
}

var _ repository.PlanTransactional = (*Writer)(nil)
