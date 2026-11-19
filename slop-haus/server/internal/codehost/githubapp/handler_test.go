package githubapp_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/secretbox"
)

func connectBody(t *testing.T, key string) string {
	t.Helper()
	return jsonBody(t, map[string]any{"app_id": appID, "installation_id": installationID, "private_key": key})
}

func TestHandler_NoResponseCarriesTheKeyOrAToken(t *testing.T) {
	f := newFixture(t)
	a := f.tenant()
	key := pkcs1PEM(rsaKey(t, 0))
	pemBody := strings.Split(key, "\n")[1]
	f.github.installation = answer{Body: map[string]any{"account": map[string]string{"login": "acme"}}}
	f.mintsFor()
	f.github.repos = func(int) answer {
		return answer{Body: map[string]any{"repositories": []map[string]string{{"full_name": "acme/app"}}}}
	}
	mux := f.server(a.owner)

	responses := map[string]*httptest.ResponseRecorder{
		"connect": call(mux, http.MethodPut, "/api/codehost/github-app", connectBody(t, key)),
		"get":     call(mux, http.MethodGet, "/api/codehost/github-app", ""),
		"verify":  call(mux, http.MethodPost, "/api/codehost/github-app/verify", ""),
		"replace": call(mux, http.MethodPut, "/api/codehost/github-app", connectBody(t, pkcs1PEM(rsaKey(t, 1)))),
		"bad key": call(mux, http.MethodPut, "/api/codehost/github-app", connectBody(t, "-----BEGIN RSA PRIVATE KEY-----\n"+pemBody+"\n")),
		"remove":  call(mux, http.MethodDelete, "/api/codehost/github-app", ""),
	}
	for name, rec := range responses {
		assert.NotContains(t, rec.Body.String(), pemBody, name)
		assert.NotContains(t, rec.Body.String(), "PRIVATE KEY", name)
		assert.NotContains(t, rec.Body.String(), secretToken, name)
	}
	assert.Equal(t, http.StatusOK, responses["connect"].Code, responses["connect"].Body.String())
	assert.Equal(t, http.StatusOK, responses["verify"].Code, responses["verify"].Body.String())
	assert.Equal(t, http.StatusUnprocessableEntity, responses["bad key"].Code)
	assert.Equal(t, http.StatusNoContent, responses["remove"].Code)

	var got struct {
		Enabled    bool            `json:"enabled"`
		CanEdit    bool            `json:"can_edit"`
		Credential json.RawMessage `json:"credential"`
	}
	require.NoError(t, json.Unmarshal(responses["get"].Body.Bytes(), &got))
	assert.True(t, got.Enabled)
	assert.True(t, got.CanEdit)
	assert.JSONEq(t, `{"app_id":4242,"installation_id":777}`, onlyKeys(t, got.Credential, "app_id", "installation_id"))
	var fields map[string]any
	require.NoError(t, json.Unmarshal(got.Credential, &fields))
	assert.ElementsMatch(t, []string{"app_id", "app_slug", "installation_id", "pending", "key_fingerprint", "created_at", "updated_at", "verified_at"}, keysOf(fields))
}

func onlyKeys(t *testing.T, raw json.RawMessage, keep ...string) string {
	t.Helper()
	var all map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &all))
	out := map[string]json.RawMessage{}
	for _, k := range keep {
		out[k] = all[k]
	}
	b, err := json.Marshal(out)
	require.NoError(t, err)
	return string(b)
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestHandler_AMalformedBodyNeverEchoesTheKey(t *testing.T) {
	f := newFixture(t)
	a := f.tenant()
	mux := f.server(a.owner)
	key := pkcs1PEM(rsaKey(t, 0))
	pemBody := strings.Split(key, "\n")[1]

	// A key sent where a number belongs, and one sent with the wrong shape
	// around it: validation errors quote values back.
	for name, body := range map[string]string{
		"key as app id":    `{"app_id":"` + pemBody + `","installation_id":1,"private_key":"x"}`,
		"key as an object": `{"app_id":1,"installation_id":1,"private_key":{"pem":"` + pemBody + `"}}`,
		"truncated json":   `{"app_id":1,"installation_id":1,"private_key":"` + pemBody,
	} {
		rec := call(mux, http.MethodPut, "/api/codehost/github-app", body)
		assert.Equal(t, http.StatusUnprocessableEntity, rec.Code, name)
		assert.NotContains(t, rec.Body.String(), pemBody, name)
	}
	assert.NotContains(t, f.logs.String(), pemBody)
}

