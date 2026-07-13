// Package process owns the policy-constrained external-process boundary. It
// accepts an executable plus argument array only; no shell command string is
// represented anywhere in this API.
package process

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"time"

	"github.com/lyming99/autoplan/backend/internal/config"
	domainsecrets "github.com/lyming99/autoplan/backend/internal/domain/secrets"
	platformsecrets "github.com/lyming99/autoplan/backend/internal/platform/secrets"
	"github.com/lyming99/autoplan/backend/internal/runtime/scheduler"
)

var (
	ErrInvalidSpec             = errors.New("process specification is invalid")
	ErrInputLimit              = errors.New("process input exceeds configured limit")
	ErrEnvironmentLimit        = errors.New("process environment exceeds configured limit")
	ErrWorkDirDenied           = errors.New("process working directory is not authorized")
	ErrWorkDirChanged          = errors.New("process working directory changed after authorization")
	ErrSpawn                   = errors.New("process could not be started")
	ErrTimedOut                = errors.New("process timed out")
	ErrCancelled               = errors.New("process was cancelled")
	ErrOutputRedaction         = errors.New("process output redaction failed")
	ErrOutputRead              = errors.New("process output could not be read")
	ErrRunnerUnavailable       = errors.New("process runner is unavailable")
	ErrSecretUnavailable       = errors.New("process secret material is unavailable")
	ErrProjectConcurrencyLimit = errors.New("project process concurrency limit reached")
	ErrGlobalConcurrencyLimit  = errors.New("global process concurrency limit reached")
)

// FailureCode is the safe diagnostic surface for callers. The underlying
// error values deliberately contain no command, path, environment or secret
// material and may be mapped to an Operation failure_code without inspection.
type FailureCode string

const (
	CodeInvalidSpec             FailureCode = "PROCESS_SPEC_INVALID"
	CodeInputLimit              FailureCode = "PROCESS_INPUT_LIMIT"
	CodeEnvironmentLimit        FailureCode = "PROCESS_ENVIRONMENT_LIMIT"
	CodeWorkDirDenied           FailureCode = "PROCESS_WORKDIR_DENIED"
	CodeWorkDirChanged          FailureCode = "PROCESS_WORKDIR_CHANGED"
	CodeSpawn                   FailureCode = "PROCESS_START_FAILED"
	CodeTimedOut                FailureCode = "RUNTIME_TIMEOUT"
	CodeCancelled               FailureCode = "OPERATION_CANCELLED"
	CodeOutputRedaction         FailureCode = "OUTPUT_REDACTION_FAILED"
	CodeOutputRead              FailureCode = "PROCESS_OUTPUT_READ_FAILED"
	CodeRunnerUnavailable       FailureCode = "RUNTIME_UNAVAILABLE"
	CodeSecretUnavailable       FailureCode = "PROCESS_SECRET_UNAVAILABLE"
	CodeProjectConcurrencyLimit FailureCode = "PROJECT_PROCESS_LIMIT"
	CodeGlobalConcurrencyLimit  FailureCode = "GLOBAL_PROCESS_LIMIT"
)

func ErrorCode(err error) FailureCode {
	switch {
	case errors.Is(err, ErrInputLimit):
		return CodeInputLimit
	case errors.Is(err, ErrEnvironmentLimit):
		return CodeEnvironmentLimit
	case errors.Is(err, ErrWorkDirDenied):
		return CodeWorkDirDenied
	case errors.Is(err, ErrWorkDirChanged):
		return CodeWorkDirChanged
	case errors.Is(err, ErrSpawn):
		return CodeSpawn
	case errors.Is(err, ErrTimedOut):
		return CodeTimedOut
	case errors.Is(err, ErrCancelled):
		return CodeCancelled
	case errors.Is(err, ErrOutputRedaction):
		return CodeOutputRedaction
	case errors.Is(err, ErrOutputRead):
		return CodeOutputRead
	case errors.Is(err, ErrSecretUnavailable):
		return CodeSecretUnavailable
	case errors.Is(err, ErrProjectConcurrencyLimit):
		return CodeProjectConcurrencyLimit
	case errors.Is(err, ErrGlobalConcurrencyLimit):
		return CodeGlobalConcurrencyLimit
	case errors.Is(err, ErrRunnerUnavailable):
		return CodeRunnerUnavailable
	default:
		return CodeInvalidSpec
	}
}

