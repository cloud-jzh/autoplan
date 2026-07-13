package sqlite

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"testing"

	domainautomation "github.com/lyming99/autoplan/backend/internal/domain/automation"
	"github.com/lyming99/autoplan/backend/internal/repository"
)

func TestAutomationContractRejectsUnscopedReadsBeforeSQL(t *testing.T) {
	backend := &scriptBackend{}
	writer, cleanup := newTestWriter(t, backend)
	defer cleanup()

	err := writer.TransactAutomation(context.Background(), func(transaction repository.AutomationWriteTransaction) error {
		_, listErr := transaction.ListScripts(context.Background(), domainautomation.ListOptions{ProjectID: 0})
		return listErr
	})
	if !errors.Is(err, repository.ErrInvalidAutomation) {
		t.Fatalf("unscoped script list error=%v", err)
	}
	backend.assertFinished(t, 0, 1)
}

func TestAutomationContractBoundsPagesAndRejectsConcurrentVersionReuse(t *testing.T) {
	if boundedAutomationPage(0) != 100 || boundedAutomationPage(201) != 200 || boundedAutomationPage(2) != 2 {
		t.Fatal("automation page bounds drifted")
	}
	input := domainautomation.Reorder{
		ProjectID: 1, IDs: []int64{8, 8}, ExpectedVersion: map[int64]int64{8: 1}, UpdatedAt: "2026-07-11T00:00:01.000Z",
	}
	if domainautomation.ValidateReorder(input) == nil {
		t.Fatal("duplicate reorder ids must reject a concurrent stale retry")
	}
	if err := requireAutomationWrite(scriptResult{affected: 0}); !errors.Is(err, repository.ErrVersionConflict) {
		t.Fatalf("zero-row conditional write error=%v", err)
	}
}

func TestAutomationContractFailureRollsBackBeforeCommit(t *testing.T) {
	backend := &scriptBackend{}
	writer, cleanup := newTestWriter(t, backend)
	defer cleanup()

	err := writer.TransactAutomation(context.Background(), func(repository.AutomationWriteTransaction) error {
		return repository.ErrTransaction
	})
	if !errors.Is(err, repository.ErrTransaction) {
		t.Fatalf("transaction fault error=%v", err)
	}
	backend.assertFinished(t, 0, 1)
}

func TestAutomationContractImportLabelsRemainDeterministic(t *testing.T) {
	used := map[string]struct{}{"Build": {}, "Build (2)": {}}
	if got := uniqueExecutorLabel("Build", used); got != "Build (3)" {
		t.Fatalf("deduplicated label=%q", got)
	}
}

func TestAutomationContractRejectsDuplicateImportBeforeAnyWrite(t *testing.T) {
	backend := &scriptBackend{steps: []scriptStep{
		queryStep("SELECT 1 FROM projects", []string{"1"}, []driver.Value{int64(1)}),
		queryStep("FROM executors WHERE project_id", []string{}),
	}}
	writer, cleanup := newTestWriter(t, backend)
	defer cleanup()
	dedupe := false
	err := writer.TransactAutomation(context.Background(), func(transaction repository.AutomationWriteTransaction) error {
		_, importErr := transaction.ImportExecutors(context.Background(), domainautomation.Import{
			ProjectID: 1, Items: []domainautomation.ExecutorInput{automationTestExecutorInput("Build"), automationTestExecutorInput("Build")},
			DedupeLabels: &dedupe, UpdatedAt: "2026-07-11T00:00:01.000Z",
		})
		return importErr
	})
	if !errors.Is(err, repository.ErrDuplicate) {
		t.Fatalf("duplicate import error=%v", err)
	}
	backend.assertFinished(t, 0, 1)
}

func TestAutomationContractWriteFaultRollsBackExecutorCreate(t *testing.T) {
	backend := &scriptBackend{steps: []scriptStep{
		queryStep("SELECT 1 FROM projects", []string{"1"}, []driver.Value{int64(1)}),
		queryStep("SELECT EXISTS(SELECT 1 FROM executors", []string{"exists"}, []driver.Value{int64(0)}),
		execStep("INSERT INTO executors", 1, 1),
	}}
	writer, cleanup := newTestWriter(t, backend)
	defer cleanup()
	writer.faults.afterWrite = func(label string) error {
		if label == "executors:create" {
			return errors.New("injected executor write fault")
		}
		return nil
	}
	err := writer.TransactAutomation(context.Background(), func(transaction repository.AutomationWriteTransaction) error {
		_, createErr := transaction.CreateExecutor(context.Background(), domainautomation.ExecutorCreate{
			ProjectID: 1, Input: automationTestExecutorInput("Build"), CreatedAt: "2026-07-11T00:00:01.000Z",
		})
		return createErr
	})
	if !errors.Is(err, repository.ErrTransaction) {
		t.Fatalf("executor write fault error=%v", err)
	}
	backend.assertFinished(t, 0, 1)
}

func automationTestExecutorInput(label string) domainautomation.ExecutorInput {
	command := "fixture-command"
	args := json.RawMessage(`[]`)
	options := json.RawMessage(`{}`)
	presentation := json.RawMessage(`{}`)
	dependsOn := json.RawMessage(`[]`)
	return domainautomation.ExecutorInput{Label: &label, Command: &command, ArgsJSON: &args,
		OptionsJSON: &options, PresentationJSON: &presentation, DependsOnJSON: &dependsOn}
}
