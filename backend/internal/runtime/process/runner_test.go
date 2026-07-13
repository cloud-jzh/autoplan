package process

import (
	"context"
	"strings"
	"testing"

	"github.com/lyming99/autoplan/backend/internal/config"
	domainfiles "github.com/lyming99/autoplan/backend/internal/domain/files"
)

type policyStub struct {
	decision domainfiles.Decision
	err      error
}

func (stub policyStub) AuthorizeWorkingDirectory(context.Context, string, string) (domainfiles.Decision, error) {
	return stub.decision, stub.err
}

func TestSpecValidationRejectsShellLikeExecutableInput(t *testing.T) {
	runtime := config.DefaultProcessRuntime()
	spec := Spec{ProjectID: 1, Workspace: "C:/workspace", WorkingDirectory: "C:/workspace", Executable: "tool\nother"}
	if spec.validate(runtime) != ErrInvalidSpec {
		t.Fatal("newline-bearing executable must be rejected")
	}
}

func TestNewRunnerRejectsEnvironmentOutsideAllowlist(t *testing.T) {
	_, err := NewRunner(Dependencies{
		Config:          config.DefaultProcessRuntime(),
		Policy:          policyStub{},
		BaseEnvironment: map[string]string{"UNAPPROVED": "value"},
	})
	if err != ErrRunnerUnavailable {
		t.Fatal("runner accepted an unallowlisted base environment")
	}
}

func TestResolveExecutableDoesNotConsultHostPath(t *testing.T) {
	if _, err := resolveExecutable("unconfigured-tool", nil); err != ErrSpawn {
		t.Fatal("bare executable without allowlisted PATH must be rejected")
	}
}

func TestOutputTailIsBoundedAndRedacted(t *testing.T) {
	budget := newOutputBudget(128, 16)
	collector := newOutputCollector(128, 16, 32, 8, budget)
	collector.append([]byte("TOKEN=s3cr3t\nC:/private/work/output\n"))
	output := collector.finalize(NewRedactor(map[string]string{"TOKEN": "s3cr3t"}, "C:/private/work"))
	if strings.Contains(output.Tail, "s3cr3t") || strings.Contains(output.Tail, "C:/private/work") {
		t.Fatal("redacted tail retained protected material")
	}
	if output.Bytes == 0 || output.Tail == "" {
		t.Fatal("collector did not retain bounded metadata")
	}
}

func TestVerifyWorkingDirectoryAcceptsPolicyResolvedDirectory(t *testing.T) {
	directory := t.TempDir()
	resolved, err := verifyWorkingDirectory(domainfiles.Decision{Allowed: true, ResolvedTarget: directory})
	if err != nil || resolved == "" {
		t.Fatal("policy-resolved existing directory must remain usable")
	}
}
