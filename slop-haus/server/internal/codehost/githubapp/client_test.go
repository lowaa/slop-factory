package githubapp_test

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost/githubapp"
)

func TestNewClient_TakesOnlyAnHTTPBaseURLWithoutCredentials(t *testing.T) {
	for _, ok := range []string{"", "https://api.github.com", "https://ghe.example/api/v3/", "http://127.0.0.1:9"} {
		_, err := githubapp.NewClient(&githubapp.ClientOptions{BaseURL: ok})
		assert.NoError(t, err, ok)
	}
	for _, bad := range []string{"api.github.com", "ftp://x", "https://", "https://u:p@api.github.com", "https://x?y=1", "https://x#f", "file:///etc"} {
		_, err := githubapp.NewClient(&githubapp.ClientOptions{BaseURL: bad})
		assert.Error(t, err, bad)
	}
}

// A redirect would carry the App's JWT to wherever it points.
func TestClient_DoesNotFollowARedirectWithTheCredential(t *testing.T) {
	var hits atomic.Int32
	evil := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	t.Cleanup(evil.Close)

	f := newFixture(t)
	f.github.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, evil.URL+"/steal", http.StatusTemporaryRedirect)
	})
	a := f.tenant()
	f.connect(a, 0)

	_, err := f.svc.TokenFor(a.ctx, a.id, acmeApp, codehost.OpenChange)
	var apiErr *githubapp.APIError
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, http.StatusTemporaryRedirect, apiErr.Status)
	assert.Zero(t, hits.Load(), "nothing followed the redirect")
}

func TestClient_AnUnreachableGitHubIsErrUnreachable(t *testing.T) {
	f := newFixture(t)
	a := f.tenant()
	f.connect(a, 0)
	f.github.srv.Close()

	_, err := f.svc.TokenFor(a.ctx, a.id, acmeApp, codehost.OpenChange)
	assert.ErrorIs(t, err, githubapp.ErrUnreachable)
}

func TestClient_AGarbledTokenResponseNeverEchoesTheBody(t *testing.T) {
	f := newFixture(t)
	a := f.tenant()
	f.connect(a, 0)
	f.github.token = func(int) answer { return answer{Body: "ghs_looks_like_a_token but is a json string"} }

	_, err := f.svc.TokenFor(a.ctx, a.id, acmeApp, codehost.OpenChange)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "ghs_")
}

func TestClient_AResponseWithNoExpiryIsRefused(t *testing.T) {
	f := newFixture(t)
	a := f.tenant()
	f.connect(a, 0)
	f.github.token = func(int) answer {
		return answer{Body: map[string]any{"token": "x", "repositories": []map[string]string{{"full_name": "acme/app"}}}}
	}

	_, err := f.svc.TokenFor(a.ctx, a.id, acmeApp, codehost.OpenChange)
	require.Error(t, err)
	assert.NotErrorIs(t, err, githubapp.ErrRepoNotCovered, "refused for the missing expiry, not for the repo")
}

func TestVerify_ListsEveryPageOfRepositories(t *testing.T) {
	f := newFixture(t)
	a := f.tenant()
	f.connect(a, 0)
	f.github.installation = answer{Body: map[string]any{"account": map[string]string{"login": "acme"}}}
	f.github.token = func(int) answer { return answer{Body: tokenBody("t", time.Now().Add(time.Hour))} }
	page := func(n, count int) []map[string]string {
		out := make([]map[string]string, count)
		for i := range out {
			out[i] = map[string]string{"full_name": "acme/r" + string(rune('a'+n)) + string(rune('a'+i%26)) + string(rune('a'+i/26))}
		}
		return out
	}
	f.github.repos = func(p int) answer {
		if p == 1 {
			return answer{Body: map[string]any{"repositories": page(1, 100)}}
		}
		return answer{Body: map[string]any{"repositories": page(2, 3)}}
	}

	v, err := f.svc.Verify(a.ctx, a.owner)
	require.NoError(t, err)
	require.Len(t, v.Repos, 103)
	assert.Equal(t, "acme/rbaa", v.Repos[0], "page one's names come first")
	assert.Equal(t, "acme/rcca", v.Repos[102], "and page two's last")
	assert.Len(t, f.github.Calls("/installation/repositories"), 2)
}

func TestVerify_AnInstallationBeyondWhatItListsIsAnErrorNotAShortList(t *testing.T) {
	f := newFixture(t)
	a := f.tenant()
	f.connect(a, 0)
	f.github.installation = answer{Body: map[string]any{"account": map[string]string{"login": "acme"}}}
	f.github.token = func(int) answer { return answer{Body: tokenBody("t", time.Now().Add(time.Hour))} }
	full := make([]map[string]string, 100)
	for i := range full {
		full[i] = map[string]string{"full_name": "acme/r"}
	}
	f.github.repos = func(p int) answer { return answer{Body: map[string]any{"repositories": full}} }

	_, err := f.svc.Verify(a.ctx, a.owner)
	assert.ErrorIs(t, err, githubapp.ErrTooManyRepos)
}

func TestVerify_ExactlyTheListingLimitIsListedAndOneMoreIsAnError(t *testing.T) {
	for name, tc := range map[string]struct {
		extra   int
		wantErr bool
	}{"exactly the limit": {0, false}, "one more": {1, true}} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			a := f.tenant()
			f.connect(a, 0)
			f.github.installation = answer{Body: map[string]any{"account": map[string]string{"login": "acme"}}}
			f.github.token = func(int) answer { return answer{Body: tokenBody("t", time.Now().Add(time.Hour))} }
			repos := func(n int) []map[string]string {
				out := make([]map[string]string, n)
				for i := range out {
					out[i] = map[string]string{"full_name": "acme/r"}
				}
				return out
			}
			f.github.repos = func(p int) answer {
				if p <= 10 {
					return answer{Body: map[string]any{"repositories": repos(100)}}
				}
				return answer{Body: map[string]any{"repositories": repos(tc.extra)}}
			}

			v, err := f.svc.Verify(a.ctx, a.owner)
			if tc.wantErr {
				assert.ErrorIs(t, err, githubapp.ErrTooManyRepos)
				return
			}
			require.NoError(t, err)
			assert.Len(t, v.Repos, 1000)
		})
	}
}
