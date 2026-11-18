package logintest_test

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-auth/login/logintest"
)

const callback = "http://127.0.0.1/auth/callback"

func authorize(t *testing.T, idp *logintest.Server, redirectURI string) *http.Response {
	t.Helper()
	q := url.Values{
		"client_id": {"app"}, "response_type": {"code"}, "redirect_uri": {redirectURI}, "state": {"st"},
		"code_challenge": {"challenge"}, "code_challenge_method": {"S256"}, "nonce": {"n"},
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Get(idp.URL + "/authorize?" + q.Encode())
	require.NoError(t, err)
	_ = resp.Body.Close()
	return resp
}

func TestAuthorizeRedirectsToTheRegisteredURI(t *testing.T) {
	idp := logintest.New(t, "app", "secret", "http://127.0.0.1/other", callback)

	resp := authorize(t, idp, callback)
	require.Equal(t, http.StatusFound, resp.StatusCode)
	loc, err := resp.Location()
	require.NoError(t, err)
	assert.Equal(t, "st", loc.Query().Get("state"))
	assert.NotEmpty(t, loc.Query().Get("code"))
	loc.RawQuery = ""
	assert.Equal(t, callback, loc.String())
}

func TestAuthorizeRefusesAnUnregisteredRedirect(t *testing.T) {
	idp := logintest.New(t, "app", "secret", callback)
	for _, uri := range []string{"https://evil.example/cb", callback + "/", callback + "?x=1", ""} {
		resp := authorize(t, idp, uri)
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode, uri)
		assert.Empty(t, resp.Header.Get("Location"), uri)
	}
}
