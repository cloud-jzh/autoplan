package executors

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	domainautomation "github.com/lyming99/autoplan/backend/internal/domain/automation"
	domainfiles "github.com/lyming99/autoplan/backend/internal/domain/files"
)

type p12ExecutorFilePolicy struct{}

func (p12ExecutorFilePolicy) AuthorizeWorkingDirectory(_ context.Context, _ string, target string) (domainfiles.Decision, error) {
	return domainfiles.Decision{Allowed: true, ResolvedTarget: target}, nil
}

func TestP12ExecutorSpecPreservesArgumentBoundaries(t *testing.T) {
	workspace := filepath.Join("p12-fixture", "workspace")
	executor := domainautomation.Executor{
		ID: 21, Command: "fixture-tool", ArgsJSON: json.RawMessage(`["two words","--literal=;not-a-shell"]`),
		OptionsJSON: json.RawMessage(`{"cwd":"${workspace}","env":{"MODE":"fixture"},"timeoutMs":1000}`),
	}
	spec, err := processSpec(context.Background(), p12ExecutorFilePolicy{}, 7, workspace, executor, nil)
	if err != nil {
		t.Fatalf("process spec: %v", err)
	}
	if spec.Executable != "fixture-tool" || len(spec.Args) != 2 || spec.Args[0] != "two words" || spec.Args[1] != "--literal=;not-a-shell" {
		t.Fatalf("argument boundary lost: %#v", spec)
	}
	if spec.Environment["MODE"] != "fixture" || spec.Resource.ID != 21 {
		t.Fatalf("persisted metadata lost: %#v", spec)
	}
}

func TestP12ExecutorRejectsShellMetacharactersAndMalformedEnvironment(t *testing.T) {
	workspace := filepath.Join("p12-fixture", "workspace")
	for _, command := range []string{"fixture;whoami", "fixture|child", "fixture\nchild", "fixture$(child)"} {
		executor := domainautomation.Executor{ID: 22, Command: command, ArgsJSON: json.RawMessage(`[]`), OptionsJSON: json.RawMessage(`{}`)}
		if _, err := processSpec(context.Background(), p12ExecutorFilePolicy{}, 7, workspace, executor, nil); !errors.Is(err, ErrInvalidCommand) {
			t.Fatalf("unsafe command %q error=%v", command, err)
		}
	}
	if _, err := parseExecutorOptions(json.RawMessage(`{"env":{"MODE":"line\nbreak"}}`)); !errors.Is(err, ErrInvalidCommand) {
		t.Fatalf("multiline environment error=%v", err)
	}
}
