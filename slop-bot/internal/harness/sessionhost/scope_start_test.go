package sessionhost_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/harness/sessionhost"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/sessionhost/tmuxtest"
)

// The server is started in its scope once: a session started on a running
// server doesn't go through the scope again. The scope here is a shell that
// notes each use and runs what it was given, in place of systemd-run.
func TestOnlyTheFirstStartGoesThroughTheScope(t *testing.T) {
	t.Parallel()
	ctx := ctxFor(t)
	uses := filepath.Join(t.TempDir(), "uses")
	host := tmuxtest.Host(t, &sessionhost.TmuxOptions{
		Scope: []string{"/bin/sh", "-c", `echo used >> "$0"; exec "$@"`, uses},
	})

	shell(ctx, t, host, "s1", "sleep 600")
	shell(ctx, t, host, "s2", "sleep 600")

	got, err := os.ReadFile(uses) // #nosec G304 -- the test's own file
	require.NoError(t, err)
	assert.Equal(t, 1, strings.Count(string(got), "used"))
	for _, s := range []string{"s1", "s2"} {
		st, err := host.Status(ctx, s)
		require.NoError(t, err, s)
		assert.False(t, st.Dead, s)
	}
}
