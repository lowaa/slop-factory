package githubapp_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost/githubapp"
)

const (
	apiPath    = "/api/codehost/github-app"
	manifestOK = "0123456789abcdef0123456789abcdef01234567"
)

type setupFixture struct {
	*fixture
	tn  *tenant
	mux *http.ServeMux
	pem string
}

func newSetupFixture(t *testing.T) *setupFixture {
	t.Helper()
	f := newFixture(t)
	tn := f.tenant()
	pem := pkcs1PEM(rsaKey(t, 0))
	f.github.conversion = answer{Body: map[string]any{
		"id": appID, "slug": "slop-haus-abc", "pem": pem,
		"client_secret": "client-secret-value", "webhook_secret": "webhook-secret-value",
	}}
	f.github.installations = answer{Body: []map[string]any{}}
	f.github.installationByID = map[int64]answer{
		installationID: {Body: map[string]any{"id": installationID, "app_id": appID, "account": map[string]string{"login": "acme"}}},
	}
	return &setupFixture{fixture: f, tn: tn, mux: f.server(tn.owner), pem: pem}
}

func stateOf(t *testing.T, raw string) string {
	t.Helper()
	u, err := url.Parse(raw)
	require.NoError(t, err)
	state := u.Query().Get("state")
	require.NotEmpty(t, state)
	return state
}

func (s *setupFixture) begin(account string) (*httptest.ResponseRecorder, string) {
	rec := call(s.mux, http.MethodPost, apiPath+"/manifest", jsonBody(s.t, map[string]any{"account": account}))
	if rec.Code != http.StatusOK {
		return rec, ""
	}
	var body struct {
		ActionURL string `json:"action_url"`
	}
	require.NoError(s.t, json.Unmarshal(rec.Body.Bytes(), &body))
	return rec, stateOf(s.t, body.ActionURL)
}

func (s *setupFixture) created(code, state string) *httptest.ResponseRecorder {
	return call(s.mux, http.MethodPost, apiPath+"/manifest/complete", jsonBody(s.t, map[string]any{"code": code, "state": state}))
}

func (s *setupFixture) installed(id int64, action, state string) *httptest.ResponseRecorder {
	return call(s.mux, http.MethodPost, apiPath+"/install/complete",
		jsonBody(s.t, map[string]any{"installation_id": id, "setup_action": action, "state": state}))
}

// createdApp runs Create to the point where the App is stored and pending,
// and returns the state for the install step.
func (s *setupFixture) createdApp() string {
	s.t.Helper()
	_, state := s.begin("")
	rec := s.created(manifestOK, state)
	require.Equal(s.t, http.StatusOK, rec.Code, rec.Body.String())
	var body struct {
		InstallURL string `json:"install_url"`
	}
	require.NoError(s.t, json.Unmarshal(rec.Body.Bytes(), &body))
	return stateOf(s.t, body.InstallURL)
}

func (s *setupFixture) status() map[string]any {
	s.t.Helper()
	var body struct {
		Credential map[string]any `json:"credential"`
	}
	rec := call(s.mux, http.MethodGet, apiPath, "")
	require.NoError(s.t, json.Unmarshal(rec.Body.Bytes(), &body))
	return body.Credential
}

