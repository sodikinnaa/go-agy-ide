//go:build !windows

package terminal

import (
	"os/exec"
	"syscall"
)

func setSysProcAttrSetsid(cmd *exec.Cmd) {
	if cmd != nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	}
}

func killProcessGroup(pid int) {
	if pid > 0 {
		_ = syscall.Kill(-pid, syscall.SIGKILL)
	}
}
