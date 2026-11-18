package claudecli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func touch(t *testing.T, path string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte("x"), 0o600))
}

// The checkout's secret files are denied even to a review scoped to a
// subdirectory of it, and so are a slop-haus tenant's secrets above it and
// the credential stores under home; ordinary files are not.
func TestReadDenied(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	tenant := filepath.Join(root, "tenant")
	top := filepath.Join(tenant, "slots", "pilot-1", "repo")
	require.NoError(t, os.MkdirAll(filepath.Join(top, ".git"), 0o755))
	for _, p := range []string{
		filepath.Join(top, ".env"),
		filepath.Join(top, "svc", "deploy", "key.pem"),
		filepath.Join(top, "svc", "main.go"),
		filepath.Join(top, "node_modules", "pkg", ".env"),
		filepath.Join(tenant, "secrets.env"),
		filepath.Join(tenant, "run.env"),
	} {
		touch(t, p)
	}
	vault := filepath.Join(root, "vault", "widget.env")
	touch(t, vault)
	require.NoError(t, os.Symlink(vault, filepath.Join(top, "svc", ".env")))
	home := filepath.Join(root, "home")
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".ssh"), 0o700))
	touch(t, filepath.Join(home, ".netrc"))
	cfg := filepath.Join(root, "claude-config")
	touch(t, filepath.Join(cfg, ".credentials.json"))

	got := readDenied(filepath.Join(top, "svc"), home, cfg, nil)
	assert.ElementsMatch(t, []string{
		filepath.Join(top, ".env"),
		filepath.Join(top, "svc", "deploy", "key.pem"),
		filepath.Join(top, "svc", ".env"),
		vault,
		filepath.Join(tenant, "secrets.env"),
		filepath.Join(home, ".ssh"),
		filepath.Join(home, ".netrc"),
		filepath.Join(cfg, ".credentials.json"),
	}, got)

	rules := readDenyRules(got)
	assert.Contains(t, rules, "Read(/"+filepath.Join(home, ".ssh")+"/**)", "a directory is denied with everything in it")
	assert.Contains(t, rules, "Read(/"+filepath.Join(top, ".env")+")")
}

// What is denied reaches both the sandbox (the shell) and the permission
// rules (the file tools).
func TestExec_DeniesTheCheckoutsSecretsToShellAndTools(t *testing.T) {
	f := newFakeClaude(t, "ok")
	repo, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, os.Mkdir(filepath.Join(repo, ".git"), 0o755))
	touch(t, filepath.Join(repo, ".env"))

	_, err = newRunner(f).Exec(context.Background(), execRequest(repo))
	require.NoError(t, err)

	var settings struct {
		Permissions struct {
			Deny []string `json:"deny"`
		} `json:"permissions"`
		Sandbox struct {
			Filesystem struct {
				DenyRead []string `json:"denyRead"`
			} `json:"filesystem"`
		} `json:"sandbox"`
	}
	require.NoError(t, json.Unmarshal([]byte(valueAfter(t, f.argv(t), "--settings")), &settings))
	assert.Contains(t, settings.Sandbox.Filesystem.DenyRead, filepath.Join(repo, ".env"))
	assert.Contains(t, settings.Permissions.Deny, "Read(/"+filepath.Join(repo, ".env")+")")
}

// claude's own login can't be kept from its shell, so a review that quotes
// it has it taken out before the review goes anywhere.
func TestExec_RedactsClaudesLoginFromTheReview(t *testing.T) {
	f := newFakeClaude(t, "leaklogin")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "sk-ant-oat01-abcdefghijklmnop")
	res, err := newRunner(f).Exec(context.Background(), execRequest(t.TempDir()))
	require.NoError(t, err)
	assert.NotContains(t, string(res.JSON), "sk-ant-oat01-abcdefghijklmnop")
	assert.Contains(t, string(res.JSON), "[redacted]")
}

