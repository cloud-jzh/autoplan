// Package files owns transport- and platform-neutral file authorization rules.
package files

import (
	"errors"
	"strings"
)

type ErrorCode string

const (
	CodeInvalidPath      ErrorCode = "FILE_PATH_INVALID"
	CodeOutsideScope     ErrorCode = "FILE_PATH_OUTSIDE_SCOPE"
	CodeResolutionFailed ErrorCode = "FILE_PATH_RESOLUTION_FAILED"
	CodeSymlinkEscape    ErrorCode = "FILE_PATH_SYMLINK_ESCAPE"
	CodeRaceDetected     ErrorCode = "FILE_PATH_RACE_DETECTED"
	CodeControlledTarget ErrorCode = "FILE_PATH_CONTROLLED_TARGET_INVALID"
	CodeInvalidPolicy    ErrorCode = "FILE_POLICY_INVALID"
	CodeVersionRequired  ErrorCode = "FILE_POLICY_VERSION_REQUIRED"
	CodeVersionConflict  ErrorCode = "FILE_POLICY_VERSION_CONFLICT"
)

var (
	ErrInvalidPath      = errors.New("file path is invalid")
	ErrOutsideScope     = errors.New("file path is outside authorized scope")
	ErrResolutionFailed = errors.New("file path resolution failed")
	ErrSymlinkEscape    = errors.New("file path escapes through a link")
	ErrRaceDetected     = errors.New("file path changed during authorization")
	ErrControlledTarget = errors.New("controlled file target is invalid")
	ErrInvalidPolicy    = errors.New("file policy is invalid")
	ErrVersionRequired  = errors.New("file policy version is required")
	ErrVersionConflict  = errors.New("file policy version conflict")
)

type Scope string

const (
	ScopeProject   Scope = "project"
	ScopeWorkspace Scope = "workspace"
	ScopeCustom    Scope = "custom"
	ScopeAll       Scope = "all"
)

type Policy struct {
	Scope             Scope
	AllowCrossProject bool
	AllowedRoots      []string
	Version           int64
}

func DefaultPolicy() Policy { return Policy{Scope: ScopeProject, AllowedRoots: []string{}} }

func (policy Policy) Validate() error {
	switch policy.Scope {
	case ScopeProject, ScopeWorkspace, ScopeCustom, ScopeAll:
	default:
		return ErrInvalidPolicy
	}
	if policy.Version < 0 || policy.AllowedRoots == nil || len(policy.AllowedRoots) > 128 {
		return ErrInvalidPolicy
	}
	for _, root := range policy.AllowedRoots {
		if strings.TrimSpace(root) == "" || strings.ContainsRune(root, 0) || len(root) > 4096 {
			return ErrInvalidPolicy
		}
	}
	return nil
}

func (policy Policy) UsesCustomRoots() bool {
	return policy.Scope == ScopeCustom || policy.AllowCrossProject
}

func (policy Policy) UnrestrictedRead() bool { return policy.Scope == ScopeAll }

type Operation string

const (
	OperationReadFile     Operation = "read_file"
	OperationSearchFiles  Operation = "search_files"
	OperationOpenFile     Operation = "open_file"
	OperationAttachment   Operation = "attachment"
	OperationScript       Operation = "script"
	OperationExecutorCWD  Operation = "executor_cwd"
	OperationTerminalCWD  Operation = "terminal_cwd"
	OperationGenericWrite Operation = "generic_write"
	OperationPlanMarkdown Operation = "plan_markdown_write"
	OperationPlanSpec     Operation = "plan_spec_write"
	OperationPlanManifest Operation = "plan_manifest_write"
	OperationProgressLog  Operation = "progress_log_write"
)

func (operation Operation) Validate() error {
	switch operation {
	case OperationReadFile, OperationSearchFiles, OperationOpenFile, OperationAttachment,
		OperationScript, OperationExecutorCWD, OperationTerminalCWD, OperationGenericWrite,
		OperationPlanMarkdown, OperationPlanSpec, OperationPlanManifest, OperationProgressLog:
		return nil
	default:
		return ErrInvalidPath
	}
}

func (operation Operation) IsControlledWrite() bool {
	switch operation {
	case OperationPlanMarkdown, OperationPlanSpec, OperationPlanManifest, OperationProgressLog:
		return true
	default:
		return false
	}
}

func (operation Operation) IsReadLike() bool {
	switch operation {
	case OperationReadFile, OperationSearchFiles, OperationOpenFile:
		return true
	default:
		return false
	}
}

type AuthorizationRequest struct {
	Policy        Policy
	Operation     Operation
	WorkspaceRoot string
	TargetPath    string
	AllowMissing  bool
}

type Decision struct {
	Allowed        bool
	HighRisk       bool
	Controlled     bool
	ResolvedTarget string
	DisplayPath    string
	RootLabel      string
}

type PolicyError struct {
	Code ErrorCode
	Err  error
}

func (failure *PolicyError) Error() string { return string(failure.Code) }
func (failure *PolicyError) Unwrap() error { return failure.Err }

func NewError(code ErrorCode, err error) error { return &PolicyError{Code: code, Err: err} }

func ErrorCodeOf(err error) ErrorCode {
	var failure *PolicyError
	if errors.As(err, &failure) {
		return failure.Code
	}
	switch {
	case errors.Is(err, ErrVersionRequired):
		return CodeVersionRequired
	case errors.Is(err, ErrVersionConflict):
		return CodeVersionConflict
	case errors.Is(err, ErrInvalidPolicy):
		return CodeInvalidPolicy
	default:
		return CodeResolutionFailed
	}
}
