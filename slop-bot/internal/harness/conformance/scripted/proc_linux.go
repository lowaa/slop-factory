//go:build linux

package scripted

import (
	"errors"
	"net"
	"time"

	"golang.org/x/sys/unix"
)

// endWait bounds the wait for a killed harness to be gone: a safety net,
// never a synchronisation, since SIGKILL ends it at once.
const endWait = 10 * time.Second

// track keeps a pidfd for a launched harness, so the puppet can end it
// (endLaunched). Only a pid the socket's peer credentials vouch for is
// kept: the launcher reports its own pid, so a report of any other (a unit
// test's made-up one) is never a process to kill. The pidfd is opened
// while the harness runs, so it can't later reach a process that reused
// the pid.
func (p *Puppet) track(conn net.Conn, pid int) {
	peer, err := peerPid(conn)
	if err != nil || peer != pid {
		p.t.Logf("scripted: not tracking reported pid %d (peer %d, %v): it won't be ended at cleanup", pid, peer, err)
		return
	}
	pidfd, err := unix.PidfdOpen(pid, 0)
	if err != nil {
		p.t.Logf("scripted: not tracking pid %d: %v", pid, err)
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.procs = append(p.procs, pidfd)
}

func peerPid(conn net.Conn) (int, error) {
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return 0, errors.New("not a unix socket")
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return 0, err
	}
	var cred *unix.Ucred
	var credErr error
	if err := raw.Control(func(fd uintptr) {
		cred, credErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED) // #nosec G115 -- a socket's file descriptor
	}); err != nil {
		return 0, err
	}
	if credErr != nil {
		return 0, credErr
	}
	return int(cred.Pid), nil
}

// endLaunched kills every harness this puppet launched that still runs, and
// waits for each to be gone. A TUI harness isn't the test's child: tmux
// ends its session with a SIGHUP, which agy answers by writing its state
// for a moment longer, into the temp dirs the test is about to remove.
func (p *Puppet) endLaunched() {
	p.mu.Lock()
	procs := p.procs
	p.procs = nil
	p.mu.Unlock()
	for _, fd := range procs {
		// ESRCH: it has already exited.
		_ = unix.PidfdSendSignal(fd, unix.SIGKILL, nil, 0)
		if err := awaitExit(fd, endWait); err != nil {
			p.t.Logf("scripted: a launched harness may outlive the test: %v", err)
		}
		_ = unix.Close(fd)
	}
}

// awaitExit waits for pidfd's process to exit: a pidfd polls readable then.
// poll is never restarted after a signal handler, and the Go runtime's
// signals (preemption, SIGCHLD) arrive often, so EINTR waits again for
// what is left of the bound.
func awaitExit(pidfd int, bound time.Duration) error {
	deadline := time.Now().Add(bound)
	for {
		left := time.Until(deadline)
		if left <= 0 {
			return errors.New("still running after SIGKILL")
		}
		fds := []unix.PollFd{{Fd: int32(pidfd), Events: unix.POLLIN}} // #nosec G115 -- a file descriptor fits an int32
		n, err := unix.Poll(fds, int(left.Milliseconds())+1)
		switch {
		case errors.Is(err, unix.EINTR):
			continue
		case err != nil:
			return err
		case n > 0:
			return nil
		}
	}
}
