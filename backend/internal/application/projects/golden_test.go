package projects

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	applicationconfig "github.com/lyming99/autoplan/backend/internal/application/config"
	applicationidempotency "github.com/lyming99/autoplan/backend/internal/application/idempotency"
	applicationsnapshot "github.com/lyming99/autoplan/backend/internal/application/snapshot"
	domainconfig "github.com/lyming99/autoplan/backend/internal/domain/config"
	"github.com/lyming99/autoplan/backend/internal/domain/contracts"
	domainproject "github.com/lyming99/autoplan/backend/internal/domain/project"
	"github.com/lyming99/autoplan/backend/internal/repository"
	"github.com/lyming99/autoplan/backend/internal/repository/sqlite"
)

type goldenExport struct {
	Projects        []contracts.Project   `json:"projects"`
	EmptySnapshot   contracts.AppSnapshot `json:"emptySnapshot"`
	ProjectSnapshot contracts.AppSnapshot `json:"projectSnapshot"`
	MissingSnapshot string                `json:"missingSnapshot"`
}

// TestGoldenExport is invoked serially by compare-golden.js after sql.js has
// closed. With no controlled environment it skips and discovers no database.
func TestGoldenExport(t *testing.T) {
	databasePath := os.Getenv("AUTOPLAN_P03_DATABASE")
	allowedRoot := os.Getenv("AUTOPLAN_P03_ALLOWED_ROOT")
	outputPath := os.Getenv("AUTOPLAN_P03_GO_OUTPUT")
	projectIDText := os.Getenv("AUTOPLAN_P03_PROJECT_ID")
	if databasePath == "" && allowedRoot == "" && outputPath == "" && projectIDText == "" {
		t.Skip("controlled golden export environment is absent")
	}
	if databasePath == "" || allowedRoot == "" || outputPath == "" || projectIDText == "" ||
		!filepath.IsAbs(databasePath) || !filepath.IsAbs(allowedRoot) || !filepath.IsAbs(outputPath) {
		t.Fatal("controlled golden export environment is incomplete")
	}
	outputRelative, err := filepath.Rel(filepath.Clean(allowedRoot), filepath.Clean(outputPath))
	if err != nil || outputRelative == "." || outputRelative == ".." || filepath.IsAbs(outputRelative) ||
		len(outputRelative) >= 3 && outputRelative[:3] == ".."+string(filepath.Separator) {
		t.Fatal("controlled golden output is outside the allowed root")
	}
	projectID, err := strconv.ParseInt(projectIDText, 10, 64)
	if err != nil || projectID <= 0 || strconv.FormatInt(projectID, 10) != projectIDText {
		t.Fatal("controlled project id is invalid")
	}
	before, err := os.ReadFile(databasePath)
	if err != nil {
		t.Fatal("fixture could not be read")
	}
	reader, err := sqlite.Open(context.Background(), sqlite.Options{
		Path: databasePath, AllowedRoot: allowedRoot, Kind: sqlite.TargetFixture,
	})
	if err != nil {
		t.Fatal("fixture could not be opened read-only")
	}
	t.Cleanup(func() { _ = reader.Close() })
	service := NewService(reader)
	visibility := domainproject.Visibility{WorkspacePath: true}
	projects, err := service.List(context.Background(), visibility)
	if err != nil {
		t.Fatal("projects export failed")
	}
	empty, err := service.Snapshot(context.Background(), nil, visibility)
	if err != nil {
		t.Fatal("empty snapshot export failed")
	}
	projectSnapshot, err := service.Snapshot(context.Background(), &projectID, visibility)
	if err != nil {
		t.Fatal("project snapshot export failed")
	}
	missingID := int64(1<<62 - 1)
	if _, err := service.Snapshot(context.Background(), &missingID, visibility); !errors.Is(err, domainproject.ErrNotFound) {
		t.Fatal("missing snapshot semantics drifted")
	}
	if err := reader.Close(); err != nil {
		t.Fatal("read-only fixture close failed")
	}
	after, err := os.ReadFile(databasePath)
	if err != nil || sha256.Sum256(before) != sha256.Sum256(after) {
		t.Fatal("Go read changed fixture bytes")
	}
	writeGoldenExport(t, outputPath, goldenExport{
		Projects: projects, EmptySnapshot: empty, ProjectSnapshot: projectSnapshot,
		MissingSnapshot: "project_not_found",
	})
}

