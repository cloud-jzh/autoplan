package files

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	domainfiles "github.com/lyming99/autoplan/backend/internal/domain/files"
	platformfs "github.com/lyming99/autoplan/backend/internal/platform/filesystem"
)

type policyConfigFixture struct {
	policy domainfiles.Policy
	puts   int
}

func (fixture *policyConfigFixture) GetFilePolicy(ctx context.Context) (domainfiles.Policy, error) {
	if err := ctx.Err(); err != nil {
		return domainfiles.Policy{}, err
	}
	result := fixture.policy
	result.AllowedRoots = append([]string(nil), result.AllowedRoots...)
	return result, nil
}

func (fixture *policyConfigFixture) PutFilePolicy(_ context.Context, expected int64, policy domainfiles.Policy) (domainfiles.Policy, error) {
	if expected <= 0 {
		return domainfiles.Policy{}, domainfiles.ErrVersionRequired
	}
	if expected != fixture.policy.Version {
		return domainfiles.Policy{}, domainfiles.ErrVersionConflict
	}
	fixture.puts++
	policy.Version = expected + 1
	fixture.policy = policy
	return policy, nil
}

func TestPolicyServiceUsesVersionedConfigAndNormalizedRoots(t *testing.T) {
	workspace := t.TempDir()
	allowed := filepath.Join(t.TempDir(), "allowed")
	if err := os.MkdirAll(allowed, 0o700); err != nil {
		t.Fatal(err)
	}
	config := &policyConfigFixture{policy: domainfiles.Policy{Scope: domainfiles.ScopeProject, AllowedRoots: []string{}, Version: 3}}
	service := New(config, platformfs.Resolver{})
	result, err := service.Save(context.Background(), 3, domainfiles.Policy{
		Scope: domainfiles.ScopeCustom, AllowCrossProject: true,
		AllowedRoots: []string{allowed, filepath.Join(allowed, ".")},
	})
	if err != nil || result.Version != 4 || len(result.AllowedRoots) != 1 || !filepath.IsAbs(result.AllowedRoots[0]) || config.puts != 1 {
		t.Fatalf("result=%#v puts=%d err=%v", result, config.puts, err)
	}
	target := filepath.Join(allowed, "new.txt")
	decision, err := service.Authorize(context.Background(), domainfiles.OperationGenericWrite, workspace, target, true)
	if err != nil || !decision.Allowed || decision.HighRisk {
		t.Fatalf("decision=%#v err=%v", decision, err)
	}
	if _, err := service.Save(context.Background(), 3, domainfiles.DefaultPolicy()); !errors.Is(err, domainfiles.ErrVersionConflict) {
		t.Fatalf("stale save=%v", err)
	}
}

func TestPolicyServiceAllIsHighRiskReadOnlyRelaxation(t *testing.T) {
	workspace := t.TempDir()
	outside := t.TempDir()
	target := filepath.Join(outside, "fixture.txt")
	if err := os.WriteFile(target, []byte("synthetic"), 0o600); err != nil {
		t.Fatal(err)
	}
	config := &policyConfigFixture{policy: domainfiles.Policy{Scope: domainfiles.ScopeAll, AllowedRoots: []string{}, Version: 1}}
	service := New(config, platformfs.Resolver{})
	read, err := service.Authorize(context.Background(), domainfiles.OperationReadFile, workspace, target, false)
	if err != nil || !read.Allowed || !read.HighRisk || read.DisplayPath != "<all-path>" {
		t.Fatalf("read=%#v err=%v", read, err)
	}
	if _, err := service.Authorize(context.Background(), domainfiles.OperationGenericWrite, workspace, target, false); !errors.Is(err, domainfiles.ErrOutsideScope) {
		t.Fatalf("all write=%v", err)
	}
}

func TestPolicyServiceRejectsMissingVersionInvalidAndNonexistentRoots(t *testing.T) {
	config := &policyConfigFixture{policy: domainfiles.Policy{Scope: domainfiles.ScopeProject, AllowedRoots: []string{}, Version: 1}}
	service := New(config, platformfs.Resolver{})
	if _, err := service.Save(context.Background(), 0, domainfiles.DefaultPolicy()); !errors.Is(err, domainfiles.ErrVersionRequired) {
		t.Fatalf("missing version=%v", err)
	}
	invalid := domainfiles.Policy{Scope: "unknown", AllowedRoots: []string{}}
	if _, err := service.Save(context.Background(), 1, invalid); !errors.Is(err, domainfiles.ErrInvalidPolicy) {
		t.Fatalf("invalid policy=%v", err)
	}
	missingRoot := domainfiles.Policy{Scope: domainfiles.ScopeCustom, AllowedRoots: []string{filepath.Join(t.TempDir(), "missing")}}
	if _, err := service.Save(context.Background(), 1, missingRoot); domainfiles.ErrorCodeOf(err) != domainfiles.CodeResolutionFailed {
		t.Fatalf("missing root=%v", err)
	}
}

var _ ConfigService = (*policyConfigFixture)(nil)