func TestHandler_OnlyOwnersWriteAndMembersCanRead(t *testing.T) {
	f := newFixture(t)
	a := f.tenant()
	f.connect(a, 0)
	member := f.server(a.member)

	assert.Equal(t, http.StatusForbidden, call(member, http.MethodPut, "/api/codehost/github-app", connectBody(t, pkcs1PEM(rsaKey(t, 1)))).Code)
	assert.Equal(t, http.StatusForbidden, call(member, http.MethodPost, "/api/codehost/github-app/verify", "").Code)
	assert.Equal(t, http.StatusForbidden, call(member, http.MethodDelete, "/api/codehost/github-app", "").Code)

	rec := call(member, http.MethodGet, "/api/codehost/github-app", "")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"can_edit":false`)
	assert.Contains(t, rec.Body.String(), `"app_id":4242`)
}

func TestHandler_DisabledAnswersConflictAndStatusSaysSo(t *testing.T) {
	f := newFixture(t)
	f.svc = f.service(nil)
	a := f.tenant()
	mux := f.server(a.owner)

	assert.Equal(t, http.StatusConflict, call(mux, http.MethodPut, "/api/codehost/github-app", connectBody(t, pkcs1PEM(rsaKey(t, 0)))).Code)
	rec := call(mux, http.MethodGet, "/api/codehost/github-app", "")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"enabled":false`)
}

func TestHandler_GitHubFailuresAreABadGatewayNotAServerError(t *testing.T) {
	f := newFixture(t)
	a := f.tenant()
	f.connect(a, 0)
	f.github.installation = answer{Status: 404, Body: map[string]string{"message": "Not Found"}}
	rec := call(f.server(a.owner), http.MethodPost, "/api/codehost/github-app/verify", "")
	assert.Equal(t, http.StatusBadGateway, rec.Code)
	assert.Contains(t, rec.Body.String(), "Not Found")
}

// Where the server talks to GitHub is its own configuration. Nothing a
// tenant or a request can say moves it: a JWT signed with the tenant's key
// goes to whoever is at the URL.
func TestTheGitHubURLIsServerConfigurationNotAnythingARequestCanSet(t *testing.T) {
	var hits atomic.Int32
	evil := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	t.Cleanup(evil.Close)

	f := newFixture(t)
	a := f.tenant()
	f.github.installation = answer{Body: map[string]any{"account": map[string]string{"login": "acme"}}}
	f.mintsFor()
	f.github.repos = func(int) answer { return answer{Body: map[string]any{"repositories": []any{}}} }
	mux := f.server(a.owner)

	// A body that names a URL is refused, not ignored.
	for _, field := range []string{"api_base_url", "base_url", "url", "host"} {
		body := jsonBody(t, map[string]any{
			"app_id": appID, "installation_id": installationID, "private_key": pkcs1PEM(rsaKey(t, 0)), field: evil.URL,
		})
		rec := call(mux, http.MethodPut, "/api/codehost/github-app", body)
		assert.Equal(t, http.StatusUnprocessableEntity, rec.Code, field)
	}
	_, err := f.store.Get(a.ctx)
	require.ErrorIs(t, err, codehost.ErrNotConnected, "nothing was stored from them")

	// And every other place a request could name one is not read.
	req := httptest.NewRequest(http.MethodPut, "/api/codehost/github-app?api_base_url="+evil.URL+"&url="+evil.URL,
		strings.NewReader(connectBody(t, pkcs1PEM(rsaKey(t, 0)))))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Api-Url", evil.URL)
	req.Header.Set("X-Forwarded-Host", evil.URL)
	req.Host = evil.URL[len("http://"):]
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, http.StatusOK, call(mux, http.MethodPost, "/api/codehost/github-app/verify", "").Code)
	f.mintsFor("acme/app")
	_, err = f.svc.TokenFor(a.ctx, a.id, acmeApp, codehost.OpenChange)
	require.NoError(t, err)

	assert.Zero(t, hits.Load(), "nothing was sent to a URL a request named")
	assert.Positive(t, f.github.Total(), "everything went to the configured one")

	// And nothing of the kind is stored or answered.
	var cols []string
	rows, err := f.owners.Global().Query(t.Context(),
		`SELECT column_name FROM information_schema.columns WHERE table_name = 'codehost_apps'`)
	require.NoError(t, err)
	defer rows.Close()
	for rows.Next() {
		var c string
		require.NoError(t, rows.Scan(&c))
		cols = append(cols, c)
	}
	for _, c := range cols {
		if c == "webhook_url" {
			// Only a record of the URL this environment itself set on
			// GitHub's webhook; no call is ever made to it.
			continue
		}
		assert.NotContains(t, c, "url", "no column holds a URL")
		assert.NotContains(t, c, "host")
	}
}

