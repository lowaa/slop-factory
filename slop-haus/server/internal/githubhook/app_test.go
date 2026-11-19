package githubhook_test

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost/github"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost/githubapp"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/githubhook"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/secretbox"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// Deliveries from a tenant's GitHub App: found by installation, checked
// against the secret stored for it, and applied by the ticket rules.

// webhookPurpose is the purpose the App's webhook secret is sealed for (see
// githubapp.OpenWebhookSecret).
const webhookPurpose = "webhook secret"

const appInstallation = int64(777)

type openerFromKeyring struct{ keyring *secretbox.Keyring }

func (o openerFromKeyring) Open(tenant, credential uuid.UUID, sealed []byte) (string, error) {
	s, err := githubapp.OpenWebhookSecret(o.keyring, tenant, credential, sealed)
	if err != nil {
		return "", err
	}
	return s.Reveal(), nil
}

// useApp is the keyring the fixture's door opens secrets with.
func (f *fixture) useApp() *secretbox.Keyring { return f.keyring }

// connectApp stores a credential for tenant with installation and webhook
// secret, as the credential service does.
func (f *fixture) connectApp(keyring *secretbox.Keyring, tenant *tenantFixture, installation int64, secret string) {
	f.t.Helper()
	id := uuid.New()
	sealed, err := keyring.Seal([]byte(secret), secretbox.Binding{Tenant: tenant.id, Row: id, Purpose: webhookPurpose})
	require.NoError(f.t, err)
	_, err = f.database.Exec(tenant.ctx, `
		INSERT INTO codehost_apps (id, provider, app_id, private_key_enc, key_id, key_fingerprint,
		                           webhook_secret_enc, webhook_secret_key_id)
		VALUES ($1, 'github_app', 1, '\x00', $2, $3, $4, $2)`,
		id, keyring.CurrentID(), strings.Repeat("f", 64), sealed)
	require.NoError(f.t, err)
	_, err = f.database.Exec(tenant.ctx, `
		INSERT INTO codehost_credentials (id, app_ref, installation_id, binding) VALUES (gen_random_uuid(), $1, $2, 'github.com')`,
		id, installation)
	require.NoError(f.t, err)
}

type appDelivery struct {
	event string
	id    string
	body  string
	// signWith signs the body; empty is no signature at all.
	signWith string
}

func (f *fixture) deliverApp(d appDelivery) *httptest.ResponseRecorder {
	f.t.Helper()
	if d.id == "" {
		d.id = uuid.NewString()
	}
	req := httptest.NewRequest(http.MethodPost, "/hooks/github-app", strings.NewReader(d.body))
	req.Header.Set("X-GitHub-Event", d.event)
	req.Header.Set("X-GitHub-Delivery", d.id)
	if d.signWith != "" {
		req.Header.Set("X-Hub-Signature-256", signature(d.signWith, []byte(d.body)))
	}
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	return rec
}

// withInstallation adds the installation GitHub puts on every App delivery.
func withInstallation(body string, installation int64) string {
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		return body
	}
	m["installation"] = map[string]any{"id": installation}
	b, _ := json.Marshal(m)
	return string(b)
}

func (f *fixture) appRecords(tenant *tenantFixture) (n int) {
	f.t.Helper()
	require.NoError(f.t, f.database.QueryRow(tenant.ctx, `SELECT count(*) FROM github_deliveries WHERE installation_id IS NOT NULL`).Scan(&n))
	return n
}

func TestReceiveApp_AnApprovalSignedWithTheAppSecretApprovesTheTicket(t *testing.T) {
	f := newFixture(t)
	keyring := f.useApp()
	f.connectApp(keyring, f.a, appInstallation, "app-secret")
	reviewed := f.inReview(f.a, prURL)

	rec := f.deliverApp(appDelivery{event: "pull_request_review", signWith: "app-secret",
		body: withInstallation(reviewBody("approved", reviewerGitHubID, time.Now(), ""), appInstallation)})

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, fmt.Sprintf("applied: #%d approved\n", reviewed.ID), rec.Body.String())
	assert.Equal(t, ticket.StatusOpen, f.status(f.a, reviewed.ID))
	assert.Equal(t, 1, f.appRecords(f.a), "recorded")
	assert.Len(t, f.deliveries(f.a), 1, "and listed on the board")
}

