package config

import "time"

const (
	DefaultEventRetentionAge        = 30 * 24 * time.Hour
	DefaultEventRetentionGlobal     = 10000
	DefaultEventRetentionPerProject = 2000
	DefaultEventRetentionBatch      = 200
	DefaultEventDispatchBatch       = 100
	DefaultEventSubscriptionBuffer  = 64
	DefaultEventReplayLimit         = 5000
	DefaultEventDispatchInterval    = 250 * time.Millisecond
	DefaultEventRetentionInterval   = 15 * time.Minute

	maximumEventRetentionAge        = 365 * 24 * time.Hour
	maximumEventRetentionGlobal     = 100000
	maximumEventRetentionPerProject = 20000
	maximumEventRetentionBatch      = 1000
	maximumEventDispatchBatch       = 500
	maximumEventSubscriptionBuffer  = 1024
	maximumEventReplayLimit         = 20000
	maximumEventDispatchInterval    = time.Minute
	maximumEventRetentionInterval   = 24 * time.Hour
)

// Events contains bounded runtime settings for the durable P10 event stream.
// It deliberately has no "disabled" value: an absent or invalid setting is
// replaced with the conservative bounded default rather than retaining events
// forever or permitting an unbounded subscriber queue.
type Events struct {
	RetentionAge        time.Duration
	RetentionGlobal     int
	RetentionPerProject int
	RetentionBatch      int
	DispatchBatch       int
	SubscriptionBuffer  int
	ReplayLimit         int
	DispatchInterval    time.Duration
	RetentionInterval   time.Duration
}

func DefaultEvents() Events {
	return Events{
		RetentionAge:        DefaultEventRetentionAge,
		RetentionGlobal:     DefaultEventRetentionGlobal,
		RetentionPerProject: DefaultEventRetentionPerProject,
		RetentionBatch:      DefaultEventRetentionBatch,
		DispatchBatch:       DefaultEventDispatchBatch,
		SubscriptionBuffer:  DefaultEventSubscriptionBuffer,
		ReplayLimit:         DefaultEventReplayLimit,
		DispatchInterval:    DefaultEventDispatchInterval,
		RetentionInterval:   DefaultEventRetentionInterval,
	}
}

// Normalized returns an all-or-nothing safe configuration. Keeping the
// fallback atomic prevents a partially supplied configuration from silently
// disabling retention or making only one of the runtime queues unbounded.
func (value Events) Normalized() Events {
	if !value.Valid() {
		return DefaultEvents()
	}
	return value
}

// Valid reports whether every retention and delivery bound is explicitly
// finite. Callers should use Normalized for production fallback behavior.
func (value Events) Valid() bool {
	return value.RetentionAge > 0 && value.RetentionAge <= maximumEventRetentionAge &&
		value.RetentionGlobal > 0 && value.RetentionGlobal <= maximumEventRetentionGlobal &&
		value.RetentionPerProject > 0 && value.RetentionPerProject <= maximumEventRetentionPerProject &&
		value.RetentionPerProject <= value.RetentionGlobal &&
		value.RetentionBatch > 0 && value.RetentionBatch <= maximumEventRetentionBatch &&
		value.DispatchBatch > 0 && value.DispatchBatch <= maximumEventDispatchBatch &&
		value.SubscriptionBuffer > 0 && value.SubscriptionBuffer <= maximumEventSubscriptionBuffer &&
		value.ReplayLimit > 0 && value.ReplayLimit <= maximumEventReplayLimit &&
		value.DispatchInterval > 0 && value.DispatchInterval <= maximumEventDispatchInterval &&
		value.RetentionInterval > 0 && value.RetentionInterval <= maximumEventRetentionInterval
}
