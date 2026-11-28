// Package testexec writes executables for tests to run.
package testexec

import (
	"os"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
)

// WriteScript writes an executable script to path.
//
// Executing a file just written can fail with ETXTBSY ("text file busy")
// when a parallel test forks while the file is still open for writing: the
// child holds a copy of the write descriptor until it execs. Holding
// syscall.ForkLock's read side across create, write and close keeps any fork
// out of that window, which is what the lock exists for (golang/go#22315).
func WriteScript(t testing.TB, path, body string) {
	t.Helper()
	syscall.ForkLock.RLock()
	defer syscall.ForkLock.RUnlock()
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o700) // #nosec G302 G304 -- a test's own executable, at a path it chose
	require.NoError(t, err)
	_, werr := f.WriteString(body)
	cerr := f.Close()
	require.NoError(t, werr)
	require.NoError(t, cerr)
}
