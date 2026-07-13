//go:build windows

package process

import (
	"errors"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"unsafe"
)

const (
	jobObjectExtendedLimitInformationClass = 9
	jobObjectLimitKillOnJobClose           = 0x00002000
	processSetQuota                        = 0x0100
	processTerminate                       = 0x0001
)

var (
	kernel32                 = syscall.NewLazyDLL("kernel32.dll")
	createJobObject          = kernel32.NewProc("CreateJobObjectW")
	setInformationJobObject  = kernel32.NewProc("SetInformationJobObject")
	assignProcessToJobObject = kernel32.NewProc("AssignProcessToJobObject")
)

type jobObjectBasicLimitInformation struct {
	PerProcessUserTimeLimit int64
	PerJobUserTimeLimit     int64
	LimitFlags              uint32
	MinimumWorkingSetSize   uintptr
	MaximumWorkingSetSize   uintptr
	ActiveProcessLimit      uint32
	Affinity                uintptr
	PriorityClass           uint32
	SchedulingClass         uint32
}

type ioCounters struct {
	ReadOperationCount  uint64
	WriteOperationCount uint64
	OtherOperationCount uint64
	ReadTransferCount   uint64
	WriteTransferCount  uint64
	OtherTransferCount  uint64
}

type jobObjectExtendedLimitInformation struct {
	BasicLimitInformation jobObjectBasicLimitInformation
	IOInfo                ioCounters
	ProcessMemoryLimit    uintptr
	JobMemoryLimit        uintptr
	PeakProcessMemoryUsed uintptr
	PeakJobMemoryUsed     uintptr
}

// Windows has no portable process-group equivalent exposed by os/exec. The
// system taskkill utility receives a fixed argument array and /T walks the
// descendant tree. It is intentionally not launched through cmd.exe.
func prepareTree(command *exec.Cmd) {
	if command == nil {
		return
	}
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
}

// registerTree assigns the child to a kill-on-close Job Object. It provides
// crash cleanup that taskkill alone cannot: if the daemon dies, Windows closes
// the Job handle and terminates the entire registered descendant tree.
func registerTree(command *exec.Cmd) (func(), error) {
	if command == nil || command.Process == nil {
		return func() {}, nil
	}
	return RegisterPID(command.Process.Pid)
}

// RegisterPID attaches a separately-spawned process (such as a ConPTY child)
// to a kill-on-close Job Object. It is deliberately PID-based so terminal and
// ordinary process adapters share the same crash-cleanup primitive.
func RegisterPID(pid int) (func(), error) {
	if pid <= 0 {
		return nil, syscall.EINVAL
	}
	job, _, callErr := createJobObject.Call(0, 0)
	if job == 0 {
		return nil, callError(callErr)
	}
	closeJob := func() { _ = syscall.CloseHandle(syscall.Handle(job)) }
	information := jobObjectExtendedLimitInformation{}
	information.BasicLimitInformation.LimitFlags = jobObjectLimitKillOnJobClose
	result, _, callErr := setInformationJobObject.Call(
		job,
		uintptr(jobObjectExtendedLimitInformationClass),
		uintptr(unsafe.Pointer(&information)),
		unsafe.Sizeof(information),
	)
	if result == 0 {
		closeJob()
		return nil, callError(callErr)
	}
	process, err := syscall.OpenProcess(processSetQuota|processTerminate, false, uint32(pid))
	if err != nil {
		closeJob()
		return nil, err
	}
	defer syscall.CloseHandle(process)
	result, _, callErr = assignProcessToJobObject.Call(job, uintptr(process))
	if result == 0 {
		closeJob()
		return nil, callError(callErr)
	}
	return closeJob, nil
}

func callError(err error) error {
	if err == nil || err == syscall.Errno(0) {
		return syscall.EINVAL
	}
	return err
}

func terminateTree(command *exec.Cmd, force bool) error {
	if command == nil || command.Process == nil {
		return nil
	}
	return TerminatePID(command.Process.Pid, force)
}

// TerminatePID uses taskkill's fixed argument array and /T descendant walk.
// No terminal process is ever routed through cmd.exe or a formatted shell
// expression.
func TerminatePID(pid int, force bool) error {
	if pid <= 0 {
		return nil
	}
	arguments := []string{"/PID", strconv.Itoa(pid), "/T"}
	if force {
		arguments = append(arguments, "/F")
	}
	helper := exec.Command("taskkill.exe", arguments...)
	helper.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if err := helper.Run(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	return nil
}
