package process

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	filesapp "github.com/lyming99/autoplan/backend/internal/application/files"
	"github.com/lyming99/autoplan/backend/internal/config"
	domainfiles "github.com/lyming99/autoplan/backend/internal/domain/files"
	platformsecrets "github.com/lyming99/autoplan/backend/internal/platform/secrets"
	"github.com/lyming99/autoplan/backend/internal/runtime/scheduler"
)

// WorkDirectoryPolicy is deliberately narrower than the Files service. The
// runner can authorize a working directory but cannot read or alter policy.
type WorkDirectoryPolicy interface {
	AuthorizeWorkingDirectory(context.Context, string, string) (domainfiles.Decision, error)
}

var _ WorkDirectoryPolicy = (*filesapp.Service)(nil)

type Dependencies struct {
	Config          config.ProcessRuntime
	Policy          WorkDirectoryPolicy
	Secrets         platformsecrets.Provider
	BaseEnvironment map[string]string
	Clock           Clock
	Supervisor      *Supervisor
}

// Runner owns the complete lifecycle of one external process. It has no shell
// entry point: exec.Command receives the executable and argument array as-is.
type Runner struct {
	config     config.ProcessRuntime
	policy     WorkDirectoryPolicy
	secrets    platformsecrets.Provider
	baseEnv    map[string]string
	allowedEnv map[string]string
	clock      Clock
	supervisor *Supervisor
	shutdown   chan struct{}
	closeOnce  sync.Once
	startMu    sync.Mutex
}

func NewRunner(dependencies Dependencies) (*Runner, error) {
	if !dependencies.Config.Valid() || dependencies.Policy == nil {
		return nil, ErrRunnerUnavailable
	}
	allowed := make(map[string]string, len(dependencies.Config.AllowedEnvironment))
	for _, name := range dependencies.Config.AllowedEnvironment {
		canonical := canonicalEnvironmentName(name)
		if _, duplicate := allowed[canonical]; duplicate {
			return nil, ErrRunnerUnavailable
		}
		allowed[canonical] = canonical
	}
	if len(dependencies.BaseEnvironment) > dependencies.Config.MaxEnvironmentEntries {
		return nil, ErrRunnerUnavailable
	}
	base := make(map[string]string, len(dependencies.BaseEnvironment))
	baseBytes := 0
	for name, value := range dependencies.BaseEnvironment {
		canonical, ok := allowed[canonicalEnvironmentName(name)]
		if !ok || !validEnvironmentName(name) || strings.ContainsAny(value, "\x00\r\n") {
			return nil, ErrRunnerUnavailable
		}
		if _, duplicate := base[canonical]; duplicate {
			return nil, ErrRunnerUnavailable
		}
		baseBytes += len(canonical) + len(value) + 1
		if baseBytes > dependencies.Config.MaxEnvironmentBytes {
			return nil, ErrRunnerUnavailable
		}
		base[canonical] = value
	}
	clock := dependencies.Clock
	if clock == nil {
		clock = scheduler.NewSystemClock()
	}
	supervisor := dependencies.Supervisor
	if supervisor == nil {
		var err error
		supervisor, err = NewSupervisor(dependencies.Config)
		if err != nil {
			return nil, ErrRunnerUnavailable
		}
	}
	return &Runner{
		config: dependencies.Config, policy: dependencies.Policy, secrets: dependencies.Secrets,
		baseEnv: base, allowedEnv: allowed, clock: clock, supervisor: supervisor, shutdown: make(chan struct{}),
	}, nil
}

// Shutdown cancels all in-flight Run calls. Each invocation owns its own wait
// channel, so cancellation still reaps the child before returning and never
// leaves a process or zombie behind.
func (runner *Runner) Shutdown() {
	if runner == nil || runner.shutdown == nil {
		return
	}
	runner.closeOnce.Do(func() {
		runner.startMu.Lock()
		close(runner.shutdown)
		runner.startMu.Unlock()
		if runner.supervisor != nil {
			runner.supervisor.Shutdown()
		}
	})
}

func (runner *Runner) Run(ctx context.Context, spec Spec) (Result, error) {
	return runner.run(ctx, spec, nil, false)
}

