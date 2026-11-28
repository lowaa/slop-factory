package cli

import (
	"context"
	"path/filepath"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/control"
)

// A snapshot that fails because the TUI is closing (its context is done) is
// not an error to report; one that fails while it is running is.
func TestStatusFeedSnapshotFailureIsAnErrorOnlyWhileRunning(t *testing.T) {
	t.Parallel()
	// Nothing listens on this socket, so every request fails.
	feed := &statusFeed{client: control.NewClient(filepath.Join(t.TempDir(), "none.sock"), "")}
	send := func(m tea.Msg) { require.FailNow(t, "nothing should be sent", "%v", m) }

	require.Error(t, feed.snapshot(context.Background(), send))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	assert.NoError(t, feed.snapshot(ctx, send))
}
