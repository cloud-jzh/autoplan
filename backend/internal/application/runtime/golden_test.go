package runtime_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	domainloop "github.com/lyming99/autoplan/backend/internal/domain/loop"
	domainoperation "github.com/lyming99/autoplan/backend/internal/domain/operation"
)

type stateMachineFixture struct {
	RequiredFamilies []string `json:"required_families"`
	Cases            []struct {
		ID        string   `json:"id"`
		Family    string   `json:"family"`
		Operation string   `json:"operation"`
		Initial   string   `json:"initial"`
		Target    string   `json:"target"`
		Legal     bool     `json:"legal"`
		Events    []string `json:"events"`
		Backoff   []int    `json:"backoff_seconds"`
	} `json:"cases"`
}

func TestP11GoldenStateMachineMatrixCoversFrozenRuntimeFamilies(t *testing.T) {
	fixture := loadP11Fixture(t, "state-machine-cases.json")
	var matrix stateMachineFixture
	if err := json.Unmarshal(fixture, &matrix); err != nil {
		t.Fatalf("state machine fixture: %v", err)
	}
	if len(matrix.Cases) < 20 || len(matrix.RequiredFamilies) != 5 {
		t.Fatalf("incomplete matrix: %#v", matrix)
	}
	seen := make(map[string]struct{}, len(matrix.Cases))
	families := make(map[string]bool)
	for _, item := range matrix.Cases {
		if item.ID == "" || item.Operation == "" || item.Family == "" {
			t.Fatalf("unsafe matrix item: %#v", item)
		}
		if _, duplicate := seen[item.ID]; duplicate {
			t.Fatalf("duplicate matrix id: %s", item.ID)
		}
		seen[item.ID] = struct{}{}
		families[item.Family] = true
		if item.Legal && len(item.Events) == 0 {
			t.Fatalf("legal action without observable event sequence: %s", item.ID)
		}
	}
	for _, family := range matrix.RequiredFamilies {
		if !families[family] {
			t.Fatalf("missing required family %q", family)
		}
	}

	// Compare the public runtime action inventory with the sanitized Node
	// golden so a Go migration cannot silently drop a frozen action family.
	var golden struct {
		RuntimeActions []struct {
			ID string `json:"id"`
		} `json:"runtime_actions"`
	}
	if err := json.Unmarshal(loadP11Fixture(t, "node-runtime.golden.json"), &golden); err != nil {
		t.Fatalf("node golden: %v", err)
	}
	for _, action := range golden.RuntimeActions {
		found := false
		for _, item := range matrix.Cases {
			found = found || item.Operation == action.ID
		}
		if !found {
			t.Fatalf("state matrix action missing: %s", action.ID)
		}
	}
	for _, item := range matrix.Cases {
		if item.ID == "task_retry_backoff_schedule" {
			if len(item.Backoff) != 4 || item.Backoff[0] != 5 || item.Backoff[1] != 10 || item.Backoff[2] != 20 || item.Backoff[3] != 30 {
				t.Fatalf("retry backoff matrix drifted: %#v", item.Backoff)
			}
			return
		}
	}
	t.Fatal("retry backoff matrix case missing")
}

func TestP11LoopAndOperationStateTransitionsRemainDeterministic(t *testing.T) {
	state := domainloop.State{ProjectID: 7, WorkspaceConfigured: true, IntervalSeconds: 30, Phase: domainloop.PhaseIdle, Version: 1}
	started, changed, err := domainloop.Start(state)
	if err != nil || !changed || !started.Running || started.Phase != domainloop.PhaseRunning {
		t.Fatalf("start result=%#v changed=%t err=%v", started, changed, err)
	}
	replayed, changed, err := domainloop.Start(started)
	if err != nil || changed || replayed != started {
		t.Fatalf("replayed start result=%#v changed=%t err=%v", replayed, changed, err)
	}
	stopped, changed, err := domainloop.Stop(started)
	if err != nil || !changed || stopped.Running || stopped.Phase != domainloop.PhaseStopped {
		t.Fatalf("stop result=%#v changed=%t err=%v", stopped, changed, err)
	}
	if domainoperation.ResolveTransition(domainoperation.StatusRunning, domainoperation.StatusCancelled) != domainoperation.TransitionApply ||
		domainoperation.ResolveTransition(domainoperation.StatusCancelled, domainoperation.StatusSucceeded) != domainoperation.TransitionReject ||
		domainoperation.ResolveTransition(domainoperation.StatusCancelled, domainoperation.StatusCancelled) != domainoperation.TransitionNoop {
		t.Fatal("operation cancel/complete race contract drifted")
	}
}

func loadP11Fixture(t *testing.T, name string) []byte {
	t.Helper()
	directory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if info, statErr := os.Stat(filepath.Join(directory, "go.mod")); statErr == nil && !info.IsDir() {
			bytes, readErr := os.ReadFile(filepath.Join(filepath.Dir(directory), "fixtures", "migration", "p11", name))
			if readErr != nil {
				t.Fatal(readErr)
			}
			return bytes
		}
		next := filepath.Dir(directory)
		if next == directory {
			t.Fatal("repository root unavailable")
		}
		directory = next
	}
}