func TestReceiveApp_AWrongOrMissingSignatureIsRefusedAndNothingIsRecorded(t *testing.T) {
	f := newFixture(t)
	keyring := f.useApp()
	f.connectApp(keyring, f.a, appInstallation, "app-secret")
	reviewed := f.inReview(f.a, prURL)
	body := withInstallation(reviewBody("approved", reviewerGitHubID, time.Now(), ""), appInstallation)

	for name, signWith := range map[string]string{
		"wrong secret": "other-secret",
		"no signature": "",
	} {
		t.Run(name, func(t *testing.T) {
			rec := f.deliverApp(appDelivery{event: "pull_request_review", signWith: signWith, body: body})
			assert.Equal(t, http.StatusUnauthorized, rec.Code)
			assert.Equal(t, "githubhook: bad signature\n", rec.Body.String())
		})
	}
	assert.Equal(t, ticket.StatusReview, f.status(f.a, reviewed.ID))
	assert.Zero(t, f.appRecords(f.a))
}

// GitHub sends one App's every installation to its one URL, so an
// installation nobody here has connected is expected traffic, not an attack.
func TestReceiveApp_AnUnknownInstallationOrNoneIsIgnored(t *testing.T) {
	f := newFixture(t)
	keyring := f.useApp()
	f.connectApp(keyring, f.a, appInstallation, "app-secret")
	reviewed := f.inReview(f.a, prURL)
	review := reviewBody("approved", reviewerGitHubID, time.Now(), "")

	for name, body := range map[string]string{
		"another installation": withInstallation(review, 999),
		"no installation":      `{"zen":"Keep it logically awesome."}`,
		"not json":             `nope`,
	} {
		t.Run(name, func(t *testing.T) {
			rec := f.deliverApp(appDelivery{event: "pull_request_review", signWith: "app-secret", body: body})
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			assert.True(t, strings.HasPrefix(rec.Body.String(), "ignored: "), rec.Body.String())
		})
	}
	assert.Equal(t, ticket.StatusReview, f.status(f.a, reviewed.ID))
	assert.Zero(t, f.appRecords(f.a))
}

func TestReceiveApp_APingIsAcknowledged(t *testing.T) {
	f := newFixture(t)
	keyring := f.useApp()
	f.connectApp(keyring, f.a, appInstallation, "app-secret")

	rec := f.deliverApp(appDelivery{event: "ping", signWith: "app-secret",
		body: withInstallation(`{"zen":"Speak like a human."}`, appInstallation)})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.True(t, strings.HasPrefix(rec.Body.String(), "applied: ping"), rec.Body.String())
}

// Tenants that registered the same installation id (a misconfiguration, as
// an installation belongs to one App with one secret): the secret that signed
// the delivery says whose it is, so one cannot take another's deliveries.
func TestReceiveApp_AnInstallationSharedByTwoTenantsGoesToTheOneWhoseSecretSigned(t *testing.T) {
	f := newFixture(t)
	keyring := f.useApp()
	b := f.newTenant()
	f.linkGitHub(b.member, 5005)
	f.connectApp(keyring, f.a, appInstallation, "secret-a")
	f.connectApp(keyring, b, appInstallation, "secret-b")
	inA := f.inReview(f.a, prURL)
	inB := f.inReview(b, prURL)

	rec := f.deliverApp(appDelivery{event: "pull_request_review", signWith: "secret-b",
		body: withInstallation(reviewBody("approved", 5005, time.Now(), ""), appInstallation)})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "applied")
	assert.Equal(t, ticket.StatusReview, f.status(f.a, inA.ID))
	assert.Equal(t, ticket.StatusOpen, f.status(b, inB.ID))
}

func TestReceiveApp_ARedeliveredDeliveryIsHandledOnce(t *testing.T) {
	f := newFixture(t)
	keyring := f.useApp()
	f.connectApp(keyring, f.a, appInstallation, "app-secret")
	f.inReview(f.a, prURL)
	d := appDelivery{event: "pull_request_review", signWith: "app-secret", id: "same",
		body: withInstallation(reviewBody("approved", reviewerGitHubID, time.Now(), ""), appInstallation)}

	require.Contains(t, f.deliverApp(d).Body.String(), "applied")
	assert.Equal(t, "duplicate: already handled\n", f.deliverApp(d).Body.String())
}

