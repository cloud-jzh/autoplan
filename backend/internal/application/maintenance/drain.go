package maintenance

import "context"

// DrainResult makes every legacy writer category explicit.  A zero value is
// drained; any remaining process, queued write, or unfinished final persist
// blocks the cutover.
type DrainResult struct {
	LoopRuns        int  `json:"loop_runs"`
	ChatRuns        int  `json:"chat_runs"`
	CLIRuns         int  `json:"cli_runs"`
	ScriptRuns      int  `json:"script_runs"`
	ExecutorRuns    int  `json:"executor_runs"`
	QueuedWrites    int  `json:"queued_writes"`
	FinalPersisting bool `json:"final_persisting"`
}

func (result DrainResult) Complete() bool {
	return result.LoopRuns == 0 && result.ChatRuns == 0 && result.CLIRuns == 0 &&
		result.ScriptRuns == 0 && result.ExecutorRuns == 0 && result.QueuedWrites == 0 && !result.FinalPersisting
}

// Drainer must stop and join all legacy execution categories before it reports
// success.  It may not leave detached work behind for a later cutover stage.
type Drainer interface {
	Drain(context.Context) (DrainResult, error)
}
