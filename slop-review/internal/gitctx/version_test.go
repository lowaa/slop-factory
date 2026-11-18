package gitctx

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseGitVersion(t *testing.T) {
	cases := map[string]struct {
		output string
		want   gitVersion
	}{
		"plain":              {"git version 2.43.0", gitVersion{2, 43}},
		"trailing newline":   {"git version 2.43.0\n", gitVersion{2, 43}},
		"vendor suffix":      {"git version 2.39.2 (Apple Git-143)", gitVersion{2, 39}},
		"windows patch tail": {"git version 2.43.0.windows.1", gitVersion{2, 43}},
		"single-digit minor": {"git version 3.0.0", gitVersion{3, 0}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := parseGitVersion(tc.output)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestParseGitVersion_RejectsUnrecognisedOutput(t *testing.T) {
	cases := map[string]string{
		"empty":      "",
		"no version": "git version",
		"wrong tool": "hg version 6.0",
		"garbage":    "not even close",
	}
	for name, output := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := parseGitVersion(output)
			assert.Error(t, err)
		})
	}
}

func TestGitVersion_AtLeast(t *testing.T) {
	cases := map[string]struct {
		v, min gitVersion
		want   bool
	}{
		"equal":       {gitVersion{2, 40}, gitVersion{2, 40}, true},
		"newer minor": {gitVersion{2, 41}, gitVersion{2, 40}, true},
		"older minor": {gitVersion{2, 39}, gitVersion{2, 40}, false},
		"newer major": {gitVersion{3, 0}, gitVersion{2, 40}, true},
		"older major": {gitVersion{1, 99}, gitVersion{2, 40}, false},
		"older major, high minor number does not compensate": {gitVersion{1, 999}, gitVersion{2, 40}, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.want, tc.v.atLeast(tc.min))
		})
	}
}

func TestCheckGitVersionOutput_AcceptsSupportedVersions(t *testing.T) {
	for _, output := range []string{"git version 2.40.0", "git version 2.43.0", "git version 3.0.0"} {
		t.Run(output, func(t *testing.T) {
			assert.NoError(t, checkGitVersionOutput(output))
		})
	}
}

func TestCheckGitVersionOutput_RejectsOlderGitWithAClearMessage(t *testing.T) {
	err := checkGitVersionOutput("git version 2.39.2 (Apple Git-143)")
	require.Error(t, err)
	assert.ErrorContains(t, err, "slop-review needs git 2.40 or newer")
	assert.ErrorContains(t, err, "found git 2.39")
}

func TestCheckGitVersionOutput_UnparsableOutputIsAlsoAClearError(t *testing.T) {
	err := checkGitVersionOutput("not a version string")
	require.Error(t, err)
	assert.ErrorContains(t, err, "gitctx: checking git's version")
}

// ensureCheckAttrSourceSupported shells out to the real git, so this only
// checks that it agrees with the direct decision function above for
// whatever git is actually on PATH -- a negative case (an old git) isn't
// reproducible without one installed.
func TestEnsureCheckAttrSourceSupported_AgreesWithTheRealGit(t *testing.T) {
	dir := newTestRepo(t)
	out, err := runGit(dir, "version")
	require.NoError(t, err)
	want := checkGitVersionOutput(out)

	got := ensureCheckAttrSourceSupported(dir)

	if want == nil {
		assert.NoError(t, got)
	} else {
		require.Error(t, got)
		assert.Equal(t, want.Error(), got.Error())
	}
}
