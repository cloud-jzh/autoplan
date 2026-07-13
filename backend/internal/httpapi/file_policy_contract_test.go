package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lyming99/autoplan/backend/internal/config"
	domainfiles "github.com/lyming99/autoplan/backend/internal/domain/files"
	platformfs "github.com/lyming99/autoplan/backend/internal/platform/filesystem"
	"github.com/lyming99/autoplan/backend/internal/platform/session"
)

type filePolicyServiceFixture struct {
	policy    domainfiles.Policy
	err       error
	getCalls  int
	saveCalls int
	expected  int64
}

func (fixture *filePolicyServiceFixture) Get(ctx context.Context) (domainfiles.Policy, error) {
	fixture.getCalls++
	if err := ctx.Err(); err != nil {
		return domainfiles.Policy{}, err
	}
	return fixture.policy, fixture.err
}

func (fixture *filePolicyServiceFixture) Save(ctx context.Context, expected int64, policy domainfiles.Policy) (domainfiles.Policy, error) {
	fixture.saveCalls++
	fixture.expected = expected
	if err := ctx.Err(); err != nil {
		return domainfiles.Policy{}, err
	}
	if fixture.err != nil {
		return domainfiles.Policy{}, fixture.err
	}
	policy.Version = expected + 1
	fixture.policy = policy
	return policy, nil
}

func TestFilePolicyHTTPContractUsesVersionedSharedService(t *testing.T) {
	fixture := &filePolicyServiceFixture{policy: domainfiles.Policy{
		Scope: domainfiles.ScopeProject, AllowedRoots: []string{}, Version: 4,
	}}
	router, credential := newFilePolicyRouter(t, fixture)

	read := serveFilePolicyRequest(router, credential, http.MethodGet, "", "")
	if read.Code != http.StatusOK || fixture.getCalls != 1 ||
		!strings.Contains(read.Body.String(), `"allow_cross_project":false`) ||
		!strings.Contains(read.Body.String(), `"version":4`) ||
		strings.Contains(read.Body.String(), "allowCrossProject") {
		t.Fatalf("file policy read contract drifted: %s", read.Body.String())
	}

	write := serveFilePolicyRequest(router, credential, http.MethodPatch,
		`{"version":4,"scope":"all","allow_cross_project":false,"allowed_roots":[]}`, "policy-intent")
	if write.Code != http.StatusOK || fixture.saveCalls != 1 || fixture.expected != 4 ||
		!strings.Contains(write.Body.String(), `"version":5`) ||
		!strings.Contains(write.Body.String(), `"high_risk":true`) {
		t.Fatalf("file policy write contract drifted: %s", write.Body.String())
	}
	if strings.Contains(write.Body.String(), "policy-intent") || strings.Contains(write.Body.String(), credential) {
		t.Fatal("file policy response leaked transport identity")
	}
}

func TestFilePolicyHTTPContractRejectsVersionSecurityAndStableFailures(t *testing.T) {
	fixture := &filePolicyServiceFixture{policy: domainfiles.Policy{
		Scope: domainfiles.ScopeProject, AllowedRoots: []string{}, Version: 2,
	}}
	router, credential := newFilePolicyRouter(t, fixture)
	for _, body := range []string{
		`{}`,
		`{"version":0,"scope":"project","allow_cross_project":false,"allowed_roots":[]}`,
	} {
		response := serveFilePolicyRequest(router, credential, http.MethodPatch, body, "")
		assertContractError(t, response, http.StatusPreconditionRequired, string(CodeVersionRequired), false)
	}
	invalid := serveFilePolicyRequest(router, credential, http.MethodPatch,
		`{"version":2,"scope":"unknown","allow_cross_project":false,"allowed_roots":[]}`, "")
	assertContractError(t, invalid, http.StatusBadRequest, string(CodeInvalidConfig), false)
	unknown := serveFilePolicyRequest(router, credential, http.MethodPatch,
		`{"version":2,"scope":"project","allow_cross_project":false,"allowed_roots":[],"extra":true}`, "")
	assertContractError(t, unknown, http.StatusBadRequest, string(CodeInvalidJSON), false)

	fixture.err = domainfiles.ErrVersionConflict
	conflict := serveFilePolicyRequest(router, credential, http.MethodPatch,
		`{"version":2,"scope":"project","allow_cross_project":false,"allowed_roots":[]}`, "")
	assertContractError(t, conflict, http.StatusConflict, string(CodeVersionConflict), false)
	fixture.err = errors.New("synthetic private policy detail")
	internal := serveFilePolicyRequest(router, credential, http.MethodGet, "", "")
	assertContractError(t, internal, http.StatusInternalServerError, string(CodeInternal), false)
	if strings.Contains(internal.Body.String(), "synthetic private") {
		t.Fatal("file policy application detail escaped")
	}

	unauthorized := httptest.NewRequest(http.MethodGet, "http://"+testAuthority+FilePolicyPath, nil)
	unauthorized.Header.Set("Origin", testOrigin)
	unauthorizedResponse := httptest.NewRecorder()
	router.ServeHTTP(unauthorizedResponse, unauthorized)
	assertContractError(t, unauthorizedResponse, http.StatusUnauthorized, string(CodeUnauthorized), false)
}

