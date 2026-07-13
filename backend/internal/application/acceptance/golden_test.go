package acceptance

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	domainplan "github.com/lyming99/autoplan/backend/internal/domain/plan"
)

func TestP07StateMachineGoldenCoversLegalAndRejectedAcceptanceStates(t *testing.T) {
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller unavailable")
	}
	data, err := os.ReadFile(filepath.Join(filepath.Dir(source), "..", "..", "..", "..", "fixtures", "migration", "p07", "state-machine-cases.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Scenarios []struct {
			ID       string         `json:"id"`
			Action   string         `json:"action"`
			Target   string         `json:"target"`
			Prestate map[string]any `json:"prestate"`
			Response struct {
				OK          bool   `json:"ok"`
				Error       string `json:"error"`
				Mutation    bool   `json:"mutation"`
				AuditEvents int    `json:"audit_events"`
			} `json:"response"`
		} `json:"scenarios"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	seen := make(map[string]bool)
	for _, scenario := range fixture.Scenarios {
		seen[scenario.ID] = true
		if !scenario.Response.OK && (scenario.Response.Mutation || scenario.Response.AuditEvents != 0 || scenario.Response.Error == "") {
			t.Fatalf("rejected transition changed durable state: %#v", scenario)
		}
		if scenario.ID == "accept-plan-completed" && !domainplan.IsAcceptablePlan(domainplan.StatusCompleted) {
			t.Fatal("completed plan must remain acceptable")
		}
		if scenario.ID == "accept-task-done" && !domainplan.IsAcceptableTask(domainplan.TaskDone) {
			t.Fatal("done task must remain acceptable")
		}
		if scenario.ID == "accept-running-task-rejected" && domainplan.IsAcceptableTask(domainplan.TaskRunning) {
			t.Fatal("running task must remain unacceptable")
		}
	}
	for _, id := range []string{
		"accept-plan-completed", "unaccept-plan-completed", "accept-task-done", "unaccept-task-passed",
		"redo-completed-plan-with-completed-tasks", "redo-completed-task", "reorder-complete-project-set",
		"delete-idle-plan-keeps-linked-intakes", "cross-project-target-rejected", "missing-target-rejected",
		"stale-reorder-rejected", "delete-running-plan-protected", "delete-plan-with-running-task-protected",
	} {
		if !seen[id] {
			t.Fatalf("state-machine case %q is missing", id)
		}
	}
}
