package terminal

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	filesapp "github.com/lyming99/autoplan/backend/internal/application/files"
	"github.com/lyming99/autoplan/backend/internal/config"
	domainfiles "github.com/lyming99/autoplan/backend/internal/domain/files"
)

var (
	ErrConfiguration       = errors.New("terminal configuration is unavailable")
	ErrPolicyUnavailable   = errors.New("terminal files policy is unavailable")
	ErrWorkingDirDenied    = errors.New("terminal working directory is denied")
	ErrWorkingDirChanged   = errors.New("terminal working directory changed")
	ErrInvalidRequest      = errors.New("terminal request is invalid")
	ErrInputLimit          = errors.New("terminal input exceeds configured limit")
	ErrResizeLimit         = errors.New("terminal resize rate exceeds configured limit")
	ErrSessionLimit        = errors.New("terminal session limit reached")
	ErrPlatformUnavailable = errors.New("terminal pty platform capability is unavailable")
	ErrSpawn               = errors.New("terminal pty could not be started")
	ErrSessionClosed       = errors.New("terminal session is closed")
)

// FailureCode is the safe diagnostic surface for later application and HTTP
// adapters. It intentionally never includes platform, command, cwd, env or
// process-handle details.
type FailureCode string

const (
	CodeConfiguration       FailureCode = "terminal_pty_unavailable"
	CodePolicyUnavailable   FailureCode = "terminal_pty_unavailable"
	CodeWorkingDirDenied    FailureCode = "terminal_cwd_outside_workspace"
	CodeWorkingDirChanged   FailureCode = "terminal_cwd_outside_workspace"
	CodeInvalidRequest      FailureCode = "terminal_invalid_payload"
	CodeInputLimit          FailureCode = "terminal_rate_limited"
	CodeResizeLimit         FailureCode = "terminal_rate_limited"
	CodeSessionLimit        FailureCode = "terminal_session_limit"
	CodePlatformUnavailable FailureCode = "terminal_platform_blocked"
	CodeSpawn               FailureCode = "terminal_pty_unavailable"
	CodeSessionClosed       FailureCode = "terminal_session_not_found"
)

func ErrorCode(err error) FailureCode {
	switch {
	case errors.Is(err, ErrPolicyUnavailable):
		return CodePolicyUnavailable
	case errors.Is(err, ErrWorkingDirDenied):
		return CodeWorkingDirDenied
	case errors.Is(err, ErrWorkingDirChanged):
		return CodeWorkingDirChanged
	case errors.Is(err, ErrInvalidRequest):
		return CodeInvalidRequest
	case errors.Is(err, ErrInputLimit):
		return CodeInputLimit
	case errors.Is(err, ErrResizeLimit):
		return CodeResizeLimit
	case errors.Is(err, ErrSessionLimit):
		return CodeSessionLimit
	case errors.Is(err, ErrPlatformUnavailable):
		return CodePlatformUnavailable
	case errors.Is(err, ErrSpawn):
		return CodeSpawn
	case errors.Is(err, ErrSessionClosed):
		return CodeSessionClosed
	default:
		return CodeConfiguration
	}
}

// WorkingDirectoryPolicy is the sole Files Policy capability granted to the
// PTY factory. It has no policy write, broad read, repository or caller-token
// access. The concrete files Service also verifies realpath immediately before
// this factory performs its own final race check.
type WorkingDirectoryPolicy interface {
	AuthorizeTerminalWorkingDirectory(context.Context, string, string) (domainfiles.Decision, error)
}

var _ WorkingDirectoryPolicy = (*filesapp.Service)(nil)

// Signal is deliberately small and portable. Platform adapters translate it
// to a process-group or job-object operation without exposing OS signals to a
// transport or application service.
type Signal uint8

const (
	SignalInterrupt Signal = iota + 1
	SignalTerminate
	SignalKill
)

// Exit is emitted once by Session.Wait. It contains no raw output, command,
// cwd, environment, PID or OS error text.
type Exit struct {
	Code     int
	Signal   string
	EndedAt  time.Time
	TimedOut bool
}

