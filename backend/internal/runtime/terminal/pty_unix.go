//go:build !windows

package terminal

import (
	"context"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	githubpty "github.com/creack/pty"
	"github.com/lyming99/autoplan/backend/internal/runtime/process"
)

// unixPTY owns one master PTY and one process group. creack/pty starts the
// child in a new session with the controlling terminal; process.TerminatePID
// then addresses the negative group ID and reaches descendants as one tree.
type unixPTY struct {
	master  *os.File
	command *exec.Cmd
	cleanup func()

	closeOnce sync.Once
	waitOnce  sync.Once
	done      chan struct{}
	exit      Exit
	waitErr   error
}

func startPlatformPTY(ctx context.Context, launch platformLaunch) (PTY, error) {
	if ctx == nil || ctx.Err() != nil || launch.executable == "" || launch.workingDirectory == "" {
		return nil, ErrSpawn
	}
	command := exec.Command(launch.executable, append([]string(nil), launch.args...)...)
	command.Dir = launch.workingDirectory
	command.Env = append([]string(nil), launch.environment...)
	master, err := githubpty.StartWithSize(command, &githubpty.Winsize{Rows: uint16(launch.rows), Cols: uint16(launch.cols)})
	if err != nil || command.Process == nil {
		return nil, ErrSpawn
	}
	cleanup, err := process.RegisterPID(command.Process.Pid)
	if err != nil {
		_ = process.TerminatePID(command.Process.Pid, true)
		_ = command.Wait()
		_ = master.Close()
		return nil, ErrPlatformUnavailable
	}
	var cleanupOnce sync.Once
	closeTree := func() {
		if cleanup != nil {
			cleanupOnce.Do(cleanup)
		}
	}
	return &unixPTY{master: master, command: command, cleanup: closeTree, done: make(chan struct{})}, nil
}

func appendPlatformEnvironment(values map[string]string, _ map[string]string, _ int) {
	// Unix launches only the fixed profile and explicitly allowed request values.
	// No parent environment is inherited through this adapter.
	_ = values
}

func (pty *unixPTY) Read(data []byte) (int, error) { return pty.master.Read(data) }

func (pty *unixPTY) Write(data []byte) (int, error) { return pty.master.Write(data) }

func (pty *unixPTY) Resize(cols, rows int) error {
	if pty == nil || pty.master == nil {
		return ErrSessionClosed
	}
	return githubpty.Setsize(pty.master, &githubpty.Winsize{Rows: uint16(rows), Cols: uint16(cols)})
}

func (pty *unixPTY) Signal(signal Signal) error {
	if pty == nil || pty.command == nil || pty.command.Process == nil {
		return ErrSessionClosed
	}
	if signal == SignalKill {
		return process.TerminatePID(pty.command.Process.Pid, true)
	}
	// SIGINT is a group-wide interrupt; SIGTERM is the normal close path.
	if signal == SignalInterrupt {
		if err := syscall.Kill(-pty.command.Process.Pid, syscall.SIGINT); err != nil && err != syscall.ESRCH {
			return err
		}
		return nil
	}
	return process.TerminatePID(pty.command.Process.Pid, false)
}

func (pty *unixPTY) Kill() error {
	if pty == nil || pty.command == nil || pty.command.Process == nil {
		return nil
	}
	return process.TerminatePID(pty.command.Process.Pid, true)
}

func (pty *unixPTY) Wait(ctx context.Context) (Exit, error) {
	if pty == nil {
		return Exit{}, ErrSessionClosed
	}
	pty.waitOnce.Do(func() {
		go func() {
			err := pty.command.Wait()
			exit := Exit{Code: -1, EndedAt: time.Now().UTC()}
			if pty.command.ProcessState != nil {
				exit.Code = pty.command.ProcessState.ExitCode()
				if status, ok := pty.command.ProcessState.Sys().(syscall.WaitStatus); ok && status.Signaled() {
					exit.Signal = status.Signal().String()
				}
			}
			if pty.cleanup != nil {
				pty.cleanup()
			}
			_ = pty.master.Close()
			pty.exit, pty.waitErr = exit, err
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

func (pty *unixPTY) Close() error {
	if pty == nil {
		return nil
	}
	pty.closeOnce.Do(func() {
		_ = pty.Kill()
		if pty.master != nil {
			_ = pty.master.Close()
		}
		if pty.cleanup != nil {
			pty.cleanup()
		}
	})
	return nil
}

func (pty *unixPTY) PID() int {
	if pty == nil || pty.command == nil || pty.command.Process == nil {
		return 0
	}
	return pty.command.Process.Pid
}
