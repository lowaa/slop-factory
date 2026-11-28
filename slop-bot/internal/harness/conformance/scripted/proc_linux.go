//go:build linux

package scripted

import (
	"net"

	"golang.org/x/sys/unix"
)

// endWaitMillis bounds the wait for a killed harness to be gone: a safety
// net, never a synchronisation, since SIGKILL ends it at once.
const endWaitMillis = 10_000

// track keeps a pidfd for a launched harness, so the puppet can end it
// (endLaunched). Only a pid the socket's peer credentials vouch for is
// kept: the launcher reports its own pid, so a report of any other (a unit
// test's made-up one) is never a process to kill. The pidfd is opened
// while the harness runs, so it can't later reach a process that reused
// the pid.
func (p *Puppet) track(conn net.Conn, pid int) {
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return
	}
	var cred *unix.Ucred
	if err := raw.Control(func(fd uintptr) {
		cred, err = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED) // #nosec G115 -- a socket's file descriptor
	}); err != nil || cred == nil || int(cred.Pid) != pid {
		return
	}
	pidfd, err := unix.PidfdOpen(pid, 0)
	if err != nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.procs = append(p.procs, pidfd)
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
		// A pidfd polls readable once its process has exited.
		_, _ = unix.Poll([]unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}, endWaitMillis) // #nosec G115 -- a file descriptor fits an int32
		_ = unix.Close(fd)
	}
}
