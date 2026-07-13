//go:build windows

package process

import (
	"os/exec"
	"testing"
)

func TestPrepareTreeHidesWindowsHelperWindows(t *testing.T) {
	command := exec.Command("ignored.exe")
	prepareTree(command)
	if command.SysProcAttr == nil || !command.SysProcAttr.HideWindow {
		t.Fatal("runner must suppress visible Windows process windows")
	}
}