// ResourceKind identifies a persisted runtime definition. It is metadata for
// authorization/audit correlation only; callers cannot supply a command, pid
// or shell mode through this reference.
type ResourceKind string

const (
	ResourceScript   ResourceKind = "script"
	ResourceExecutor ResourceKind = "executor"
)

type ResourceRef struct {
	Kind ResourceKind
	ID   int64
}

func (ref ResourceRef) valid() bool {
	return (ref.Kind == ResourceScript || ref.Kind == ResourceExecutor) && ref.ID > 0
}

// Spec is a closed process request. Workspace and WorkingDirectory are
// authorization inputs only and never appear in Result output.
type Spec struct {
	ProjectID         int64
	Resource          ResourceRef
	Workspace         string
	WorkingDirectory  string
	Executable        string
	Args              []string
	Environment       map[string]string
	SecretEnvironment []SecretEnvironment
	Input             []byte
	SecretStdin       *SecretStdin
	InlineScript      *InlineScript
	Timeout           time.Duration
}

// SecretEnvironment carries a provider reference without storing material in
// the request. Its value is resolved only after Files Policy authorization.
type SecretEnvironment struct {
	Name      string
	Binding   domainsecrets.Binding
	Reference string
}

// SecretStdin carries one opaque value to standard input. A single reference
// avoids ambiguous concatenation and ensures the Runner can clear the exact
// transient buffer after the child has been reaped.
type SecretStdin struct {
	Binding   domainsecrets.Binding
	Reference string
}

// InlineScript is a persisted Script body waiting to be staged by the Runner.
// The executable remains a separately trusted interpreter and Args remain a
// distinct array; callers cannot smuggle a shell command through the body.
type InlineScript struct {
	Body   []byte
	Suffix string
}

func (spec Spec) validate(runtime config.ProcessRuntime) error {
	if spec.ProjectID <= 0 || !validExecutable(spec.Executable) || !validPathInput(spec.Workspace) || !validPathInput(spec.WorkingDirectory) ||
		len(spec.Args) > runtime.MaxArguments || len(spec.SecretEnvironment) > runtime.MaxEnvironmentEntries ||
		((spec.Resource.Kind != "" || spec.Resource.ID != 0) && !spec.Resource.valid()) {
		return ErrInvalidSpec
	}
	argumentBytes := 0
	for _, argument := range spec.Args {
		if !validArgument(argument) {
			return ErrInvalidSpec
		}
		argumentBytes += len(argument)
		if argumentBytes > runtime.MaxArgumentBytes {
			return ErrInvalidSpec
		}
	}
	if len(spec.Input) > runtime.MaxInputBytes {
		return ErrInputLimit
	}
	if spec.SecretStdin != nil {
		if len(spec.Input) != 0 || platformsecrets.ValidateBinding(spec.SecretStdin.Binding) != nil ||
			platformsecrets.ValidateReference(spec.SecretStdin.Reference) != nil {
			return ErrInvalidSpec
		}
	}
	if spec.InlineScript != nil {
		if spec.Resource.Kind != ResourceScript || len(spec.InlineScript.Body) == 0 ||
			!validInlineSuffix(spec.InlineScript.Suffix) ||
			!validInlineInterpreter(spec.Executable) {
			return ErrInvalidSpec
		}
		if len(spec.InlineScript.Body) > runtime.MaxInputBytes || len(spec.Input)+len(spec.InlineScript.Body) > runtime.MaxInputBytes {
			return ErrInputLimit
		}
	}
	environmentBytes := 0
	if len(spec.Environment)+len(spec.SecretEnvironment) > runtime.MaxEnvironmentEntries {
		return ErrEnvironmentLimit
	}
	for name, value := range spec.Environment {
		if !validEnvironmentName(name) || sensitiveEnvironmentName(name) || strings.ContainsAny(value, "\x00\r\n") {
			return ErrInvalidSpec
		}
		environmentBytes += len(name) + len(value) + 1
		if environmentBytes > runtime.MaxEnvironmentBytes {
			return ErrEnvironmentLimit
		}
	}
	seenSecrets := make(map[string]struct{}, len(spec.SecretEnvironment))
	for _, item := range spec.SecretEnvironment {
		if !validEnvironmentName(item.Name) || platformsecrets.ValidateBinding(item.Binding) != nil ||
			platformsecrets.ValidateReference(item.Reference) != nil {
			return ErrInvalidSpec
		}
		if _, duplicate := seenSecrets[item.Name]; duplicate {
			return ErrInvalidSpec
		}
		seenSecrets[item.Name] = struct{}{}
		if _, duplicate := spec.Environment[item.Name]; duplicate {
			return ErrInvalidSpec
		}
	}
	if spec.Timeout < 0 || spec.Timeout > config.MaximumProcessTimeout {
		return ErrInvalidSpec
	}
	return nil
}

