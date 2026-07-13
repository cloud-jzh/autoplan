// Package files coordinates persisted policy configuration and authorization.
package files

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	domainfiles "github.com/lyming99/autoplan/backend/internal/domain/files"
)

// ConfigService is the narrow file-policy facet of the shared configuration
// application boundary. Its implementation owns versioned persistence.
type ConfigService interface {
	GetFilePolicy(context.Context) (domainfiles.Policy, error)
	PutFilePolicy(context.Context, int64, domainfiles.Policy) (domainfiles.Policy, error)
}

type PathAuthorizer interface {
	Authorize(domainfiles.AuthorizationRequest) (domainfiles.Decision, error)
	NormalizeRoots([]string) ([]string, error)
}

type Service struct {
	config     ConfigService
	authorizer PathAuthorizer
}

func New(config ConfigService, authorizer PathAuthorizer) *Service {
	return &Service{config: config, authorizer: authorizer}
}

func (service *Service) Get(ctx context.Context) (domainfiles.Policy, error) {
	if err := service.ready(ctx); err != nil {
		return domainfiles.Policy{}, err
	}
	policy, err := service.config.GetFilePolicy(ctx)
	if err != nil {
		return domainfiles.Policy{}, err
	}
	if policy.AllowedRoots == nil {
		policy.AllowedRoots = []string{}
	}
	if policy.Validate() != nil || policy.Version <= 0 {
		return domainfiles.Policy{}, domainfiles.ErrInvalidPolicy
	}
	return policy, nil
}

func (service *Service) Save(ctx context.Context, expectedVersion int64, input domainfiles.Policy) (domainfiles.Policy, error) {
	if err := service.ready(ctx); err != nil {
		return domainfiles.Policy{}, err
	}
	if expectedVersion <= 0 {
		return domainfiles.Policy{}, domainfiles.ErrVersionRequired
	}
	input.Version = 0
	if input.AllowedRoots == nil {
		input.AllowedRoots = []string{}
	}
	if input.Validate() != nil {
		return domainfiles.Policy{}, domainfiles.ErrInvalidPolicy
	}
	roots, err := service.authorizer.NormalizeRoots(input.AllowedRoots)
	if err != nil {
		return domainfiles.Policy{}, err
	}
	input.AllowedRoots = roots
	result, err := service.config.PutFilePolicy(ctx, expectedVersion, input)
	if err != nil {
		return domainfiles.Policy{}, err
	}
	if result.Validate() != nil || result.Version <= 0 {
		return domainfiles.Policy{}, domainfiles.ErrInvalidPolicy
	}
	return result, nil
}

func (service *Service) Authorize(
	ctx context.Context,
	operation domainfiles.Operation,
	workspaceRoot string,
	targetPath string,
	allowMissing bool,
) (domainfiles.Decision, error) {
	policy, err := service.Get(ctx)
	if err != nil {
		return domainfiles.Decision{}, err
	}
	return service.authorizer.Authorize(domainfiles.AuthorizationRequest{
		Policy: policy, Operation: operation, WorkspaceRoot: workspaceRoot,
		TargetPath: targetPath, AllowMissing: allowMissing,
	})
}

// AuthorizeWorkingDirectory is the process-runner boundary for P05 policy.
// A working directory is execution-capable, so ScopeAll intentionally does
// not turn it into an unrestricted path; the underlying ExecutorCWD operation
// keeps the existing workspace/custom-root authorization semantics.
func (service *Service) AuthorizeWorkingDirectory(
	ctx context.Context,
	workspaceRoot string,
	workingDirectory string,
) (domainfiles.Decision, error) {
	decision, err := service.Authorize(
		ctx,
		domainfiles.OperationExecutorCWD,
		workspaceRoot,
		workingDirectory,
		false,
	)
	if err != nil {
		return domainfiles.Decision{}, err
	}
	if !decision.Allowed || decision.ResolvedTarget == "" {
		return domainfiles.Decision{}, domainfiles.ErrOutsideScope
	}
	return decision, nil
}

// AuthorizeTerminalWorkingDirectory is the narrower P14 execution boundary.
// Terminal sessions cannot reuse ExecutorCWD authorization: the distinct
// OperationTerminalCWD rule prevents a broad read or executor policy from
// silently becoming a PTY launch permit. The post-authorization realpath check
// closes the final symlink/junction replacement window before spawn.
func (service *Service) AuthorizeTerminalWorkingDirectory(
	ctx context.Context,
	workspaceRoot string,
	workingDirectory string,
) (domainfiles.Decision, error) {
	decision, err := service.Authorize(
		ctx,
		domainfiles.OperationTerminalCWD,
		workspaceRoot,
		workingDirectory,
		false,
	)
	if err != nil {
		return domainfiles.Decision{}, err
	}
	if !decision.Allowed || decision.ResolvedTarget == "" {
		return domainfiles.Decision{}, domainfiles.ErrOutsideScope
	}
	return verifyTerminalWorkingDirectory(decision)
}

