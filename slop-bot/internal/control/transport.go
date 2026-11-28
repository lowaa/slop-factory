// Package control is the channel between the slop-bot CLI and its daemon:
// HTTP over a unix socket that only the user's own uid may connect to, and
// the auth that tells the user apart from the agents.
package control

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"time"
)

// Listener accepts connections on the control socket from the daemon's own
// uid only. The socket's 0700 directory is the first barrier; the peer check
// is the second, for a directory someone loosened.
type Listener struct {
	net.Listener
	path   string
	uid    int
	logger *slog.Logger
	peer   func(*net.UnixConn) (int, error)
}

// Listen creates the socket at path, removing a stale one left by a daemon
// that died. It refuses if a live daemon already answers there.
func Listen(path string, logger *slog.Logger) (*Listener, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("control: creating %s: %w", dir, err)
	}
	// MkdirAll leaves an existing directory's mode alone, and a loosened one
	// would let other users reach the socket.
	if err := os.Chmod(dir, 0o700); err != nil { // #nosec G302 -- a directory needs its execute bit; 0700 is owner-only
		return nil, fmt.Errorf("control: securing %s: %w", dir, err)
	}
	if err := removeStale(path); err != nil {
		return nil, err
	}
	l, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("control: listening on %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = l.Close()
		return nil, fmt.Errorf("control: securing %s: %w", path, err)
	}
	return &Listener{Listener: l, path: path, uid: os.Getuid(), logger: logger, peer: peerUID}, nil
}

func removeStale(path string) error {
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	c, err := net.DialTimeout("unix", path, time.Second)
	if err == nil {
		_ = c.Close()
		return fmt.Errorf("control: a daemon is already listening on %s", path)
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("control: removing stale socket %s: %w", path, err)
	}
	return nil
}

// Accept returns the next connection from the daemon's uid, dropping any
// other.
func (l *Listener) Accept() (net.Conn, error) {
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		uc, ok := c.(*net.UnixConn)
		if !ok {
			_ = c.Close()
			continue
		}
		uid, err := l.peer(uc)
		if err != nil || uid != l.uid {
			l.logger.Warn("control: refused a connection", "peer_uid", uid, "err", err)
			_ = c.Close()
			continue
		}
		return c, nil
	}
}

// Close stops listening and removes the socket file.
func (l *Listener) Close() error {
	err := l.Listener.Close()
	if rmErr := os.Remove(l.path); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
		err = errors.Join(err, rmErr)
	}
	return err
}

// Dialer connects to the daemon's socket.
func Dialer(path string) func(ctx context.Context, _, _ string) (net.Conn, error) {
	return func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		c, err := d.DialContext(ctx, "unix", path)
		if err != nil {
			return nil, &NotRunningError{Socket: path, Err: err}
		}
		return c, nil
	}
}

// NotRunningError names the socket, so "is the daemon running?" has an
// answer to check, rather than a bare "connection refused".
type NotRunningError struct {
	Socket string
	Err    error
}

func (e *NotRunningError) Error() string {
	return fmt.Sprintf("can't reach the slop-bot daemon at %s (is it running? `slop-bot daemon`): %v", e.Socket, e.Err)
}

func (e *NotRunningError) Unwrap() error { return e.Err }