func TestSetup_BeginBuildsTheGitHubURLsOnTheServer(t *testing.T) {
	s := newSetupFixture(t)

	rec, _ := s.begin("")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var personal struct {
		ActionURL string `json:"action_url"`
		Manifest  string `json:"manifest"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &personal))
	assert.True(t, strings.HasPrefix(personal.ActionURL, webURL+"/settings/apps/new?state="), personal.ActionURL)
	var manifest struct {
		RedirectURL string `json:"redirect_url"`
		SetupURL    string `json:"setup_url"`
		Public      bool   `json:"public"`
	}
	require.NoError(t, json.Unmarshal([]byte(personal.Manifest), &manifest))
	assert.Equal(t, boardURL+"/workspace/github-app/created", manifest.RedirectURL)
	assert.Equal(t, boardURL+"/workspace/github-app/installed", manifest.SetupURL)
	assert.False(t, manifest.Public)

	rec, _ = s.begin("acme-inc")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), webURL+"/organizations/acme-inc/settings/apps/new?state=")
	assert.Zero(t, s.github.Total(), "beginning never calls GitHub")
}

func TestSetup_TheAccountIsValidatedStrictly(t *testing.T) {
	s := newSetupFixture(t)
	for name, account := range map[string]string{
		"path":      "../../evil",
		"slash":     "a/b",
		"dot":       "evil.com",
		"space":     "a b",
		"query":     "a?x=1",
		"url":       "https://evil.example",
		"leading -": "-acme",
		"too long":  strings.Repeat("a", 40),
	} {
		rec, _ := s.begin(account)
		assert.Equal(t, http.StatusUnprocessableEntity, rec.Code, name)
		assert.NotContains(t, rec.Body.String(), "evil.example", name)
	}
}

func TestSetup_OnlyAnOwnerWithARecentSignInSetsUp(t *testing.T) {
	s := newSetupFixture(t)
	member := s.server(s.tn.member)
	assert.Equal(t, http.StatusForbidden, call(member, http.MethodPost, apiPath+"/manifest", `{"account":""}`).Code)
	assert.Equal(t, http.StatusForbidden, call(member, http.MethodPost, apiPath+"/manifest/complete", `{"code":"`+manifestOK+`","state":"x"}`).Code)
	assert.Equal(t, http.StatusForbidden, call(member, http.MethodPost, apiPath+"/install-url", ``).Code)
	assert.Equal(t, http.StatusForbidden, call(member, http.MethodPost, apiPath+"/install/complete", `{"installation_id":1,"state":"x"}`).Code)

	stale := *s.tn.owner
	stale.AuthTime = time.Now().Add(-48 * time.Hour)
	old := s.server(&stale)
	rec := call(old, http.MethodPost, apiPath+"/manifest", `{"account":""}`)
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), "reauth_required")
}

func TestSetup_TheCodeIsExchangedAndTheKeyStoredEncryptedAndNeverReturned(t *testing.T) {
	s := newSetupFixture(t)
	pemBody := strings.Split(s.pem, "\n")[1]
	_, state := s.begin("")

	rec := s.created(manifestOK, state)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.NotContains(t, rec.Body.String(), pemBody)
	assert.NotContains(t, rec.Body.String(), "PRIVATE KEY")
	assert.NotContains(t, rec.Body.String(), "secret-value")
	assert.Contains(t, rec.Body.String(), webURL+"/apps/slop-haus-abc/installations/new?state=")

	// The exchange is the unauthenticated call with the code in its path.
	calls := s.github.Calls("/conversions")
	require.Len(t, calls, 1)
	assert.Equal(t, "/app-manifests/"+manifestOK+"/conversions", calls[0].Path)
	assert.Equal(t, http.MethodPost, calls[0].Method)
	assert.Empty(t, calls[0].Auth)

	cred := s.status()
	assert.EqualValues(t, appID, cred["app_id"])
	assert.Equal(t, true, cred["pending"])
	assert.EqualValues(t, 0, cred["installation_id"])
	assert.Equal(t, "slop-haus-abc", cred["app_slug"])
	statusBody := call(s.mux, http.MethodGet, apiPath, "").Body.String()
	assert.NotContains(t, statusBody, pemBody)

	var blob []byte
	require.NoError(t, s.owners.Global().QueryRow(context.Background(),
		`SELECT private_key_enc FROM codehost_credentials WHERE tenant_id = $1`, s.tn.id).Scan(&blob))
	assert.NotContains(t, string(blob), pemBody)
	assert.NotContains(t, string(blob), "PRIVATE KEY")
	for _, secret := range []string{pemBody, manifestOK, state, "secret-value"} {
		assert.NotContains(t, s.logs.String(), secret)
	}

	// Stored, but nothing can act with it until it is installed.
	connected, err := s.svc.Connected(s.tn.ctx)
	require.NoError(t, err)
	assert.False(t, connected)
}

func TestSetup_ABadReusedExpiredOrMisdirectedStateIsRefusedBeforeGitHubIsAsked(t *testing.T) {
	s := newSetupFixture(t)
	otherOwner := s.server(s.join(s.tn.id, "owner"))

	_, used := s.begin("")
	require.Equal(t, http.StatusOK, s.created(manifestOK, used).Code)
	_, expired := s.begin("")
	s.advance(11 * time.Minute)
	install := s.installURLState()
	_, forOwner := s.begin("")
	asked := len(s.github.Calls("/conversions"))

	for name, tc := range map[string]struct {
		mux   *http.ServeMux
		state string
	}{
		"unknown":           {s.mux, "not-a-state"},
		"empty":             {s.mux, ""},
		"reused":            {s.mux, used},
		"expired":           {s.mux, expired},
		"another user's":    {otherOwner, forOwner},
		"a guess at a hash": {s.mux, strings.Repeat("A", 43)},
	} {
		rec := call(tc.mux, http.MethodPost, apiPath+"/manifest/complete", jsonBody(t, map[string]any{"code": manifestOK, "state": tc.state}))
		assert.Equal(t, http.StatusBadRequest, rec.Code, name+": "+rec.Body.String())
		assert.Contains(t, rec.Body.String(), "github_app_setup_state_invalid", name)
	}
	assert.Len(t, s.github.Calls("/conversions"), asked, "GitHub is not asked for a code behind a bad state")

	// A state made for the install step is no good for the create step, and
	// the refusal does not spend it.
	assert.Equal(t, http.StatusBadRequest, s.created(manifestOK, install).Code)
}

// startGatedStore holds every ConsumeState until all contenders have
// arrived, so they run the real, atomic delete together rather than one after
// another.
type startGatedStore struct {
	githubapp.Store
	arrived sync.WaitGroup
}

func (g *startGatedStore) ConsumeState(ctx context.Context, hash []byte, userID uuid.UUID, kind githubapp.StateKind, now time.Time) (bool, error) {
	g.arrived.Done()
	g.arrived.Wait()
	return g.Store.ConsumeState(ctx, hash, userID, kind, now)
}

func TestSetup_AStateWorksOnceEvenWhenTheRequestsRace(t *testing.T) {
	const contenders = 6
	s := newSetupFixture(t)
	gate := &startGatedStore{Store: s.store}
	gate.arrived.Add(contenders)
	s.svc = s.serviceOver(s.keyring, gate)
	s.mux = s.server(s.tn.owner)
	// Issuing does not pass through the gate's wait.
	_, state := s.begin("")

	results := make(chan int, contenders)
	for range contenders {
		go func() { results <- s.created(manifestOK, state).Code }()
	}
	won := 0
	for range contenders {
		if <-results == http.StatusOK {
			won++
		}
	}
	assert.Equal(t, 1, won)
	assert.Len(t, s.github.Calls("/conversions"), 1, "only the winner reaches GitHub")
}

func TestSetup_AnAnswerFromGitHubThatIsNotAnAppStoresNothing(t *testing.T) {
	pemOf := func(s *setupFixture) string { return s.pem }
	for name, respond := range map[string]func(s *setupFixture) answer{
		"refused": func(*setupFixture) answer {
			return answer{Status: http.StatusNotFound, Body: map[string]string{"message": "Not Found"}}
		},
		"no id": func(s *setupFixture) answer {
			return answer{Body: map[string]any{"id": 0, "slug": "x", "pem": pemOf(s)}}
		},
		"no slug": func(s *setupFixture) answer {
			return answer{Body: map[string]any{"id": appID, "slug": "", "pem": pemOf(s)}}
		},
		"slug as a path": func(s *setupFixture) answer {
			return answer{Body: map[string]any{"id": appID, "slug": "../x", "pem": pemOf(s)}}
		},
		"no key": func(*setupFixture) answer { return answer{Body: map[string]any{"id": appID, "slug": "x", "pem": ""}} },
		"not a key": func(*setupFixture) answer {
			return answer{Body: map[string]any{"id": appID, "slug": "x", "pem": "-----BEGIN RSA PRIVATE KEY-----\nQUJD\n-----END RSA PRIVATE KEY-----"}}
		},
		"not json": func(*setupFixture) answer { return answer{Body: "just text"} },
	} {
		t.Run(name, func(t *testing.T) {
			s := newSetupFixture(t)
			s.github.conversion = respond(s)
			_, state := s.begin("")

			rec := s.created(manifestOK, state)

			assert.GreaterOrEqual(t, rec.Code, 400, rec.Body.String())
			assert.Nil(t, s.status(), "no credential was stored")
			assert.NotContains(t, rec.Body.String(), "BEGIN")
		})
	}
}

func TestSetup_AnInstallationIsConfirmedAsTheAppsOwnBeforeItIsSaved(t *testing.T) {
	s := newSetupFixture(t)
	state := s.createdApp()

	rec := s.installed(installationID, "install", state)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	cred := s.status()
	assert.EqualValues(t, installationID, cred["installation_id"])
	assert.Equal(t, false, cred["pending"])

	// It asked GitHub as the App itself: a JWT issued for this App.
	asked := s.github.Calls("/app/installations/777")
	require.Len(t, asked, 1)
	token, _, err := jwt.NewParser().ParseUnverified(strings.TrimPrefix(asked[0].Auth, "Bearer "), jwt.MapClaims{})
	require.NoError(t, err)
	iss, _ := token.Claims.GetIssuer()
	assert.Equal(t, "4242", iss)
}

func TestSetup_AnInstallationOfAnotherAppIsRefused(t *testing.T) {
	s := newSetupFixture(t)
	s.github.installationByID[999] = answer{Body: map[string]any{"id": 999, "app_id": 1, "account": map[string]string{"login": "evil"}}}
	s.github.installationByID[998] = answer{Status: http.StatusNotFound, Body: map[string]string{"message": "Not Found"}}
	s.createdApp()

	for _, id := range []int64{999, 998} {
		rec := s.installed(id, "install", s.installURLState())
		assert.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
		assert.Contains(t, rec.Body.String(), "github_app_wrong_installation")
		assert.Equal(t, true, s.status()["pending"], "nothing was saved")
	}
	assert.EqualValues(t, 0, s.status()["installation_id"])
}

func (s *setupFixture) installURLState() string {
	s.t.Helper()
	rec := call(s.mux, http.MethodPost, apiPath+"/install-url", "")
	require.Equal(s.t, http.StatusOK, rec.Code, rec.Body.String())
	var body struct {
		InstallURL string `json:"install_url"`
	}
	require.NoError(s.t, json.Unmarshal(rec.Body.Bytes(), &body))
	return stateOf(s.t, body.InstallURL)
}

func TestSetup_ARequestedInstallStaysPendingAndVerifyPicksItUpOnceItExists(t *testing.T) {
	s := newSetupFixture(t)
	state := s.createdApp()

	rec := s.installed(0, "request", state)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"requested":true`)
	assert.Equal(t, true, s.status()["pending"])

	// Not approved yet: Verify says so, and keeps waiting.
	verify := call(s.mux, http.MethodPost, apiPath+"/verify", "")
	assert.Equal(t, http.StatusConflict, verify.Code, verify.Body.String())
	assert.Contains(t, verify.Body.String(), "github_app_not_installed")
	assert.Equal(t, true, s.status()["pending"])

	// An App with no installation is no connection: no token is minted for it.
	_, err := s.svc.TokenFor(s.tn.ctx, s.tn.id, acmeApp, codehost.OpenChange)
	require.ErrorIs(t, err, codehost.ErrNotConnected)
	assert.Empty(t, s.github.Calls("/access_tokens"), "GitHub was not asked for a token")

	// An owner approved it; another App's installation in the list is ignored.
	s.github.installations = answer{Body: []map[string]any{
		{"id": 5, "app_id": 1, "account": map[string]string{"login": "other"}},
		{"id": installationID, "app_id": appID, "account": map[string]string{"login": "acme"}},
	}}
	s.mintsFor()
	s.github.repos = func(int) answer {
		return answer{Body: map[string]any{"repositories": []map[string]string{{"full_name": "acme/app"}}}}
	}
	verify = call(s.mux, http.MethodPost, apiPath+"/verify", "")
	require.Equal(t, http.StatusOK, verify.Code, verify.Body.String())
	listed := s.github.Calls("/app/installations")
	require.NotEmpty(t, listed)
	listJWT, _, err := jwt.NewParser().ParseUnverified(strings.TrimPrefix(listed[0].Auth, "Bearer "), jwt.MapClaims{})
	require.NoError(t, err)
	listIss, _ := listJWT.Claims.GetIssuer()
	assert.Equal(t, "4242", listIss, "installations are listed as the App")
	cred := s.status()
	assert.Equal(t, false, cred["pending"])
	assert.EqualValues(t, installationID, cred["installation_id"])
	assert.NotNil(t, cred["verified_at"])

	connected, err := s.svc.Connected(s.tn.ctx)
	require.NoError(t, err)
	assert.True(t, connected)
}

