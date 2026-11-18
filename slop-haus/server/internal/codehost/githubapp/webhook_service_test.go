package githubapp_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	goauthn "github.com/lowaa/slop-factory/go-auth/authn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost/githubapp"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
)

// fakeAppDeliveries returns what the test gives it and records nothing else.
type fakeAppDeliveries struct {
	mu     sync.Mutex
	latest *githubapp.AppDelivery
	// ofEvents is answered for LatestOf whatever the events asked for.
	ofEvents *githubapp.AppDelivery
	err      error
}

func (d *fakeAppDeliveries) Latest(context.Context) (*githubapp.AppDelivery, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.latest, d.err
}

func (d *fakeAppDeliveries) LatestOf(context.Context, []string) (*githubapp.AppDelivery, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.ofEvents, d.err
}

// withWebhook makes the fixture's service one that manages the App's
// webhook for hooksBase, as the server does when WEBHOOK_KEY is set.
func (f *fixture) withWebhook(hooks githubapp.AppDeliveries) {
	f.t.Helper()
	f.svc = f.serviceWith(f.keyring, f.store, func(o *githubapp.Options) {
		o.Webhook = f.configurer(f.keyring, f.store)
		o.Deliveries = hooks
	})
}

func (f *fixture) webhookStatus(tn *tenant) map[string]any {
	f.t.Helper()
	rec := call(f.server(tn.owner), http.MethodGet, apiPath+"/webhook", "")
	require.Equal(f.t, http.StatusOK, rec.Code, rec.Body.String())
	var out map[string]any
	require.NoError(f.t, json.Unmarshal(rec.Body.Bytes(), &out))
	return out
}

func review() *githubapp.AppDelivery {
	return &githubapp.AppDelivery{Event: "pull_request_review", Action: "submitted", Outcome: "applied", ReceivedAt: time.Now()}
}

func TestWebhookStatus_IsUnavailableWhereTheServerHasNoDoor(t *testing.T) {
	f := newFixture(t)
	tn := f.tenant()
	f.connect(tn, 0)

	st := f.webhookStatus(tn)
	assert.Equal(t, "unavailable", st["state"])
	assert.Empty(t, f.github.Calls("/app/hook/config"), "an App is left alone where the server cannot receive for it")

	rec := call(f.server(tn.owner), http.MethodPost, apiPath+"/webhook/check", "")
	assert.Equal(t, http.StatusConflict, rec.Code)
	assert.Contains(t, rec.Body.String(), "github_app_webhook_unavailable")
}

func TestWebhookStatus_ConnectingAnInstalledAppAppliesTheWebhookAndWaitsForAnEvent(t *testing.T) {
	f := newFixture(t)
	hooks := &fakeAppDeliveries{}
	f.withWebhook(hooks)
	tn := f.tenant()
	f.connect(tn, 0)

	require.Len(t, f.patches(), 1)
	st := f.webhookStatus(tn)
	assert.Equal(t, "awaiting_events", st["state"])
	assert.Equal(t, expectedURL, st["expected_url"])
	assert.Equal(t, expectedURL, st["configured_url"])
	assert.Equal(t, []any{"pull_request", "pull_request_review"}, st["events"])
	assert.Nil(t, st["last_event"])

	hooks.mu.Lock()
	hooks.latest, hooks.ofEvents = review(), review()
	hooks.mu.Unlock()
	st = f.webhookStatus(tn)
	assert.Equal(t, "receiving", st["state"])
	assert.NotNil(t, st["last_event"])
	assert.NotContains(t, f.logs.String(), f.patches()[0].Secret)
}

func TestWebhookStatus_APingAloneIsNotReceiving(t *testing.T) {
	f := newFixture(t)
	f.withWebhook(&fakeAppDeliveries{latest: &githubapp.AppDelivery{Event: "ping", Outcome: "ignored", ReceivedAt: time.Now()}})
	tn := f.tenant()
	f.connect(tn, 0)

	st := f.webhookStatus(tn)
	assert.Equal(t, "awaiting_events", st["state"])
	assert.NotNil(t, st["last_delivery"])
	assert.Nil(t, st["last_event"])
}