// RunWithInput uses the same policy, concurrency, timeout, redaction and
// process-tree lifecycle as Run while keeping caller input out of Spec. It is
// intended for bounded interactive prompts such as Chat; its bytes are treated
// as sensitive for output redaction and cleared before this method returns.
func (runner *Runner) RunWithInput(ctx context.Context, spec Spec, input []byte) (Result, error) {
	if runner == nil {
		return Result{}, ErrRunnerUnavailable
	}
	if len(spec.Input) != 0 || spec.SecretStdin != nil {
		return Result{}, ErrInvalidSpec
	}
	if len(input) > runner.config.MaxInputBytes {
		return Result{}, ErrInputLimit
	}
	if !utf8.Valid(input) || bytes.IndexByte(input, 0) >= 0 {
		return Result{}, ErrInvalidSpec
	}
	copy := append([]byte(nil), input...)
	defer clearBytes(copy)
	return runner.run(ctx, spec, copy, true)
}

func (runner *Runner) run(ctx context.Context, spec Spec, directInput []byte, directInputSensitive bool) (Result, error) {
	if runner == nil || runner.policy == nil || runner.clock == nil || runner.supervisor == nil || !runner.config.Valid() {
		return Result{}, ErrRunnerUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-runner.shutdown:
		return Result{}, ErrCancelled
	default:
	}
	if contextError(ctx) != nil {
		return Result{}, ErrCancelled
	}
	if err := spec.validate(runner.config); err != nil {
		return Result{}, err
	}
	decision, err := runner.policy.AuthorizeWorkingDirectory(ctx, spec.Workspace, spec.WorkingDirectory)
	if err != nil || !decision.Allowed {
		return Result{}, ErrWorkDirDenied
	}
	workingDirectory, err := verifyWorkingDirectory(decision)
	if err != nil {
		return Result{}, err
	}
	release, err := runner.supervisor.Acquire(ctx, spec.ProjectID)
	if err != nil {
		return Result{}, err
	}
	defer release()
	stagedScript, cleanupScript, err := stageInlineScript(spec.InlineScript)
	if err != nil {
		return Result{}, ErrInvalidSpec
	}
	defer cleanupScript()
	environment, redactor, err := runner.buildEnvironment(ctx, spec, workingDirectory)
	if err != nil {
		return Result{}, err
	}
	defer clearEnvironmentList(environment)
	input, sensitiveInput, err := runner.resolveInput(ctx, spec, directInput, directInputSensitive)
	if err != nil {
		return Result{}, err
	}
	defer clearBytes(input)
	defer clearStrings(sensitiveInput)
	redactor = redactor.WithSensitiveValues(sensitiveInput...)
	redactor = redactor.WithPaths(stagedScript)
	executable, err := resolveExecutable(spec.Executable, environment)
	if err != nil {
		return Result{}, ErrSpawn
	}

	arguments := append([]string(nil), spec.Args...)
	if stagedScript != "" {
		arguments = append(arguments, stagedScript)
	}
	command := exec.Command(executable, arguments...)
	command.Dir = workingDirectory
	command.Env = environment
	if len(input) != 0 {
		command.Stdin = bytes.NewReader(input)
	}
	prepareTree(command)
	budget := newOutputBudget(runner.config.MaxCombinedBytes, runner.config.MaxCombinedLines)
	sequencer := &outputSequencer{}
	stdoutCollector := newRedactedOutputCollector(OutputStdout, runner.config.MaxStreamBytes, runner.config.MaxStreamLines, runner.config.TailBytes, runner.config.ReadChunkBytes, budget, redactor, sequencer)
	stderrCollector := newRedactedOutputCollector(OutputStderr, runner.config.MaxStreamBytes, runner.config.MaxStreamLines, runner.config.TailBytes, runner.config.ReadChunkBytes, budget, redactor, sequencer)
	command.Stdout = stdoutCollector
	command.Stderr = stderrCollector
	runner.startMu.Lock()
	select {
	case <-runner.shutdown:
		runner.startMu.Unlock()
		return Result{}, ErrCancelled
	default:
	}
	if contextError(ctx) != nil {
		runner.startMu.Unlock()
		return Result{}, ErrCancelled
	}
	err = command.Start()
	runner.startMu.Unlock()
	if err != nil {
		return Result{}, ErrSpawn
	}
	treeCleanup, err := registerTree(command)
	if err != nil {
		_ = terminateTree(command, true)
		_ = command.Wait()
		return Result{}, ErrRunnerUnavailable
	}
	defer treeCleanup()

	result := Result{ExitCode: -1, StartedAt: runner.clock.Now()}

	wait := make(chan error, 1)
	go func() { wait <- command.Wait() }()
	timer := runner.clock.NewTimer(spec.effectiveTimeout(runner.config))
	defer timer.Stop()
	var waitErr error
	select {
	case waitErr = <-wait:
	case <-ctx.Done():
		result.Cancelled = true
		waitErr = runner.stopAndReap(command, wait)
	case <-runner.shutdown:
		result.Cancelled = true
		waitErr = runner.stopAndReap(command, wait)
	case <-timer.C():
		result.TimedOut = true
		waitErr = runner.stopAndReap(command, wait)
	}
	result.EndedAt = runner.clock.Now()
	if command.ProcessState != nil {
		result.ExitCode = command.ProcessState.ExitCode()
	}
	result.Stdout = stdoutCollector.finalize(redactor)
	result.Stderr = stderrCollector.finalize(redactor)

	if result.Stdout.RedactionFailed || result.Stderr.RedactionFailed {
		return result, ErrOutputRedaction
	}
	if result.TimedOut {
		return result, ErrTimedOut
	}
	if result.Cancelled {
		return result, ErrCancelled
	}
	if waitErr != nil && command.ProcessState == nil {
		return result, ErrRunnerUnavailable
	}
	return result, nil
}