func envOf(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func checkout(t *testing.T) (root, repo string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	repo = filepath.Join(root, "work", "repo")
	require.NoError(t, os.MkdirAll(filepath.Join(repo, ".git"), 0o755))
	return root, repo
}

// Each login variable is denied as a whole directory, to the shell and to
// the file tools; one that is unset, empty or missing is skipped.
func TestLoginDirs_DeniesEachVariable(t *testing.T) {
	for _, name := range loginDirEnv {
		t.Run(name, func(t *testing.T) {
			root, repo := checkout(t)
			dir := filepath.Join(root, "harness", "login")
			touch(t, filepath.Join(dir, "auth.json"))

			dirs, err := loginDirs(repo, envOf(map[string]string{name: dir}))
			require.NoError(t, err)
			assert.Equal(t, []string{dir}, dirs)
			denied := readDenied(repo, "", "", dirs)
			assert.Equal(t, []string{dir}, denied)
			assert.Equal(t, []string{"Read(/" + dir + "/**)"}, readDenyRules(denied))

			dirs, err = loginDirs(repo, envOf(nil))
			require.NoError(t, err)
			assert.Empty(t, dirs, "unset")
			dirs, err = loginDirs(repo, envOf(map[string]string{name: ""}))
			require.NoError(t, err)
			assert.Empty(t, dirs, "empty")

			missing := filepath.Join(root, "nowhere")
			dirs, err = loginDirs(repo, envOf(map[string]string{name: missing}))
			require.NoError(t, err)
			assert.Empty(t, readDenied(repo, "", "", dirs), "a path that does not exist has nothing to bind over")
		})
	}
}

func TestLoginDirs_ResolvesRelativeAndLinkedPaths(t *testing.T) {
	root, repo := checkout(t)
	dir := filepath.Join(root, "harness", "codex")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	t.Chdir(filepath.Join(root, "harness"))
	dirs, err := loginDirs(repo, envOf(map[string]string{"CODEX_HOME": "codex"}))
	require.NoError(t, err)
	assert.Equal(t, []string{dir}, dirs, "relative to the working directory")

	link := filepath.Join(root, "link")
	require.NoError(t, os.Symlink(dir, link))
	dirs, err = loginDirs(repo, envOf(map[string]string{"PI_CODING_AGENT_DIR": link}))
	require.NoError(t, err)
	assert.Equal(t, []string{dir}, dirs, "the real directory is bound")
}

// A login directory that is, or holds, the checkout would hide the review's
// own repository, so the run is refused.
func TestLoginDirs_RefusesWhatContainsTheCheckout(t *testing.T) {
	for _, name := range loginDirEnv {
		t.Run(name, func(t *testing.T) {
			root, repo := checkout(t)
			sub := filepath.Join(repo, "svc")
			require.NoError(t, os.MkdirAll(sub, 0o755))
			viaLink := filepath.Join(root, "alias")
			require.NoError(t, os.Symlink(filepath.Join(root, "work"), viaLink))
			for _, bad := range []string{repo, filepath.Dir(repo), root, "/", viaLink} {
				_, err := loginDirs(sub, envOf(map[string]string{name: bad}))
				require.Error(t, err, bad)
				assert.Contains(t, err.Error(), name)
				assert.Contains(t, err.Error(), "contains the checkout \""+sub+"\"", "names the checkout it would hide")
			}
			inside := filepath.Join(repo, ".codex")
			require.NoError(t, os.MkdirAll(inside, 0o755))
			dirs, err := loginDirs(sub, envOf(map[string]string{name: inside}))
			require.NoError(t, err, "a login dir inside the checkout is fine")
			assert.Equal(t, []string{inside}, dirs)
		})
	}
}

// The login directories reach both the shell sandbox and the file tools'
// rules of the session actually started.
func TestExec_DeniesLoginDirsToShellAndTools(t *testing.T) {
	f := newFakeClaude(t, "ok")
	root, repo := checkout(t)
	codex := filepath.Join(root, "harness", "codex")
	pi := filepath.Join(root, "harness", "pi")
	touch(t, filepath.Join(codex, "auth.json"))
	touch(t, filepath.Join(pi, "auth.json"))
	t.Setenv("CODEX_HOME", codex)
	t.Setenv("PI_CODING_AGENT_DIR", pi)

	_, err := newRunner(f).Exec(context.Background(), execRequest(repo))
	require.NoError(t, err)

	var settings struct {
		Permissions struct {
			Deny []string `json:"deny"`
		} `json:"permissions"`
		Sandbox struct {
			Filesystem struct {
				DenyRead []string `json:"denyRead"`
			} `json:"filesystem"`
		} `json:"sandbox"`
	}
	require.NoError(t, json.Unmarshal([]byte(valueAfter(t, f.argv(t), "--settings")), &settings))
	for _, dir := range []string{codex, pi} {
		assert.Contains(t, settings.Sandbox.Filesystem.DenyRead, dir)
		assert.Contains(t, settings.Permissions.Deny, "Read(/"+dir+"/**)")
	}
}

func TestExec_RefusesLoginDirHoldingTheCheckout(t *testing.T) {
	f := newFakeClaude(t, "ok")
	repo, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	t.Setenv("CODEX_HOME", filepath.Dir(repo))
	_, err = newRunner(f).Exec(context.Background(), execRequest(repo))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "CODEX_HOME")
}

// CLAUDE_CONFIG_DIR stays readable (claude needs its settings); only the
// login file in it is denied.
func TestReadDenied_ClaudeConfigDirOnlyLosesItsCredentials(t *testing.T) {
	root, repo := checkout(t)
	cfg := filepath.Join(root, "cfg")
	touch(t, filepath.Join(cfg, ".credentials.json"))
	touch(t, filepath.Join(cfg, "settings.json"))
	assert.Equal(t, []string{filepath.Join(cfg, ".credentials.json")}, readDenied(repo, "", cfg, nil))
}
