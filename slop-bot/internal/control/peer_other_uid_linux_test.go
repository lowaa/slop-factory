//go:build linux

package control

import (
	"net"
	"os"
	"runtime"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const otherUID = 20001

// socketpairAs returns a connected unix socket pair created by a thread
// running as uid. The kernel stamps a socket's peer credentials when it is
// created, so each end reports uid to the other.
func socketpairAs(t *testing.T, uid int) (*net.UnixConn, *net.UnixConn) {
	t.Helper()
	type result struct {
		a, b *net.UnixConn
		err  error
	}
	done := make(chan result, 1)
	go func() {
		// A raw SYS_SETUID changes only this thread, unlike syscall.Setuid,
		// which changes all of them. The goroutine never unlocks, so the
		// thread exits with it instead of going back to the pool as uid.
		runtime.LockOSThread()
		if _, _, errno := syscall.RawSyscall(syscall.SYS_SETUID, uintptr(uid), 0, 0); errno != 0 {
			done <- result{err: errno}
			return
		}
		fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
		if err != nil {
			done <- result{err: err}
			return
		}
		var conns [2]*net.UnixConn
		for i, fd := range fds {
			f := os.NewFile(uintptr(fd), "socketpair")
			c, err := net.FileConn(f)
			_ = f.Close()
			if err != nil {
				done <- result{err: err}
				return
			}
			conns[i] = c.(*net.UnixConn)
		}
		done <- result{a: conns[0], b: conns[1]}
	}()
	r := <-done
	require.NoError(t, r.err)
	t.Cleanup(func() { _ = r.a.Close(); _ = r.b.Close() })
	return r.a, r.b
}

// Needs root to become another user, so it runs in tier1.sh's container
// (as root) and skips everywhere else: the case worth testing is a peer whose
// uid differs from ours, which the same-user test can't show.
func TestPeerUIDOfAnotherUser(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root to act as a second uid; tier1.sh runs it in a container")
	}
	a, b := socketpairAs(t, otherUID)
	for _, c := range []*net.UnixConn{a, b} {
		uid, err := peerUID(c)
		require.NoError(t, err)
		assert.Equal(t, otherUID, uid)
		assert.NotEqual(t, os.Getuid(), uid)
	}
}
