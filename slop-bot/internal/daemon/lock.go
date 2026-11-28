package daemon

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// ErrAlreadyRunning means another daemon holds the lock.
var ErrAlreadyRunning = errors.New("daemon: another slop-bot daemon is already running")

// instanceLock is an exclusive flock on the lock file. The kernel drops it
// when the process dies, so a crashed daemon never leaves a stale lock.
type instanceLock struct {
	path string
	f    *os.File
}

func acquireLock(path string) (*instanceLock, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600) // #nosec G304 -- the lock path comes from paths.Resolve, not from input
	if err != nil {
		return nil, fmt.Errorf("daemon: opening %s: %w", path, err)
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, fmt.Errorf("%w (lock %s)", ErrAlreadyRunning, path)
		}
		return nil, fmt.Errorf("daemon: locking %s: %w", path, err)
	}
	if err := f.Truncate(0); err == nil {
		_, _ = fmt.Fprintf(f, "%d\n", os.Getpid())
	}
	return &instanceLock{path: path, f: f}, nil
}

func (l *instanceLock) release() error {
	// Closing drops the flock. The file stays: removing it would race a
	// second daemon that already opened it.
	return l.f.Close()
}
