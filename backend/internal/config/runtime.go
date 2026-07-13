package config

import "time"

const (
	DefaultSchedulerWorkers       = 4
	DefaultSchedulerQueueCapacity = 32
	DefaultActorQueueCapacity     = 64
	MaximumSchedulerWorkers       = 128
	MaximumSchedulerQueueCapacity = 4096
	MaximumActorQueueCapacity     = 4096
)

// SchedulerRuntime is deliberately independent from the filesystem runtime
// target. It controls only in-memory scheduling limits and therefore never
// selects a database, workspace, process command, or user configuration.
type SchedulerRuntime struct {
	WorkerLimit        int
	QueueCapacity      int
	ActorQueueCapacity int
	ShutdownTimeout    time.Duration
}

// DefaultSchedulerRuntime is the production-safe scheduler shape. The
// bounded queue makes overload visible to callers instead of allocating an
// unbounded goroutine or an unowned Operation.
func DefaultSchedulerRuntime() SchedulerRuntime {
	return SchedulerRuntime{
		WorkerLimit:        DefaultSchedulerWorkers,
		QueueCapacity:      DefaultSchedulerQueueCapacity,
		ActorQueueCapacity: DefaultActorQueueCapacity,
		ShutdownTimeout:    DefaultShutdown,
	}
}

// Valid reports whether settings can safely construct the shared scheduler.
// Zero values are invalid rather than silently converted to a global default.
func (value SchedulerRuntime) Valid() bool {
	return value.WorkerLimit > 0 && value.WorkerLimit <= MaximumSchedulerWorkers &&
		value.QueueCapacity >= 0 && value.QueueCapacity <= MaximumSchedulerQueueCapacity &&
		value.ActorQueueCapacity > 0 && value.ActorQueueCapacity <= MaximumActorQueueCapacity &&
		value.ShutdownTimeout > 0 && value.ShutdownTimeout <= MaximumCloseTimeout
}
