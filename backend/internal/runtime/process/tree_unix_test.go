//go:build !windows

package process

import (
	"os/exec"
	"testing"
)

func TestPrepareTreeCreatesUnixProcessGroup(t *testing.T) {
	command := exec.Command("ignored")
	prepareTree(command)
	if command.SysProcAttr == nil || !command.SysProcAttr.Setpgid {
		t.Fatal("runner must create a dedicated Unix process group")
	}
}
