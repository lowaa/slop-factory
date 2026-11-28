package sessionhost

import (
	"os"
	"os/exec"
	"strings"
)

// SystemdScope is the TmuxOptions.Scope that starts socket's server in a
// transient systemd scope of its own, when slop-bot runs under a user's
// systemd manager (its service, or an app's cgroup such as a terminal's);
// nil otherwise.
//
// Stopping or restarting a service ends every process in its cgroup, and a
// tmux server started from the daemon would be one of them (it forks and
// calls setsid, which leaves the session but not the cgroup): the agents'
// sessions would die with every daemon restart. A scope of its own keeps
// the server, and the sessions, until the last one ends; KillMode=mixed
// still ends the daemon's other children with it.
func SystemdScope(socket string) []string {
	cgroup, _ := os.ReadFile("/proc/self/cgroup")
	run, _ := exec.LookPath("systemd-run")
	return systemdScope(string(cgroup), run, socket)
}

// systemdScope is SystemdScope, given /proc/self/cgroup and systemd-run's
// path ("" when it isn't installed).
func systemdScope(cgroup, systemdRun, socket string) []string {
	if systemdRun == "" || !underUserManager(cgroup) {
		return nil
	}
	return []string{
		systemdRun, "--user", "--scope", "--quiet", "--collect",
		"--unit=slop-bot-tmux-" + socket,
		"--description=slop-bot's agent sessions (tmux -L " + socket + ")",
		"--",
	}
}

// underUserManager says whether the cgroup v2 path is under a user's
// systemd manager (user@<uid>.service), the one systemd-run --user reaches.
func underUserManager(cgroup string) bool {
	for _, line := range strings.Split(cgroup, "\n") {
		if path, ok := strings.CutPrefix(line, "0::"); ok {
			return strings.Contains(path, "/user@") && strings.Contains(path, ".service/")
		}
	}
	return false
}

// scopeEnv is what systemd-run needs to reach the user's manager: the
// runtime dir (its private socket) and the session bus, where set.
func scopeEnv() []string {
	var env []string
	for _, k := range []string{"XDG_RUNTIME_DIR", "DBUS_SESSION_BUS_ADDRESS", "PATH"} {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	return env
}
