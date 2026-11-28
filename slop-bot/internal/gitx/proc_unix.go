//go:build unix

package gitx

import (
	"os/exec"
	"syscall"
)

func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func terminateGroup(cmd *exec.Cmd) error {
	// With Setpgid, git's pid is its group's id; the minus signals the group.
	return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
}
