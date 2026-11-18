package report

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRemoteKey_EveryFormOfOneRemoteIsOneKey(t *testing.T) {
	for _, remote := range []string{
		"git@github.com:example/widgets.git",
		"git@github.com:example/widgets",
		"ssh://git@github.com/example/widgets.git",
		"ssh://git@github.com:22/example/widgets",
		"https://github.com/example/widgets.git",
		"https://someone@github.com/example/widgets/",
		"https://GitHub.com/Example/Widgets.git",
		"  git@github.com:example/widgets.git\n",
	} {
		key, err := RemoteKey(remote)
		require.NoError(t, err, remote)
		assert.Equal(t, "github.com/example/widgets", key, remote)
	}
}

func TestRemoteKey_DifferentReposAreDifferentKeys(t *testing.T) {
	a, err := RemoteKey("git@github.com:example/widgets.git")
	require.NoError(t, err)
	b, err := RemoteKey("git@gitlab.com:example/widgets.git")
	require.NoError(t, err)
	c, err := RemoteKey("git@github.com:someone-else/widgets.git")
	require.NoError(t, err)
	assert.NotEqual(t, a, b, "same path, different host")
	assert.NotEqual(t, a, c, "same name, different owner")
}

func TestRemoteKey_RejectsWhatNamesNoSharedRepo(t *testing.T) {
	for _, remote := range []string{"", "   ", "/home/me/repo", "./repo", "file:///home/me/repo.git", "https://github.com/", "git@github.com:"} {
		_, err := RemoteKey(remote)
		assert.Error(t, err, remote)
	}
}

func TestRepoName(t *testing.T) {
	assert.Equal(t, "widgets", RepoName("github.com/example/widgets"))
}

func TestRedactRemote(t *testing.T) {
	cases := map[string]struct {
		remote string
		want   string
	}{
		"user and password": {
			"https://x-access-token:ghp_secrettoken@github.com/o/r.git",
			"https://github.com/o/r.git",
		},
		"token-only user, no password": {
			"https://ghp_secrettoken@github.com/o/r",
			"https://github.com/o/r",
		},
		"ssh URL with user": {
			"ssh://git@github.com:22/example/widgets",
			"ssh://github.com:22/example/widgets",
		},
		"no userinfo": {
			"https://github.com/example/widgets.git",
			"https://github.com/example/widgets.git",
		},
		"scp-like form is returned unchanged": {
			"git@github.com:example/widgets.git",
			"git@github.com:example/widgets.git",
		},
		"scp-like form is trimmed": {
			"  git@github.com:example/widgets.git\n",
			"git@github.com:example/widgets.git",
		},
		"unparsable URL-form remote is never echoed back": {
			"https://[::not-valid",
			"",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := RedactRemote(tc.remote)
			assert.Equal(t, tc.want, got)
			assert.NotContains(t, got, "secrettoken", "a credential must never survive redaction")
		})
	}
}

// A remote that failed to parse, or that carries a credential, must never
// have its raw form quoted back in RemoteKey's error -- that error is what
// reaches client warnings and server 422 responses.
func TestRemoteKey_ErrorsNeverContainTheCredential(t *testing.T) {
	for name, remote := range map[string]string{
		"unparsable URL with embedded secret":         "https://ghp_secrettoken@[::not-valid",
		"file-scheme local path with embedded secret": "file://ghp_secrettoken@localhost/home/me/repo.git",
		"credentialed URL that names no repository":   "https://ghp_secrettoken@github.com/",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := RemoteKey(remote)
			require.Error(t, err)
			assert.NotContains(t, err.Error(), "ghp_secrettoken")
		})
	}
}
