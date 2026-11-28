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
func endGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	}
}
