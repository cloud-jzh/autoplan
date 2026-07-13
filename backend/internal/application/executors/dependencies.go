package executors

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	applicationoperations "github.com/lyming99/autoplan/backend/internal/application/operations"
	domainautomation "github.com/lyming99/autoplan/backend/internal/domain/automation"
	"github.com/lyming99/autoplan/backend/internal/repository"
	"github.com/lyming99/autoplan/backend/internal/runtime/process"
)

type repositoryStore struct {
	writer repository.AutomationTransactional
}

func NewRepositoryStore(writer repository.AutomationTransactional) Store {
	if writer == nil {
		return nil
	}
	return repositoryStore{writer: writer}
}

func (store repositoryStore) Check(ctx context.Context) error { return store.writer.Check(ctx) }

func (store repositoryStore) GetProject(ctx context.Context, projectID int64) (repository.Project, bool, error) {
	var value repository.Project
	var found bool
	err := store.writer.TransactAutomation(ctx, func(transaction repository.AutomationWriteTransaction) error {
		var err error
		value, found, err = transaction.GetProject(ctx, projectID)
		return err
	})
	return value, found, err
}

func (store repositoryStore) GetExecutor(ctx context.Context, projectID, executorID int64) (domainautomation.Executor, bool, error) {
	var value domainautomation.Executor
	var found bool
	err := store.writer.TransactAutomation(ctx, func(transaction repository.AutomationWriteTransaction) error {
		var err error
		value, found, err = transaction.GetExecutor(ctx, projectID, executorID)
		return err
	})
	return value, found, err
}

func (store repositoryStore) ListExecutors(ctx context.Context, options domainautomation.ListOptions) ([]domainautomation.Executor, error) {
	var values []domainautomation.Executor
	err := store.writer.TransactAutomation(ctx, func(transaction repository.AutomationWriteTransaction) error {
		var err error
		values, err = transaction.ListExecutors(ctx, options)
		return err
	})
	return values, err
}

func (service *Service) listAllExecutors(ctx context.Context, projectID int64) ([]domainautomation.Executor, error) {
	const pageSize = 200
	result := make([]domainautomation.Executor, 0)
	for offset := 0; ; offset += pageSize {
		page, err := service.store.ListExecutors(ctx, domainautomation.ListOptions{ProjectID: projectID, Limit: pageSize, Offset: offset})
		if err != nil {
			return nil, err
		}
		result = append(result, page...)
		if len(page) < pageSize {
			return result, nil
		}
	}
}

type executionContext struct {
	service   *Service
	projectID int64
	workspace string
	rootID    int64
	byID      map[int64]domainautomation.Executor
	byLabel   map[string]domainautomation.Executor

	mu      sync.Mutex
	futures map[int64]*nodeFuture
}

type nodeFuture struct {
	done   chan struct{}
	result nodeResult
}

type nodeResult struct {
	ExecutorID int64
	Label      string
	Status     string
	ExitCode   int
	DurationMS int64
	Code       string
	Depends    []nodeResult
}

func (result nodeResult) ok() bool { return result.Status == "ok" }

func (result nodeResult) cancelled() bool { return result.Status == "stopped" }

func newExecutionContext(service *Service, projectID int64, workspace string, rootID int64, values []domainautomation.Executor) *executionContext {
	byID := make(map[int64]domainautomation.Executor, len(values))
	byLabel := make(map[string]domainautomation.Executor, len(values))
	for _, value := range values {
		byID[value.ID] = value
		if _, exists := byLabel[value.Label]; !exists {
			byLabel[value.Label] = value
		}
	}
	return &executionContext{service: service, projectID: projectID, workspace: workspace, rootID: rootID, byID: byID, byLabel: byLabel, futures: make(map[int64]*nodeFuture)}
}