// PTY is the single cross-platform PTY abstraction. It purposely does not
// expose exec.Cmd, files, Job Objects or platform handles outside this package.
type PTY interface {
	Read([]byte) (int, error)
	Write([]byte) (int, error)
	Resize(cols, rows int) error
	Signal(Signal) error
	Kill() error
	Wait(context.Context) (Exit, error)
	Close() error
	PID() int
}

// SpawnRequest has no executable, shell string, argument list or raw profile
// object. The application can select only an ID from controlled configuration;
// optional env values are individually allowlisted and bounded before spawn.
type SpawnRequest struct {
	ProjectID        int64
	Workspace        string
	WorkingDirectory string
	ProfileID        string
	Environment      map[string]string
	Cols             int
	Rows             int
}

type Dependencies struct {
	Config     config.TerminalRuntime
	Policy     WorkingDirectoryPolicy
	Supervisor *Supervisor
}

// Factory owns only the PTY runtime. Session ownership, authorization of the
// caller and renderer/REST/WebSocket routing remain application work in P003+
// and cannot bypass this Files Policy constrained boundary.
type Factory struct {
	config     config.TerminalRuntime
	limits     Limits
	policy     WorkingDirectoryPolicy
	supervisor *Supervisor
	allowedEnv map[string]string
}

func NewFactory(dependencies Dependencies) (*Factory, error) {
	if dependencies.Policy == nil {
		return nil, ErrPolicyUnavailable
	}
	limits, err := limitsFromConfig(dependencies.Config)
	if err != nil {
		return nil, err
	}
	allowed := make(map[string]string, len(dependencies.Config.AllowedEnvironment))
	for _, name := range dependencies.Config.AllowedEnvironment {
		canonical := canonicalEnvironmentName(name)
		if _, duplicate := allowed[canonical]; duplicate {
			return nil, ErrConfiguration
		}
		allowed[canonical] = canonical
	}
	supervisor := dependencies.Supervisor
	if supervisor == nil {
		supervisor, err = NewSupervisor(limits)
		if err != nil {
			return nil, err
		}
	}
	return &Factory{config: dependencies.Config, limits: limits, policy: dependencies.Policy, supervisor: supervisor, allowedEnv: allowed}, nil
}

func (factory *Factory) Spawn(ctx context.Context, request SpawnRequest) (*Session, error) {
	if factory == nil || factory.policy == nil || factory.supervisor == nil {
		return nil, ErrPolicyUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, ErrSessionClosed
	}
	if request.ProjectID <= 0 || invalidPathInput(request.Workspace) || invalidPathInput(request.WorkingDirectory) {
		return nil, ErrInvalidRequest
	}
	cols, rows := request.Cols, request.Rows
	if cols == 0 {
		cols = factory.limits.DefaultCols
	}
	if rows == 0 {
		rows = factory.limits.DefaultRows
	}
	if !factory.limits.validSize(cols, rows) {
		return nil, ErrInvalidRequest
	}
	profile, found := factory.config.Profile(request.ProfileID)
	if !found {
		return nil, ErrInvalidRequest
	}
	environment, err := factory.environment(profile, request.Environment)
	if err != nil {
		return nil, err
	}
	decision, err := factory.policy.AuthorizeTerminalWorkingDirectory(ctx, request.Workspace, request.WorkingDirectory)
	if err != nil || !decision.Allowed || decision.ResolvedTarget == "" {
		return nil, ErrWorkingDirDenied
	}
	workingDirectory, err := verifyWorkingDirectory(decision)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, ErrSessionClosed
	}
	lease, err := factory.supervisor.Acquire(ctx, request.ProjectID)
	if err != nil {
		return nil, err
	}
	launch := platformLaunch{executable: profile.Executable, args: profile.Args, environment: environment, workingDirectory: workingDirectory, cols: cols, rows: rows}
	pty, err := startPlatformPTY(ctx, launch)
	if err != nil {
		lease.Release()
		return nil, err
	}
	session := newSession(pty, factory.limits)
	lease.Bind(session)
	session.start(lease.Release)
	session.watchContext(ctx)
	return session, nil
}

