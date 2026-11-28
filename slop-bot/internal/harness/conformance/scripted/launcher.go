package scripted

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// The launcher is the test binary itself, run again with these set: it
// reports its pid to the puppet, then execs the harness in its place, so the
// pid it reported is the harness's. The adapters launch it as they would the
// harness's own binary, which keeps them unchanged under test.
const (
	envLaunchTarget = "SLOP_BOT_TIER2_LAUNCH_TARGET"
	envLaunchSocket = "SLOP_BOT_TIER2_LAUNCH_SOCKET"
)

// Launcher is what to point a harness adapter at in place of the harness
// binary target: the binary to run, and the variables its workspace's Env
// must carry. The test's TestMain must call ExecIfLauncher.
func (p *Puppet) Launcher(target string) (string, map[string]string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", nil, err
	}
	return self, map[string]string{envLaunchTarget: target, envLaunchSocket: p.listener.Addr().String()}, nil
}

// ExecIfLauncher becomes the harness when this process was started as a
// Launcher, and returns otherwise. Call it first in TestMain.
func ExecIfLauncher() {
	target := os.Getenv(envLaunchTarget)
	if target == "" {
		return
	}
	if err := launch(target, os.Getenv(envLaunchSocket)); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "scripted launcher: %v\n", err)
		os.Exit(70)
	}
}

func launch(target, socket string) error {
	if err := reportPid(socket, os.Getpid()); err != nil {
		return err
	}
	// argv[0] is the target's path: pi's launcher script finds its install
	// from $0.
	args := append([]string{target}, os.Args[1:]...)
	return syscall.Exec(target, args, launchEnv(os.Environ())) // #nosec G204 G702 -- the harness binary the test itself chose
}

// reportPid tells the puppet's socket the pid the harness is about to have.
func reportPid(socket string, pid int) error {
	conn, err := net.Dial("unix", socket) // #nosec G704 -- the puppet's own socket, which the test passed in
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(conn, "%d\n", pid)
	if cerr := conn.Close(); err == nil {
		err = cerr
	}
	return err
}

// launchEnv is environ without the launcher's own variables, which the
// harness has no use for and its children must not mistake for a launch.
func launchEnv(environ []string) []string {
	env := make([]string, 0, len(environ))
	for _, kv := range environ {
		if !strings.HasPrefix(kv, envLaunchTarget+"=") && !strings.HasPrefix(kv, envLaunchSocket+"=") {
			env = append(env, kv)
		}
	}
	return env
}

// acceptLaunches passes each launcher's pid to Started, until the listener
// closes.
func (p *Puppet) acceptLaunches() {
	for {
		conn, err := p.listener.Accept()
		if err != nil {
			return
		}
		line, err := bufio.NewReader(conn).ReadString('\n')
		pid, perr := strconv.Atoi(strings.TrimSpace(line))
		if err != nil || perr != nil {
			_ = conn.Close()
			continue
		}
		p.track(conn, pid)
		_ = conn.Close()
		p.pids <- pid
	}
}
