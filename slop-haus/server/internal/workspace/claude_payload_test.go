package workspace_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// claudePayload is what a real Claude Code sent a hook, as the hook's stdin:
// the fixtures in the hookserver package's testdata/claude, which
// harness_e2e/test.sh --capture-hook-payloads writes. Tests of the hook
// scripts start from one, so a Claude Code release that changes a payload is
// seen here; payloads a real session does not produce (an unreachable
// transcript, a subagent's Stop) stay hand-written where they are used.
func claudePayload(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "hookserver", "testdata", "claude", name+".json"))
	require.NoError(t, err)
	return strings.TrimSpace(string(raw))
}