func TestRegisterRoutes_EveryOperationIsForASignedInUser(t *testing.T) {
	f := newFixture(t)
	mux := f.server(nil) // no user on the context: the handler is mounted without the session middleware
	for method, path := range map[string]string{http.MethodGet: "/api/codehost/github-app", http.MethodDelete: "/api/codehost/github-app"} {
		rec := call(mux, method, path, "")
		assert.Equal(t, http.StatusUnauthorized, rec.Code, method)
	}
}

func TestHandler_VerifyAnswersEmptyListsNotNull(t *testing.T) {
	f := newFixture(t)
	a := f.tenant()
	f.connect(a, 0)
	f.github.installation = answer{Body: map[string]any{"account": map[string]string{"login": "acme"}}}
	f.mintsFor()
	f.github.repos = func(int) answer { return answer{Body: map[string]any{"repositories": []any{}}} }
	// No manifest, and a manifest with no github.com remote: both compare to nothing.
	for name, remotes := range map[string][]string{"no manifest": nil, "no github remotes": {"gitlab.com/acme/app"}} {
		f.manifest.remotes = remotes
		rec := call(f.server(a.owner), http.MethodPost, "/api/codehost/github-app/verify", "")
		require.Equal(t, http.StatusOK, rec.Code, name)
		assert.Contains(t, rec.Body.String(), `"manifest":[]`, name)
		assert.Contains(t, rec.Body.String(), `"repos":[]`, name)
	}
}

func TestHandler_KnownVerifyFailuresAreNotServerErrors(t *testing.T) {
	ok := func(f *fixture) {
		f.github.installation = answer{Body: map[string]any{"account": map[string]string{"login": "acme"}}}
		f.mintsFor()
		f.github.repos = func(int) answer { return answer{Body: map[string]any{"repositories": []any{}}} }
	}
	for name, tc := range map[string]struct {
		arrange func(f *fixture, a *tenant)
		status  int
		contain string
	}{
		"too many repositories": {func(f *fixture, a *tenant) {
			ok(f)
			full := make([]map[string]string, 100)
			for i := range full {
				full[i] = map[string]string{"full_name": "acme/r"}
			}
			f.github.repos = func(int) answer { return answer{Body: map[string]any{"repositories": full}} }
		}, http.StatusConflict, "restrict the installation"},
		"unreadable GitHub response": {func(f *fixture, a *tenant) {
			ok(f)
			f.github.installation = answer{Body: "not an object"}
		}, http.StatusBadGateway, "unexpected"},
		"stored key not a key": {func(f *fixture, a *tenant) {
			ok(f)
			rec, err := f.store.Get(a.ctx)
			require.NoError(t, err)
			blob, err := f.keyring.Seal([]byte("not a pem"), secretbox.Binding{Tenant: a.id, Row: rec.ID})
			require.NoError(t, err)
			_, err = f.owners.Global().Exec(context.Background(),
				`UPDATE codehost_apps SET private_key_enc = $1 WHERE id = $2`, blob, rec.ID)
			require.NoError(t, err)
		}, http.StatusConflict, "connect the GitHub App again"},
		"credential replaced mid-verify": {func(f *fixture, a *tenant) {
			ok(f)
			f.github.repos = func(int) answer {
				f.connect(a, 1)
				return answer{Body: map[string]any{"repositories": []any{}}}
			}
		}, http.StatusConflict, "verify again"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			a := f.tenant()
			f.connect(a, 0)
			tc.arrange(f, a)
			rec := call(f.server(a.owner), http.MethodPost, "/api/codehost/github-app/verify", "")
			assert.Equal(t, tc.status, rec.Code, rec.Body.String())
			assert.Contains(t, rec.Body.String(), tc.contain)
		})
	}
}

func TestHandler_AValidBodyFollowedByAnotherValueIsRefused(t *testing.T) {
	f := newFixture(t)
	a := f.tenant()
	rec := call(f.server(a.owner), http.MethodPut, "/api/codehost/github-app", connectBody(t, pkcs1PEM(rsaKey(t, 0)))+`{"x":1}`)
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
	_, err := f.store.Get(a.ctx)
	assert.ErrorIs(t, err, codehost.ErrNotConnected)
}
