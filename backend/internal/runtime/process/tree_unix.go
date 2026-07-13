//go:build !windows

package process

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// prepareTree places each child in its own process group before exec. Sending
// a signal to the negative pid then reaches descendants without depending on
// shell semantics or parsing process listings.
func prepareTree(command *exec.Cmd) {
	if command == nil {
		return
	}
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// registerTree is intentionally a no-op on Unix: Setpgid is applied before
// exec, and the process group is the daemon-owned tree registration. Closing
// the daemon still reaches descendants through terminateTree.
func registerTree(command *exec.Cmd) (func(), error) {
	return func() {}, nil
}

// RegisterPID keeps the cross-platform terminal adapter on the same explicit
// tree-registration contract. Unix registration is the process group created
// before exec, so no additional handle is required.
func RegisterPID(pid int) (func(), error) {
	if pid <= 0 {
		return nil, syscall.EINVAL
	}
	return func() {}, nil
}

func terminateTree(command *exec.Cmd, force bool) error {
	if command == nil || command.Process == nil {
		return nil
	}
	return TerminatePID(command.Process.Pid, force)
}

// TerminatePID signals the negative process-group ID, reaching the entire
// terminal/process tree rather than only its immediate shell process.
func TerminatePID(pid int, force bool) error {
	if pid <= 0 {
		return nil
	}
	signal := syscall.SIGTERM
	if force {
		signal = syscall.SIGKILL
	}
	if err := syscall.Kill(-pid, signal); err != nil && !errors.Is(err, syscall.ESRCH) && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	return nil
}