func TestWebhookStatus_AConnectThatGitHubRefusesStillConnectsAndShowsNotApplied(t *testing.T) {
	f := newFixture(t)
	f.withWebhook(&fakeAppDeliveries{})
	f.github.hookPatch = answer{Status: http.StatusForbidden, Body: map[string]string{"message": "nope"}}
	tn := f.tenant()
	f.connect(tn, 0)

	_, err := f.store.Get(tn.ctx)
	require.NoError(t, err)
	assert.Equal(t, "not_applied", f.webhookStatus(tn)["state"])
}

func TestWebhookStatus_AnAppWithAnotherEnvironmentsURLIsLeftAloneUntilTheOwnerTakesOver(t *testing.T) {
	f := newFixture(t)
	f.withWebhook(&fakeAppDeliveries{})
	f.ghHasURL("https://other-env.test/hooks/github-app")
	tn := f.tenant()
	f.connect(tn, 0)
	assert.Empty(t, f.patches(), "connecting never takes another environment's webhook over")
	assert.Equal(t, "not_applied", f.webhookStatus(tn)["state"])

	mux := f.server(tn.owner)
	rec := call(mux, http.MethodPost, apiPath+"/webhook/check", "")
	assert.Equal(t, http.StatusConflict, rec.Code)
	assert.Contains(t, rec.Body.String(), "github_app_webhook_foreign")
	assert.Empty(t, f.patches())

	rec = call(mux, http.MethodPost, apiPath+"/webhook/check?take_over=true", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"outcome":"applied"`)
	require.Len(t, f.patches(), 1)
	assert.Equal(t, expectedURL, f.patches()[0].URL)
}

func TestWebhookStatus_AMovedEnvironmentShowsTheOldURLUntilTheOwnerTakesTheAppOver(t *testing.T) {
	f := newFixture(t)
	f.withWebhook(&fakeAppDeliveries{})
	tn := f.tenant()
	f.connect(tn, 0)

	// The environment's public address changes: a service started with the new one.
	moved, err := githubapp.NewWebhookConfigurer(&githubapp.WebhookOptions{
		Store: f.store, Keyring: f.keyring, Client: f.client(), PublicBaseURL: "https://hooks.moved.test", Now: f.clock,
		Logger: f.logHandler(),
	})
	require.NoError(t, err)
	f.svc = f.serviceWith(f.keyring, f.store, func(o *githubapp.Options) { o.Webhook = moved; o.Deliveries = &fakeAppDeliveries{} })
	assert.Equal(t, "url_differs", f.webhookStatus(tn)["state"])

	// GitHub still holds the old host's URL, which Check will not replace
	// unprompted: the owner says the App is not shared.
	f.ghHasURL(expectedURL)
	mux := f.server(tn.owner)
	assert.Equal(t, http.StatusConflict, call(mux, http.MethodPost, apiPath+"/webhook/check", "").Code)
	rec := call(mux, http.MethodPost, apiPath+"/webhook/check?take_over=true", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "awaiting_events", f.webhookStatus(tn)["state"])
	assert.Equal(t, "https://hooks.moved.test"+githubapp.AppWebhookPath, f.webhookStatus(tn)["configured_url"])
}

func TestWebhookCheckAndRotate_AreForOwnersAndRotateNeedsARecentSignIn(t *testing.T) {
	f := newFixture(t)
	f.withWebhook(&fakeAppDeliveries{})
	tn := f.tenant()
	f.connect(tn, 0)
	before := len(f.patches())

	asMember := f.server(tn.member)
	for _, path := range []string{"/webhook/check", "/webhook/rotate"} {
		rec := call(asMember, http.MethodPost, apiPath+path, "")
		assert.Equal(t, http.StatusForbidden, rec.Code, path)
	}
	// A member may see the status, not change it.
	rec := call(asMember, http.MethodGet, apiPath+"/webhook", "")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"can_edit":false`)

	stale := *tn.owner
	stale.AuthTime = time.Now().Add(-48 * time.Hour)
	_, err := f.svc.RotateWebhook(tn.ctx, &stale)
	assert.ErrorIs(t, err, goauthn.ErrReauthRequired)
	assert.Len(t, f.patches(), before, "nothing reached GitHub")

	rec = call(f.server(tn.owner), http.MethodPost, apiPath+"/webhook/rotate", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	patches := f.patches()
	require.Len(t, patches, before+1)
	assert.NotEqual(t, patches[0].Secret, patches[1].Secret)
	stored, _, _ := f.storedSecret(tn, f.keyring)
	assert.Equal(t, patches[1].Secret, stored)
	assert.Contains(t, f.logs.String(), "codehost_webhook_rotated")
	assert.NotContains(t, rec.Body.String(), patches[1].Secret)
}

func TestWebhookStatus_ASecondWorkspaceOnTheSameInstallationKeepsItsRepoWebhooks(t *testing.T) {
	f := newFixture(t)
	f.withWebhook(&fakeAppDeliveries{})
	first, second := f.tenant(), f.tenant()
	f.connect(first, 0)
	require.Len(t, f.patches(), 1)
	f.connect(second, 0)
	assert.Len(t, f.patches(), 1, "the second workspace did not overwrite the first's secret on GitHub")

	assert.Equal(t, "shared_installation", f.webhookStatus(second)["state"])
	assert.Equal(t, "awaiting_events", f.webhookStatus(first)["state"])

	rec := call(f.server(second.owner), http.MethodPost, apiPath+"/webhook/rotate", "")
	assert.Equal(t, http.StatusConflict, rec.Code)
	assert.Contains(t, rec.Body.String(), "github_app_webhook_shared")
	rec = call(f.server(second.owner), http.MethodPost, apiPath+"/webhook/check", "")
	assert.Equal(t, http.StatusConflict, rec.Code)
	assert.Len(t, f.patches(), 1)

	// The first workspace can still rotate its own.
	require.Equal(t, http.StatusOK, call(f.server(first.owner), http.MethodPost, apiPath+"/webhook/rotate", "").Code)
}

// afterHeldCheckStore holds each caller that has just checked the
// installation until both have, or a while has passed. Without the store's
// installation lock both arrive and both go on to set GitHub; with it the
// second cannot start its check until the first is done, so the first gives
// up waiting.
type afterHeldCheckStore struct {
	githubapp.Store
	arrived sync.WaitGroup
}

func (s *afterHeldCheckStore) InstallationHeldByOther(ctx context.Context) (bool, error) {
	held, err := s.Store.InstallationHeldByOther(ctx)
	s.arrived.Done()
	done := make(chan struct{})
	go func() { s.arrived.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
	}
	return held, err
}

func TestWebhookApply_TwoWorkspacesRacingForOneInstallationHaveExactlyOneWinner(t *testing.T) {
	f := newFixture(t)
	// Connect before the webhook service exists, so neither has applied.
	a, b := f.tenant(), f.tenant()
	f.connect(a, 0)
	f.connect(b, 0)
	gate := &afterHeldCheckStore{Store: f.store}
	gate.arrived.Add(2)
	f.svc = f.serviceWith(f.keyring, gate, func(o *githubapp.Options) {
		o.Webhook = f.configurer(f.keyring, gate)
	})

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, tn := range []*tenant{a, b} {
		wg.Go(func() { _, _, errs[i] = f.svc.CheckWebhook(tn.ctx, tn.owner, false) })
	}
	wg.Wait()

	wins := 0
	for _, err := range errs {
		if err == nil {
			wins++
		} else {
			assert.True(t, errors.Is(err, githubapp.ErrInstallationShared), err.Error())
		}
	}
	assert.Equal(t, 1, wins)
	assert.Len(t, f.patches(), 1)
}

// getFailsFor fails the credential read of one tenant and hands every other
// call to the real store.
type getFailsFor struct {
	githubapp.Store
	tenant uuid.UUID
}

func (g *getFailsFor) Get(ctx context.Context) (*githubapp.Record, error) {
	if id, err := db.TenantFrom(ctx); err == nil && id == g.tenant {
		return nil, errors.New("store unavailable")
	}
	return g.Store.Get(ctx)
}

func TestWebhookReconcileAll_PutsRightEveryTenantsAppAndSkipsOthers(t *testing.T) {
	f := newFixture(t)
	f.withWebhook(&fakeAppDeliveries{})
	f.ghHasURL("")
	connectedTenant := f.tenant()
	_ = f.tenant() // no App connected
	f.connect(connectedTenant, 0)
	require.Len(t, f.patches(), 1)

	f.ghHasURL("https://hooks.test/stale")
	require.NoError(t, f.svc.ReconcileWebhooks(context.Background()))
	assert.Len(t, f.patches(), 2, "reconcile re-applied the one differing URL; the tenant with no App cost nothing")
}

func TestWebhookReconcileAll_OneFailingTenantDoesNotStopTheRest(t *testing.T) {
	f := newFixture(t)
	f.ghHasURL("")
	// Connect both, in the order the tenants will be listed in or not: either
	// way one of them fails and the other must still be put right.
	failing, healthy := f.tenant(), f.tenant()
	f.connect(failing, 0)
	f.connect(healthy, 1)
	store := &getFailsFor{Store: f.store, tenant: failing.id}
	f.svc = f.serviceWith(f.keyring, store, func(o *githubapp.Options) {
		o.Webhook = f.configurer(f.keyring, store)
	})

	require.NoError(t, f.svc.ReconcileWebhooks(context.Background()))

	require.Len(t, f.patches(), 1, "the healthy tenant's App was set")
	_, _, url := f.storedSecret(healthy, f.keyring)
	assert.Equal(t, expectedURL, url)
	assert.Contains(t, f.logs.String(), "could not configure a GitHub App webhook")
}

func TestWebhook_AnAppWithNoInstallationYetIsNotConfigured(t *testing.T) {
	s := newSetupFixture(t)
	s.withWebhook(&fakeAppDeliveries{})
	s.mux = s.server(s.tn.owner)
	s.createdApp()

	_, _, err := s.svc.CheckWebhook(s.tn.ctx, s.tn.owner, false)

	assert.ErrorIs(t, err, githubapp.ErrNotInstalled)
	assert.Empty(t, s.github.Calls("/app/hook/config"))
	assert.Equal(t, "unavailable", s.webhookStatus(s.tn)["state"])
}

func TestWebhookInstallStep_AppliesOnceTheAppIsInstalled(t *testing.T) {
	s := newSetupFixture(t)
	s.withWebhook(&fakeAppDeliveries{})
	s.mux = s.server(s.tn.owner)
	state := s.createdApp()
	assert.Empty(t, s.patches(), "an App with no installation cannot receive: nothing is set yet")

	rec := s.installed(installationID, "install", state)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Len(t, s.patches(), 1)
}

func TestManifestStart_AsksGitHubToDeliverTheReviewEventsHere(t *testing.T) {
	s := newSetupFixture(t)
	s.withWebhook(&fakeAppDeliveries{})
	s.mux = s.server(s.tn.owner)
	rec, _ := s.begin("")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var body struct {
		Manifest string `json:"manifest"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	var m struct {
		Hook struct {
			URL    string `json:"url"`
			Active bool   `json:"active"`
		} `json:"hook_attributes"`
		Events []string `json:"default_events"`
	}
	require.NoError(t, json.Unmarshal([]byte(body.Manifest), &m))
	assert.Equal(t, expectedURL, m.Hook.URL)
	assert.True(t, m.Hook.Active)
	assert.Equal(t, githubapp.AppWebhookEvents, m.Events)
}
