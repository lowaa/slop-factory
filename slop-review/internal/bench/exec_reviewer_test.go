package bench

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeFakeReviewer writes body as an executable bash script standing in
// for the slop-review binary, so ExecReviewer's process-wiring (argv, env,
// stdout/stderr handling) can be tested without actually invoking an LLM.
func writeFakeReviewer(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-slop-review.sh")
	script := "#!/bin/bash\nset -e\n" + body
	require.NoError(t, os.WriteFile(path, []byte(script), 0o755))
	return path
}

// fakeReviewerArgvEnv is the env var the fake scripts below read to learn
// where to dump their argv -- set per test via t.Setenv rather than baked
// into the script, so the same script text can't accidentally hide a path
// that should have come from the real invocation.
const fakeReviewerArgvEnv = "FAKE_REVIEWER_ARGV_FILE"

func TestExecReviewer_Review_RunsWithExpectedArgsAndParsesOutput(t *testing.T) {
	argvFile := filepath.Join(t.TempDir(), "argv.txt")
	t.Setenv(fakeReviewerArgvEnv, argvFile)

	bin := writeFakeReviewer(t, `printf '%s\n' "$@" > "$`+fakeReviewerArgvEnv+`"
echo "reviewing repo" >&2
cat <<'JSON'
{"review":{"verdict":"approve_with_comments","summary":"one nit","findings":[{"file":"a.go","line":3,"severity":"minor","summary":"empty branch","detail":"consider removing it"}]}}
JSON
`)

	personaDir := t.TempDir()
	reviewDir := t.TempDir()
	logDir := t.TempDir()
	e := &ExecReviewer{Bin: bin, PersonaDir: personaDir, Persona: "standard", LogDir: logDir}

	rev, err := e.Review(context.Background(), RunSpec{Case: "case1", Variant: "v1", Run: 2, Dir: reviewDir, Args: []string{"--max-turns=5", "--foo"}})
	require.NoError(t, err)
	require.NotNil(t, rev)
	assert.Equal(t, "approve_with_comments", string(rev.Verdict))
	assert.Equal(t, "one nit", rev.Summary)
	require.Len(t, rev.Findings, 1)
	assert.Equal(t, "a.go", rev.Findings[0].File)
	assert.Equal(t, 3, rev.Findings[0].Line)
	assert.Equal(t, "empty branch", rev.Findings[0].Summary)

	argv, err := os.ReadFile(argvFile)
	require.NoError(t, err)
	gotArgs := strings.Split(strings.TrimRight(string(argv), "\n"), "\n")
	assert.Equal(t, []string{
		"--persona-dir=" + personaDir,
		"--repo-root=" + reviewDir,
		"--persona=standard",
		"--max-turns=5",
		"--foo",
		"--bench-case=case1",
		"--bench-variant=v1",
		"--bench-run=2",
	}, gotArgs, "the flags this benchmarks are supposed to fix must come before the variant's own extra args, in order, with the bench tag flags last")

	logContent, err := os.ReadFile(filepath.Join(logDir, "run-001.log"))
	require.NoError(t, err)
	assert.Contains(t, string(logContent), "reviewing repo", "LogDir should receive the run's stderr")
}

func TestExecReviewer_Review_NonZeroExitReturnsLastStderrLine(t *testing.T) {
	bin := writeFakeReviewer(t, `echo "starting review" >&2
echo "fatal: could not resolve base ref" >&2
exit 1
`)

	e := &ExecReviewer{Bin: bin, PersonaDir: t.TempDir(), Persona: "standard"}
	rev, err := e.Review(context.Background(), RunSpec{Dir: t.TempDir()})

	assert.Nil(t, rev)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "fatal: could not resolve base ref")
	assert.NotContains(t, err.Error(), "starting review", "only the last stderr line should be surfaced")
}

func TestExecReviewer_Review_MalformedJSONIsAnError(t *testing.T) {
	// set -e in the fake script's shebang would make "echo" alone exit 0
	// fine -- the point here is a zero exit with unparseable stdout.
	bin := writeFakeReviewer(t, `echo 'this is not json{'
`)

	e := &ExecReviewer{Bin: bin, PersonaDir: t.TempDir(), Persona: "standard"}
	rev, err := e.Review(context.Background(), RunSpec{Dir: t.TempDir()})

	assert.Nil(t, rev)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse")
}