func TestReceiveApp_IsShutWithoutAppSecrets(t *testing.T) {
	f := newFixture(t)
	f.svc = newService(t, githubhook.NewPostgresStore(f.database), f.a.tickets, &githubhook.Config{Host: github.New()})
	f.handler = githubhook.NewReceiver(f.svc, slog.DiscardHandler).Handler()
	rec := f.deliverApp(appDelivery{event: "ping", signWith: "x", body: withInstallation(`{}`, appInstallation)})
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestReceiveApp_ARemovedCredentialStopsDeliveries(t *testing.T) {
	f := newFixture(t)
	keyring := f.useApp()
	f.connectApp(keyring, f.a, appInstallation, "app-secret")
	_, err := f.database.Exec(f.a.ctx, `DELETE FROM codehost_apps`)
	require.NoError(t, err)

	rec := f.deliverApp(appDelivery{event: "ping", signWith: "app-secret", body: withInstallation(`{}`, appInstallation)})
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "ignored")
}

// The same review can reach slop-haus twice under different delivery ids.
// The ticket rules alone would take the second one if the ticket had been
// sent back and put into review again in between, as an agent can within
// seconds.
func TestReceiveApp_TheSameReviewInTwoDeliveriesAppliesOnce(t *testing.T) {
	f := newFixture(t)
	app := f.install(f.a)
	reviewed := f.inReview(f.a, prURL)
	// After the ticket's review began, as GitHub's own clock may put it.
	review := reviewBody("changes_requested", reviewerGitHubID, time.Now().Add(time.Hour), "Split it up.")

	first := f.deliver(delivery{app: app, event: "pull_request_review", body: review})
	require.Contains(t, first.Body.String(), "sent back for changes")
	back := ticket.StatusReview
	_, err := f.a.tickets.Update(f.a.ctx, reviewed.ID, &ticket.UpdateParams{Status: &back})
	require.NoError(t, err)

	second := f.deliver(delivery{app: app, event: "pull_request_review", body: review})
	require.Equal(t, http.StatusOK, second.Code, second.Body.String())
	assert.Equal(t, "ignored: already applied from another delivery\n", second.Body.String())

	comments, err := f.a.tickets.ListComments(f.a.ctx, reviewed.ID)
	require.NoError(t, err)
	assert.Len(t, comments, 1, "the second copy added no comment")
	assert.Equal(t, ticket.StatusReview, f.status(f.a, reviewed.ID), "and did not send the ticket back again")
}

func TestReceiveApp_TheSameApprovalInTwoDeliveriesAppliesOnce(t *testing.T) {
	f := newFixture(t)
	app := f.install(f.a)
	reviewed := f.inReview(f.a, prURL)
	review := reviewBody("approved", reviewerGitHubID, time.Now().Add(time.Hour), "")

	rec := f.deliver(delivery{app: app, event: "pull_request_review", body: review})
	require.Contains(t, rec.Body.String(), "approved")
	back := ticket.StatusReview
	_, err := f.a.tickets.Update(f.a.ctx, reviewed.ID, &ticket.UpdateParams{Status: &back})
	require.NoError(t, err)

	rec = f.deliver(delivery{app: app, event: "pull_request_review", body: review})
	assert.Equal(t, "ignored: already applied from another delivery\n", rec.Body.String())
	assert.Equal(t, ticket.StatusReview, f.status(f.a, reviewed.ID), "the repeat did not approve the new cycle")
}

func TestReceiveApp_TheSameMergeInTwoDeliveriesClosesOnce(t *testing.T) {
	f := newFixture(t)
	app := f.install(f.a)
	reviewed := f.inReview(f.a, prURL)

	rec := f.deliver(delivery{app: app, event: "pull_request", body: mergedBody(reviewerGitHubID)})
	require.Contains(t, rec.Body.String(), "closed as merged")
	rec = f.deliver(delivery{app: app, event: "pull_request", body: mergedBody(reviewerGitHubID)})
	assert.True(t, strings.HasPrefix(rec.Body.String(), "ignored: "), rec.Body.String())
	assert.Equal(t, ticket.StatusDone, f.status(f.a, reviewed.ID))
}