func writeGoldenExport(t *testing.T, outputPath string, value goldenExport) {
	t.Helper()
	file, err := os.OpenFile(outputPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal("golden output could not be created")
	}
	encoder := json.NewEncoder(file)
	encoder.SetEscapeHTML(true)
	encodeErr := encoder.Encode(value)
	closeErr := file.Close()
	if encodeErr != nil || closeErr != nil {
		t.Fatal("golden output could not be written")
	}
}

type mutationGoldenExport struct {
	SchemaVersion int              `json:"schemaVersion"`
	Version       string           `json:"version"`
	FixtureRoot   string           `json:"fixtureRoot"`
	Database      map[string]any   `json:"database"`
	Scenarios     []map[string]any `json:"scenarios"`
	Handoff       map[string]bool  `json:"handoff"`
}

type sequenceGoldenClock struct{ next time.Time }

func (clock *sequenceGoldenClock) Now() time.Time {
	value := clock.next
	clock.next = clock.next.Add(time.Second)
	return value
}

// TestMutationGoldenExport executes the same frozen intent sequence through
// the shared application services. It writes only to an explicitly authorized
// temporary output after all in-memory transactions have completed.
func TestMutationGoldenExport(t *testing.T) {
	allowedRoot := os.Getenv("AUTOPLAN_P05_ALLOWED_ROOT")
	outputPath := os.Getenv("AUTOPLAN_P05_GO_OUTPUT")
	if allowedRoot == "" && outputPath == "" {
		t.Skip("controlled P05 mutation export environment is absent")
	}
	if allowedRoot == "" || outputPath == "" || !filepath.IsAbs(allowedRoot) || !filepath.IsAbs(outputPath) ||
		!pathInsideGoldenRoot(allowedRoot, outputPath) {
		t.Fatal("controlled P05 mutation export environment is invalid")
	}
	workspaceAlpha := filepath.Join(allowedRoot, "workspace-alpha")
	workspaceBeta := filepath.Join(allowedRoot, "workspace-beta")
	for _, directory := range []string{workspaceAlpha, workspaceBeta} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal("synthetic workspace could not be created")
		}
	}

	store := newMutationStore()
	seedGoldenDefaultProject(t, store)
	seedGoldenSettings(store)
	clock := &sequenceGoldenClock{next: time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC)}
	assembler := applicationsnapshot.New(applicationsnapshot.TransactionalReader(store))
	idempotency := applicationidempotency.New()
	projectService := NewServiceWithDependencies(Dependencies{
		Assembler: assembler, Writer: store, Idempotency: idempotency, Clock: clock,
	})
	configService := applicationconfig.NewService(applicationconfig.Dependencies{
		Assembler: assembler, Writer: store, Idempotency: idempotency, Clock: clock,
	})
	visibility := domainproject.Visibility{WorkspacePath: true}
	ctx := context.Background()
	scenarios := make([]map[string]any, 0, 11)
	databaseBefore := goldenStoreHash(t, store)

	createRequest := map[string]any{
		"name": "  Alpha Project  ", "workspacePath": workspaceAlpha,
		"description": "Synthetic project", "intervalSeconds": 7,
		"validationCommand": "<redacted-command>", "projectPrompt": "Synthetic prompt",
		"agentCliProvider": "CODEX", "codexReasoningEffort": "HIGH",
	}
	createConfig := goldenCreateConfig()
	created, err := projectService.Create(ctx, CreateCommand{
		Project: domainproject.Create{Name: "  Alpha Project  ", WorkspacePath: workspaceAlpha, Description: "Synthetic project"},
		Config:  &createConfig,
	}, visibility)
	projectID := int64(2)
	if err == nil {
		completeGoldenFixtureState(t, store, projectID, createConfig)
		created, err = projectService.Snapshot(ctx, &projectID, visibility)
	}
	appendGoldenSuccess(t, &scenarios, "create", createRequest, created, err, nil)

	duplicate, err := projectService.Create(ctx, CreateCommand{
		Project: domainproject.Create{Name: "  Alpha Project  ", WorkspacePath: workspaceAlpha, Description: "Synthetic project"},
		Config:  &createConfig,
	}, visibility)
	duplicateID := int64(3)
	if err == nil {
		completeGoldenFixtureState(t, store, duplicateID, createConfig)
		duplicate, err = projectService.Snapshot(ctx, &duplicateID, visibility)
	}
	appendGoldenSuccess(t, &scenarios, "duplicate-create", createRequest, duplicate, err,
		map[string]any{"observation": "legacy Node creates a distinct project"})
	deletedDuplicate, err := projectService.Delete(ctx, DeleteCommand{ProjectID: duplicateID}, visibility)
	appendGoldenSuccess(t, &scenarios, "delete-duplicate-create", map[string]any{"projectId": duplicateID}, deletedDuplicate, err, nil)

	updatedName := "  Updated Alpha  "
	updatedWorkspace := workspaceBeta
	updated, err := projectService.Update(ctx, UpdateCommand{
		ProjectID: projectID,
		Project:   domainproject.Update{Name: &updatedName, WorkspacePath: &updatedWorkspace},
	}, visibility)
	appendGoldenSuccess(t, &scenarios, "update", map[string]any{
		"projectId": projectID, "name": updatedName, "workspacePath": workspaceBeta, "description": nil,
	}, updated, err, nil)

	configureRequest := goldenConfigureRequest(projectID)
	configured, err := configService.Configure(ctx, applicationconfig.ConfigureCommand{
		ProjectID: projectID, ExpectedVersion: 2, Config: goldenConfiguredLoop(),
		Settings: goldenMCPMutations(store),
	}, visibility)
	if err == nil {
		completeGoldenFixtureState(t, store, projectID, goldenConfiguredLoop())
		configured, err = projectService.Snapshot(ctx, &projectID, visibility)
	}
	appendGoldenSuccess(t, &scenarios, "configure", configureRequest, configured, err, nil)
	configuredAgain, err := configService.Configure(ctx, applicationconfig.ConfigureCommand{
		ProjectID: projectID, ExpectedVersion: 3, Config: goldenConfiguredLoop(),
		Settings: goldenMCPMutations(store),
	}, visibility)
	appendGoldenSuccess(t, &scenarios, "duplicate-configure", configureRequest, configuredAgain, err,
		map[string]any{"observation": "legacy Node writes updated_at again"})

	_, err = projectService.Update(ctx, UpdateCommand{
		ProjectID: 999999, Project: domainproject.Update{Name: stringGoldenPointer("Missing")},
	}, visibility)
	appendGoldenFailure(t, &scenarios, "missing-update", map[string]any{"projectId": 999999, "name": "Missing"}, err, nil)
	_, err = projectService.Delete(ctx, DeleteCommand{ProjectID: 999999}, visibility)
	appendGoldenFailure(t, &scenarios, "missing-delete", map[string]any{"projectId": 999999}, err, nil)

	store.deleteError = repository.ErrProjectRunning
	_, err = projectService.Delete(ctx, DeleteCommand{ProjectID: projectID}, visibility)
	appendGoldenFailure(t, &scenarios, "running-delete", map[string]any{"projectId": projectID}, err, nil)
	store.deleteError = nil

	deleted, err := projectService.Delete(ctx, DeleteCommand{ProjectID: projectID}, visibility)
	appendGoldenSuccess(t, &scenarios, "delete", map[string]any{"projectId": projectID}, deleted, err,
		map[string]any{"preconditionRelationCounts": goldenRelationCounts()})
	_, err = projectService.Delete(ctx, DeleteCommand{ProjectID: projectID}, visibility)
	appendGoldenFailure(t, &scenarios, "duplicate-delete", map[string]any{"projectId": projectID}, err,
		map[string]any{"observation": "without transport idempotency replay, legacy Node returns missing"})

	writeMutationGoldenExport(t, outputPath, mutationGoldenExport{
		SchemaVersion: 1,
		Version:       "p05-node-mutation-golden-v1",
		FixtureRoot:   allowedRoot,
		Database: map[string]any{
			"kind": "authorized-transactional-test-copy", "schema_version": 1,
			"before_sha256": databaseBefore, "after_sha256": goldenStoreHash(t, store),
		},
		Scenarios: scenarios,
		Handoff:   map[string]bool{"sqlJsClosed": true, "databaseOwnerReleased": true},
	})
}

