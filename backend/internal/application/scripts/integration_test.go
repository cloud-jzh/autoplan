package scripts

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	domainautomation "github.com/lyming99/autoplan/backend/internal/domain/automation"
	domainfiles "github.com/lyming99/autoplan/backend/internal/domain/files"
	"github.com/lyming99/autoplan/backend/internal/repository"
)

type p12ScriptFilePolicy struct{ denySource bool }

func (policy p12ScriptFilePolicy) AuthorizeWorkingDirectory(_ context.Context, _ string, target string) (domainfiles.Decision, error) {
	return domainfiles.Decision{Allowed: true, ResolvedTarget: target}, nil
}

func (policy p12ScriptFilePolicy) AuthorizeScriptSource(_ context.Context, _ string, target string) (domainfiles.Decision, error) {
	if policy.denySource {
		return domainfiles.Decision{Allowed: false}, nil
	}
	return domainfiles.Decision{Allowed: true, ResolvedTarget: target}, nil
}

func TestP12ScriptSpecUsesPersistedInterpreterAndInlineBodyOnly(t *testing.T) {
	workspace := filepath.Join("p12-fixture", "workspace")
	request := &runRequest{
		service: &Service{files: p12ScriptFilePolicy{}},
		command: RunCommand{ProjectID: 7, Context: Context{}}, trigger: TriggerManual, stage: "manual",
	}
	script := domainautomation.Script{
		ID: 11, Runtime: "node", SourceType: "inline", Body: "console.log('fixture')",
		TimeoutSeconds: 30, ContextInject: "none",
	}
	spec, err := request.processSpec(context.Background(), repository.Project{ID: 7, WorkspacePath: workspace}, script)
	if err != nil {
		t.Fatalf("process spec: %v", err)
	}
	if spec.Executable != "node" || len(spec.Args) != 0 || spec.InlineScript == nil || string(spec.InlineScript.Body) != script.Body {
		t.Fatalf("unexpected closed script spec: %#v", spec)
	}
	if len(spec.Environment) != 0 || len(spec.Input) != 0 || spec.Resource.ID != script.ID {
		t.Fatalf("script spec exposed non-persisted runtime input: %#v", spec)
	}
}

func TestP12ScriptSourceAndContextFailClosedBeforeRunner(t *testing.T) {
	workspace := filepath.Join("p12-fixture", "workspace")
	request := &runRequest{
		service: &Service{files: p12ScriptFilePolicy{denySource: true}},
		command: RunCommand{ProjectID: 7, Context: Context{}}, trigger: TriggerManual, stage: "manual",
	}
	script := domainautomation.Script{ID: 12, Runtime: "node", SourceType: "file", Path: "scripts/fixture.cjs", TimeoutSeconds: 30}
	if err := request.authorizeSource(context.Background(), repository.Project{ID: 7, WorkspacePath: workspace}, script); !errors.Is(err, ErrInvalidCommand) {
		t.Fatalf("unapproved script source error=%v", err)
	}
	request.service.files = p12ScriptFilePolicy{}
	request.command.Context.ValidationCommand = "safe\nrunner-smuggling"
	if _, err := request.processSpec(context.Background(), repository.Project{ID: 7, WorkspacePath: workspace}, domainautomation.Script{
		ID: 12, Runtime: "node", SourceType: "inline", Body: "fixture", TimeoutSeconds: 30, ContextInject: "stdin",
	}); !errors.Is(err, ErrInvalidCommand) {
		t.Fatalf("multiline caller context error=%v", err)
	}
}
