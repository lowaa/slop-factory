package codexcli

import (
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func argsFor(opts *Options) []string {
	return New(opts, slog.DiscardHandler).args(&ExecRequest{Cwd: "/repo"}, "/s.json", "/o.json")
}

func filesystemFlag(argv []string) (string, bool) {
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == "-c" && strings.HasPrefix(argv[i+1], "permissions.slop-review.filesystem=") {
			return argv[i+1], true
		}
	}
	return "", false
}

func TestArgs_StrictByDefault(t *testing.T) {
	argv := argsFor(&Options{WritableRoots: []string{"/cache"}})

	_, ok := filesystemFlag(argv)
	assert.False(t, ok, "without RunChecks nothing is writable, whatever roots were passed")
	assert.True(t, containsPair(argv, "-c", `permissions.slop-review.network.enabled=false`))
}

func TestArgs_RunChecksAddsWritableRootsAndScratchTempDirButKeepsNetworkOff(t *testing.T) {
	argv := argsFor(&Options{RunChecks: true, WritableRoots: []string{"/cache/go-build", `/we"ird\dir`}, TempDir: "/scratch"})

	fs, ok := filesystemFlag(argv)
	require.True(t, ok)
	assert.Equal(t, `permissions.slop-review.filesystem={"/cache/go-build"="write","/we\"ird\\dir"="write","/scratch"="write"}`, fs)
	assert.NotContains(t, fs, "tmpdir", "the system temp dir may contain the repository")
	assert.True(t, containsPair(argv, "-c", `permissions.slop-review.extends=":read-only"`))
	assert.True(t, containsPair(argv, "-c", `permissions.slop-review.network.enabled=false`))
	assert.True(t, containsPair(argv, "-c", `default_permissions="slop-review"`))
}

func TestArgs_RunChecksSkipsRootsThatCannotBeQuoted(t *testing.T) {
	argv := argsFor(&Options{RunChecks: true, WritableRoots: []string{"/bad\ndir"}, TempDir: "/scratch"})

	fs, ok := filesystemFlag(argv)
	require.True(t, ok)
	assert.Equal(t, `permissions.slop-review.filesystem={"/scratch"="write"}`, fs)
}

func TestEnv_RunChecksPointsTMPDIRAtTheScratchDir(t *testing.T) {
	t.Setenv("TMPDIR", "/inherited")
	env := New(&Options{RunChecks: true, TempDir: "/scratch"}, slog.DiscardHandler).env()
	// exec uses the last value of a duplicated key, so the override must
	// come after the inherited one.
	assert.Equal(t, "TMPDIR=/scratch", lastWithPrefix(env, "TMPDIR="))
	assert.NotContains(t, New(&Options{TempDir: "/scratch"}, slog.DiscardHandler).env(), "TMPDIR=/scratch", "only under run-checks")
}

func TestEnv_RunChecksStopsPythonWritingBytecodeIntoTheRepo(t *testing.T) {
	t.Setenv("PYTHONDONTWRITEBYTECODE", "")
	assert.Contains(t, New(&Options{RunChecks: true}, slog.DiscardHandler).env(), "PYTHONDONTWRITEBYTECODE=1")
	// The parent may already export it, so only what env() adds counts.
	require.NoError(t, os.Unsetenv("PYTHONDONTWRITEBYTECODE"))
	assert.NotContains(t, New(&Options{}, slog.DiscardHandler).env(), "PYTHONDONTWRITEBYTECODE=1")
}

func lastWithPrefix(env []string, prefix string) string {
	last := ""
	for _, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			last = kv
		}
	}
	return last
}