func (factory *Factory) CloseProject(projectID int64) int {
	if factory == nil || factory.supervisor == nil {
		return 0
	}
	return factory.supervisor.CloseProject(projectID)
}

func (factory *Factory) Shutdown() {
	if factory != nil && factory.supervisor != nil {
		factory.supervisor.Shutdown()
	}
}

func (factory *Factory) environment(profile config.TerminalProfile, input map[string]string) ([]string, error) {
	if len(input) > factory.config.MaxEnvironmentEntries {
		return nil, ErrInvalidRequest
	}
	values := make(map[string]string, len(profile.Environment)+len(input)+len(factory.allowedEnv))
	for name, value := range profile.Environment {
		canonical, permitted := factory.allowedEnv[canonicalEnvironmentName(name)]
		if !permitted || !validEnvironmentName(name) || sensitiveEnvironmentName(name) || !validEnvironmentValue(value, factory.config.MaxEnvironmentValueBytes) {
			clearEnvironment(values)
			return nil, ErrConfiguration
		}
		values[canonical] = value
	}
	for name, value := range input {
		canonical, permitted := factory.allowedEnv[canonicalEnvironmentName(name)]
		if !permitted || !validEnvironmentName(name) || sensitiveEnvironmentName(name) || !validEnvironmentValue(value, factory.config.MaxEnvironmentValueBytes) {
			clearEnvironment(values)
			return nil, ErrInvalidRequest
		}
		values[canonical] = value
	}
	appendPlatformEnvironment(values, factory.allowedEnv, factory.config.MaxEnvironmentValueBytes)
	if len(values) > factory.config.MaxEnvironmentEntries || environmentBytes(values) > factory.config.MaxEnvironmentBytes {
		clearEnvironment(values)
		return nil, ErrInvalidRequest
	}
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	result := make([]string, 0, len(names))
	for _, name := range names {
		result = append(result, name+"="+values[name])
	}
	clearEnvironment(values)
	return result, nil
}

type platformLaunch struct {
	executable       string
	args             []string
	environment      []string
	workingDirectory string
	cols             int
	rows             int
}

func verifyWorkingDirectory(decision domainfiles.Decision) (string, error) {
	if !decision.Allowed || decision.ResolvedTarget == "" {
		return "", ErrWorkingDirDenied
	}
	lexical, err := os.Lstat(decision.ResolvedTarget)
	if err != nil || lexical.Mode()&os.ModeSymlink != 0 || !lexical.IsDir() {
		return "", ErrWorkingDirChanged
	}
	resolved, err := filepath.EvalSymlinks(decision.ResolvedTarget)
	if err != nil {
		return "", ErrWorkingDirChanged
	}
	abs, err := filepath.Abs(resolved)
	if err != nil || !samePath(filepath.Clean(abs), filepath.Clean(decision.ResolvedTarget)) {
		return "", ErrWorkingDirChanged
	}
	info, err := os.Stat(abs)
	if err != nil || !info.IsDir() {
		return "", ErrWorkingDirChanged
	}
	return filepath.Clean(abs), nil
}

func invalidPathInput(value string) bool {
	return value == "" || strings.TrimSpace(value) != value || len(value) > 4096 || strings.ContainsRune(value, 0)
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

func validEnvironmentValue(value string, maximum int) bool {
	return utf8.ValidString(value) && len(value) <= maximum && !strings.ContainsAny(value, "\x00\r\n")
}

func environmentBytes(values map[string]string) int {
	total := 0
	for name, value := range values {
		total += len(name) + len(value) + 1
	}
	return total
}

func canonicalEnvironmentName(value string) string {
	if runtime.GOOS == "windows" {
		return strings.ToUpper(value)
	}
	return value
}

func samePath(left, right string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}

func clearEnvironment(values map[string]string) {
	for name := range values {
		values[name] = ""
		delete(values, name)
	}
}