func (execution *executionContext) run(ctx context.Context, executorID int64, stack []int64) nodeResult {
	for _, item := range stack {
		if item == executorID {
			return nodeResult{ExecutorID: executorID, Status: "bad", ExitCode: -1, Code: "EXECUTOR_DEPENDENCY_CYCLE"}
		}
	}
	execution.mu.Lock()
	if future := execution.futures[executorID]; future != nil {
		execution.mu.Unlock()
		select {
		case <-future.done:
			return future.result
		case <-ctx.Done():
			return nodeResult{ExecutorID: executorID, Status: "stopped", ExitCode: -1, Code: "OPERATION_CANCELLED"}
		}
	}
	future := &nodeFuture{done: make(chan struct{})}
	execution.futures[executorID] = future
	execution.mu.Unlock()

	result := execution.runOnce(ctx, executorID, append(stack, executorID))
	execution.mu.Lock()
	future.result = result
	close(future.done)
	execution.mu.Unlock()
	return result
}

func (execution *executionContext) runOnce(ctx context.Context, executorID int64, stack []int64) nodeResult {
	executor, exists := execution.byID[executorID]
	if !exists {
		return nodeResult{ExecutorID: executorID, Status: "bad", ExitCode: -1, Code: "EXECUTOR_DEPENDENCY_MISSING"}
	}
	result := nodeResult{ExecutorID: executor.ID, Label: executor.Label, Status: "bad", ExitCode: -1}
	if !executor.Enabled {
		result.Code = "RESOURCE_DISABLED"
		execution.service.setRuntimeTerminal(execution.projectID, executor.ID, "bad", -1, 0, "")
		return result
	}
	result.Depends = execution.runDependencies(ctx, executor, stack)
	for _, dependency := range result.Depends {
		if !dependency.ok() {
			if dependency.cancelled() {
				result.Status, result.Code = "stopped", "OPERATION_CANCELLED"
			} else if dependency.Code == "EXECUTOR_DEPENDENCY_CYCLE" || dependency.Code == "EXECUTOR_DEPENDENCY_MISSING" {
				result.Code = dependency.Code
			} else {
				result.Code = "EXECUTOR_DEPENDENCY_FAILED"
			}
			execution.service.setRuntimeTerminal(execution.projectID, executor.ID, result.Status, -1, 0, "")
			return result
		}
	}
	return execution.runProcess(ctx, executor, result.Depends)
}

func (execution *executionContext) runDependencies(ctx context.Context, executor domainautomation.Executor, stack []int64) []nodeResult {
	labels, err := dependencyLabels(executor.DependsOnJSON)
	if err != nil {
		return []nodeResult{{ExecutorID: executor.ID, Label: executor.Label, Status: "bad", ExitCode: -1, Code: "EXECUTOR_DEPENDENCY_MISSING"}}
	}
	if len(labels) == 0 {
		return nil
	}
	runOne := func(label string) nodeResult {
		dependency, found := execution.byLabel[label]
		if !found {
			return nodeResult{Label: label, Status: "bad", ExitCode: -1, Code: "EXECUTOR_DEPENDENCY_MISSING"}
		}
		return execution.run(ctx, dependency.ID, stack)
	}
	if executor.DependsOrder == "sequence" {
		result := make([]nodeResult, 0, len(labels))
		for _, label := range labels {
			value := runOne(label)
			result = append(result, value)
			if !value.ok() {
				break
			}
		}
		return result
	}
	result := make([]nodeResult, len(labels))
	var group sync.WaitGroup
	for index, label := range labels {
		group.Add(1)
		go func(index int, label string) {
			defer group.Done()
			result[index] = runOne(label)
		}(index, label)
	}
	group.Wait()
	return result
}