func (spec Spec) effectiveTimeout(runtime config.ProcessRuntime) time.Duration {
	if spec.Timeout > 0 {
		return spec.Timeout
	}
	return runtime.DefaultTimeout
}

func validExecutable(value string) bool {
	return value != "" && strings.TrimSpace(value) == value && len(value) <= 4096 &&
		!strings.ContainsAny(value, "\x00\r\n")
}

func validArgument(value string) bool {
	return len(value) <= 8192 && !strings.ContainsAny(value, "\x00\r\n")
}

func validPathInput(value string) bool {
	return value != "" && strings.TrimSpace(value) == value && len(value) <= 4096 && !strings.ContainsRune(value, 0)
}

func validInlineSuffix(value string) bool {
	switch value {
	case ".cjs", ".js", ".sh", ".ps1", ".cmd", ".bat":
		return true
	default:
		return false
	}
}

func validInlineInterpreter(value string) bool {
	name := strings.ToLower(filepath.Base(value))
	name = strings.TrimSuffix(name, ".exe")
	switch name {
	case "node", "bash", "powershell", "pwsh", "cmd":
		return true
	default:
		return false
	}
}

func validEnvironmentName(value string) bool {
	if len(value) == 0 || len(value) > 128 {
		return false
	}
	for index, character := range value {
		if index == 0 && !(character == '_' || character >= 'A' && character <= 'Z' || character >= 'a' && character <= 'z') {
			return false
		}
		if index > 0 && !(character == '_' || character >= 'A' && character <= 'Z' || character >= 'a' && character <= 'z' || character >= '0' && character <= '9') {
			return false
		}
	}
	return true
}

func sensitiveEnvironmentName(value string) bool {
	upper := strings.ToUpper(value)
	if upper == "TOKEN" || upper == "SECRET" || upper == "PASSWORD" || upper == "PASSPHRASE" || upper == "API_KEY" || upper == "AUTHORIZATION" || upper == "COOKIE" {
		return true
	}
	for _, suffix := range []string{"_TOKEN", "_SECRET", "_PASSWORD", "_PASSPHRASE", "_API_KEY", "_AUTH_TOKEN", "_AUTHORIZATION", "_COOKIE"} {
		if strings.HasSuffix(upper, suffix) {
			return true
		}
	}
	return false
}

func secretBindings(items []SecretEnvironment) []platformsecrets.EnvironmentBinding {
	result := make([]platformsecrets.EnvironmentBinding, 0, len(items))
	for _, item := range items {
		result = append(result, platformsecrets.EnvironmentBinding{Name: item.Name, Binding: item.Binding, Reference: item.Reference})
	}
	return result
}

func secretStdinBinding(value *SecretStdin) platformsecrets.StdinBinding {
	if value == nil {
		return platformsecrets.StdinBinding{}
	}
	return platformsecrets.StdinBinding{Binding: value.Binding, Reference: value.Reference}
}

// Result contains only bounded and redacted output. It is safe for internal
// Operation metadata but transports must still expose stable status fields,
// never raw process output or launch configuration.
type Result struct {
	ExitCode  int
	StartedAt time.Time
	EndedAt   time.Time
	TimedOut  bool
	Cancelled bool
	Stdout    Output
	Stderr    Output
}

// Clock is shared with the scheduler so deterministic runtime fakes drive
// queue retries and child-process timeout/grace behavior through one clock.
type Clock = scheduler.Clock
type Timer = scheduler.Timer

func contextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}
