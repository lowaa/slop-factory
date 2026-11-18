package codexcli

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSandbox_RunChecksLetsGoTestRunButKeepsTheRepoReadOnly applies the
// exact sandbox policy a review gets to a shell command through `codex
// sandbox`, which makes no model call. It is gated because it depends on
// the installed codex's platform sandbox.
func TestSandbox_RunChecksLetsGoTestRunButKeepsTheRepoReadOnly(t *testing.T) {
	if os.Getenv("SLOP_REVIEW_E2E_CODEX") != "1" {
		t.Skip("set SLOP_REVIEW_E2E_CODEX=1 to run against the real codex sandbox")
	}
	if _, err := exec.LookPath("codex"); err != nil {
		t.Skip("codex is not on PATH")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go is not on PATH")
	}
	// A fresh cache outside the repo, so the go test below can only pass
	// if the cache root is really writable, not by reusing a warm cache.
	cache := t.TempDir()
	scratch := t.TempDir()
	runner := New(&Options{RunChecks: true, WritableRoots: []string{cache}, TempDir: scratch}, slog.DiscardHandler)
	env := append(os.Environ(), "GOCACHE="+cache, "TMPDIR="+scratch)

	makeRepo := func(t *testing.T, parent string) string {
		repo, err := os.MkdirTemp(parent, ".slop-review-e2e-")
		require.NoError(t, err)
		t.Cleanup(func() { _ = os.RemoveAll(repo) })
		files := map[string]string{
			"go.mod":       "module example.com/tiny\n\ngo 1.21\n",
			"tiny.go":      "package tiny\n\nfunc One() int { return 1 }\n",
			"tiny_test.go": "package tiny\n\nimport \"testing\"\n\nfunc TestOne(t *testing.T) {\n\tif One() != 1 {\n\t\tt.Fatal(\"no\")\n\t}\n}\n",
		}
		for name, body := range files {
			require.NoError(t, os.WriteFile(filepath.Join(repo, name), []byte(body), 0o644))
		}
		for _, args := range [][]string{
			{"init", "-q"},
			{"add", "."},
			{"-c", "user.email=x@x", "-c", "user.name=x", "commit", "-q", "-m", "init"},
		} {
			o, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput()
			require.NoError(t, err, string(o))
		}
		return repo
	}
	sandboxEnv := func(env []string, repo string, flags []string, script string) (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		argv := append([]string{"sandbox"}, flags...)
		argv = append(argv, "--", "sh", "-c", script)
		cmd := exec.CommandContext(ctx, "codex", argv...)
		cmd.Dir = repo
		cmd.Env = env
		o, err := cmd.CombinedOutput()
		return string(o), err
	}
	sandbox := func(repo string, flags []string, script string) (string, error) {
		return sandboxEnv(env, repo, flags, script)
	}

	home, err := os.UserHomeDir()
	require.NoError(t, err)
	repo := makeRepo(t, home)
	flags := sandboxFlags(true, runner.writeRoots())

	t.Run("repo writes are denied", func(t *testing.T) {
		o, err := sandbox(repo, flags, "echo x > written")
		assert.Error(t, err, o)
		assert.NoFileExists(t, filepath.Join(repo, "written"))
	})
	t.Run("the scratch temp dir is writable", func(t *testing.T) {
		o, err := sandbox(repo, flags, "echo x > \"$TMPDIR/probe\"")
		assert.NoError(t, err, o)
		assert.FileExists(t, filepath.Join(scratch, "probe"))
	})
	t.Run("a repo under the system temp dir is still read-only", func(t *testing.T) {
		tmpRepo := makeRepo(t, os.TempDir())
		// With the inherited TMPDIR, where a ":tmpdir" grant would land on
		// the repo; the runner's own TMPDIR points at the scratch dir.
		o, err := sandboxEnv(os.Environ(), tmpRepo, flags, "echo x > written")
		assert.Error(t, err, o)
		assert.NoFileExists(t, filepath.Join(tmpRepo, "written"))
		o, err = sandbox(tmpRepo, flags, "go test -count=1 ./...")
		assert.NoError(t, err, o)
	})
	t.Run("go test runs", func(t *testing.T) {
		o, err := sandbox(repo, flags, "go test -count=1 ./...")
		assert.NoError(t, err, o)
	})
	t.Run("network is denied", func(t *testing.T) {
		// Without curl the command fails for the wrong reason.
		if _, err := exec.LookPath("curl"); err != nil {
			t.Skip("curl is not on PATH")
		}
		// An offline machine fails the sandboxed curl too, which would
		// prove nothing about the sandbox.
		if out, err := exec.Command("curl", "-sS", "-m", "5", "-o", "/dev/null", "https://example.com").CombinedOutput(); err != nil {
			t.Skipf("this machine can't reach the network itself: %v: %s", err, out)
		}
		o, err := sandbox(repo, flags, "curl -sS -m 5 -o /dev/null https://example.com")
		assert.Error(t, err, o)
	})
	t.Run("without run-checks go test cannot even build", func(t *testing.T) {
		o, err := sandbox(repo, sandboxFlags(false, nil), "go test -count=1 ./...")
		assert.Error(t, err, o)
	})
}