// AuthorizeScriptSource verifies a persisted file-backed Script source before
// an application service creates a ProcessSpec. The request has no allow-miss
// mode: a missing, absolute-escape or symlink-raced source must fail before a
// runner can select an interpreter or create a child process.
func (service *Service) AuthorizeScriptSource(
	ctx context.Context,
	workspaceRoot string,
	sourcePath string,
) (domainfiles.Decision, error) {
	return service.authorizeRuntimeInput(ctx, domainfiles.OperationScript, workspaceRoot, sourcePath)
}

// AuthorizeExecutorInput applies the existing execution-capable CWD policy to
// a persisted Executor input file. Keeping this separate from generic read
// authorization prevents a ScopeAll read policy from silently becoming a
// process input allowlist.
func (service *Service) AuthorizeExecutorInput(
	ctx context.Context,
	workspaceRoot string,
	inputPath string,
) (domainfiles.Decision, error) {
	return service.authorizeRuntimeInput(ctx, domainfiles.OperationExecutorCWD, workspaceRoot, inputPath)
}

func (service *Service) authorizeRuntimeInput(
	ctx context.Context,
	operation domainfiles.Operation,
	workspaceRoot string,
	targetPath string,
) (domainfiles.Decision, error) {
	decision, err := service.Authorize(ctx, operation, workspaceRoot, targetPath, false)
	if err != nil {
		return domainfiles.Decision{}, err
	}
	if !decision.Allowed || decision.ResolvedTarget == "" {
		return domainfiles.Decision{}, domainfiles.ErrOutsideScope
	}
	return verifyRuntimeInput(decision)
}

// verifyRuntimeInput closes the authorization-to-open race for a file used as
// process input. P05's resolver establishes the allowed root; this final
// check rejects a replacement symlink, directory or case-changing path before
// a Script interpreter or Executor can receive it.
func verifyRuntimeInput(decision domainfiles.Decision) (domainfiles.Decision, error) {
	if !decision.Allowed || decision.ResolvedTarget == "" {
		return domainfiles.Decision{}, domainfiles.ErrOutsideScope
	}
	lexical, err := os.Lstat(decision.ResolvedTarget)
	if err != nil || lexical.Mode()&os.ModeSymlink != 0 {
		return domainfiles.Decision{}, domainfiles.ErrRaceDetected
	}
	if lexical.IsDir() {
		return domainfiles.Decision{}, domainfiles.ErrInvalidPath
	}
	resolved, err := filepath.EvalSymlinks(decision.ResolvedTarget)
	if err != nil {
		return domainfiles.Decision{}, domainfiles.ErrRaceDetected
	}
	abs, err := filepath.Abs(resolved)
	if err != nil || !sameRuntimePath(filepath.Clean(abs), filepath.Clean(decision.ResolvedTarget)) {
		return domainfiles.Decision{}, domainfiles.ErrRaceDetected
	}
	decision.ResolvedTarget = filepath.Clean(abs)
	return decision, nil
}

func verifyTerminalWorkingDirectory(decision domainfiles.Decision) (domainfiles.Decision, error) {
	if !decision.Allowed || decision.ResolvedTarget == "" {
		return domainfiles.Decision{}, domainfiles.ErrOutsideScope
	}
	lexical, err := os.Lstat(decision.ResolvedTarget)
	if err != nil || lexical.Mode()&os.ModeSymlink != 0 || !lexical.IsDir() {
		return domainfiles.Decision{}, domainfiles.ErrRaceDetected
	}
	resolved, err := filepath.EvalSymlinks(decision.ResolvedTarget)
	if err != nil {
		return domainfiles.Decision{}, domainfiles.ErrRaceDetected
	}
	abs, err := filepath.Abs(resolved)
	if err != nil || !sameRuntimePath(filepath.Clean(abs), filepath.Clean(decision.ResolvedTarget)) {
		return domainfiles.Decision{}, domainfiles.ErrRaceDetected
	}
	info, err := os.Stat(abs)
	if err != nil || !info.IsDir() {
		return domainfiles.Decision{}, domainfiles.ErrRaceDetected
	}
	decision.ResolvedTarget = filepath.Clean(abs)
	return decision, nil
}

func sameRuntimePath(left, right string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}

func (service *Service) ready(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if service == nil || service.config == nil || service.authorizer == nil {
		return domainfiles.ErrInvalidPolicy
	}
	return nil
}