// A delivery that did nothing leaves nothing behind that stops a later one:
// a review from someone who links their account afterwards still counts.
func TestReceive_ADeliveryThatChangedNothingDoesNotUseUpTheReview(t *testing.T) {
	f := newFixture(t)
	ep := f.install(f.a)
	reviewed := f.inReview(f.a, prURL)
	late := f.newMember(f.a.id)
	review := reviewBody("approved", 6006, time.Now().Add(time.Hour), "")

	rec := f.deliver(delivery{app: ep, event: "pull_request_review", body: review})
	require.Contains(t, rec.Body.String(), "not a member")
	f.linkGitHub(late, 6006)
	rec = f.deliver(delivery{app: ep, event: "pull_request_review", body: review})
	require.Contains(t, rec.Body.String(), "applied")
	assert.Equal(t, ticket.StatusOpen, f.status(f.a, reviewed.ID))
}

func TestPostgresStore_ClaimEvent(t *testing.T) {
	f := newFixture(t)
	store := githubhook.NewPostgresStore(f.database)
	ctx := f.a.ctx
	claim := func(ctx context.Context, delivery string) bool {
		ok, err := store.ClaimEvent(ctx, "review:x", delivery)
		require.NoError(t, err)
		return ok
	}

	assert.True(t, claim(ctx, "d1"))
	assert.False(t, claim(ctx, "d2"), "held by another delivery")
	assert.True(t, claim(ctx, "d1"), "the holder may take its own claim up again")
	require.NoError(t, store.FinishEvent(ctx, "review:x", "d1", false))
	assert.True(t, claim(ctx, "d2"), "given up")

	require.NoError(t, store.FinishEvent(ctx, "review:x", "d2", true))
	assert.False(t, claim(ctx, "d2"), "applied stays applied")
	assert.False(t, claim(ctx, "d3"))
	assert.True(t, claim(f.newTenant().ctx, "d3"), "and is per tenant")

	_, err := f.database.Exec(ctx, `UPDATE github_event_keys SET state = 'claimed', claimed_at = now() - interval '1 hour'`)
	require.NoError(t, err)
	assert.True(t, claim(ctx, "d4"), "a claim whose delivery died is taken over")
}

// A claim whose delivery died is taken over by another, and the first, if it
// was only slow, must not then end the second's claim.
func TestPostgresStore_ASlowClaimantCannotEndATakeOver(t *testing.T) {
	f := newFixture(t)
	store := githubhook.NewPostgresStore(f.database)
	ctx := f.a.ctx
	ok, err := store.ClaimEvent(ctx, "review:y", "slow")
	require.NoError(t, err)
	require.True(t, ok)
	_, err = f.database.Exec(ctx, `UPDATE github_event_keys SET claimed_at = now() - interval '1 hour'`)
	require.NoError(t, err)
	ok, err = store.ClaimEvent(ctx, "review:y", "taker")
	require.NoError(t, err)
	require.True(t, ok)

	require.NoError(t, store.FinishEvent(ctx, "review:y", "slow", false))
	ok, err = store.ClaimEvent(ctx, "review:y", "third")
	require.NoError(t, err)
	assert.False(t, ok, "the taker still holds it")
}

// Two copies of one review racing each other: one applies.
func TestPostgresStore_ConcurrentClaimsOfOneEventHaveOneWinner(t *testing.T) {
	f := newFixture(t)
	store := githubhook.NewPostgresStore(f.database)
	const racers = 8
	var wg sync.WaitGroup
	start := make(chan struct{})
	var mu sync.Mutex
	winners := 0
	for i := range racers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			ok, err := store.ClaimEvent(f.a.ctx, "review:z", fmt.Sprintf("d%d", i))
			assert.NoError(t, err)
			if ok {
				mu.Lock()
				winners++
				mu.Unlock()
			}
		}()
	}
	close(start)
	wg.Wait()
	assert.Equal(t, 1, winners)
}

func TestReceiveApp_RefusesMissingOrOversizedHeadersOnceSigned(t *testing.T) {
	f := newFixture(t)
	keyring := f.useApp()
	f.connectApp(keyring, f.a, appInstallation, "app-secret")
	body := withInstallation(`{}`, appInstallation)

	for name, d := range map[string]appDelivery{
		"no event":           {event: "", id: "d", signWith: "app-secret", body: body},
		"an oversized id":    {event: "ping", id: strings.Repeat("x", 101), signWith: "app-secret", body: body},
		"an oversized event": {event: strings.Repeat("x", 101), id: "d", signWith: "app-secret", body: body},
	} {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, http.StatusBadRequest, f.deliverApp(d).Code)
		})
	}
	assert.Zero(t, f.appRecords(f.a))
}