func TestMutationGoldenMatrixIsCompleteVersionedAndSanitized(t *testing.T) {
	root := t.TempDir()
	output := filepath.Join(root, "go-mutations.json")
	t.Setenv("AUTOPLAN_P05_ALLOWED_ROOT", root)
	t.Setenv("AUTOPLAN_P05_GO_OUTPUT", output)
	TestMutationGoldenExport(t)
	encoded, err := os.ReadFile(output)
	if err != nil {
		t.Fatal("mutation golden matrix was not written")
	}
	var result mutationGoldenExport
	if json.Unmarshal(encoded, &result) != nil || result.SchemaVersion != 1 || len(result.Scenarios) != 11 ||
		!result.Handoff["sqlJsClosed"] || !result.Handoff["databaseOwnerReleased"] {
		t.Fatal("mutation golden matrix shape drifted")
	}
	want := []string{
		"create", "duplicate-create", "delete-duplicate-create", "update", "configure",
		"duplicate-configure", "missing-update", "missing-delete", "running-delete", "delete", "duplicate-delete",
	}
	for index, scenario := range result.Scenarios {
		if scenario["id"] != want[index] {
			t.Fatalf("mutation scenario order drifted at %d", index)
		}
	}
	text := string(encoded)
	for _, forbidden := range []string{"non-working-", "secret_refs", "PRIVATE_VALUE"} {
		if strings.Contains(text, forbidden) {
			t.Fatal("mutation golden matrix contains sensitive fixture material")
		}
	}
}