func (execution *executionContext) runProcess(ctx context.Context, executor domainautomation.Executor, dependencies []nodeResult) nodeResult {
	started := execution.service.clock.Now()
	execution.service.setRuntimeRunning(execution.projectID, executor.ID, executor.Type == "plugin", "")
	spec, err := processSpec(ctx, execution.service.files, execution.projectID, execution.workspace, executor, nil)
	if err != nil {
		execution.service.setRuntimeTerminal(execution.projectID, executor.ID, "bad", -1, 0, "")
		return nodeResult{ExecutorID: executor.ID, Label: executor.Label, Status: "bad", ExitCode: -1, Code: "PROCESS_SPEC_INVALID", Depends: dependencies}
	}
	value, runErr := execution.service.runner.Run(ctx, spec)
	duration := elapsedMilliseconds(started, value.EndedAt, execution.service.clock.Now())
	if errors.Is(runErr, process.ErrCancelled) || errors.Is(runErr, context.Canceled) {
		execution.service.setRuntimeTerminal(execution.projectID, executor.ID, "stopped", value.ExitCode, duration, "")
		return nodeResult{ExecutorID: executor.ID, Label: executor.Label, Status: "stopped", ExitCode: value.ExitCode, DurationMS: duration, Code: "OPERATION_CANCELLED", Depends: dependencies}
	}
	if runErr != nil || value.ExitCode != 0 {
		code := "EXECUTOR_EXIT_NONZERO"
		if runErr != nil {
			code = string(process.ErrorCode(runErr))
		}
		execution.service.setRuntimeTerminal(execution.projectID, executor.ID, "bad", value.ExitCode, duration, "")
		return nodeResult{ExecutorID: executor.ID, Label: executor.Label, Status: "bad", ExitCode: value.ExitCode, DurationMS: duration, Code: code, Depends: dependencies}
	}
	execution.service.setRuntimeTerminal(execution.projectID, executor.ID, "ok", value.ExitCode, duration, "")
	return nodeResult{ExecutorID: executor.ID, Label: executor.Label, Status: "ok", ExitCode: value.ExitCode, DurationMS: duration, Depends: dependencies}
}

func dependencyLabels(raw json.RawMessage) ([]string, error) {
	var values []string
	if json.Unmarshal(raw, &values) != nil {
		return nil, ErrInvalidCommand
	}
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			return nil, ErrInvalidCommand
		}
		result = append(result, value)
	}
	return result, nil
}

type executorOptions struct {
	CWD     string
	Env     map[string]string
	Timeout time.Duration
}

type pluginAction struct {
	Name    string
	Type    string
	Command string
	Args    []string
	Input   string
}

func processSpec(ctx context.Context, policy FilePolicy, projectID int64, workspace string, executor domainautomation.Executor, action *pluginAction) (process.Spec, error) {
	if policy == nil || projectID <= 0 || strings.TrimSpace(workspace) == "" {
		return process.Spec{}, ErrInvalidCommand
	}
	options, err := parseExecutorOptions(executor.OptionsJSON)
	if err != nil {
		return process.Spec{}, err
	}
	workingDirectory, err := resolveWorkingDirectory(options.CWD, workspace)
	if err != nil {
		return process.Spec{}, err
	}
	decision, err := policy.AuthorizeWorkingDirectory(ctx, workspace, workingDirectory)
	if err != nil || !decision.Allowed || strings.TrimSpace(decision.ResolvedTarget) == "" {
		return process.Spec{}, ErrInvalidCommand
	}
	command, args := executor.Command, executor.ArgsJSON
	if action != nil {
		command, args = action.Command, nil
	}
	values, err := parseArguments(args)
	if err != nil {
		return process.Spec{}, err
	}
	if action != nil {
		values = append([]string(nil), action.Args...)
	}
	if strings.TrimSpace(command) == "" || strings.TrimSpace(command) != command || strings.ContainsAny(command, "\r\n\x00|&;`$><") {
		return process.Spec{}, ErrInvalidCommand
	}
	return process.Spec{
		ProjectID: projectID, Resource: process.ResourceRef{Kind: process.ResourceExecutor, ID: executor.ID},
		Workspace: workspace, WorkingDirectory: workingDirectory, Executable: command, Args: values,
		Environment: options.Env, Timeout: options.Timeout,
	}, nil
}

func parseExecutorOptions(raw json.RawMessage) (executorOptions, error) {
	var source struct {
		CWD       string            `json:"cwd"`
		Env       map[string]string `json:"env"`
		TimeoutMS *int64            `json:"timeoutMs"`
		TimeoutSN *int64            `json:"timeout_ms"`
	}
	if json.Unmarshal(raw, &source) != nil {
		return executorOptions{}, ErrInvalidCommand
	}
	result := executorOptions{CWD: strings.TrimSpace(source.CWD), Env: make(map[string]string, len(source.Env))}
	for name, value := range source.Env {
		if strings.TrimSpace(name) == "" || strings.ContainsAny(value, "\x00\r\n") {
			return executorOptions{}, ErrInvalidCommand
		}
		result.Env[name] = value
	}
	timeout := source.TimeoutMS
	if timeout == nil {
		timeout = source.TimeoutSN
	}
	if timeout != nil {
		if *timeout <= 0 || *timeout > int64((2*time.Hour)/time.Millisecond) {
			return executorOptions{}, ErrInvalidCommand
		}
		result.Timeout = time.Duration(*timeout) * time.Millisecond
	}
	return result, nil
}

