//go:build unix

package doctor

import (
	"os/exec"
	"syscall"
)

// inOwnGroup runs a probe in its own process group, ended as a group: an
// app-server's children (MCP servers, say) would otherwise outlive it and
// hold its pipes open.
func inOwnGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// With Setpgid the probe's pid is its group's id; the minus signals
	// the group.
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) }
}

// endGroup ends what is left of a probe's group after it exited.
func endGroup(cmd *exec.Cmd) { signalGroup(cmd, syscall.SIGTERM) }

// killGroup kills what is left of the group when ending it didn't.
func killGroup(cmd *exec.Cmd) { signalGroup(cmd, syscall.SIGKILL) }

func signalGroup(cmd *exec.Cmd, sig syscall.Signal) {
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, sig)
	}
}