func pathInsideGoldenRoot(root, target string) bool {
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(target))
	return err == nil && relative != "." && relative != ".." && !filepath.IsAbs(relative) &&
		!(len(relative) >= 3 && relative[:3] == ".."+string(filepath.Separator))
}

func seedGoldenDefaultProject(t *testing.T, store *mutationStore) {
	t.Helper()
	updated := "2026-07-10T23:59:00.000Z"
	state, err := domainconfig.DefaultProjectState(1, updated)
	if err != nil {
		t.Fatal("default state fixture is invalid")
	}
	store.projects = []repository.Project{{
		ID: 1, Name: "默认项目", Description: "从旧版单项目数据自动迁移",
		CreatedAt: updated, UpdatedAt: updated,
	}}
	store.states[1] = state
}

func goldenStoreHash(t *testing.T, store *mutationStore) string {
	t.Helper()
	store.mu.Lock()
	defer store.mu.Unlock()
	encoded, err := json.Marshal(struct {
		Projects   []repository.Project
		States     map[int64]repository.ProjectState
		Settings   map[string]repository.Setting
		Operations map[string]repository.IdempotencyRecord
	}{store.projects, store.states, store.settings, store.operations})
	if err != nil {
		t.Fatal("transactional fixture could not be hashed")
	}
	digest := sha256.Sum256(encoded)
	return fmt.Sprintf("%x", digest[:])
}

func seedGoldenSettings(store *mutationStore) {
	for key, value := range map[string]string{
		"mcp.enabled": "true", "mcp.transport": "http", "mcp.host": "127.0.0.1",
		"mcp.port": "43847", "mcp.path": "/mcp", "mcp.authToken": "non-working-initial-fixture-0000",
	} {
		store.settings[key] = repository.Setting{Key: key, Value: value, Version: 1}
	}
}

func goldenCreateConfig() domainconfig.LoopConfig {
	effort := "HIGH"
	provider := "CODEX"
	value := domainconfig.DefaultLoopConfig()
	value.IntervalSeconds = 7
	value.ValidationCommand = "synthetic validation command"
	value.ProjectPrompt = "Synthetic prompt"
	value.AgentCLIProvider = "CODEX"
	value.CodexReasoningEffort = &effort
	value.PlanGenerationProvider = &provider
	value.PlanGenerationCodexReasoningEffort = &effort
	value.PlanExecutionProvider = &provider
	value.PlanExecutionCodexReasoningEffort = &effort
	return value
}