func (runner *Runner) stopAndReap(command *exec.Cmd, wait <-chan error) error {
	_ = terminateTree(command, false)
	grace := runner.clock.NewTimer(runner.config.GracePeriod)
	defer grace.Stop()
	select {
	case err := <-wait:
		return err
	case <-grace.C():
		_ = terminateTree(command, true)
		return <-wait
	}
}

func (runner *Runner) buildEnvironment(ctx context.Context, spec Spec, workingDirectory string) ([]string, Redactor, error) {
	values := make(map[string]string, len(runner.baseEnv)+len(spec.Environment)+len(spec.SecretEnvironment))
	for name, value := range runner.baseEnv {
		values[name] = value
	}
	explicit := make(map[string]struct{}, len(spec.Environment))
	for name, value := range spec.Environment {
		canonical, ok := runner.allowedEnv[canonicalEnvironmentName(name)]
		if !ok {
			clearEnvironment(values)
			return nil, Redactor{}, ErrInvalidSpec
		}
		if _, duplicate := explicit[canonical]; duplicate {
			clearEnvironment(values)
			return nil, Redactor{}, ErrInvalidSpec
		}
		explicit[canonical] = struct{}{}
		values[canonical] = value
	}
	if len(spec.SecretEnvironment) != 0 {
		if runner.secrets == nil {
			clearEnvironment(values)
			return nil, Redactor{}, ErrSecretUnavailable
		}
		resolved, err := platformsecrets.ResolveEnvironmentBounded(
			ctx, runner.secrets, secretBindings(spec.SecretEnvironment),
			runner.config.MaxEnvironmentEntries, runner.config.MaxEnvironmentBytes, runner.config.MaxEnvironmentBytes,
		)
		if err != nil {
			clearEnvironment(values)
			if contextError(ctx) != nil {
				return nil, Redactor{}, ErrCancelled
			}
			if errors.Is(err, platformsecrets.ErrTooLarge) {
				return nil, Redactor{}, ErrEnvironmentLimit
			}
			return nil, Redactor{}, ErrSecretUnavailable
		}
		secretNames := make(map[string]struct{}, len(resolved))
		for name, value := range resolved {
			canonical, ok := runner.allowedEnv[canonicalEnvironmentName(name)]
			if !ok || explicitContains(explicit, canonical) {
				clearEnvironment(resolved)
				clearEnvironment(values)
				return nil, Redactor{}, ErrInvalidSpec
			}
			if _, duplicate := secretNames[canonical]; duplicate {
				clearEnvironment(resolved)
				clearEnvironment(values)
				return nil, Redactor{}, ErrInvalidSpec
			}
			secretNames[canonical] = struct{}{}
			values[canonical] = value
		}
		clearEnvironment(resolved)
	}
	if len(values) > runner.config.MaxEnvironmentEntries || environmentBytes(values) > runner.config.MaxEnvironmentBytes {
		clearEnvironment(values)
		return nil, Redactor{}, ErrEnvironmentLimit
	}
	redactor := NewRedactor(values, spec.Workspace, spec.WorkingDirectory, workingDirectory)
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	environment := make([]string, 0, len(names))
	for _, name := range names {
		environment = append(environment, name+"="+values[name])
	}
	clearEnvironment(values)
	return environment, redactor, nil
}

