package plans

import (
	"encoding/json"
	"testing"

	domainplan "github.com/lyming99/autoplan/backend/internal/domain/plan"
)

func TestPlanSnapshotSuppressesAbsoluteSourceReference(t *testing.T) {
	value, err := PlanSnapshot(domainplan.Plan{
		ID: 7, ProjectID: 3, IssueHash: "issue", SourceRef: `C:\private\plan.md`, Digest: "digest",
		Status: domainplan.StatusCompleted, SortOrder: 1, TotalTasks: 1, CompletedTasks: 1,
		PlanGeneration: domainplan.BackendConfig{Strategy: "external-cli-markdown"},
		PlanExecution:  domainplan.BackendConfig{Strategy: "external-cli"}, GenerationMillis: 0,
		CreatedAt: "2026-07-11T00:00:00.000Z", UpdatedAt: "2026-07-11T00:00:00.000Z",
	})
	if err != nil {
		t.Fatal(err)
	}
	var filePath string
	if err := json.Unmarshal(value["file_path"], &filePath); err != nil || filePath != "" {
		t.Fatalf("file_path=%q error=%v", filePath, err)
	}
	var title string
	if err := json.Unmarshal(value["title"], &title); err != nil || title != "Plan #7" {
		t.Fatalf("title=%q error=%v", title, err)
	}
}

func TestNormalizeAcceptanceTargetsDeduplicatesInCallerOrder(t *testing.T) {
	updatedAt := "2026-07-11T00:00:00.000Z"
	targets, err := normalizeAcceptanceTargets([]AcceptanceTarget{
		{TargetType: TargetPlan, ID: 2, ExpectedUpdatedAt: updatedAt},
		{TargetType: TargetPlan, ID: 2, ExpectedUpdatedAt: updatedAt},
		{TargetType: TargetTask, ID: 9, ExpectedUpdatedAt: updatedAt},
	})
	if err != nil || len(targets) != 2 || targets[0].ID != 2 || targets[1].ID != 9 {
		t.Fatalf("targets=%#v error=%v", targets, err)
	}
}