func completeGoldenFixtureState(t *testing.T, store *mutationStore, projectID int64, value domainconfig.LoopConfig) {
	t.Helper()
	normalized, err := domainconfig.NormalizeLoopConfig(value)
	if err != nil {
		t.Fatal("golden config fixture is invalid")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	state, exists := store.states[projectID]
	if !exists {
		t.Fatal("golden project state is missing")
	}
	state = applyFakeConfig(state, normalized)
	state.PlanGenerationCodexReasoningEffort = normalized.PlanGenerationCodexReasoningEffort
	state.PlanGenerationClaudeBaseURL = normalized.PlanGenerationClaudeBaseURL
	state.PlanGenerationClaudeModel = normalized.PlanGenerationClaudeModel
	state.PlanExecutionCodexReasoningEffort = normalized.PlanExecutionCodexReasoningEffort
	state.PlanExecutionClaudeBaseURL = normalized.PlanExecutionClaudeBaseURL
	state.PlanExecutionClaudeModel = normalized.PlanExecutionClaudeModel
	store.states[projectID] = state
}

func goldenConfiguredLoop() domainconfig.LoopConfig {
	generationProvider, executionProvider, generationEffort := "codex", "claude", "high"
	return domainconfig.LoopConfig{
		IntervalSeconds: 7, ValidationCommand: "synthetic replacement command", ProjectPrompt: "",
		AgentCLIProvider: "claude", AgentCLICommand: "synthetic-agent",
		PlanGenerationStrategy: "external-cli-markdown", PlanGenerationProvider: &generationProvider,
		PlanGenerationCommand: "synthetic-generator", PlanGenerationCodexReasoningEffort: &generationEffort,
		PlanExecutionStrategy: "external-cli", PlanExecutionProvider: &executionProvider,
		PlanExecutionCommand: "synthetic-executor", PlanExecutionClaudeAuthToken: "non-working-fixture-token-0000",
		EnvVars: `[{"name":"SYNTHETIC_NAME","value":"non-secret-fixture-value"}]`,
	}
}

func goldenConfigureRequest(projectID int64) map[string]any {
	return map[string]any{
		"projectId": projectID, "intervalSeconds": 0,
		"validation_command": "<redacted-command>", "project_prompt": "",
		"agent_cli_provider": "claude", "agent_cli_command": "<redacted-command>",
		"planGenerationStrategy": "external-cli-markdown", "planGenerationProvider": "codex",
		"planGenerationCommand": "<redacted-command>", "planExecutionProvider": "claude",
		"planExecutionCommand": "<redacted-command>", "planExecutionClaudeAuthToken": "<redacted>",
		"envVars": "<redacted-env-vars>", "mcpEnabled": false, "mcpPort": 43999,
		"mcpAuthToken": "<redacted>",
	}
}

func goldenMCPMutations(store *mutationStore) []repository.SettingMutation {
	keys := []struct{ key, value string }{
		{"mcp.authToken", "non-working-mcp-fixture-0000"},
		{"mcp.enabled", "false"},
		{"mcp.port", "43999"},
	}
	result := make([]repository.SettingMutation, 0, len(keys))
	for _, item := range keys {
		result = append(result, repository.SettingMutation{
			Key: item.key, Value: item.value, ExpectedVersion: store.settings[item.key].Version,
		})
	}
	return result
}

func goldenRelationCounts() map[string]int {
	return map[string]int{
		"requirements": 0, "feedback": 0, "attachments": 0, "plans": 0, "events": 0,
		"scan_files": 0, "scripts": 0, "executors": 0, "conversations": 0,
		"chat_messages": 0, "intake_plan_links": 0, "project_states": 1,
	}
}

func appendGoldenSuccess(t *testing.T, scenarios *[]map[string]any, id string, request any,
	snapshot contracts.AppSnapshot, err error, extras map[string]any) {
	t.Helper()
	if err != nil || snapshot.Validate() != nil {
		t.Fatalf("successful mutation scenario %s is invalid", id)
	}
	scenario := map[string]any{"id": id, "request": request, "response": map[string]any{"ok": true, "snapshot": snapshot}}
	for key, value := range extras {
		scenario[key] = value
	}
	*scenarios = append(*scenarios, scenario)
}

func appendGoldenFailure(t *testing.T, scenarios *[]map[string]any, id string, request any,
	err error, extras map[string]any) {
	t.Helper()
	code := goldenErrorCode(err)
	if code == "" {
		t.Fatalf("mutation failure scenario %s returned an unstable error", id)
	}
	scenario := map[string]any{"id": id, "request": request, "response": map[string]any{
		"ok": false, "error": map[string]any{"code": code},
	}}
	for key, value := range extras {
		scenario[key] = value
	}
	*scenarios = append(*scenarios, scenario)
}

func goldenErrorCode(err error) string {
	switch {
	case errors.Is(err, repository.ErrNotFound), errors.Is(err, domainproject.ErrNotFound):
		return "project_not_found"
	case errors.Is(err, repository.ErrProjectRunning), errors.Is(err, domainproject.ErrRunning):
		return "project_running"
	case errors.Is(err, repository.ErrRelationConflict), errors.Is(err, domainproject.ErrRelation):
		return "relation_conflict"
	case errors.Is(err, repository.ErrVersionConflict):
		return "version_conflict"
	default:
		return ""
	}
}

func stringGoldenPointer(value string) *string { return &value }

func writeMutationGoldenExport(t *testing.T, outputPath string, value mutationGoldenExport) {
	t.Helper()
	file, err := os.OpenFile(outputPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal("mutation golden output could not be created")
	}
	encoder := json.NewEncoder(file)
	encoder.SetEscapeHTML(true)
	encodeErr := encoder.Encode(value)
	closeErr := file.Close()
	if encodeErr != nil || closeErr != nil {
		t.Fatal("mutation golden output could not be written")
	}
}
