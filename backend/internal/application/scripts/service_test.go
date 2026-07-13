package scripts

import (
	"path/filepath"
	"testing"
	"time"
)

func TestCronSundayAliasAndStepRemainDeterministic(t *testing.T) {
	expression, err := parseCron("*/5 9 12 7 0,7")
	if err != nil {
		t.Fatalf("parse cron: %v", err)
	}
	sunday := time.Date(2026, time.July, 12, 9, 10, 0, 0, time.Local)
	if !expression.due(sunday) {
		t.Fatal("Sunday aliases must match the same minute")
	}
	if expression.due(sunday.Add(time.Minute)) {
		t.Fatal("step expression must not run twice in adjacent minutes")
	}
}

func TestScriptPathUsesOnlyPersistedWorkspacePlaceholders(t *testing.T) {
	workspace, err := filepath.Abs(filepath.Join("fixture", "workspace"))
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	resolved, err := resolveScriptPath(`${planDir}`, workspace)
	if err != nil {
		t.Fatalf("resolve script path: %v", err)
	}
	want := filepath.Join(workspace, "docs", "plan")
	if resolved != want {
		t.Fatalf("resolved=%q want=%q", resolved, want)
	}
}