func parseArguments(raw json.RawMessage) ([]string, error) {
	if len(raw) == 0 {
		return []string{}, nil
	}
	var source []any
	if json.Unmarshal(raw, &source) != nil {
		return nil, ErrInvalidCommand
	}
	result := make([]string, 0, len(source))
	for _, item := range source {
		switch value := item.(type) {
		case nil:
			result = append(result, "")
		case string:
			result = append(result, value)
		case float64:
			result = append(result, strconv.FormatFloat(value, 'f', -1, 64))
		case bool:
			result = append(result, strconv.FormatBool(value))
		case map[string]any:
			rawValue, found := value["value"]
			if !found {
				return nil, ErrInvalidCommand
			}
			result = append(result, fmt.Sprint(rawValue))
		default:
			return nil, ErrInvalidCommand
		}
	}
	return result, nil
}

func parsePluginAction(executor domainautomation.Executor, name string) (*pluginAction, error) {
	if executor.Type != "plugin" || executor.ActionsJSON == nil || (name != "start" && name != "reload" && name != "stop") {
		return nil, ErrActionInvalid
	}
	var document map[string]struct {
		Type    string          `json:"type"`
		Command string          `json:"command"`
		Args    json.RawMessage `json:"args"`
		Input   string          `json:"input"`
	}
	if json.Unmarshal(*executor.ActionsJSON, &document) != nil {
		return nil, ErrActionInvalid
	}
	entry, found := document[name]
	if !found {
		return nil, ErrActionInvalid
	}
	entry.Type, entry.Command = strings.TrimSpace(entry.Type), strings.TrimSpace(entry.Command)
	if entry.Type == "" {
		entry.Type = "command"
	}
	args, err := parseArguments(entry.Args)
	if err != nil {
		return nil, ErrActionInvalid
	}
	if entry.Type == "input" {
		if name != "reload" || strings.TrimSpace(entry.Input) == "" {
			return nil, ErrActionInvalid
		}
		return &pluginAction{Name: name, Type: entry.Type, Input: entry.Input, Args: args}, nil
	}
	if entry.Type != "command" || entry.Command == "" {
		return nil, ErrActionInvalid
	}
	return &pluginAction{Name: name, Type: entry.Type, Command: entry.Command, Args: args}, nil
}

func resolveWorkingDirectory(value, workspace string) (string, error) {
	if strings.TrimSpace(workspace) == "" {
		return "", ErrInvalidCommand
	}
	if strings.TrimSpace(value) == "" {
		return filepath.Clean(workspace), nil
	}
	value = strings.ReplaceAll(value, "${workspace}", workspace)
	if !filepath.IsAbs(value) {
		value = filepath.Join(workspace, value)
	}
	return filepath.Clean(value), nil
}

func validCaller(caller Caller, projectID int64) bool {
	return projectID > 0 && caller.ProjectID == projectID && validIdentity(caller.ID, 128)
}

func validIdentity(value string, maximum int) bool {
	if value == "" || len(value) > maximum || strings.ContainsAny(value, "\r\n\x00") {
		return false
	}
	for index, character := range value {
		if !(character >= 'A' && character <= 'Z' || character >= 'a' && character <= 'z' || character >= '0' && character <= '9' ||
			(index > 0 && (character == '.' || character == '_' || character == ':' || character == '-'))) {
			return false
		}
	}
	return true
}

func operationCaller(command RunCommand) applicationoperations.Caller {
	return applicationoperations.Caller{ID: command.Caller.ID, ProjectID: command.ProjectID}
}

func actionCaller(command ActionCommand) applicationoperations.Caller {
	return applicationoperations.Caller{ID: command.Caller.ID, ProjectID: command.ProjectID}
}

func elapsedMilliseconds(start, end, fallback time.Time) int64 {
	if end.IsZero() {
		end = fallback
	}
	if start.IsZero() || end.Before(start) {
		return 0
	}
	return end.Sub(start).Milliseconds()
}
