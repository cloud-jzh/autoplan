package filesystem

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	domainfiles "github.com/lyming99/autoplan/backend/internal/domain/files"
)

func TestWorkspaceCustomAndAllPolicyBoundaries(t *testing.T) {
	base := t.TempDir()
	workspace := filepath.Join(base, "workspace")
	allowed := filepath.Join(base, "allowed")
	outside := filepath.Join(base, "outside")
	for _, directory := range []string{workspace, allowed, outside} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	insideFile := writeFixture(t, filepath.Join(workspace, "inside.txt"))
	allowedFile := writeFixture(t, filepath.Join(allowed, "allowed.txt"))
	outFile := writeFixture(t, filepath.Join(outside, "outside.txt"))
	resolver := Resolver{}

	project := domainfiles.DefaultPolicy()
	assertAllowed(t, resolver, project, workspace, insideFile, domainfiles.OperationReadFile, false, false)
	assertDenied(t, resolver, project, workspace, allowedFile, domainfiles.OperationReadFile, domainfiles.CodeOutsideScope)
	rootDecision, err := resolver.Authorize(domainfiles.AuthorizationRequest{
		Policy: project, Operation: domainfiles.OperationSearchFiles, WorkspaceRoot: workspace, TargetPath: workspace,
	})
	if err != nil || !rootDecision.Allowed || rootDecision.DisplayPath != "." {
		t.Fatalf("root decision=%#v err=%v", rootDecision, err)
	}

	custom := domainfiles.Policy{Scope: domainfiles.ScopeCustom, AllowedRoots: []string{allowed}}
	assertAllowed(t, resolver, custom, workspace, allowedFile, domainfiles.OperationOpenFile, false, false)
	assertDenied(t, resolver, custom, workspace, outFile, domainfiles.OperationReadFile, domainfiles.CodeOutsideScope)

	legacyCrossProject := domainfiles.Policy{Scope: domainfiles.ScopeProject, AllowCrossProject: true, AllowedRoots: []string{allowed}}
	assertAllowed(t, resolver, legacyCrossProject, workspace, allowedFile, domainfiles.OperationReadFile, false, false)

	all := domainfiles.Policy{Scope: domainfiles.ScopeAll, AllowedRoots: []string{}}
	assertAllowed(t, resolver, all, workspace, outFile, domainfiles.OperationReadFile, false, true)
	assertDenied(t, resolver, all, workspace, outFile, domainfiles.OperationGenericWrite, domainfiles.CodeOutsideScope)
	assertDenied(t, resolver, all, workspace, outFile, domainfiles.OperationScript, domainfiles.CodeOutsideScope)
}

func TestMissingTargetsAndSymlinkEscapeFailClosed(t *testing.T) {
	base := t.TempDir()
	workspace := filepath.Join(base, "workspace")
	outside := filepath.Join(base, "outside")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	resolver := Resolver{}
	missing := filepath.Join(workspace, "new", "child.txt")
	assertAllowed(t, resolver, domainfiles.DefaultPolicy(), workspace, missing, domainfiles.OperationGenericWrite, true, false)
	assertDenied(t, resolver, domainfiles.DefaultPolicy(), workspace, filepath.Join("relative", "file"), domainfiles.OperationReadFile, domainfiles.CodeInvalidPath)
	assertDenied(t, resolver, domainfiles.DefaultPolicy(), workspace, workspace+string(filepath.Separator)+".."+string(filepath.Separator)+"outside", domainfiles.OperationReadFile, domainfiles.CodeInvalidPath)
	assertDenied(t, resolver, domainfiles.DefaultPolicy(), workspace, workspace+"\x00bad", domainfiles.OperationReadFile, domainfiles.CodeInvalidPath)

	link := filepath.Join(workspace, "escape")
	if err := os.Symlink(outside, link); err == nil {
		target := writeFixture(t, filepath.Join(outside, "secret.txt"))
		assertDenied(t, resolver, domainfiles.DefaultPolicy(), workspace, filepath.Join(link, filepath.Base(target)), domainfiles.OperationReadFile, domainfiles.CodeSymlinkEscape)
	} else {
		// Windows environments without symlink privilege still execute a
		// deterministic reparse/path-boundary matrix instead of skipping escape coverage.
		if WindowsPathWithin(`C:\workspace`, `C:\workspace-link\secret`) ||
			WindowsPathWithin(`C:\workspace`, `D:\workspace\secret`) {
			t.Fatal("Windows boundary substitute allowed an escape")
		}
	}
}

