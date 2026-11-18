package workspace_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/workspace"
)

// These run the real setup and update scripts with a manifest. The assets/sh
// sync-repos.bats suite covers the cloning rules; this is what shows the
// scripts hand it the manifest, and carry on when it objects.

type repoFixture struct {
	t       *testing.T
	remotes string
	env     []string
}

// newRepoFixture makes bare repos for acme/app and acme/deploy and an
// environment in which example.test/<owner>/<repo> is cloned from them, so
// nothing reaches a network.
// gitEnv isolates git from the machine's own config: a global excludes file
// that already ignores .claude/, say, would have the script rightly add
// nothing and these tests wrongly fail.
func gitEnv(t *testing.T, extra ...string) []string {
	t.Helper()
	home := t.TempDir()
	return append(append(os.Environ(),
		"HOME="+home,
		"XDG_CONFIG_HOME="+home,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
	), extra...)
}

func gitInit(t *testing.T, dir string) {
	t.Helper()
	out, err := exec.Command("git", "init", "-q", dir).CombinedOutput()
	require.NoError(t, err, "git init: %s", out)
}

func newRepoFixture(t *testing.T, extra ...string) *repoFixture {
	t.Helper()
	remotes := t.TempDir()
	cfg := filepath.Join(t.TempDir(), "gitconfig")
	require.NoError(t, os.WriteFile(cfg, []byte("[url \"file://"+remotes+"/\"]\n\tinsteadOf = https://example.test/\n[init]\n\tdefaultBranch = main\n"), 0o644))
	env := gitEnv(t, append([]string{
		"GIT_CONFIG_GLOBAL=" + cfg,
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.test",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.test",
	}, extra...)...)
	f := &repoFixture{t: t, remotes: remotes, env: env}
	for _, name := range []string{"app", "deploy"} {
		f.makeRemote(name)
	}
	return f
}

func (f *repoFixture) git(dir string, args ...string) {
	f.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = f.env
	out, err := cmd.CombinedOutput()
	require.NoError(f.t, err, "git %v: %s", args, out)
}

func (f *repoFixture) makeRemote(name string) {
	f.t.Helper()
	seed := f.t.TempDir()
	f.git(seed, "init", "-q")
	require.NoError(f.t, os.WriteFile(filepath.Join(seed, "README"), []byte("from "+name), 0o644))
	f.git(seed, "add", "README")
	f.git(seed, "commit", "-q", "-m", "seed")
	require.NoError(f.t, os.MkdirAll(filepath.Join(f.remotes, "acme"), 0o755))
	f.git(seed, "clone", "-q", "--bare", ".", filepath.Join(f.remotes, "acme", name+".git"))
}

func (f *repoFixture) run(dir string, script string) (string, error) {
	f.t.Helper()
	path := filepath.Join(f.t.TempDir(), "script.sh")
	require.NoError(f.t, os.WriteFile(path, []byte(script), 0o755))
	cmd := exec.Command("bash", path)
	cmd.Dir = dir
	cmd.Env = f.env
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (f *repoFixture) setup(dir string, p workspace.Params) string {
	f.t.Helper()
	p.AgentName, p.Server = "builder-1", "http://127.0.0.1:1"
	script, err := workspace.Script(p)
	require.NoError(f.t, err)
	out, err := f.run(dir, script)
	require.NoError(f.t, err, "setup failed:\n%s", out)
	return out
}

func (f *repoFixture) update(dir string, p workspace.Params) string {
	f.t.Helper()
	p.AgentName, p.Server = "builder-1", "http://127.0.0.1:1"
	script, err := workspace.UpdateScript(p)
	require.NoError(f.t, err)
	out, err := f.run(dir, script)
	require.NoError(f.t, err, "update failed:\n%s", out)
	return out
}

var bothRepos = []workspace.Repo{
	{Path: "app", Remote: "example.test/acme/app"},
	{Path: "deploy", Remote: "example.test/acme/deploy"},
}

func TestScript_SetupClonesTheManifestIntoARootThatIsNotARepo(t *testing.T) {
	f := newRepoFixture(t)
	dir := t.TempDir()

	f.setup(dir, workspace.Params{Repos: bothRepos})

	assert.Equal(t, "from app", fileIn(t, dir, "app/README"))
	assert.Equal(t, "from deploy", fileIn(t, dir, "deploy/README"))
	_, err := os.Stat(filepath.Join(dir, ".git"))
	assert.True(t, os.IsNotExist(err), "setup must not turn the root into a repo")
	assert.FileExists(t, filepath.Join(dir, workspace.RepoSyncer))
}

func TestScript_UpdateClonesAMissingRepoAndLeavesAMismatchedOneAlone(t *testing.T) {
	f := newRepoFixture(t)
	dir := t.TempDir()
	f.setup(dir, workspace.Params{})

	// A directory at "app" that is some other repo entirely.
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "app"), 0o755))
	f.git(filepath.Join(dir, "app"), "init", "-q")
	f.git(filepath.Join(dir, "app"), "remote", "add", "origin", "https://example.test/someone/else.git")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "app", "mine"), []byte("work"), 0o644))
	before := snapshot(t, filepath.Join(dir, "app"))

	out := f.update(dir, workspace.Params{Repos: bothRepos})

	assert.Contains(t, out, "app already exists -- left alone")
	assert.Equal(t, before, snapshot(t, filepath.Join(dir, "app")))
	assert.Equal(t, "from deploy", fileIn(t, dir, "deploy/README"))
}

func TestScript_OptOutAtSetupIsRespectedByEveryLaterUpdate(t *testing.T) {
	f := newRepoFixture(t, "SLOP_HAUS_CLONE=no")
	dir := t.TempDir()

	f.setup(dir, workspace.Params{Repos: bothRepos})
	assert.FileExists(t, filepath.Join(dir, ".slop-haus", "no-clone"))

	// The variable is gone from the next run: the recorded choice is what holds.
	f.env = append(f.env, "SLOP_HAUS_CLONE=")
	f.update(dir, workspace.Params{Repos: bothRepos})

	for _, name := range []string{"app", "deploy"} {
		_, err := os.Stat(filepath.Join(dir, name))
		assert.True(t, os.IsNotExist(err), "%s must not be cloned after opting out", name)
	}
}

func TestScript_AHostileManifestNeitherRunsNorStopsTheWorkspace(t *testing.T) {
	f := newRepoFixture(t)
	dir := t.TempDir()
	canary := filepath.Join(t.TempDir(), "canary")

	out := f.setup(dir, workspace.Params{Repos: []workspace.Repo{
		{Path: "$(touch " + canary + ")", Remote: "example.test/acme/app"},
		{Path: "../escape", Remote: "example.test/acme/app"},
		{Path: "app", Remote: "-oProxyCommand=touch " + canary},
		{Path: "app'; touch " + canary + "; '", Remote: "example.test/acme/app\ntouch " + canary},
		{Path: "deploy", Remote: "example.test/acme/deploy"},
	}})

	assert.NoFileExists(t, canary)
	assert.NoDirExists(t, filepath.Join(dir, "app"))
	assert.NoDirExists(t, filepath.Join(filepath.Dir(dir), "escape"))
	// The valid entry after them still cloned, and setup finished the rest.
	assert.Equal(t, "from deploy", fileIn(t, dir, "deploy/README"))
	assert.FileExists(t, filepath.Join(dir, workspace.HeadlessBriefing))
	assert.Contains(t, out, "not every manifest repo could be set up")
}