func (runner *Runner) resolveInput(ctx context.Context, spec Spec, directInput []byte, directInputSensitive bool) ([]byte, []string, error) {
	if directInput != nil {
		if len(directInput) > runner.config.MaxInputBytes || !utf8.Valid(directInput) || bytes.IndexByte(directInput, 0) >= 0 {
			return nil, nil, ErrInputLimit
		}
		input := append([]byte(nil), directInput...)
		if !directInputSensitive || len(input) == 0 {
			return input, nil, nil
		}
		return input, []string{string(input)}, nil
	}
	if spec.SecretStdin == nil {
		if len(spec.Input) == 0 {
			return nil, nil, nil
		}
		input := append([]byte(nil), spec.Input...)
		return input, nil, nil
	}
	if runner.secrets == nil {
		return nil, nil, ErrSecretUnavailable
	}
	input, err := platformsecrets.ResolveStdin(ctx, runner.secrets, secretStdinBinding(spec.SecretStdin), runner.config.MaxInputBytes)
	if err != nil {
		if contextError(ctx) != nil {
			return nil, nil, ErrCancelled
		}
		if errors.Is(err, platformsecrets.ErrTooLarge) {
			return nil, nil, ErrInputLimit
		}
		return nil, nil, ErrSecretUnavailable
	}
	return input, []string{string(input)}, nil
}

func verifyWorkingDirectory(decision domainfiles.Decision) (string, error) {
	if !decision.Allowed || decision.ResolvedTarget == "" {
		return "", ErrWorkDirDenied
	}
	lexicalInfo, err := os.Lstat(decision.ResolvedTarget)
	if err != nil || lexicalInfo.Mode()&os.ModeSymlink != 0 {
		return "", ErrWorkDirChanged
	}
	info, err := os.Stat(decision.ResolvedTarget)
	if err != nil || !info.IsDir() {
		return "", ErrWorkDirChanged
	}
	resolved, err := filepath.EvalSymlinks(decision.ResolvedTarget)
	if err != nil {
		return "", ErrWorkDirChanged
	}
	absolute, err := filepath.Abs(resolved)
	if err != nil || !samePath(filepath.Clean(absolute), filepath.Clean(decision.ResolvedTarget)) {
		return "", ErrWorkDirChanged
	}
	return filepath.Clean(absolute), nil
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

func clearEnvironmentList(values []string) {
	for index := range values {
		values[index] = ""
	}
}

func clearBytes(values []byte) {
	for index := range values {
		values[index] = 0
	}
}

func clearStrings(values []string) {
	for index := range values {
		values[index] = ""
	}
}

func environmentBytes(values map[string]string) int {
	total := 0
	for name, value := range values {
		total += len(name) + len(value) + 1
	}
	return total
}

func explicitContains(values map[string]struct{}, name string) bool {
	_, found := values[name]
	return found
}

func stageInlineScript(script *InlineScript) (string, func(), error) {
	if script == nil {
		return "", func() {}, nil
	}
	directory, err := os.MkdirTemp("", "autoplan-process-script-")
	if err != nil {
		return "", func() {}, err
	}
	cleanup := func() { _ = os.RemoveAll(directory) }
	name := filepath.Join(directory, "script"+script.Suffix)
	file, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		cleanup()
		return "", func() {}, err
	}
	count, writeErr := file.Write(script.Body)
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil || count != len(script.Body) {
		cleanup()
		return "", func() {}, ErrInvalidSpec
	}
	return name, cleanup, nil
}

// resolveExecutable never consults the host process environment. A bare name
// is resolved only through the same allowlisted PATH passed to the child;
// explicit paths remain explicit and are not split or shell-expanded.
func resolveExecutable(value string, environment []string) (string, error) {
	if filepath.IsAbs(value) || strings.ContainsAny(value, `\\/`) {
		return value, nil
	}
	path, found := environmentValue(environment, "PATH")
	if !found || path == "" {
		return "", ErrSpawn
	}
	extensions := []string{""}
	if runtime.GOOS == "windows" && filepath.Ext(value) == "" {
		if extensionPath, ok := environmentValue(environment, "PATHEXT"); ok {
			for _, extension := range strings.Split(extensionPath, ";") {
				if extension != "" {
					extensions = append(extensions, extension)
				}
			}
		}
	}
	for _, directory := range filepath.SplitList(path) {
		if directory == "" {
			continue
		}
		for _, extension := range extensions {
			candidate := filepath.Join(directory, value+extension)
			info, err := os.Stat(candidate)
			if err != nil || info.IsDir() {
				continue
			}
			if runtime.GOOS != "windows" && info.Mode().Perm()&0111 == 0 {
				continue
			}
			return candidate, nil
		}
	}
	return "", ErrSpawn
}

func environmentValue(environment []string, wanted string) (string, bool) {
	for _, item := range environment {
		name, value, found := strings.Cut(item, "=")
		if found && canonicalEnvironmentName(name) == canonicalEnvironmentName(wanted) {
			return value, true
		}
	}
	return "", false
}
