package projects

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	applicationidempotency "github.com/lyming99/autoplan/backend/internal/application/idempotency"
	applicationsnapshot "github.com/lyming99/autoplan/backend/internal/application/snapshot"
	domainconfig "github.com/lyming99/autoplan/backend/internal/domain/config"
	domainproject "github.com/lyming99/autoplan/backend/internal/domain/project"
	"github.com/lyming99/autoplan/backend/internal/repository"
)

type fakeStore struct {
	projects []repository.Project
	states   map[int64]repository.ProjectState
	settings []repository.Setting
	err      error
	closed   bool
}

func (store *fakeStore) Check(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return store.err
}

func (store *fakeStore) ListProjects(context.Context) ([]repository.Project, error) {
	if store.err != nil {
		return nil, store.err
	}
	return append([]repository.Project(nil), store.projects...), nil
}

func (store *fakeStore) GetProject(_ context.Context, id int64) (repository.Project, bool, error) {
	if store.err != nil {
		return repository.Project{}, false, store.err
	}
	for _, project := range store.projects {
		if project.ID == id {
			return project, true, nil
		}
	}
	return repository.Project{}, false, nil
}

func (store *fakeStore) ListSettings(_ context.Context, prefix string) ([]repository.Setting, error) {
	if store.err != nil {
		return nil, store.err
	}
	result := make([]repository.Setting, 0)
	for _, setting := range store.settings {
		if strings.HasPrefix(setting.Key, prefix) {
			result = append(result, setting)
		}
	}
	return result, nil
}

func (store *fakeStore) GetProjectState(_ context.Context, id int64) (repository.ProjectState, bool, error) {
	if store.err != nil {
		return repository.ProjectState{}, false, store.err
	}
	state, exists := store.states[id]
	return state, exists, nil
}

func (store *fakeStore) Close() error {
	store.closed = true
	return nil
}

