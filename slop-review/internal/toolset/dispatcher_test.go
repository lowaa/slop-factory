package toolset

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestDispatcher(t *testing.T, allowSecrets bool) (*Dispatcher, string) {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "sub"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n\nfunc main() {\n\tprintln(\"hi\")\n}\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "sub", "nested.go"), []byte("package sub\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".env"), []byte("SECRET=shh\n"), 0o644))

	outside := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(outside, "outside.txt"), []byte("should not be reachable"), 0o644))

	return New(&Options{RepoRoot: root, OutputCapBytes: 4096, AllowSecrets: allowSecrets}), root
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return string(b)
}

func TestReadFile(t *testing.T) {
	d, _ := newTestDispatcher(t, false)

	out := d.Dispatch("read_file", mustJSON(t, map[string]any{"path": "main.go"}))
	assert.Contains(t, out, "func main()")
}

func TestReadFile_LineRange(t *testing.T) {
	d, _ := newTestDispatcher(t, false)

	out := d.Dispatch("read_file", mustJSON(t, map[string]any{"path": "main.go", "start_line": 3, "end_line": 3}))
	assert.Equal(t, "func main() {", out)
}

func TestReadFile_RejectsPathEscape(t *testing.T) {
	d, _ := newTestDispatcher(t, false)

	out := d.Dispatch("read_file", mustJSON(t, map[string]any{"path": "../outside.txt"}))
	assert.Contains(t, out, "error:")
	assert.Contains(t, out, "escapes the repository root")
}

func TestReadFile_RejectsAbsolutePath(t *testing.T) {
	d, _ := newTestDispatcher(t, false)

	out := d.Dispatch("read_file", mustJSON(t, map[string]any{"path": "/etc/passwd"}))
	assert.Contains(t, out, "error:")
	assert.Contains(t, out, "must be relative")
}

func TestReadFile_RefusesSecretByDefault(t *testing.T) {
	d, _ := newTestDispatcher(t, false)

	out := d.Dispatch("read_file", mustJSON(t, map[string]any{"path": ".env"}))
	assert.Contains(t, out, "error:")
	assert.Contains(t, out, "secret-shaped")
	assert.NotContains(t, out, "shh")
	assert.Empty(t, d.Warnings())
}

func TestReadFile_AllowSecretsReturnsContentAndWarns(t *testing.T) {
	d, _ := newTestDispatcher(t, true)

	out := d.Dispatch("read_file", mustJSON(t, map[string]any{"path": ".env"}))
	assert.Contains(t, out, "shh")
	require.Len(t, d.Warnings(), 1)
	assert.Contains(t, d.Warnings()[0], ".env")
}

func TestReadFile_StartLineAfterEndLineIsAnErrorNotAPanic(t *testing.T) {
	d, _ := newTestDispatcher(t, false)

	out := d.Dispatch("read_file", mustJSON(t, map[string]any{"path": "main.go", "start_line": 250, "end_line": 70}))
	assert.Contains(t, out, "error:")
	assert.Contains(t, out, "start_line")
}

func TestReadFile_StartLineBeyondEndOfFile(t *testing.T) {
	d, _ := newTestDispatcher(t, false)

	out := d.Dispatch("read_file", mustJSON(t, map[string]any{"path": "main.go", "start_line": 9999}))
	assert.Equal(t, "", out)
}

func TestReadFile_TruncatesLargeOutput(t *testing.T) {
	root := t.TempDir()
	big := make([]byte, 5000)
	for i := range big {
		big[i] = 'a'
	}
	require.NoError(t, os.WriteFile(filepath.Join(root, "big.txt"), big, 0o644))
	d := New(&Options{RepoRoot: root, OutputCapBytes: 100, AllowSecrets: false})

	out := d.Dispatch("read_file", mustJSON(t, map[string]any{"path": "big.txt"}))
	assert.LessOrEqual(t, len(out), 100, "outputCap must be a hard cap (truncation note included), not just a cap on the content before it")
	assert.Contains(t, out, "truncated")
}

func TestListDir(t *testing.T) {
	d, _ := newTestDispatcher(t, false)

	out := d.Dispatch("list_dir", mustJSON(t, map[string]any{"path": "."}))
	assert.Contains(t, out, "main.go")
	assert.Contains(t, out, "sub/")
	assert.NotContains(t, out, ".git")
}

func TestGrep_FindsMatch(t *testing.T) {
	d, _ := newTestDispatcher(t, false)

	out := d.Dispatch("grep", mustJSON(t, map[string]any{"pattern": "func main"}))
	assert.Contains(t, out, "main.go:3:")
}

func TestGrep_NoMatches(t *testing.T) {
	d, _ := newTestDispatcher(t, false)

	out := d.Dispatch("grep", mustJSON(t, map[string]any{"pattern": "nope-not-here"}))
	assert.Equal(t, "no matches", out)
}

func TestGrep_InvalidPattern(t *testing.T) {
	d, _ := newTestDispatcher(t, false)

	out := d.Dispatch("grep", mustJSON(t, map[string]any{"pattern": "("}))
	assert.Contains(t, out, "error:")
	assert.Contains(t, out, "invalid pattern")
}

func TestGrep_SkipsSecretFilesByDefault(t *testing.T) {
	d, _ := newTestDispatcher(t, false)

	out := d.Dispatch("grep", mustJSON(t, map[string]any{"pattern": "shh"}))
	assert.Equal(t, "no matches", out)
	assert.Empty(t, d.Warnings())
}

