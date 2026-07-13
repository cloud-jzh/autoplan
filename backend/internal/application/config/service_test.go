package config

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

type fixedConfigClock struct{ value time.Time }

func (clock fixedConfigClock) Now() time.Time { return clock.value }

func TestNormalizeSettingsCanonicalizesBusinessTargets(t *testing.T) {
	settings, err := NormalizeSettings([]repository.SettingMutation{
		{Key: "mcp.port", Value: "043847", ExpectedVersion: 2},
		{Key: "mcp.enabled", Value: "ON", ExpectedVersion: 3},
		{Key: "mcp.authToken", Value: "  preserve-secret-bytes  ", ExpectedVersion: 4},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(settings) != 3 || settings[0].Key != "mcp.authToken" || settings[0].Value != "  preserve-secret-bytes  " ||
		settings[1].Value != "true" || settings[2].Value != "43847" {
		t.Fatalf("normalized settings = %#v", settings)
	}
	if _, err := NormalizeSettings([]repository.SettingMutation{{Key: "unscoped.setting", Value: "x"}}); !errors.Is(err, repository.ErrSettingNotWritable) {
		t.Fatalf("unknown setting = %v", err)
	}
	if _, err := NormalizeSettings([]repository.SettingMutation{{Key: "mcp.host", Value: "example.invalid"}}); !errors.Is(err, domainconfig.ErrInvalid) {
		t.Fatalf("non-loopback host = %v", err)
	}
}

func TestConfigureCommitsThenRereadsSanitizedVersionedSnapshot(t *testing.T) {
	store := newConfigStore()
	assembler := applicationsnapshot.New(applicationsnapshot.TransactionalReader(store))
	service := NewService(Dependencies{
		Assembler: assembler, Writer: store, Idempotency: applicationidempotency.New(),
		Clock: fixedConfigClock{time.Date(2026, 7, 11, 9, 0, 0, 0, time.UTC)},
	})
	value := domainconfig.DefaultLoopConfig()
	value.IntervalSeconds = 12
	value.PlanExecutionClaudeAuthToken = "non-working-config-secret"
	value.EnvVars = domainconfig.NormalizeEnvVars([]domainconfig.EnvVar{{Name: "PRIVATE", Value: "hidden"}})
	command := ConfigureCommand{
		ProjectID: 1, ExpectedVersion: 1, Config: value,
		Settings: []repository.SettingMutation{{Key: "mcp.enabled", Value: "false", ExpectedVersion: 1}},
		Metadata: MutationMetadata{CallerScope: "caller-digest", IdempotencyKey: "config-1", RequestID: "request-1"},
	}
	snapshot, err := service.Configure(context.Background(), command, domainproject.Visibility{})
	if err != nil || snapshot.State == nil || snapshot.ActiveProjectID == nil || *snapshot.ActiveProjectID != 1 {
		t.Fatalf("configure snapshot = %#v %v", snapshot, err)
	}
	var state map[string]any
	encodedState, _ := json.Marshal(snapshot.State)
	_ = json.Unmarshal(encodedState, &state)
	if state["version"] != float64(2) || state["interval_seconds"] != float64(12) ||
		state["env_vars"] != domainproject.RedactedEnvironment || state["plan_execution_claude_auth_token"] != "····cret" {
		t.Fatalf("state = %#v", state)
	}
	encoded, _ := json.Marshal(snapshot)
	if strings.Contains(string(encoded), "non-working-config-secret") || strings.Contains(string(encoded), "hidden") {
		t.Fatal("config snapshot leaked secret material")
	}
	if store.mutationCommits != 1 || store.readCommits != 1 {
		t.Fatalf("mutations=%d reads=%d", store.mutationCommits, store.readCommits)
	}

	// Same target with the latest version is a business no-op: no version bump.
	command.Metadata = MutationMetadata{}
	command.ExpectedVersion = 2
	command.Settings[0].ExpectedVersion = 2
	second, err := service.Configure(context.Background(), command, domainproject.Visibility{})
	if err != nil || second.State == nil {
		t.Fatalf("same-target configure = %#v %v", second, err)
	}
	encodedState, _ = json.Marshal(second.State)
	_ = json.Unmarshal(encodedState, &state)
	if state["version"] != float64(2) {
		t.Fatal("same-target configure incremented state version")
	}
}

func TestConfigureIdempotencyReplayBypassesStaleVersion(t *testing.T) {
	store := newConfigStore()
	assembler := applicationsnapshot.New(applicationsnapshot.TransactionalReader(store))
	service := NewService(Dependencies{
		Assembler: assembler, Writer: store, Idempotency: applicationidempotency.New(),
		Clock: fixedConfigClock{time.Date(2026, 7, 11, 9, 1, 0, 0, time.UTC)},
	})
	value := domainconfig.DefaultLoopConfig()
	value.IntervalSeconds = 8
	command := ConfigureCommand{
		ProjectID: 1, ExpectedVersion: 1, Config: value,
		Metadata: MutationMetadata{CallerScope: "caller", IdempotencyKey: "same", RequestID: "request"},
	}
	if _, err := service.Configure(context.Background(), command, domainproject.Visibility{}); err != nil {
		t.Fatal(err)
	}
	for _, operation := range store.operations {
		if operation.ProjectID == nil || *operation.ProjectID != 1 {
			t.Fatalf("idempotency project reference = %#v", operation.ProjectID)
		}
	}
	if _, err := service.Configure(context.Background(), command, domainproject.Visibility{}); err != nil {
		t.Fatalf("replay reached stale CAS: %v", err)
	}
	different := command
	different.Config.IntervalSeconds = 9
	if _, err := service.Configure(context.Background(), different, domainproject.Visibility{}); !errors.Is(err, repository.ErrIdempotencyKeyReuse) {
		t.Fatalf("key reuse = %v", err)
	}
}

func TestResetAndVersionConflictAreAtomic(t *testing.T) {
	store := newConfigStore()
	store.state.IntervalSeconds = 17
	store.state.Version = 4
	assembler := applicationsnapshot.New(applicationsnapshot.TransactionalReader(store))
	service := NewService(Dependencies{
		Assembler: assembler, Writer: store, Idempotency: applicationidempotency.New(),
		Clock: fixedConfigClock{time.Date(2026, 7, 11, 9, 2, 0, 0, time.UTC)},
	})
	if _, err := service.Reset(context.Background(), ResetCommand{ProjectID: 1, ExpectedVersion: 3}, domainproject.Visibility{}); !errors.Is(err, repository.ErrVersionConflict) {
		t.Fatalf("stale reset = %v", err)
	}
	if store.state.IntervalSeconds != 17 || store.readCommits != 0 {
		t.Fatal("stale reset changed state or assembled a response")
	}
	snapshot, err := service.Reset(context.Background(), ResetCommand{ProjectID: 1, ExpectedVersion: 4}, domainproject.Visibility{})
	if err != nil || snapshot.State == nil || store.state.IntervalSeconds != 5 || store.state.Version != 5 {
		t.Fatalf("reset = %#v %v", snapshot, err)
	}
}

type configStore struct {
	mu              sync.Mutex
	project         repository.Project
	state           repository.ProjectState
	settings        map[string]repository.Setting
	operations      map[string]repository.IdempotencyRecord
	closed          bool
	mutationCommits int
	readCommits     int
}

type configTransaction struct {
	store *configStore
	wrote bool
}

func newConfigStore() *configStore {
	state, _ := domainconfig.DefaultProjectState(1, "2026-07-11T08:00:00.000Z")
	return &configStore{
		project: repository.Project{ID: 1, Name: "Synthetic", CreatedAt: "2026-07-11T08:00:00.000Z", UpdatedAt: "2026-07-11T08:00:00.000Z"},
		state:   state, settings: map[string]repository.Setting{
			"mcp.enabled": {Key: "mcp.enabled", Value: "true", Version: 1},
		}, operations: make(map[string]repository.IdempotencyRecord),
	}
}

func (store *configStore) Check(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if store.closed {
		return repository.ErrClosed
	}
	return nil
}

func (store *configStore) Transact(ctx context.Context, operation func(repository.WriteTransaction) error) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.Check(ctx); err != nil {
		return err
	}
	copy := store.copy()
	tx := &configTransaction{store: copy}
	if err := operation(tx); err != nil {
		return err
	}
	store.project, store.state, store.settings, store.operations = copy.project, copy.state, copy.settings, copy.operations
	if tx.wrote {
		store.mutationCommits++
	} else {
		store.readCommits++
	}
	return nil
}

func (store *configStore) copy() *configStore {
	copy := &configStore{project: store.project, state: store.state, closed: store.closed}
	copy.settings = make(map[string]repository.Setting, len(store.settings))
	for key, value := range store.settings {
		copy.settings[key] = value
	}
	copy.operations = make(map[string]repository.IdempotencyRecord, len(store.operations))
	for key, value := range store.operations {
		copy.operations[key] = value
	}
	return copy
}

func (store *configStore) Close() error { store.closed = true; return nil }

func (tx *configTransaction) ListProjects(context.Context) ([]repository.Project, error) {
	return []repository.Project{tx.store.project}, nil
}
func (tx *configTransaction) GetProject(_ context.Context, id int64) (repository.Project, bool, error) {
	return tx.store.project, id == tx.store.project.ID, nil
}
func (tx *configTransaction) CreateProject(context.Context, domainproject.Create, string) (repository.Project, repository.ProjectState, error) {
	return repository.Project{}, repository.ProjectState{}, repository.ErrTransaction
}
func (tx *configTransaction) UpdateProject(context.Context, int64, domainproject.Update, string) (repository.Project, error) {
	return repository.Project{}, repository.ErrTransaction
}
func (tx *configTransaction) DeleteProject(context.Context, int64) error {
	return repository.ErrTransaction
}
func (tx *configTransaction) ListSettings(_ context.Context, prefix string) ([]repository.Setting, error) {
	result := make([]repository.Setting, 0)
	for key, setting := range tx.store.settings {
		if strings.HasPrefix(key, prefix) {
			result = append(result, setting)
		}
	}
	return result, nil
}
func (tx *configTransaction) PutSetting(_ context.Context, mutation repository.SettingMutation) (repository.Setting, bool, error) {
	setting, exists := tx.store.settings[mutation.Key]
	if !exists || setting.Version != mutation.ExpectedVersion {
		return repository.Setting{}, false, repository.ErrVersionConflict
	}
	if setting.Value == mutation.Value {
		return setting, false, nil
	}
	setting.Value, setting.Version = mutation.Value, setting.Version+1
	tx.store.settings[mutation.Key] = setting
	tx.wrote = true
	return setting, true, nil
}
func (tx *configTransaction) GetProjectState(_ context.Context, id int64) (repository.ProjectState, bool, error) {
	return tx.store.state, id == tx.store.state.ProjectID, nil
}
func (tx *configTransaction) PutLoopConfig(_ context.Context, id, expected int64, value repository.LoopConfig, now string) (repository.ProjectState, bool, error) {
	if id != tx.store.state.ProjectID {
		return repository.ProjectState{}, false, repository.ErrNotFound
	}
	if expected != tx.store.state.Version {
		return repository.ProjectState{}, false, repository.ErrVersionConflict
	}
	value, err := domainconfig.NormalizeLoopConfig(value)
	if err != nil {
		return repository.ProjectState{}, false, err
	}
	if domainconfig.Equal(domainconfig.LoopConfigFromState(tx.store.state), value) {
		return tx.store.state, false, nil
	}
	state := tx.store.state
	state.IntervalSeconds, state.ValidationCommand, state.ProjectPrompt = value.IntervalSeconds, value.ValidationCommand, value.ProjectPrompt
	state.AgentCLIProvider, state.AgentCLICommand, state.CodexReasoningEffort = value.AgentCLIProvider, value.AgentCLICommand, value.CodexReasoningEffort
	state.PlanGenerationStrategy, state.PlanGenerationProvider = value.PlanGenerationStrategy, value.PlanGenerationProvider
	state.PlanExecutionStrategy, state.PlanExecutionProvider = value.PlanExecutionStrategy, value.PlanExecutionProvider
	state.PlanExecutionClaudeAuthToken, state.EnvVars = value.PlanExecutionClaudeAuthToken, value.EnvVars
	state.Version++
	state.UpdatedAt = now
	tx.store.state = state
	tx.store.project.UpdatedAt = now
	tx.wrote = true
	return state, true, nil
}
func (tx *configTransaction) ResetLoopConfig(ctx context.Context, id, expected int64, now string) (repository.ProjectState, bool, error) {
	return tx.PutLoopConfig(ctx, id, expected, domainconfig.DefaultLoopConfig(), now)
}
func (tx *configTransaction) FindIdempotency(_ context.Context, scope, key string) (repository.IdempotencyRecord, bool, error) {
	record, exists := tx.store.operations[scope+"\x00"+key]
	return record, exists, nil
}
func (tx *configTransaction) ReserveIdempotency(_ context.Context, record repository.IdempotencyRecord) error {
	tx.store.operations[record.Scope+"\x00"+record.Key] = record
	return nil
}
func (tx *configTransaction) CompleteIdempotency(_ context.Context, scope, key, status string, result, failure *string, now string) error {
	record := tx.store.operations[scope+"\x00"+key]
	record.Status, record.ResultJSON, record.ErrorJSON, record.UpdatedAt = status, result, failure, now
	tx.store.operations[scope+"\x00"+key] = record
	return nil
}

var _ repository.Transactional = (*configStore)(nil)
var _ repository.WriteTransaction = (*configTransaction)(nil)
