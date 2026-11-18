package workspace_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/contracts"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/workspace"
)

func TestHeadlessDeniedTools_MatchTheManifest(t *testing.T) {
	tools := contracts.LoadTools(t)
	var want []string
	for _, name := range tools.Roles.DeniedToHeadless {
		want = append(want, tools.Prefix+name)
	}
	want = append(want, tools.DeniedBuiltinTools...)

	assert.ElementsMatch(t, want, workspace.HeadlessDeniedTools)
}

func TestWorkspacePaths_MatchTheManifest(t *testing.T) {
	got := map[string]string{
		"env_file":                workspace.EnvFile,
		"gitignore":               workspace.GitignoreAll,
		"stop_hook":               workspace.HookScript,
		"session_start_hook":      workspace.SessionStartHookScript,
		"usage_hook":              workspace.UsageHookScript,
		"user_prompt_hook":        workspace.UserPromptHookScript,
		"usage_reporter":          workspace.UsageReporter,
		"session_describer":       workspace.SessionDescriber,
		"pi_extension":            workspace.PiExtension,
		"headless_briefing":       workspace.HeadlessBriefing,
		"token_module":            workspace.TokenModule,
		"token_script":            workspace.TokenScript,
		"updater":                 workspace.Updater,
		"repo_syncer":             workspace.RepoSyncer,
		"app_installer":           workspace.AppInstaller,
		"remote_control_launcher": workspace.RemoteControlLauncher,
		"launcher_env":            workspace.LauncherEnv,
		"server_url":              workspace.ServerURL,
		"bundle_hash":             workspace.BundleHashFile,
	}

	assert.Equal(t, contracts.LoadPaths(t).WorkspacePaths, got)
}

func TestTimeouts_MatchTheManifest(t *testing.T) {
	timeouts := contracts.LoadTimeouts(t)

	assert.EqualValues(t, timeouts.ToolCallMS.Value, workspace.ToolCallTimeout.Milliseconds())
	assert.EqualValues(t, timeouts.TokenAndStopScriptS.Value, workspace.StopHookTimeout)
}

// An env var read or set by the scripts the server hands out has to be in the
// registry, with who sets and reads it; otherwise the table in the design doc
// is a guess.
func TestEnvVars_AreInTheRegistry(t *testing.T) {
	registry := contracts.LoadEnv(t).Env
	name := regexp.MustCompile(`(?:^|[^A-Za-z0-9_])(SLOP_HAUS_[A-Z][A-Z_]*[A-Z])\b`)

	var files []string
	for _, glob := range []string{"*.go", "assets/*/*.*"} {
		matches, err := filepath.Glob(glob)
		require.NoError(t, err)
		files = append(files, matches...)
	}
	require.NotEmpty(t, files)
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") || strings.HasSuffix(file, ".bats") || strings.HasSuffix(file, ".test.ts") {
			continue
		}
		source, err := os.ReadFile(file)
		require.NoError(t, err)
		for _, m := range name.FindAllStringSubmatch(string(source), -1) {
			assert.Contains(t, registry, m[1], "%s uses an env var contracts/env.json does not list", file)
		}
	}
	for variable, entry := range registry {
		assert.NotEmpty(t, entry.Setter, variable)
		assert.NotEmpty(t, entry.Readers, variable)
		assert.NotEmpty(t, entry.Meaning, variable)
	}
}