func TestListMatchesNodeOrderingStateSummaryAndVisibility(t *testing.T) {
	store := syntheticStore()
	service := NewService(store)
	visible, err := service.List(context.Background(), domainproject.Visibility{WorkspacePath: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(visible) != 3 || visible[0].ID != 3 || visible[1].ID != 2 || visible[2].ID != 1 {
		t.Fatal("project ordering drifted")
	}
	if visible[0].WorkspacePath == "" || visible[0].Running == nil || *visible[0].Running != 0 ||
		visible[0].Phase == nil || *visible[0].Phase != "idle" || visible[0].IntervalSeconds == nil ||
		*visible[0].IntervalSeconds != 5 || visible[0].AgentCLIProvider == nil || *visible[0].AgentCLIProvider != "codex" {
		t.Fatal("missing-state defaults or authorized workspace drifted")
	}
	if visible[2].AgentCLIProvider == nil || *visible[2].AgentCLIProvider != "claude" ||
		visible[2].Phase == nil || *visible[2].Phase != "stopped" {
		t.Fatal("stored state summary drifted")
	}
	hidden, err := service.List(context.Background(), domainproject.Visibility{})
	if err != nil {
		t.Fatal(err)
	}
	for _, project := range hidden {
		if project.WorkspacePath != "" {
			t.Fatal("unauthorized workspace path escaped")
		}
	}
}

func TestSnapshotHasCompleteShapeAndIrreversibleSecretFiltering(t *testing.T) {
	store := syntheticStore()
	service := NewService(store)
	projectID := int64(1)
	snapshot, err := service.Snapshot(context.Background(), &projectID, domainproject.Visibility{WorkspacePath: true})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.ActiveProjectID == nil || *snapshot.ActiveProjectID != 1 || snapshot.ActiveProject == nil ||
		snapshot.ActiveProject.WorkspacePath == "" || snapshot.ActiveProject.Running != nil || snapshot.State == nil {
		t.Fatal("active project snapshot semantics drifted")
	}
	if snapshot.Requirements == nil || snapshot.Feedback == nil || snapshot.Attachments == nil ||
		snapshot.Plans == nil || snapshot.Tasks == nil || snapshot.Events == nil || snapshot.Scans == nil ||
		snapshot.Scripts == nil || snapshot.Executors == nil || snapshot.Terminals == nil ||
		snapshot.ActiveOperations == nil {
		t.Fatal("snapshot contains a nil compatibility collection")
	}
	var state map[string]any
	stateBytes, err := json.Marshal(snapshot.State)
	if err != nil || json.Unmarshal(stateBytes, &state) != nil {
		t.Fatal("state could not be inspected")
	}
	if state["env_vars"] != domainproject.RedactedEnvironment ||
		state["plan_generation_claude_auth_token"] != "····alue" ||
		state["plan_generation_has_claude_auth_token"] != true ||
		state["plan_generation_claude_base_url"] != "https://example.invalid/claude" ||
		state["last_error"] != "<redacted_error>" {
		t.Fatal("state secret filtering drifted")
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	for _, forbidden := range []string{
		"fixture-private-environment", "fixture-sensitive-value", "fixture-mcp-secret",
		"fixture-user", "fixture-pass", "fixture-url-secret", "fixture-error-secret",
	} {
		if strings.Contains(text, forbidden) {
			t.Fatal("snapshot leaked raw secret material")
		}
	}
	if snapshot.Validate() != nil {
		t.Fatal("service emitted an invalid snapshot contract")
	}
	if store.states[1].EnvVars != "fixture-private-environment" ||
		store.states[1].PlanGenerationClaudeAuthToken != "fixture-sensitive-value" {
		t.Fatal("service mutated repository input")
	}
}

func TestEmptyAndMissingSnapshotsRemainDistinct(t *testing.T) {
	service := NewService(syntheticStore())
	empty, err := service.Snapshot(context.Background(), nil, domainproject.Visibility{})
	if err != nil {
		t.Fatal(err)
	}
	if empty.ActiveProjectID != nil || empty.ActiveProject != nil || empty.State != nil || len(empty.Projects) != 3 {
		t.Fatal("empty snapshot semantics drifted")
	}
	missing := int64(999)
	if _, err := service.Snapshot(context.Background(), &missing, domainproject.Visibility{}); !errors.Is(err, domainproject.ErrNotFound) {
		t.Fatalf("missing project returned %v", err)
	}
	if _, err := service.Get(context.Background(), 999, domainproject.Visibility{}); !errors.Is(err, domainproject.ErrNotFound) {
		t.Fatalf("missing single project returned %v", err)
	}
}

func TestServicePropagatesCancellationStableErrorsAndInvalidRows(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	service := NewService(syntheticStore())
	if _, err := service.List(cancelled, domainproject.Visibility{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled query returned %v", err)
	}
	if _, err := NewService(nil).List(context.Background(), domainproject.Visibility{}); !errors.Is(err, domainproject.ErrUnavailable) {
		t.Fatalf("nil store returned %v", err)
	}
	stable := errors.New("stable repository failure")
	if _, err := NewService(&fakeStore{err: stable}).List(context.Background(), domainproject.Visibility{}); !errors.Is(err, stable) {
		t.Fatalf("repository failure was replaced: %v", err)
	}
	invalid := syntheticStore()
	invalid.projects[0].UpdatedAt = "not-a-time"
	if _, err := NewService(invalid).List(context.Background(), domainproject.Visibility{}); !errors.Is(err, domainproject.ErrInvalidRecord) {
		t.Fatalf("invalid timestamp returned %v", err)
	}
}

func syntheticStore() *fakeStore {
	provider := "claude"
	return &fakeStore{
		projects: []repository.Project{
			{ID: 1, Name: "Synthetic Alpha", WorkspacePath: "/__autoplan_fixture__/p03/alpha", Description: "", CreatedAt: "2026-01-02T03:04:05Z", UpdatedAt: "2026-01-02T03:04:07Z"},
			{ID: 3, Name: "Synthetic Gamma", WorkspacePath: "/__autoplan_fixture__/p03/gamma", Description: "tie", CreatedAt: "2026-01-02T03:04:05Z", UpdatedAt: "2026-01-02T03:04:08Z"},
			{ID: 2, Name: "Synthetic Beta", WorkspacePath: "/__autoplan_fixture__/p03/beta", Description: "coverage", CreatedAt: "2026-01-02T03:04:05Z", UpdatedAt: "2026-01-02T03:04:08Z"},
		},
		states: map[int64]repository.ProjectState{
			1: {
				ProjectID: 1, Running: 1, Phase: "running", IntervalSeconds: 9,
				ProjectPrompt: "Synthetic prompt", AgentCLIProvider: provider,
				PlanGenerationStrategy: "external-cli-structured", PlanGenerationProvider: &provider,
				PlanGenerationClaudeBaseURL:   "https://fixture-user:fixture-pass@example.invalid/claude?auth_token=fixture-url-secret",
				PlanGenerationClaudeAuthToken: "fixture-sensitive-value", PlanGenerationClaudeModel: "synthetic-model",
				PlanExecutionStrategy: "external-cli", PlanExecutionProvider: stringPointer("codex"),
				PlanExecutionCodexReasoningEffort: stringPointer("medium"), EnvVars: "fixture-private-environment",
				LastError: stringPointer("token=fixture-error-secret"), UpdatedAt: "2026-01-02T03:04:07Z",
			},
			2: {
				ProjectID: 2, Phase: "stopped", IntervalSeconds: 5, ValidationCommand: "npm test",
				AgentCLIProvider: "codex", PlanGenerationStrategy: "external-cli-markdown",
				PlanExecutionStrategy: "external-cli", UpdatedAt: "2026-01-02T03:04:08Z",
			},
		},
		settings: []repository.Setting{
			{Key: "mcp.enabled", Value: "false"},
			{Key: "mcp.authToken", Value: "fixture-mcp-secret"},
			{Key: "unrelated.secret", Value: "must-not-be-read"},
		},
	}
}

func stringPointer(value string) *string { return &value }

var _ repository.ReadOnly = (*fakeStore)(nil)

type fixedProjectClock struct{ value time.Time }

func (clock fixedProjectClock) Now() time.Time { return clock.value }

func TestCreateReplaysIdempotentlyAndAssemblesOnlyCommittedRows(t *testing.T) {
	store := newMutationStore()
	assembler := applicationsnapshot.New(applicationsnapshot.TransactionalReader(store))
	service := NewServiceWithDependencies(Dependencies{
		Assembler: assembler, Writer: store, Idempotency: applicationidempotency.New(),
		Clock: fixedProjectClock{time.Date(2026, 7, 11, 8, 0, 0, 0, time.UTC)},
	})
	secret := "non-working-secret-value"
	config := domainconfig.DefaultLoopConfig()
	config.PlanExecutionClaudeAuthToken = secret
	config.EnvVars = domainconfig.NormalizeEnvVars([]domainconfig.EnvVar{{Name: "SYNTHETIC", Value: "private-value"}})
	command := CreateCommand{
		Project: domainproject.Create{Name: "  Synthetic  ", WorkspacePath: "fixture/workspace"}, Config: &config,
		Metadata: MutationMetadata{CallerScope: "caller-digest", IdempotencyKey: "create-1", RequestID: "request-1"},
	}
	first, err := service.Create(context.Background(), command, domainproject.Visibility{WorkspacePath: true})
	if err != nil || first.ActiveProjectID == nil || len(first.Projects) != 1 || first.State == nil {
		t.Fatalf("create snapshot = %#v %v", first, err)
	}
	second, err := service.Create(context.Background(), command, domainproject.Visibility{WorkspacePath: true})
	if err != nil || second.ActiveProjectID == nil || *second.ActiveProjectID != *first.ActiveProjectID {
		t.Fatalf("replay snapshot = %#v %v", second, err)
	}
	if store.projectCount() != 1 || store.businessWrites != 1 || store.readTransactions < 2 {
		t.Fatalf("projects=%d writes=%d reads=%d", store.projectCount(), store.businessWrites, store.readTransactions)
	}
	encoded, _ := json.Marshal(second)
	if strings.Contains(string(encoded), secret) || strings.Contains(string(encoded), "private-value") ||
		strings.Contains(store.idempotencyJSON(), secret) || strings.Contains(store.idempotencyJSON(), "caller-digest") {
		t.Fatal("snapshot or idempotency record leaked secret material")
	}
	different := command
	different.Project.Description = "different"
	if _, err := service.Create(context.Background(), different, domainproject.Visibility{}); !errors.Is(err, repository.ErrIdempotencyKeyReuse) {
		t.Fatalf("reused key error = %v", err)
	}
}

func TestMutationCommitFailureDoesNotPublishOrAssembleSnapshot(t *testing.T) {
	store := newMutationStore()
	store.failNextCommit = true
	assembler := applicationsnapshot.New(applicationsnapshot.TransactionalReader(store))
	service := NewServiceWithDependencies(Dependencies{
		Assembler: assembler, Writer: store, Idempotency: applicationidempotency.New(),
		Clock: fixedProjectClock{time.Date(2026, 7, 11, 8, 1, 0, 0, time.UTC)},
	})
	_, err := service.Create(context.Background(), CreateCommand{
		Project: domainproject.Create{Name: "Synthetic"},
	}, domainproject.Visibility{})
	if !errors.Is(err, repository.ErrCommit) || store.projectCount() != 0 || store.readTransactions != 0 {
		t.Fatalf("commit failure = %v projects=%d reads=%d", err, store.projectCount(), store.readTransactions)
	}
}

func TestUpdateVersionConflictRollsBackProjectAndDeleteErrorsStayStable(t *testing.T) {
	store := newMutationStore()
	store.seedProject(1, 3)
	assembler := applicationsnapshot.New(applicationsnapshot.TransactionalReader(store))
	service := NewServiceWithDependencies(Dependencies{
		Assembler: assembler, Writer: store, Idempotency: applicationidempotency.New(),
		Clock: fixedProjectClock{time.Date(2026, 7, 11, 8, 2, 0, 0, time.UTC)},
	})
	name := "Changed"
	config := domainconfig.DefaultLoopConfig()
	if _, err := service.Update(context.Background(), UpdateCommand{
		ProjectID: 1, Project: domainproject.Update{Name: &name}, Config: &config, ExpectedStateVersion: 2,
	}, domainproject.Visibility{}); !errors.Is(err, repository.ErrVersionConflict) {
		t.Fatalf("version conflict = %v", err)
	}
	project, _, _ := store.GetProject(context.Background(), 1)
	if project.Name != "Synthetic" {
		t.Fatal("project update escaped rolled-back version conflict")
	}
	store.deleteError = repository.ErrProjectRunning
	if _, err := service.Delete(context.Background(), DeleteCommand{ProjectID: 1}, domainproject.Visibility{}); !errors.Is(err, repository.ErrProjectRunning) {
		t.Fatalf("running delete = %v", err)
	}
}

type mutationStore struct {
	mu               sync.Mutex
	projects         []repository.Project
	states           map[int64]repository.ProjectState
	settings         map[string]repository.Setting
	operations       map[string]repository.IdempotencyRecord
	closed           bool
	failNextCommit   bool
	deleteError      error
	businessWrites   int
	readTransactions int
}

type mutationTransaction struct {
	store *mutationStore
	wrote bool
}

func newMutationStore() *mutationStore {
	return &mutationStore{
		states: make(map[int64]repository.ProjectState), settings: map[string]repository.Setting{
			"mcp.enabled": {Key: "mcp.enabled", Value: "true", Version: 1},
		}, operations: make(map[string]repository.IdempotencyRecord),
	}
}

func (store *mutationStore) seedProject(id, version int64) {
	state, _ := domainconfig.DefaultProjectState(id, "2026-07-11T07:00:00.000Z")
	state.Version = version
	store.projects = append(store.projects, repository.Project{
		ID: id, Name: "Synthetic", CreatedAt: "2026-07-11T07:00:00.000Z", UpdatedAt: "2026-07-11T07:00:00.000Z",
	})
	store.states[id] = state
}

func (store *mutationStore) Check(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if store.closed {
		return repository.ErrClosed
	}
	return nil
}

func (store *mutationStore) Transact(ctx context.Context, operation func(repository.WriteTransaction) error) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.Check(ctx); err != nil {
		return err
	}
	copy := store.clone()
	tx := &mutationTransaction{store: copy}
	if err := operation(tx); err != nil {
		return err
	}
	if store.failNextCommit {
		store.failNextCommit = false
		return repository.ErrCommit
	}
	store.projects, store.states, store.settings, store.operations = copy.projects, copy.states, copy.settings, copy.operations
	if tx.wrote {
		store.businessWrites++
	} else {
		store.readTransactions++
	}
	return nil
}

func (store *mutationStore) clone() *mutationStore {
	copy := newMutationStore()
	copy.projects = append([]repository.Project(nil), store.projects...)
	copy.states = make(map[int64]repository.ProjectState, len(store.states))
	for key, value := range store.states {
		copy.states[key] = value
	}
	copy.settings = make(map[string]repository.Setting, len(store.settings))
	for key, value := range store.settings {
		copy.settings[key] = value
	}
	copy.operations = make(map[string]repository.IdempotencyRecord, len(store.operations))
	for key, value := range store.operations {
		copy.operations[key] = value
	}
	copy.deleteError = store.deleteError
	return copy
}

func (store *mutationStore) Close() error { store.closed = true; return nil }

func (store *mutationStore) projectCount() int {
	store.mu.Lock()
	defer store.mu.Unlock()
	return len(store.projects)
}

func (store *mutationStore) idempotencyJSON() string {
	store.mu.Lock()
	defer store.mu.Unlock()
	encoded, _ := json.Marshal(store.operations)
	return string(encoded)
}

func (store *mutationStore) ListProjects(context.Context) ([]repository.Project, error) {
	return append([]repository.Project(nil), store.projects...), nil
}
func (store *mutationStore) GetProject(_ context.Context, id int64) (repository.Project, bool, error) {
	for _, project := range store.projects {
		if project.ID == id {
			return project, true, nil
		}
	}
	return repository.Project{}, false, nil
}

func (tx *mutationTransaction) ListProjects(ctx context.Context) ([]repository.Project, error) {
	return tx.store.ListProjects(ctx)
}
func (tx *mutationTransaction) GetProject(ctx context.Context, id int64) (repository.Project, bool, error) {
	return tx.store.GetProject(ctx, id)
}
func (tx *mutationTransaction) CreateProject(_ context.Context, input domainproject.Create, now string) (repository.Project, repository.ProjectState, error) {
	input = domainproject.NormalizeCreate(input)
	id := int64(len(tx.store.projects) + 1)
	project := repository.Project{ID: id, Name: input.Name, WorkspacePath: input.WorkspacePath, Description: input.Description, CreatedAt: now, UpdatedAt: now}
	state, err := domainconfig.DefaultProjectState(id, now)
	if err != nil {
		return repository.Project{}, repository.ProjectState{}, err
	}
	tx.store.projects = append(tx.store.projects, project)
	tx.store.states[id] = state
	tx.wrote = true
	return project, state, nil
}
func (tx *mutationTransaction) UpdateProject(_ context.Context, id int64, update domainproject.Update, now string) (repository.Project, error) {
	for index, current := range tx.store.projects {
		if current.ID == id {
			next, err := domainproject.ApplyUpdate(current, update)
			if err != nil {
				return repository.Project{}, err
			}
			next.UpdatedAt = now
			tx.store.projects[index] = next
			tx.wrote = true
			return next, nil
		}
	}
	return repository.Project{}, repository.ErrNotFound
}
func (tx *mutationTransaction) DeleteProject(_ context.Context, id int64) error {
	if tx.store.deleteError != nil {
		return tx.store.deleteError
	}
	for index, project := range tx.store.projects {
		if project.ID == id {
			tx.store.projects = append(tx.store.projects[:index], tx.store.projects[index+1:]...)
			delete(tx.store.states, id)
			tx.wrote = true
			return nil
		}
	}
	return repository.ErrNotFound
}
func (tx *mutationTransaction) ListSettings(_ context.Context, prefix string) ([]repository.Setting, error) {
	result := make([]repository.Setting, 0)
	for key, setting := range tx.store.settings {
		if strings.HasPrefix(key, prefix) {
			result = append(result, setting)
		}
	}
	return result, nil
}
func (tx *mutationTransaction) PutSetting(_ context.Context, mutation repository.SettingMutation) (repository.Setting, bool, error) {
	current, exists := tx.store.settings[mutation.Key]
	if !exists || current.Version != mutation.ExpectedVersion {
		return repository.Setting{}, false, repository.ErrVersionConflict
	}
	if current.Value == mutation.Value {
		return current, false, nil
	}
	current.Value, current.Version = mutation.Value, current.Version+1
	tx.store.settings[mutation.Key] = current
	tx.wrote = true
	return current, true, nil
}
func (tx *mutationTransaction) GetProjectState(_ context.Context, id int64) (repository.ProjectState, bool, error) {
	state, exists := tx.store.states[id]
	return state, exists, nil
}
func (tx *mutationTransaction) PutLoopConfig(_ context.Context, id, expected int64, value repository.LoopConfig, now string) (repository.ProjectState, bool, error) {
	state, exists := tx.store.states[id]
	if !exists {
		return repository.ProjectState{}, false, repository.ErrNotFound
	}
	if state.Version != expected {
		return repository.ProjectState{}, false, repository.ErrVersionConflict
	}
	value, err := domainconfig.NormalizeLoopConfig(value)
	if err != nil {
		return repository.ProjectState{}, false, err
	}
	if domainconfig.Equal(domainconfig.LoopConfigFromState(state), value) {
		return state, false, nil
	}
	state = applyFakeConfig(state, value)
	state.Version++
	state.UpdatedAt = now
	tx.store.states[id] = state
	tx.wrote = true
	return state, true, nil
}
func (tx *mutationTransaction) ResetLoopConfig(ctx context.Context, id, expected int64, now string) (repository.ProjectState, bool, error) {
	return tx.PutLoopConfig(ctx, id, expected, domainconfig.DefaultLoopConfig(), now)
}
func (tx *mutationTransaction) FindIdempotency(_ context.Context, scope, key string) (repository.IdempotencyRecord, bool, error) {
	record, exists := tx.store.operations[scope+"\x00"+key]
	return record, exists, nil
}
func (tx *mutationTransaction) ReserveIdempotency(_ context.Context, record repository.IdempotencyRecord) error {
	tx.store.operations[record.Scope+"\x00"+record.Key] = record
	return nil
}
func (tx *mutationTransaction) CompleteIdempotency(_ context.Context, scope, key, status string, result, failure *string, now string) error {
	record, exists := tx.store.operations[scope+"\x00"+key]
	if !exists {
		return repository.ErrNotFound
	}
	record.Status, record.ResultJSON, record.ErrorJSON, record.UpdatedAt = status, result, failure, now
	tx.store.operations[scope+"\x00"+key] = record
	return nil
}

func applyFakeConfig(state repository.ProjectState, value repository.LoopConfig) repository.ProjectState {
	state.IntervalSeconds, state.ValidationCommand, state.ProjectPrompt = value.IntervalSeconds, value.ValidationCommand, value.ProjectPrompt
	state.AgentCLIProvider, state.AgentCLICommand, state.CodexReasoningEffort = value.AgentCLIProvider, value.AgentCLICommand, value.CodexReasoningEffort
	state.PlanGenerationStrategy, state.PlanGenerationProvider = value.PlanGenerationStrategy, value.PlanGenerationProvider
	state.PlanGenerationCommand, state.PlanGenerationModel = value.PlanGenerationCommand, value.PlanGenerationModel
	state.PlanGenerationClaudeAuthToken, state.PlanGenerationClaudeConfigID = value.PlanGenerationClaudeAuthToken, value.PlanGenerationClaudeConfigID
	state.PlanExecutionStrategy, state.PlanExecutionProvider = value.PlanExecutionStrategy, value.PlanExecutionProvider
	state.PlanExecutionCommand, state.PlanExecutionModel = value.PlanExecutionCommand, value.PlanExecutionModel
	state.PlanExecutionClaudeAuthToken, state.PlanExecutionClaudeConfigID = value.PlanExecutionClaudeAuthToken, value.PlanExecutionClaudeConfigID
	state.EnvVars = value.EnvVars
	return state
}

var _ repository.Transactional = (*mutationStore)(nil)
var _ repository.WriteTransaction = (*mutationTransaction)(nil)
