package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-review/internal/providers"
)

// TestLive_CLIProvidersReviewARealChange runs the whole command against the
// real codex / claude CLI, so it needs that CLI logged in and spends a
// little of its plan: gated by SLOP_REVIEW_E2E_CODEX=1 / _CLAUDE=1.
func TestLive_CLIProvidersReviewARealChange(t *testing.T) {
	for _, p := range []struct{ provider, gate string }{
		{"codex", "SLOP_REVIEW_E2E_CODEX"},
		{"claude", "SLOP_REVIEW_E2E_CLAUDE"},
	} {
		t.Run(p.provider, func(t *testing.T) {
			if os.Getenv(p.gate) != "1" {
				t.Skipf("set %s=1 to run against the real %s CLI", p.gate, p.provider)
			}
			if _, err := exec.LookPath(p.provider); err != nil {
				t.Skipf("%s is not on PATH", p.provider)
			}
			repo := newTestRepo(t)
			personaDir := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(personaDir, "standard.md"), []byte("You are a code reviewer. Keep the review brief."), 0o644))
			for _, k := range []string{"SLOP_REVIEW_BASE_URL", "SLOP_REVIEW_MODEL", "SLOP_REVIEW_REASONING_EFFORT", "SLOP_REVIEW_REPORT_URL", "SLOP_REVIEW_ALLOW_SECRETS"} {
				t.Setenv(k, "")
			}

			var stdout, stderr bytes.Buffer
			require.NoError(t, runWith([]string{
				"--provider=" + p.provider, "--repo-root=" + repo, "--persona-dir=" + personaDir,
			}, &stdout, &stderr, &providers.Factory{}), "stderr: %s", stderr.String())

			var got output
			require.NoError(t, json.Unmarshal(stdout.Bytes(), &got))
			require.Len(t, got.Sessions, 1)
			assert.Equal(t, p.provider, got.Sessions[0].Provider)
			assert.Empty(t, got.Sessions[0].Error)
			assert.NotEmpty(t, got.Review.Summary)
			assert.Positive(t, got.Sessions[0].InputTokens, "the CLI's token total is reported")
		})
	}
}
