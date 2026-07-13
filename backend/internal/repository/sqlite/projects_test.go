package sqlite

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/lyming99/autoplan/backend/internal/repository"
)

func TestProjectSettingsAndStateQueriesPreserveStorageSemantics(t *testing.T) {
	root := t.TempDir()
	fixture := writeFixtureDatabase(t, root, "fixture.sqlite", nil)
	reader, err := Open(context.Background(), Options{Path: fixture, AllowedRoot: root, Kind: TargetFixture})
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()

	projects, err := reader.ListProjects(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 3 || projects[0].ID != 3 || projects[1].ID != 2 || projects[2].ID != 1 {
		t.Fatalf("unexpected updated_at DESC, id DESC order: %#v", projects)
	}
	project, exists, err := reader.GetProject(context.Background(), 1)
	if err != nil || !exists || project.Description != "" || project.WorkspacePath != "alpha" {
		t.Fatalf("project lookup lost empty/default semantics: %#v %v %v", project, exists, err)
	}
	if _, exists, err := reader.GetProject(context.Background(), 999); err != nil || exists {
		t.Fatalf("missing project was not distinguished: %v %v", exists, err)
	}

	settings, err := reader.ListSettings(context.Background(), "mcp.")
	if err != nil || len(settings) != 2 || settings[0].Key != "mcp.authToken" || settings[1].Key != "mcp.enabled" {
		t.Fatalf("settings prefix query drifted: %d %v", len(settings), err)
	}
	if none, err := reader.ListSettings(context.Background(), "missing."); err != nil || len(none) != 0 {
		t.Fatalf("empty settings prefix result drifted: %#v %v", none, err)
	}

	state, exists, err := reader.GetProjectState(context.Background(), 1)
	if err != nil || !exists {
		t.Fatalf("state lookup failed: %v %v", exists, err)
	}
	if state.Running != 0 || state.IntervalSeconds != 9 || state.CodexReasoningEffort != nil ||
		state.PlanGenerationProvider == nil || *state.PlanGenerationProvider != "claude" || state.LastError != nil ||
		state.PlanGenerationClaudeConfigID != 0 || state.PlanExecutionClaudeConfigID != 0 {
		t.Fatal("state null/default/type semantics drifted")
	}
	if _, exists, err := reader.GetProjectState(context.Background(), 3); err != nil || exists {
		t.Fatalf("missing state was not distinguished: %v %v", exists, err)
	}
}

func TestConcurrentReadsAreDetachedAndDoNotExposeMutationAPI(t *testing.T) {
	root := t.TempDir()
	fixture := writeFixtureDatabase(t, root, "fixture.sqlite", nil)
	reader, err := Open(context.Background(), Options{Path: fixture, AllowedRoot: root, Kind: TargetFixture})
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()

	if _, exposed := any(reader).(interface {
		Exec(context.Context, string, ...any) error
	}); exposed {
		t.Fatal("read-only repository exposes an Exec capability")
	}

	projects, err := reader.ListProjects(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	projects[0].Name = "mutated caller copy"
	again, err := reader.ListProjects(context.Background())
	if err != nil || again[0].Name == projects[0].Name {
		t.Fatal("project results share mutable caller storage")
	}

	const workers = 24
	var group sync.WaitGroup
	errorsSeen := make(chan error, workers)
	for index := 0; index < workers; index++ {
		group.Add(1)
		go func(projectID int64) {
			defer group.Done()
			if _, err := reader.ListProjects(context.Background()); err != nil {
				errorsSeen <- err
				return
			}
			_, _, err := reader.GetProjectState(context.Background(), projectID)
			if err != nil {
				errorsSeen <- err
			}
		}(int64(index%3 + 1))
	}
	group.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		t.Fatalf("concurrent read failed: %v", err)
	}
}

func TestUnavailableAndReaderSatisfyOnlyReadPorts(t *testing.T) {
	var _ repository.Readiness = repository.Unavailable{}
	var _ repository.ReadOnly = (*Reader)(nil)
	if err := (repository.Unavailable{}).Check(context.Background()); !errors.Is(err, repository.ErrNotConfigured) {
		t.Fatalf("unavailable readiness returned %v", err)
	}
}