func TestFilePolicyAttackMatrixDeniesBeforeSentinelMutation(t *testing.T) {
	base := t.TempDir()
	workspace := filepath.Join(base, "workspace")
	customRoot := filepath.Join(base, "custom")
	outside := filepath.Join(base, "outside")
	for _, directory := range []string{workspace, customRoot, outside} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	sentinel := filepath.Join(outside, "sentinel.txt")
	if err := os.WriteFile(sentinel, []byte("synthetic sentinel"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := sha256.Sum256(mustReadFixture(t, sentinel))
	resolver := platformfs.Resolver{}

	assertPolicyDenied(t, resolver, domainfiles.DefaultPolicy(), workspace,
		filepath.Join(workspace, "..", "outside", "sentinel.txt"), domainfiles.OperationReadFile)
	assertPolicyDenied(t, resolver, domainfiles.DefaultPolicy(), workspace,
		sentinel, domainfiles.OperationGenericWrite)
	custom := domainfiles.Policy{Scope: domainfiles.ScopeCustom, AllowedRoots: []string{customRoot}}
	assertPolicyDenied(t, resolver, custom, workspace, sentinel, domainfiles.OperationReadFile)
	all := domainfiles.Policy{Scope: domainfiles.ScopeAll, AllowedRoots: []string{}}
	decision, err := resolver.Authorize(domainfiles.AuthorizationRequest{
		Policy: all, Operation: domainfiles.OperationReadFile, WorkspaceRoot: workspace, TargetPath: sentinel,
	})
	if err != nil || !decision.Allowed || !decision.HighRisk {
		t.Fatalf("all-scope read decision=%#v err=%v", decision, err)
	}
	assertPolicyDenied(t, resolver, all, workspace, sentinel, domainfiles.OperationGenericWrite)

	controlled := filepath.Join(workspace, "docs", "plan", "plan_requirement_12_20260711-042008.md")
	controlledDecision, err := resolver.Authorize(domainfiles.AuthorizationRequest{
		Policy: all, Operation: domainfiles.OperationPlanMarkdown,
		WorkspaceRoot: workspace, TargetPath: controlled, AllowMissing: true,
	})
	if err != nil || !controlledDecision.Allowed || !controlledDecision.Controlled || controlledDecision.HighRisk {
		t.Fatalf("controlled plan decision=%#v err=%v", controlledDecision, err)
	}
	assertPolicyDenied(t, resolver, all, workspace,
		filepath.Join(outside, "plan_requirement_12_20260711-042008.md"), domainfiles.OperationPlanMarkdown)

	link := filepath.Join(workspace, "escape-link")
	if err := os.Symlink(outside, link); err == nil {
		assertPolicyDenied(t, resolver, domainfiles.DefaultPolicy(), workspace,
			filepath.Join(link, "sentinel.txt"), domainfiles.OperationReadFile)
	} else {
		junction := filepath.Join(workspace, "junction")
		if mkdirErr := os.MkdirAll(junction, 0o700); mkdirErr != nil {
			t.Fatal(mkdirErr)
		}
		junctionTarget := filepath.Join(junction, "target.txt")
		if writeErr := os.WriteFile(junctionTarget, []byte("synthetic"), 0o600); writeErr != nil {
			t.Fatal(writeErr)
		}
		injected := platformfs.Resolver{ReparseDetector: func(path string, _ os.FileInfo) bool {
			return path == junction
		}}
		assertPolicyDenied(t, injected, domainfiles.DefaultPolicy(), workspace, junctionTarget, domainfiles.OperationReadFile)
	}

	after := sha256.Sum256(mustReadFixture(t, sentinel))
	if before != after {
		t.Fatal("denied policy attack changed the external sentinel")
	}
}

func TestFilePolicyWindowsAliasesCannotEscapeComponentOrVolume(t *testing.T) {
	if !platformfs.WindowsPathWithin(`C:\Fixture\Workspace`, `c:/fixture/workspace/child.txt`) ||
		platformfs.WindowsPathWithin(`C:\Fixture\Workspace`, `C:\Fixture\Workspace-Escape\child.txt`) ||
		platformfs.WindowsPathWithin(`C:\Fixture\Workspace`, `D:\Fixture\Workspace\child.txt`) ||
		platformfs.WindowsPathWithin(`\\server\share\root`, `\\server\other\root\child.txt`) {
		t.Fatal("Windows case/component/volume boundary drifted")
	}
	for _, unsafe := range []string{
		`\\.\PhysicalDrive0`, `C:\safe\..\escape`, `C:\safe\file.txt:stream`, `C:\safe\CON`,
	} {
		if _, err := platformfs.NormalizeWindowsPath(unsafe); err == nil {
			t.Fatalf("unsafe Windows alias accepted: %q", unsafe)
		}
	}
}

func newFilePolicyRouter(t *testing.T, service FilePolicyService) (*Router, string) {
	t.Helper()
	clock := fixedClock{value: time.Date(2026, 7, 11, 10, 0, 0, 0, time.UTC)}
	logger := &recordingLogger{}
	manager, err := session.New(bytes.NewReader(bytes.Repeat([]byte{0x52}, 32)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.Close)
	origins, err := config.NewOriginSet([]string{testOrigin})
	if err != nil {
		t.Fatal(err)
	}
	security, err := NewSecurity(SecurityOptions{
		Sessions: manager, Origins: origins, ExpectedHost: config.DefaultListenHost,
		ExpectedPort: 43123, Logger: logger, Clock: clock,
	})
	if err != nil {
		t.Fatal(err)
	}
	router, err := NewRouter(RouterOptions{
		Application: &testApplication{}, Logger: logger, Clock: clock,
		RequestIDs: fixedRequestIDs{}, BodyLimitBytes: 4096,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := RegisterFilePolicy(router, security, service); err != nil {
		t.Fatal(err)
	}
	return router, string(manager.CredentialCopy())
}

func serveFilePolicyRequest(router http.Handler, credential, method, body, key string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, "http://"+testAuthority+FilePolicyPath, strings.NewReader(body))
	request.Header.Set("Origin", testOrigin)
	request.Header.Set(session.HeaderName, credential)
	if body != "" {
		request.Header.Set("Content-Type", contentTypeJSON)
	}
	if key != "" {
		request.Header.Set(IdempotencyKeyHeader, key)
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func assertPolicyDenied(t *testing.T, resolver platformfs.Resolver, policy domainfiles.Policy,
	workspace, target string, operation domainfiles.Operation) {
	t.Helper()
	_, err := resolver.Authorize(domainfiles.AuthorizationRequest{
		Policy: policy, Operation: operation, WorkspaceRoot: workspace, TargetPath: target,
	})
	if err == nil || domainfiles.ErrorCodeOf(err) == "" || strings.Contains(err.Error(), workspace) ||
		strings.Contains(err.Error(), target) {
		t.Fatalf("unsafe policy decision: error=%v", err)
	}
}

func mustReadFixture(t *testing.T, path string) []byte {
	t.Helper()
	value, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

var _ FilePolicyService = (*filePolicyServiceFixture)(nil)
