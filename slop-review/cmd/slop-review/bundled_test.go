package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-review/internal/providers"
	"github.com/lowaa/slop-factory/slop-review/personas"
	"github.com/lowaa/slop-factory/slop-review/report"
)

// setUpBundled makes a run look like the shipped binary: no persona dir, no
// repo root, no tool version from the environment, a build version stamped
// in, and the cwd inside the repo under review.
func setUpBundled(t *testing.T, version string) *reportServer {
	t.Helper()
	rs, _ := setUpCentral(t)
	repo := os.Getenv("SLOP_REVIEW_REPO_ROOT")
	require.NotEmpty(t, repo)
	for _, k := range []string{"SLOP_REVIEW_PERSONA_DIR", "SLOP_REVIEW_REPO_ROOT", "SLOP_REVIEW_TOOL_VERSION"} {
		t.Setenv(k, "")
	}
	t.Chdir(repo)
	old := buildVersion
	buildVersion = version
	t.Cleanup(func() { buildVersion = old })
	return rs
}

func TestRun_BundledBuildRunsFromTheCwdWithEmbeddedPersonas(t *testing.T) {
	rs := setUpBundled(t, "v-bundled")
	intent := filepath.Join(t.TempDir(), "intent.md")
	require.NoError(t, os.WriteFile(intent, []byte("bundled intent"), 0o600))

	got, _ := runJSON(t, []string{"--intent=" + intent}, &providers.Factory{})
	assert.Equal(t, "standard", got.Meta.Persona)

	run := rs.all()[0].run
	require.NoError(t, run.Validate())
	assert.Equal(t, "v-bundled", run.ToolVersion.Value)
	assert.True(t, report.IsHash(run.Fingerprint), "a bundled build fingerprints from its version")
	assert.True(t, report.IsHash(run.SourceHash))
	embedded, err := personas.FS.ReadFile("standard.md")
	require.NoError(t, err)
	assert.Contains(t, run.PersonaTexts, report.HashText(string(embedded)))
}

func TestRun_ToolVersionFlagWinsOverTheBuildVersion(t *testing.T) {
	rs := setUpBundled(t, "v-bundled")

	runJSON(t, []string{"--tool-version=from-flag"}, &providers.Factory{})
	assert.Equal(t, "from-flag", rs.all()[0].run.ToolVersion.Value)
}

func TestRun_SourceDirWinsOverTheBundledFingerprint(t *testing.T) {
	rs := setUpBundled(t, "v-bundled")
	src := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(src, "main.go"), []byte("package main\n"), 0o600))

	runJSON(t, []string{"--source-dir=" + src}, &providers.Factory{})
	bundled := report.HashText("bundled:v-bundled")
	assert.NotEqual(t, bundled, rs.all()[0].run.SourceHash)
}
