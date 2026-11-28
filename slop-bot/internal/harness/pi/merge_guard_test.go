package pi

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/harness"
)

func TestTheMergeGuardLoadsAsAnExtension(t *testing.T) {
	t.Parallel()
	brief := filepath.Join(t.TempDir(), "briefing.md")
	h := &Harness{PreToolHook: []string{"/bin/slop-bot", "hook", "pre-tool"}, Environ: func() []string { return nil }}
	ws := &harness.Workspace{BriefingFile: brief}
	got, err := h.common(ws)
	require.NoError(t, err)
	ext := filepath.Join(filepath.Dir(brief), "merge-guard.ts")
	assert.Equal(t, []string{"--model", DefaultModel, "--extension", ext}, got)
	b, err := os.ReadFile(ext)
	require.NoError(t, err)
	assert.Equal(t, mergeGuardExtension, b)
	assert.Contains(t, h.env(ws), `SLOP_BOT_PRE_TOOL_HOOK=["/bin/slop-bot","hook","pre-tool"]`)

	_, err = h.common(&harness.Workspace{})
	assert.ErrorContains(t, err, "briefing file", "the extension needs the agent's own directory, never the worktree")
}

func TestNoHookNoExtension(t *testing.T) {
	t.Parallel()
	h := &Harness{Environ: func() []string { return nil }}
	got, err := h.common(&harness.Workspace{})
	require.NoError(t, err)
	assert.NotContains(t, got, "--extension")
	for _, kv := range h.env(&harness.Workspace{}) {
		assert.NotContains(t, kv, hookEnv)
	}
}
