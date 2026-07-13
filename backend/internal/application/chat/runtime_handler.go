package chat

import (
	"context"

	applicationloop "github.com/lyming99/autoplan/backend/internal/application/loop"
)

// RuntimeHandler is the only Chat runtime entry point. Conversation history
// remains on Service; send/stop/queue/title commands all share this dispatcher
// and cannot fall back to an in-process queue or a SQL handle.
type RuntimeHandler struct {
	dispatcher applicationloop.Dispatcher
}

func NewRuntimeHandler(dispatcher applicationloop.Dispatcher) *RuntimeHandler {
	return &RuntimeHandler{dispatcher: dispatcher}
}

func (handler *RuntimeHandler) Commands() []applicationloop.CommandKind {
	return []applicationloop.CommandKind{
		applicationloop.CommandChatSend,
		applicationloop.CommandChatStop,
		applicationloop.CommandChatPump,
		applicationloop.CommandChatGenerateTitle,
		applicationloop.CommandChatClear,
	}
}

func (handler *RuntimeHandler) Execute(ctx context.Context, command applicationloop.Command) (applicationloop.Result, error) {
	if err := applicationloop.RequireConversation(command); err != nil {
		return applicationloop.Result{}, err
	}
	switch command.Kind {
	case applicationloop.CommandChatSend:
		if command.Chat == nil {
			return applicationloop.Result{}, applicationloop.ErrInvalidCommand
		}
	case applicationloop.CommandChatStop, applicationloop.CommandChatPump,
		applicationloop.CommandChatGenerateTitle, applicationloop.CommandChatClear:
		if command.Chat != nil {
			return applicationloop.Result{}, applicationloop.ErrInvalidCommand
		}
	default:
		return applicationloop.Result{}, applicationloop.ErrUnsupportedCommand
	}
	return applicationloop.Dispatch(ctx, handler.dispatcher, command)
}
