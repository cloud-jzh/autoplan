package config

import (
	"context"
	"strings"

	applicationloop "github.com/lyming99/autoplan/backend/internal/application/loop"
)

// RuntimeHandler owns commands whose side effects are listener or desktop
// runtime state. Persisted static config remains on Service/StaticService;
// these commands never accept an arbitrary setting key or value.
type RuntimeHandler struct {
	dispatcher applicationloop.Dispatcher
}

func NewRuntimeHandler(dispatcher applicationloop.Dispatcher) *RuntimeHandler {
	return &RuntimeHandler{dispatcher: dispatcher}
}

func (handler *RuntimeHandler) Commands() []applicationloop.CommandKind {
	return []applicationloop.CommandKind{
		applicationloop.CommandMCPStart,
		applicationloop.CommandMCPStop,
		applicationloop.CommandTerminalConfigure,
		applicationloop.CommandUpdateConfigure,
	}
}

func (handler *RuntimeHandler) Execute(ctx context.Context, command applicationloop.Command) (applicationloop.Result, error) {
	switch command.Kind {
	case applicationloop.CommandMCPStart, applicationloop.CommandMCPStop:
		if command.Terminal != nil || command.Updates != nil {
			return applicationloop.Result{}, applicationloop.ErrInvalidCommand
		}
	case applicationloop.CommandTerminalConfigure:
		if command.Terminal == nil || command.Updates != nil || !validTerminal(command.Terminal) {
			return applicationloop.Result{}, applicationloop.ErrInvalidCommand
		}
	case applicationloop.CommandUpdateConfigure:
		if command.Updates == nil || command.Terminal != nil || !validUpdates(command.Updates) {
			return applicationloop.Result{}, applicationloop.ErrInvalidCommand
		}
	default:
		return applicationloop.Result{}, applicationloop.ErrUnsupportedCommand
	}
	return applicationloop.Dispatch(ctx, handler.dispatcher, command)
}

func validTerminal(value *applicationloop.Terminal) bool {
	if value == nil || (value.DefaultProfile == nil && value.InitialCWD == nil && value.FontSize == nil &&
		value.ScrollbackLimit == nil && value.RetainOnExit == nil && value.ConfirmBeforeKill == nil) {
		return false
	}
	if value.DefaultProfile != nil && (len(strings.TrimSpace(*value.DefaultProfile)) == 0 || len(*value.DefaultProfile) > 128) {
		return false
	}
	if value.InitialCWD != nil && (len(*value.InitialCWD) > 4096 || strings.ContainsRune(*value.InitialCWD, 0)) {
		return false
	}
	if value.FontSize != nil && (*value.FontSize < 6 || *value.FontSize > 48) {
		return false
	}
	return value.ScrollbackLimit == nil || (*value.ScrollbackLimit >= 0 && *value.ScrollbackLimit <= 100000)
}

func validUpdates(value *applicationloop.Updates) bool {
	if value == nil || (value.AutoCheck == nil && value.IntervalMinutes == nil) {
		return false
	}
	return value.IntervalMinutes == nil || (*value.IntervalMinutes >= 5 && *value.IntervalMinutes <= 10080)
}