func TestControlledPlanAndProgressTargetsIgnoreBroadPolicy(t *testing.T) {
	workspace := t.TempDir()
	resolver := Resolver{}
	all := domainfiles.Policy{Scope: domainfiles.ScopeAll, AllowedRoots: []string{}}
	plan := filepath.Join(workspace, "docs", "plan", "plan_requirement_12_20260711-042008.md")
	decision, err := resolver.Authorize(domainfiles.AuthorizationRequest{
		Policy: all, Operation: domainfiles.OperationPlanMarkdown,
		WorkspaceRoot: workspace, TargetPath: plan, AllowMissing: true,
	})
	if err != nil || !decision.Allowed || !decision.Controlled || decision.HighRisk {
		t.Fatalf("controlled decision=%#v err=%v", decision, err)
	}
	progress := filepath.Join(workspace, "docs", "progress", "logs", "20260711-042008_execute-P005.log")
	assertAllowed(t, resolver, all, workspace, progress, domainfiles.OperationProgressLog, true, false)
	for _, target := range []string{
		filepath.Join(workspace, "docs", "plan", "arbitrary.txt"),
		filepath.Join(workspace, "other", "plan_requirement_12_20260711-042008.md"),
		filepath.Join(filepath.Dir(workspace), "plan_requirement_12_20260711-042008.md"),
	} {
		assertDenied(t, resolver, all, workspace, target, domainfiles.OperationPlanMarkdown, domainfiles.CodeControlledTarget)
	}
}

func TestWindowsPathMatrixUsesComponentAndVolumeBoundaries(t *testing.T) {
	allowed := []string{
		`C:\Work\Project`, `c:/work/project/file.txt`, `\\server\share\Root`, `\\SERVER\SHARE\root\child`,
		`\\?\C:\Work\Project\child`,
	}
	for _, value := range allowed {
		if _, err := NormalizeWindowsPath(value); err != nil {
			t.Fatalf("valid Windows path %q: %v", value, err)
		}
	}
	if !WindowsPathWithin(`C:\Work\Project`, `c:/work/project/child`) ||
		WindowsPathWithin(`C:\Work\Project`, `C:\Work\Project2`) {
		t.Fatal("case-insensitive component boundary drifted")
	}
	for _, value := range []string{
		`\\.\PhysicalDrive0`, `C:\safe\..\escape`, `C:\safe\file.txt:stream`,
		`C:\safe\CON`, `\\server`, `relative\path`,
	} {
		if _, err := NormalizeWindowsPath(value); err == nil {
			t.Fatalf("unsafe Windows path accepted: %q", value)
		}
	}
}

func TestInjectedReparseDetectorCoversJunctionEscapeWithoutPrivileges(t *testing.T) {
	workspace := t.TempDir()
	junction := filepath.Join(workspace, "junction")
	if err := os.MkdirAll(junction, 0o700); err != nil {
		t.Fatal(err)
	}
	target := writeFixture(t, filepath.Join(junction, "target.txt"))
	resolver := Resolver{ReparseDetector: func(path string, _ os.FileInfo) bool {
		return path == junction
	}}
	assertDenied(t, resolver, domainfiles.DefaultPolicy(), workspace, target, domainfiles.OperationReadFile, domainfiles.CodeSymlinkEscape)
}

func writeFixture(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("synthetic"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func assertAllowed(t *testing.T, resolver Resolver, policy domainfiles.Policy, workspace, target string, operation domainfiles.Operation, missing, highRisk bool) {
	t.Helper()
	decision, err := resolver.Authorize(domainfiles.AuthorizationRequest{
		Policy: policy, Operation: operation, WorkspaceRoot: workspace, TargetPath: target, AllowMissing: missing,
	})
	if err != nil || !decision.Allowed || decision.HighRisk != highRisk || decision.DisplayPath == "" {
		t.Fatalf("decision=%#v err=%v", decision, err)
	}
}

func assertDenied(t *testing.T, resolver Resolver, policy domainfiles.Policy, workspace, target string, operation domainfiles.Operation, code domainfiles.ErrorCode) {
	t.Helper()
	_, err := resolver.Authorize(domainfiles.AuthorizationRequest{
		Policy: policy, Operation: operation, WorkspaceRoot: workspace, TargetPath: target,
	})
	if err == nil || domainfiles.ErrorCodeOf(err) != code {
		t.Fatalf("error=%v code=%s want=%s", err, domainfiles.ErrorCodeOf(err), code)
	}
	if errors.Is(err, domainfiles.ErrOutsideScope) && (stringsContainsPath(err.Error(), workspace) || stringsContainsPath(err.Error(), target)) {
		t.Fatal("policy error leaked an absolute path")
	}
}

func stringsContainsPath(message, path string) bool {
	return path != "" && len(path) > 3 && contains(message, path)
}

func contains(value, fragment string) bool {
	for index := 0; index+len(fragment) <= len(value); index++ {
		if value[index:index+len(fragment)] == fragment {
			return true
		}
	}
	return false
}
