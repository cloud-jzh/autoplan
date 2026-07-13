//go:build windows

package terminal

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/UserExistsError/conpty"
	"github.com/lyming99/autoplan/backend/internal/runtime/process"
)

// windowsPTY binds ConPTY to the shared Job Object process-tree guard. ConPTY
// owns console I/O while the Job Object guarantees child-tree cleanup if the
// daemon exits or a partial close occurs.
type windowsPTY struct {
	console *conpty.ConPty
	cleanup func()

	closeOnce sync.Once
	waitOnce  sync.Once
	done      chan struct{}
	exit      Exit
	waitErr   error
}

func startPlatformPTY(ctx context.Context, launch platformLaunch) (PTY, error) {
	if ctx == nil || ctx.Err() != nil || !conpty.IsConPtyAvailable() {
		return nil, ErrPlatformUnavailable
	}
	executable, ok := windowsExecutable(launch.executable, launch.environment)
	if !ok {
		return nil, ErrPlatformUnavailable
	}
	arguments := append([]string{executable}, launch.args...)
	console, err := conpty.Start(
		windowsCommandLine(arguments),
		conpty.ConPtyDimensions(launch.cols, launch.rows),
		conpty.ConPtyEnv(append([]string(nil), launch.environment...)),
		conpty.ConPtyWorkDir(launch.workingDirectory),
	)
	if err != nil || console == nil || console.Pid() <= 0 {
		return nil, ErrSpawn
	}
	cleanup, err := process.RegisterPID(console.Pid())
	if err != nil {
		_ = console.Close()
		return nil, ErrPlatformUnavailable
	}
	var cleanupOnce sync.Once
	closeTree := func() {
		if cleanup != nil {
			cleanupOnce.Do(cleanup)
		}
	}
	return &windowsPTY{console: console, cleanup: closeTree, done: make(chan struct{})}, nil
}

func appendPlatformEnvironment(values map[string]string, allowed map[string]string, maximumValueBytes int) {
	for _, name := range []string{"ComSpec", "PATHEXT", "SystemRoot", "TEMP", "TMP"} {
		canonical, permitted := allowed[canonicalEnvironmentName(name)]
		if !permitted {
			continue
		}
		if _, exists := values[canonical]; exists {
			continue
		}
		if value, exists := os.LookupEnv(name); exists && validEnvironmentValue(value, maximumValueBytes) {
			values[canonical] = value
		}
	}
}

func windowsExecutable(value string, environment []string) (string, bool) {
	if filepath.IsAbs(value) {
		return value, true
	}
	if !strings.EqualFold(value, "cmd.exe") {
		return "", false
	}
	for _, item := range environment {
		name, path, found := strings.Cut(item, "=")
		if found && strings.EqualFold(name, "ComSpec") && filepath.IsAbs(path) {
			return path, true
		}
	}
	return "", false
}

// windowsCommandLine follows CreateProcess quoting rules while preserving the
// pre-separated executable/argument array. It never invokes cmd.exe parsing or
// interpolates a shell expression.
func windowsCommandLine(arguments []string) string {
	parts := make([]string, 0, len(arguments))
	for _, argument := range arguments {
		parts = append(parts, quoteWindowsArgument(argument))
	}
	return strings.Join(parts, " ")
}

func quoteWindowsArgument(value string) string {
	if value != "" && !strings.ContainsAny(value, " \t\n\v\"") {
		return value
	}
	var builder strings.Builder
	builder.WriteByte('"')
	backslashes := 0
	for _, character := range value {
		if character == '\\' {
			backslashes++
			continue
		}
		if character == '"' {
			builder.WriteString(strings.Repeat("\\", backslashes*2+1))
			builder.WriteRune(character)
			backslashes = 0
			continue
		}
		builder.WriteString(strings.Repeat("\\", backslashes))
		backslashes = 0
		builder.WriteRune(character)
	}
	builder.WriteString(strings.Repeat("\\", backslashes*2))
	builder.WriteByte('"')
	return builder.String()
}

func (pty *windowsPTY) Read(data []byte) (int, error) { return pty.console.Read(data) }

func (pty *windowsPTY) Write(data []byte) (int, error) { return pty.console.Write(data) }

func (pty *windowsPTY) Resize(cols, rows int) error {
	if pty == nil || pty.console == nil {
		return ErrSessionClosed
	}
	return pty.console.Resize(cols, rows)
}

func (pty *windowsPTY) Signal(signal Signal) error {
	if pty == nil || pty.console == nil {
		return ErrSessionClosed
	}
	return process.TerminatePID(pty.console.Pid(), signal == SignalKill)
}

func (pty *windowsPTY) Kill() error {
	if pty == nil || pty.console == nil {
		return nil
	}
	if err := process.TerminatePID(pty.console.Pid(), true); err != nil {
		return err
	}
	return nil
}

func (pty *windowsPTY) Wait(ctx context.Context) (Exit, error) {
	if pty == nil || pty.console == nil {
		return Exit{}, ErrSessionClosed
	}
	pty.waitOnce.Do(func() {
		go func() {
			code, err := pty.console.Wait(context.Background())
			if pty.cleanup != nil {
				pty.cleanup()
			}
			pty.exit, pty.waitErr = Exit{Code: int(code), EndedAt: time.Now().UTC()}, err
			close(pty.done)
		}()
	})
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return Exit{}, ctx.Err()
	case <-pty.done:
		return pty.exit, pty.waitErr
	}
}

func (pty *windowsPTY) Close() error {
	if pty == nil || pty.console == nil {
		return nil
	}
	pty.closeOnce.Do(func() {
		_ = pty.console.Close()
		if pty.cleanup != nil {
			pty.cleanup()
		}
	})
	return nil
}

func (pty *windowsPTY) PID() int {
	if pty == nil || pty.console == nil {
		return 0
	}
	return pty.console.Pid()
}
