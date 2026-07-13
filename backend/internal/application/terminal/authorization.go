package terminal

import (
	"context"

	domainterminal "github.com/lyming99/autoplan/backend/internal/domain/terminal"
)

// Authorizer is supplied by the authenticated inbound boundary. It must
// establish both caller identity and current project visibility; terminal
// session IDs deliberately never serve as a capability.
type Authorizer interface {
	AuthorizeTerminal(context.Context, domainterminal.Caller, int64) error
}

// WorkspaceResolver resolves the workspace from server-side project state
// after caller authorization. CreateCommand deliberately has no workspace
// field, so a renderer-supplied absolute path cannot replace project ownership
// or Files Policy validation.
type WorkspaceResolver interface {
	TerminalWorkspace(context.Context, domainterminal.Caller, int64) (string, error)
}

type AuthorizerFunc func(context.Context, domainterminal.Caller, int64) error

func (function AuthorizerFunc) AuthorizeTerminal(ctx context.Context, caller domainterminal.Caller, projectID int64) error {
	return function(ctx, caller, projectID)
}

func (service *Service) authorize(ctx context.Context, caller domainterminal.Caller, projectID int64) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if service == nil || service.authorizer == nil || !caller.Valid() || projectID <= 0 {
		return domainterminal.ErrForbidden
	}
	if err := service.authorizer.AuthorizeTerminal(ctx, caller, projectID); err != nil {
		return domainterminal.ErrForbidden
	}
	return nil
}

func (service *Service) workspace(ctx context.Context, caller domainterminal.Caller, projectID int64) (string, error) {
	if service == nil || service.workspaces == nil {
		return "", domainterminal.ErrUnavailable
	}
	workspace, err := service.workspaces.TerminalWorkspace(ctx, caller, projectID)
	if err != nil || !validPath(workspace) {
		return "", domainterminal.ErrForbidden
	}
	return workspace, nil
}
