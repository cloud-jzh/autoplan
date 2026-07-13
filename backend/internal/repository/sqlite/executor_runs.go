package sqlite

import (
	"context"
	"encoding/json"

	domainautomation "github.com/lyming99/autoplan/backend/internal/domain/automation"
	domainoperation "github.com/lyming99/autoplan/backend/internal/domain/operation"
	"github.com/lyming99/autoplan/backend/internal/repository"
)

// PluginRuntimeArchive deliberately contains only the live/not-live decision
// and the bounded action name. A PID, process command, endpoint, environment
// or other adoption data is never persisted for a plugin.
type PluginRuntimeArchive struct {
	Running bool
	Action  string
}

type ExecutorRunFinalization struct {
	Transition TransitionOperation
	ExecutorID int64
	Archive    RuntimeRunArchive
	Plugin     *PluginRuntimeArchive
}

// FinalizeExecutorRun is the executor counterpart of FinalizeScriptRun. The
// terminal Operation, last_* values, plugin state and P10 business event are
// committed or rolled back together. A late process exit cannot overwrite a
// cancellation or recovery terminal state because Transition rejects it.
func (transaction *OperationTransaction) FinalizeExecutorRun(ctx context.Context, input ExecutorRunFinalization) (OperationMutation, error) {
	if transaction == nil || transaction.transaction == nil || input.ExecutorID <= 0 ||
		input.Transition.ProjectID <= 0 || input.Transition.OperationID == "" || !input.Transition.Target.Terminal() ||
		!runtimeArchiveMatchesExecutorTerminal(input.Archive.Status, input.Transition.Target) ||
		input.Archive.OccurredAt != input.Transition.UpdatedAt {
		return OperationMutation{}, repository.ErrTransaction
	}
	archive, logTail, err := normalizeRuntimeRunArchive(input.Archive)
	if err != nil {
		return OperationMutation{}, err
	}
	metadata := archive.Output.Metadata()
	if metadata.Validate() != nil {
		return OperationMutation{}, repository.ErrTransaction
	}
	if input.Transition.Output != nil && *input.Transition.Output != metadata {
		return OperationMutation{}, repository.ErrTransaction
	}
	if input.Transition.Output == nil {
		input.Transition.Output = &metadata
	}
	mutation, err := transaction.Transition(ctx, input.Transition)
	if err != nil || !mutation.Changed {
		return mutation, err
	}
	if err := transaction.transaction.archiveExecutorRun(ctx, input.Transition.ProjectID, input.ExecutorID, archive, logTail, input.Plugin); err != nil {
		return OperationMutation{}, err
	}
	payload, err := runtimeArchivePayload(input.ExecutorID, archive)
	if err != nil {
		return OperationMutation{}, err
	}
	operationID := mutation.Operation.OperationID
	if _, err := transaction.transaction.appendBusinessEvent(ctx, BusinessEvent{
		ProjectID: input.Transition.ProjectID, Type: "business.executor.run", OperationID: &operationID,
		RequestID: input.Transition.RequestID, OccurredAt: input.Transition.UpdatedAt, Payload: payload,
	}); err != nil {
		return OperationMutation{}, err
	}
	return mutation, nil
}

func (transaction *writeTransaction) archiveExecutorRun(
	ctx context.Context,
	projectID, executorID int64,
	archive RuntimeRunArchive,
	logTail string,
	plugin *PluginRuntimeArchive,
) error {
	current, found, err := transaction.GetExecutor(ctx, projectID, executorID)
	if err != nil {
		return err
	}
	if !found {
		return repository.ErrNotFound
	}
	if domainautomation.ValidateExecutorRecord(current) != nil || !archiveAfter(archive.OccurredAt, current.CreatedAt) {
		return repository.ErrTransaction
	}
	pluginState, err := normalizedPluginRuntimeArchive(current.Type, plugin)
	if err != nil {
		return err
	}
	result, err := transaction.tx.ExecContext(ctx,
		`UPDATE executors
		    SET last_status = ?, last_exit_code = ?, last_duration_ms = ?, last_log = ?, last_run_at = ?,
		        plugin_state_json = ?, updated_at = ?
		  WHERE id = ? AND project_id = ?`,
		archive.Status, nullableRuntimeInt64(archive.ExitCode), nullableRuntimeInt64(archive.DurationMS), nullableRuntimeText(logTail),
		archive.OccurredAt, optionalJSON(pluginState), archive.OccurredAt, executorID, projectID)
	if err != nil {
		return safeSQLError(ctx, err)
	}
	if err := requireOneRow(result); err != nil {
		return err
	}
	return transaction.wrote("executors:archive-run")
}

func runtimeArchiveMatchesExecutorTerminal(status string, target domainoperation.Status) bool {
	switch target {
	case domainoperation.StatusSucceeded:
		return status == "ok" || status == "running"
	case domainoperation.StatusFailed:
		return status == "bad"
	case domainoperation.StatusCancelled:
		return status == "stopped"
	case domainoperation.StatusInterrupted:
		return status == "interrupted"
	default:
		return false
	}
}

func normalizedPluginRuntimeArchive(executorType string, input *PluginRuntimeArchive) (*json.RawMessage, error) {
	if executorType != "plugin" {
		if input != nil {
			return nil, repository.ErrTransaction
		}
		return nil, nil
	}
	state := PluginRuntimeArchive{}
	if input != nil {
		state = *input
	}
	if state.Action != "" && state.Action != "start" && state.Action != "reload" && state.Action != "stop" {
		return nil, repository.ErrTransaction
	}
	status := "stopped"
	if state.Running {
		status = "running"
	}
	encoded, err := json.Marshal(struct {
		Running bool   `json:"running"`
		State   string `json:"state"`
		Action  string `json:"action,omitempty"`
	}{Running: state.Running, State: status, Action: state.Action})
	if err != nil {
		return nil, repository.ErrTransaction
	}
	value := json.RawMessage(encoded)
	return &value, nil
}
