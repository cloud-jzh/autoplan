package automation

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	domainautomation "github.com/lyming99/autoplan/backend/internal/domain/automation"
)

func TestGoldenStaticAutomationDTOsAreSanitized(t *testing.T) {
	projectID := int64(7)
	lastLog := "private-log-material"
	script := scriptDTO(domainautomation.Script{
		ID: 1, ProjectID: &projectID, Name: "fixture", Path: "private-script-path", Runtime: "node", Body: "private-script-body",
		Description: "fixture", TriggerMode: "manual", Enabled: true, WorkDir: "private-workdir", TimeoutSeconds: 60,
		ContextInject: "none", LastLog: &lastLog, CreatedAt: "2026-07-11T00:00:00.000Z", UpdatedAt: "2026-07-11T00:00:01.000Z", SourceType: "inline", Version: 1,
	})
	executor := executorDTO(domainautomation.Executor{
		ID: 2, ProjectID: projectID, Label: "fixture", Type: "shell", Command: "private-command", ArgsJSON: json.RawMessage(`["private-argument"]`),
		OptionsJSON: json.RawMessage(`{"cwd":"private-workdir","env":{"FIXTURE":"value"}}`), PresentationJSON: json.RawMessage(`{}`), DependsOnJSON: json.RawMessage(`[]`),
		DependsOrder: "parallel", Enabled: true, LastLog: &lastLog, CreatedAt: "2026-07-11T00:00:00.000Z", UpdatedAt: "2026-07-11T00:00:01.000Z", Version: 1,
	})

	encoded, err := json.Marshal(struct {
		Script   ScriptDTO   `json:"script"`
		Executor ExecutorDTO `json:"executor"`
	}{script, executor})
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	for _, forbidden := range []string{"private-script-path", "private-script-body", "private-workdir", "private-command", "private-argument", "private-log-material"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("static DTO leaked %q", forbidden)
		}
	}
	if !script.HasPath || !script.HasBody || !script.HasWorkDir || !script.HasLastLog || !executor.HasCommand || executor.ArgumentCount != 1 || executor.OptionsEnvKeyCount != 1 {
		t.Fatal("static DTO presence metadata drifted")
	}
}

func TestGoldenAutomationRuntimeCapabilitiesRemainClosed(t *testing.T) {
	service := NewService(Dependencies{})
	for _, err := range []error{
		service.RunScript(context.Background(), 1, 1), service.StopScript(context.Background(), 1, 1),
		service.RunExecutor(context.Background(), 1, 1), service.StopExecutor(context.Background(), 1, 1),
		service.RunExecutorAction(context.Background(), 1, 1, "fixture"),
	} {
		if !errors.Is(err, ErrRuntimeDisabled) {
			t.Fatalf("runtime capability error=%v", err)
		}
	}
}
