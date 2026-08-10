//go:build windows

package terminal

import (
	"os/exec"
)

func setSysProcAttrSetsid(cmd *exec.Cmd) {
	// Setsid is not applicable on Windows
}

func killProcessGroup(pid int) {
	// On Windows, Process.Kill() is handled by standard os.Process.Kill()
}
