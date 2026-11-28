package cli

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/control"
)

// serveUnix answers every request with handler on a fresh socket.
func serveUnix(t *testing.T, handler http.HandlerFunc) string {
	t.Helper()
	// Socket paths have a length limit that t.TempDir can exceed.
	dir, err := os.MkdirTemp("", "sbc")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "s.sock")
	l, err := net.Listen("unix", sock)
	require.NoError(t, err)
	srv := &http.Server{Handler: handler, ReadHeaderTimeout: time.Minute}
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() { _ = srv.Close() })
	return sock
}

// A daemon that fails the reconnect (a busy database, say) is retried, and
// again, until it answers; an answer refusing the attach ends the retries
// and is what the caller gets.
func TestReconnectKeepsRetryingThroughRepeatedFailuresUntilRefused(t *testing.T) {
	t.Parallel()
	var attempts atomic.Int32
	sock := serveUnix(t, func(w http.ResponseWriter, _ *http.Request) {
		status := http.StatusServiceUnavailable
		if attempts.Add(1) == 3 {
			status = http.StatusNotFound
		}
		control.WriteError(w, status, assert.AnError)
	})
	at := &attachRun{client: control.NewClient(sock, ""), agent: "a1", holder: "h", retryBase: time.Millisecond}

	body, err := at.reconnect(context.Background())

	assert.Nil(t, body)
	var apiErr *control.APIError
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, http.StatusNotFound, apiErr.Status)
	assert.EqualValues(t, 3, attempts.Load(), "two failures were retried, the refusal was not")
}
