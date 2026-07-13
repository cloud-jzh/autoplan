package scripts

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strconv"

	domainautomation "github.com/lyming99/autoplan/backend/internal/domain/automation"
	"github.com/lyming99/autoplan/backend/internal/runtime/scheduler"
)

type HookCommand struct {
	Caller    Caller
	ProjectID int64
	Stage     string
	RequestID string
	Context   Context
}

type HookRun struct {
	ScriptID  int64
	Operation string
	Status    string
}

type HookResult struct {
	Ran     bool
	Aborted bool
	Results []HookRun
}

// RunHooks preserves the frozen Node ordering: enabled hook definitions for
// one stage run in (sort_order, id) order, one at a time. Only a failed
// validation:before Script with fail_aborts set stops the remaining stage.
func (service *Service) RunHooks(ctx context.Context, command HookCommand) (HookResult, error) {
	if err := service.ready(ctx); err != nil {
		return HookResult{}, err
	}
	if !validCaller(command.Caller, command.ProjectID) || !validHookStage(command.Stage) || !validIdentity(command.RequestID, 64) || !command.Context.Valid() {
		return HookResult{}, ErrInvalidCommand
	}
	scripts, err := service.listAllScripts(ctx, command.ProjectID)
	if err != nil {
		return HookResult{}, err
	}
	result := HookResult{Results: make([]HookRun, 0, len(scripts))}
	for _, script := range scripts {
		if !matchesHook(script, command.Stage) {
			continue
		}
		result.Ran = true
		identity := derivedIdentity("hook", command.RequestID, script.ID, command.Stage)
		run, runErr := service.RunHook(ctx, RunCommand{
			Caller: command.Caller, ProjectID: command.ProjectID, ScriptID: script.ID,
			RequestID: identity, IdempotencyKey: identity, Context: command.Context,
		}, command.Stage)
		status := "rejected"
		if runErr == nil {
			status = service.waitForHook(ctx, command.ProjectID, script.ID, run.Operation.OperationID)
		}
		result.Results = append(result.Results, HookRun{ScriptID: script.ID, Operation: run.Operation.OperationID, Status: status})
		if command.Stage == "validation:before" && script.FailAborts && status != "ok" {
			result.Aborted = true
			break
		}
	}
	return result, nil
}

func matchesHook(script domainautomation.Script, stage string) bool {
	return script.Enabled && script.TriggerMode == string(TriggerHook) && script.HookStage != nil && *script.HookStage == stage
}

func validHookStage(value string) bool {
	switch value {
	case "plan:after", "task:after", "validation:before", "loop:end", "on:fail":
		return true
	default:
		return false
	}
}

func (service *Service) waitForHook(ctx context.Context, projectID, scriptID int64, operationID string) string {
	if operationID == "" {
		return "rejected"
	}
	service.mu.Lock()
	active := service.active[scriptKey{projectID: projectID, scriptID: scriptID}]
	var submission *scheduler.Submission
	if active != nil && active.operation.OperationID == operationID && active.request != nil {
		submission = active.request.submission
	}
	service.mu.Unlock()
	if submission != nil {
		_, _ = submission.Wait(ctx)
	}
	service.mu.Lock()
	last, found := service.last[scriptKey{projectID: projectID, scriptID: scriptID}]
	service.mu.Unlock()
	if !found {
		return "rejected"
	}
	return last.status
}

func derivedIdentity(kind, requestID string, scriptID int64, qualifier string) string {
	sum := sha256.Sum256([]byte(kind + "\x00" + requestID + "\x00" + strconv.FormatInt(scriptID, 10) + "\x00" + qualifier))
	return kind + "-" + hex.EncodeToString(sum[:16])
}