func TestSetup_SeveralInstallationsOfThePrivateAppAreNotTheOwnersInputToFix(t *testing.T) {
	s := newSetupFixture(t)
	require.Equal(t, http.StatusOK, s.installed(0, "request", s.createdApp()).Code)
	s.github.installations = answer{Body: []map[string]any{
		{"id": 7, "app_id": appID, "account": map[string]string{"login": "acme"}},
		{"id": 8, "app_id": appID, "account": map[string]string{"login": "acme-2"}},
	}}

	verify := call(s.mux, http.MethodPost, apiPath+"/verify", "")

	assert.Equal(t, http.StatusConflict, verify.Code, verify.Body.String())
	assert.Contains(t, verify.Body.String(), "github_app_ambiguous_installation")
	assert.Contains(t, verify.Body.String(), "remove the extra installations")
	assert.Equal(t, true, s.status()["pending"], "nothing was saved")
}

func TestSetup_AnInvalidAccountIssuesNoState(t *testing.T) {
	s := newSetupFixture(t)

	rec, _ := s.begin("a/b")

	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
	var rows int
	require.NoError(t, s.owners.Global().QueryRow(context.Background(),
		`SELECT count(*) FROM codehost_setup_states WHERE tenant_id = $1`, s.tn.id).Scan(&rows))
	assert.Zero(t, rows)
}

func TestSetup_AnInstallStepCannotReplaceAnInstalledApp(t *testing.T) {
	s := newSetupFixture(t)
	require.Equal(t, http.StatusOK, s.installed(installationID, "install", s.createdApp()).Code)
	s.github.installationByID[888] = answer{Body: map[string]any{"id": 888, "app_id": appID, "account": map[string]string{"login": "acme"}}}

	rec := s.installed(888, "install", s.installURLState())
	assert.Equal(t, http.StatusConflict, rec.Code)
	assert.EqualValues(t, installationID, s.status()["installation_id"])
}