func TestGrep_SkipsSymlinksEvenOutsideRepo(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n"), 0o644))

	outside := t.TempDir()
	secretPath := filepath.Join(outside, "outside_secret.txt")
	require.NoError(t, os.WriteFile(secretPath, []byte("top-secret-marker"), 0o644))
	require.NoError(t, os.Symlink(secretPath, filepath.Join(root, "innocuous_name.txt")))

	d := New(&Options{RepoRoot: root, OutputCapBytes: 4096, AllowSecrets: false})
	out := d.Dispatch("grep", mustJSON(t, map[string]any{"pattern": "top-secret-marker"}))
	assert.Equal(t, "no matches", out, "grep must not follow a symlink to a file outside the repo root")
}

func TestGrep_PathGlobScoping(t *testing.T) {
	d, _ := newTestDispatcher(t, false)

	out := d.Dispatch("grep", mustJSON(t, map[string]any{"pattern": "package", "path_glob": "sub/*.go"}))
	assert.Contains(t, out, "sub/nested.go")
	assert.NotContains(t, out, "main.go")
}

func TestGitTools_DispatchWiring(t *testing.T) {
	root := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
		)
		out, err := cmd.CombinedOutput()
		require.NoErrorf(t, err, "git %v: %s", args, out)
	}
	run("init", "--initial-branch=main")
	require.NoError(t, os.WriteFile(filepath.Join(root, "a.go"), []byte("package main\n"), 0o644))
	run("add", "a.go")
	run("commit", "-m", "initial commit")

	d := New(&Options{RepoRoot: root, OutputCapBytes: 4096, AllowSecrets: false})

	show := d.Dispatch("git_show", mustJSON(t, map[string]any{"ref": "HEAD"}))
	assert.Contains(t, show, "initial commit")

	log := d.Dispatch("git_log", mustJSON(t, map[string]any{}))
	assert.Contains(t, log, "initial commit")

	blame := d.Dispatch("git_blame", mustJSON(t, map[string]any{"path": "a.go"}))
	assert.Contains(t, blame, "package main")
}

func newGitRepoWithSecret(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
		)
		out, err := cmd.CombinedOutput()
		require.NoErrorf(t, err, "git %v: %s", args, out)
	}
	run("init", "--initial-branch=main")
	require.NoError(t, os.WriteFile(filepath.Join(root, "a.go"), []byte("package main\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".env"), []byte("API_KEY=top-secret-value\n"), 0o644))
	run("add", "a.go", ".env")
	run("commit", "-m", "initial commit")
	return root
}

func TestGitShow_BlobSyntaxRefusesSecretByDefault(t *testing.T) {
	root := newGitRepoWithSecret(t)
	d := New(&Options{RepoRoot: root, OutputCapBytes: 4096, AllowSecrets: false})

	out := d.Dispatch("git_show", mustJSON(t, map[string]any{"ref": "HEAD:.env"}))
	assert.Contains(t, out, "error:")
	assert.Contains(t, out, "secret-shaped")
	assert.NotContains(t, out, "top-secret-value")
	assert.Empty(t, d.Warnings())
}

func TestGitShow_BlobSyntaxAllowSecretsReturnsContentAndWarns(t *testing.T) {
	root := newGitRepoWithSecret(t)
	d := New(&Options{RepoRoot: root, OutputCapBytes: 4096, AllowSecrets: true})

	out := d.Dispatch("git_show", mustJSON(t, map[string]any{"ref": "HEAD:.env"}))
	assert.Contains(t, out, "top-secret-value")
	require.Len(t, d.Warnings(), 1)
	assert.Contains(t, d.Warnings()[0], ".env")
}

func TestGitShow_CommitDiffRedactsSecretFileByDefault(t *testing.T) {
	root := newGitRepoWithSecret(t)
	d := New(&Options{RepoRoot: root, OutputCapBytes: 4096, AllowSecrets: false})

	out := d.Dispatch("git_show", mustJSON(t, map[string]any{"ref": "HEAD"}))
	assert.Contains(t, out, "initial commit", "the commit message preamble must survive redaction")
	assert.Contains(t, out, "a.go")
	assert.Contains(t, out, ".env")
	assert.Contains(t, out, "omitted")
	assert.NotContains(t, out, "top-secret-value")
}

func TestGitBlame_RefusesSecretByDefault(t *testing.T) {
	root := newGitRepoWithSecret(t)
	d := New(&Options{RepoRoot: root, OutputCapBytes: 4096, AllowSecrets: false})

	out := d.Dispatch("git_blame", mustJSON(t, map[string]any{"path": ".env"}))
	assert.Contains(t, out, "error:")
	assert.Contains(t, out, "secret-shaped")
	assert.NotContains(t, out, "top-secret-value")
}

func TestGitBlame_AllowSecretsReturnsContentAndWarns(t *testing.T) {
	root := newGitRepoWithSecret(t)
	d := New(&Options{RepoRoot: root, OutputCapBytes: 4096, AllowSecrets: true})

	out := d.Dispatch("git_blame", mustJSON(t, map[string]any{"path": ".env"}))
	assert.Contains(t, out, "top-secret-value")
	require.Len(t, d.Warnings(), 1)
}

func TestDispatch_UnknownTool(t *testing.T) {
	d, _ := newTestDispatcher(t, false)

	out := d.Dispatch("delete_everything", "{}")
	assert.Contains(t, out, "error:")
	assert.Contains(t, out, "unknown tool")
}

func TestDispatch_InvalidArgumentsJSON(t *testing.T) {
	d, _ := newTestDispatcher(t, false)

	out := d.Dispatch("read_file", "not json")
	assert.Contains(t, out, "error:")
	assert.Contains(t, out, "invalid arguments")
}
