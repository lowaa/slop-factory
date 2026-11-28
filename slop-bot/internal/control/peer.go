//go:build linux || darwin

package control

import "net"

// peerUID is the uid of the process at the other end of c, from the kernel,
// so it can't be faked by the peer.
func peerUID(c *net.UnixConn) (int, error) {
	raw, err := c.SyscallConn()
	if err != nil {
		return -1, err
	}
	uid, sockErr := -1, error(nil)
	if err := raw.Control(func(fd uintptr) { uid, sockErr = socketPeerUID(int(fd)) }); err != nil {
		return -1, err
	}
	return uid, sockErr
}
