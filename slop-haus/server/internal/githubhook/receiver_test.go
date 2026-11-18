package githubhook_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-auth/login"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/agent"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost/codehosttest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost/github"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/githubhook"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/secretbox"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/tenantstate"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

// signature is X-Hub-Signature-256 as GitHub computes it with secret.
func signature(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// Deliveries end to end against the real store and the real ticket rules:
// a signed request in, a ticket moved (or not), and a record of which.

const (
	prURL = "https://github.com/acme/widgets/pull/12"
	// reviewerGitHubID is the member's linked GitHub account; botGitHubID
	// is an agent's bot account, which nobody links.
	reviewerGitHubID = int64(1001)
	botGitHubID      = int64(2002)
)

type tenantFixture struct {
	id      uuid.UUID
	ctx     context.Context
	member  uuid.UUID
	tickets *ticket.Service
	agents  *agent.Service
}

type fixture struct {
	t        *testing.T
	database *db.DB
	keyring  *secretbox.Keyring
	svc      *githubhook.Service
	handler  http.Handler
	a        *tenantFixture
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	return newFixtureWithHost(t, nil)
}

// newTickets is a ticket service that understands GitHub's links, which is
// what a ticket's pr_url holds whichever Host reads the delivery.
func newTickets(database *db.DB) *ticket.Service {
	hosts := codehost.NewRegistry()
	hosts.Register(github.New(), nil)
	return ticket.NewService(ticket.NewPostgresStore(database), slog.DiscardHandler, ticket.WithCodeHosts(hosts))
}

// newService is a webhook service, with a Host that reads nothing unless
// the test gave one: for a door the test never delivers to.
func newService(t *testing.T, store githubhook.Store, tickets githubhook.Tickets, cfg *githubhook.Config) *githubhook.Service {
	t.Helper()
	if cfg.Host == nil {
		cfg.Host = &codehosttest.Fake{Domain: "git.example.test"}
	}
	svc, err := githubhook.NewService(store, tickets, cfg, slog.DiscardHandler)
	require.NoError(t, err)
	return svc
}

// newFixtureWithHost is newFixture with the delivery's payloads read by
// host; nil is GitHub's.
func newFixtureWithHost(t *testing.T, host codehost.Host) *fixture {
	t.Helper()
	database := dbtest.WithDB(t)
	require.NoError(t, login.NewIssuerBindings(database.Global()).Register(context.Background(), login.GitHubProviderName, login.GitHubIssuer))
	keyring, err := secretbox.NewKeyring([]byte(strings.Repeat("c", 32)), nil)
	require.NoError(t, err)
	tickets := newTickets(database)
	if host == nil {
		host = github.New()
	}
	svc := newService(t, githubhook.NewPostgresStore(database), tickets,
		&githubhook.Config{AppSecrets: openerFromKeyring{keyring}, Host: host, Archive: tenantstate.NewChecker(database)})
	f := &fixture{t: t, database: database, keyring: keyring, svc: svc,
		handler: githubhook.NewReceiver(svc, slog.DiscardHandler).Handler()}
	f.a = f.newTenant()
	f.linkGitHub(f.a.member, reviewerGitHubID)
	return f
}

// newTenant is a tenant with one active member.
func (f *fixture) newTenant() *tenantFixture {
	f.t.Helper()
	id := dbtest.NewTenant(f.t, f.database)
	member := f.newMember(id)
	tickets := newTickets(f.database)
	return &tenantFixture{
		id: id, ctx: db.WithTenant(context.Background(), id), member: member, tickets: tickets,
		agents: agent.NewService(agent.NewPostgresStore(f.database), tickets, nil, slog.DiscardHandler),
	}
}

func (f *fixture) newMember(tenant uuid.UUID) uuid.UUID {
	f.t.Helper()
	user := dbtest.NewUser(f.t, f.database)
	f.exec(`UPDATE global_users SET is_active = true WHERE id = $1`, user)
	f.exec(`INSERT INTO global_tenant_memberships (id, tenant, user_id) VALUES ($1, $2, $3)`, uuid.New(), tenant, user)
	return user
}

func (f *fixture) linkGitHub(user uuid.UUID, githubID int64) {
	f.t.Helper()
	f.exec(`INSERT INTO global_user_identities (provider, subject, user_id, login) VALUES ('github', $1, $2, $3)`,
		fmt.Sprint(githubID), user, fmt.Sprintf("user-%d", githubID))
}

func (f *fixture) exec(sql string, args ...any) {
	f.t.Helper()
	_, err := f.database.Global().Exec(context.Background(), sql, args...)
	require.NoError(f.t, err)
}

// installation is a tenant's connected GitHub App: what GitHub names on
// its deliveries, and the secret it signs them with.
type installation struct {
	id     int64
	secret string
}

var nextInstallation atomic.Int64

// install connects a GitHub App to tenant, as the credential service does.
func (f *fixture) install(tenant *tenantFixture) installation {
	f.t.Helper()
	app := installation{id: nextInstallation.Add(1) + 1000, secret: "secret-" + uuid.NewString()}
	f.connectApp(f.keyring, tenant, app.id, app.secret)
	return app
}

// inReview puts a new ticket of tenant's into review with prURL.
func (f *fixture) inReview(tenant *tenantFixture, pr string) *ticket.Ticket {
	f.t.Helper()
	a, err := tenant.agents.Create(tenant.ctx, "builder-"+uuid.NewString()[:8])
	require.NoError(f.t, err)
	created, err := tenant.tickets.Create(tenant.ctx, &ticket.CreateParams{Title: "Widgets", RequiresApproval: true})
	require.NoError(f.t, err)
	_, err = tenant.tickets.Claim(tenant.ctx, a.ID, &created.ID)
	require.NoError(f.t, err)
	reviewed, err := tenant.tickets.Review(tenant.ctx, a.ID, &ticket.ReviewParams{PRURL: pr, Handoff: "PR open."})
	require.NoError(f.t, err)
	return reviewed
}

type delivery struct {
	app   installation
	event string
	id    string
	body  string
	// secret overrides the installation's own; contentType overrides JSON.
	secret      string
	contentType string
}

func (f *fixture) deliver(d delivery) *httptest.ResponseRecorder {
	f.t.Helper()
	if d.id == "" {
		d.id = uuid.NewString()
	}
	if d.secret == "" {
		d.secret = d.app.secret
	}
	if d.contentType == "" {
		d.contentType = "application/json"
	}
	body := withInstallation(d.body, d.app.id)
	req := httptest.NewRequest(http.MethodPost, "/hooks/github-app", strings.NewReader(body))
	req.Header.Set("X-GitHub-Event", d.event)
	req.Header.Set("X-GitHub-Delivery", d.id)
	req.Header.Set("X-Hub-Signature-256", signature(d.secret, []byte(body)))
	req.Header.Set("Content-Type", d.contentType)
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	return rec
}

func reviewBody(state string, githubID int64, submittedAt time.Time, body string) string {
	b, _ := json.Marshal(map[string]any{
		"action": "submitted",
		"review": map[string]any{
			"state": state, "body": body, "html_url": prURL + "#pullrequestreview-9",
			"submitted_at": submittedAt.UTC().Format(time.RFC3339),
			"user":         map[string]any{"id": githubID, "login": fmt.Sprintf("user-%d", githubID)},
		},
		"pull_request": map[string]any{"html_url": prURL},
	})
	return string(b)
}

func mergedBody(githubID int64) string {
	b, _ := json.Marshal(map[string]any{
		"action": "closed",
		"pull_request": map[string]any{
			"html_url": prURL, "merged": true,
			"merged_by": map[string]any{"id": githubID, "login": fmt.Sprintf("user-%d", githubID)},
		},
	})
	return string(b)
}

func (f *fixture) status(tenant *tenantFixture, id int64) ticket.Status {
	f.t.Helper()
	got, err := tenant.tickets.Get(tenant.ctx, id)
	require.NoError(f.t, err)
	return got.Status
}

func (f *fixture) deliveries(tenant *tenantFixture) []githubhook.Delivery {
	f.t.Helper()
	got, err := f.svc.Deliveries(tenant.ctx)
	require.NoError(f.t, err)
	return got
}

func TestReceive_AnApprovalByALinkedMemberApprovesTheTicket(t *testing.T) {
	f := newFixture(t)
	ep := f.install(f.a)
	reviewed := f.inReview(f.a, prURL)

	rec := f.deliver(delivery{app: ep, event: "pull_request_review",
		body: reviewBody("approved", reviewerGitHubID, time.Now(), "LGTM")})

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, fmt.Sprintf("applied: #%d approved\n", reviewed.ID), rec.Body.String())
	got, err := f.a.tickets.Get(f.a.ctx, reviewed.ID)
	require.NoError(t, err)
	assert.Equal(t, model.ReviewApproved, got.ReviewOutcome())
	require.NotNil(t, got.ApprovedByUserID)
	assert.Equal(t, f.a.member, *got.ApprovedByUserID, "attributed to the member whose GitHub account it was")

	ds := f.deliveries(f.a)
	require.Len(t, ds, 1)
	assert.Equal(t, githubhook.OutcomeApplied, ds[0].Outcome)
	assert.Equal(t, "submitted", ds[0].Action)
	require.NotNil(t, ds[0].TicketID)
	assert.Equal(t, reviewed.ID, *ds[0].TicketID)
}

// GitHub's own state spelling in a webhook is lower case; the API's is
// upper. Either counts.
func TestReceive_ReadsTheReviewStateInEitherCase(t *testing.T) {
	f := newFixture(t)
	ep := f.install(f.a)
	reviewed := f.inReview(f.a, prURL)

	rec := f.deliver(delivery{app: ep, event: "pull_request_review",
		body: reviewBody("APPROVED", reviewerGitHubID, time.Now(), "")})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, ticket.StatusOpen, f.status(f.a, reviewed.ID))
}

// The agents' bot account -- or anyone who has not linked their GitHub
// account -- approves nothing.
func TestReceive_IgnoresAReviewerWhoIsNotALinkedMember(t *testing.T) {
	f := newFixture(t)
	ep := f.install(f.a)
	reviewed := f.inReview(f.a, prURL)
	// Linked, but by a member of another tenant.
	b := f.newTenant()
	f.linkGitHub(b.member, 3003)
	// A member, linked, but whose account was never activated.
	inactive := dbtest.NewUser(t, f.database)
	f.exec(`INSERT INTO global_tenant_memberships (id, tenant, user_id) VALUES ($1, $2, $3)`, uuid.New(), f.a.id, inactive)
	f.linkGitHub(inactive, 4004)

	for _, id := range []int64{botGitHubID, 3003, 4004} {
		rec := f.deliver(delivery{app: ep, event: "pull_request_review",
			body: reviewBody("approved", id, time.Now(), "")})
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		assert.Contains(t, rec.Body.String(), fmt.Sprintf("ignored: the reviewer, GitHub user user-%d (id %d), is not a member here", id, id))
	}
	assert.Equal(t, ticket.StatusReview, f.status(f.a, reviewed.ID))
}

func TestReceive_ChangesRequestedSendsTheTicketBackWithTheReview(t *testing.T) {
	f := newFixture(t)
	ep := f.install(f.a)
	reviewed := f.inReview(f.a, prURL)

	rec := f.deliver(delivery{app: ep, event: "pull_request_review",
		body: reviewBody("changes_requested", reviewerGitHubID, time.Now(), "Split it up.")})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "sent back for changes")

	comments, err := f.a.tickets.ListComments(f.a.ctx, reviewed.ID)
	require.NoError(t, err)
	require.Len(t, comments, 1)
	assert.Equal(t, "Split it up.\n\nChanges requested on GitHub: "+prURL+"#pullrequestreview-9", comments[0].Body)
	require.NotNil(t, comments[0].AuthorUserID)
	assert.Equal(t, f.a.member, *comments[0].AuthorUserID)
}

// A review of only inline comments has no body; the comment still points
// at it.
func TestReceive_ChangesRequestedWithNoBodyStillLinksTheReview(t *testing.T) {
	f := newFixture(t)
	ep := f.install(f.a)
	reviewed := f.inReview(f.a, prURL)

	rec := f.deliver(delivery{app: ep, event: "pull_request_review",
		body: reviewBody("changes_requested", reviewerGitHubID, time.Now(), "")})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	comments, err := f.a.tickets.ListComments(f.a.ctx, reviewed.ID)
	require.NoError(t, err)
	require.Len(t, comments, 1)
	assert.Equal(t, "Changes requested on GitHub: "+prURL+"#pullrequestreview-9", comments[0].Body)
}

func TestReceive_AStaleReviewIsIgnored(t *testing.T) {
	f := newFixture(t)
	ep := f.install(f.a)
	reviewed := f.inReview(f.a, prURL)

	rec := f.deliver(delivery{app: ep, event: "pull_request_review",
		body: reviewBody("approved", reviewerGitHubID, time.Now().Add(-time.Hour), "")})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "ignored: the review predates the ticket's current review")
	assert.Equal(t, ticket.StatusReview, f.status(f.a, reviewed.ID))
}

func TestReceive_AMergeByAMemberClosesTheTicket(t *testing.T) {
	f := newFixture(t)
	ep := f.install(f.a)
	reviewed := f.inReview(f.a, prURL)

	rec := f.deliver(delivery{app: ep, event: "pull_request", body: mergedBody(reviewerGitHubID)})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, fmt.Sprintf("applied: #%d closed as merged\n", reviewed.ID), rec.Body.String())
	assert.Equal(t, ticket.StatusDone, f.status(f.a, reviewed.ID))
}

// The bot merging after approval is the agent at work: its ticket_done
// closes the ticket.
func TestReceive_AMergeByTheBotIsIgnored(t *testing.T) {
	f := newFixture(t)
	ep := f.install(f.a)
	reviewed := f.inReview(f.a, prURL)

	rec := f.deliver(delivery{app: ep, event: "pull_request", body: mergedBody(botGitHubID)})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "ignored: the merger")
	assert.Equal(t, ticket.StatusReview, f.status(f.a, reviewed.ID))
}

func TestReceive_IgnoresWhatItDoesNotActOn(t *testing.T) {
	f := newFixture(t)
	ep := f.install(f.a)
	reviewed := f.inReview(f.a, prURL)
	closedUnmerged, _ := json.Marshal(map[string]any{"action": "closed", "pull_request": map[string]any{"html_url": prURL, "merged": false}})
	edited := strings.Replace(reviewBody("approved", reviewerGitHubID, time.Now(), ""), `"submitted"`, `"edited"`, 1)

	for name, d := range map[string]delivery{
		"a push":                {event: "push", body: `{}`},
		"a comment review":      {event: "pull_request_review", body: reviewBody("commented", reviewerGitHubID, time.Now(), "")},
		"an edited review":      {event: "pull_request_review", body: edited},
		"a closed unmerged PR":  {event: "pull_request", body: string(closedUnmerged)},
		"a malformed payload":   {event: "pull_request_review", body: `{"review": 7}`},
		"a form-encoded body":   {event: "pull_request_review", body: "{}", contentType: "application/x-www-form-urlencoded"},
		"another repo's review": {event: "pull_request_review", body: strings.ReplaceAll(reviewBody("approved", reviewerGitHubID, time.Now(), ""), "acme/widgets", "acme/other")},
	} {
		t.Run(name, func(t *testing.T) {
			d.app = ep
			rec := f.deliver(d)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			assert.True(t, strings.HasPrefix(rec.Body.String(), "ignored: "), rec.Body.String())
		})
	}
	assert.Equal(t, ticket.StatusReview, f.status(f.a, reviewed.ID))
}

func TestReceive_APingSaysTheWebhookIsReachable(t *testing.T) {
	f := newFixture(t)
	ep := f.install(f.a)

	rec := f.deliver(delivery{app: ep, event: "ping", body: `{"zen":"Design for failure.","hook_id":1}`})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "applied: ping: GitHub reached this webhook\n", rec.Body.String())
}

// GitHub redelivers on request, and may deliver twice: the second copy of
// a delivery does nothing, even where the ticket rules would let it.
func TestReceive_ADuplicateDeliveryIsHandledOnce(t *testing.T) {
	f := newFixture(t)
	ep := f.install(f.a)
	reviewed := f.inReview(f.a, prURL)
	d := delivery{app: ep, event: "pull_request_review", id: "fixed-delivery-id",
		body: reviewBody("changes_requested", reviewerGitHubID, time.Now(), "Again.")}

	require.Contains(t, f.deliver(d).Body.String(), "applied")
	// Back into review, so a second request for changes would apply.
	review := ticket.StatusReview
	_, err := f.a.tickets.Update(f.a.ctx, reviewed.ID, &ticket.UpdateParams{Status: &review})
	require.NoError(t, err)

	rec := f.deliver(d)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "duplicate: already handled\n", rec.Body.String())
	comments, err := f.a.tickets.ListComments(f.a.ctx, reviewed.ID)
	require.NoError(t, err)
	assert.Len(t, comments, 1)
	assert.Len(t, f.deliveries(f.a), 1)
}

// What gets past the signature check decides nothing: every refusal before
// it is the same answer, and none of them records anything.
func TestReceive_RefusesAnythingNotSignedForTheInstallation(t *testing.T) {
	f := newFixture(t)
	ep := f.install(f.a)
	other := f.install(f.newTenant())
	f.inReview(f.a, prURL)
	body := reviewBody("approved", reviewerGitHubID, time.Now(), "")

	for name, d := range map[string]delivery{
		"a bad signature":         {app: ep, secret: "not-the-secret"},
		"another tenant's secret": {app: ep, secret: other.secret},
		"the key itself":          {app: ep, secret: "c" + strings.Repeat("c", 31)},
	} {
		t.Run(name, func(t *testing.T) {
			d.event, d.body = "pull_request_review", body
			rec := f.deliver(d)
			assert.Equal(t, http.StatusUnauthorized, rec.Code)
			assert.Equal(t, "githubhook: bad signature\n", rec.Body.String())
		})
	}

	assert.Empty(t, f.deliveries(f.a))
	assert.Equal(t, ticket.StatusReview, f.status(f.a, 1))
}

// A delivery for one tenant's installation acts on that tenant's tickets
// only, whatever PR it names -- several tenants may share a repo.
func TestReceive_OnlyEverTouchesTheInstallationsTenant(t *testing.T) {
	f := newFixture(t)
	b := f.newTenant()
	f.linkGitHub(b.member, 5005)
	aTicket := f.inReview(f.a, prURL)
	bTicket := f.inReview(b, prURL)
	bApp := f.install(b)

	rec := f.deliver(delivery{app: bApp, event: "pull_request_review",
		body: reviewBody("approved", 5005, time.Now(), "")})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "applied")
	assert.Equal(t, ticket.StatusOpen, f.status(b, bTicket.ID))
	assert.Equal(t, ticket.StatusReview, f.status(f.a, aTicket.ID), "a's ticket on the same PR is a's business")

	// a's reviewer approves nothing through b's installation.
	rec = f.deliver(delivery{app: bApp, event: "pull_request_review",
		body: reviewBody("approved", reviewerGitHubID, time.Now(), "")})
	assert.Contains(t, rec.Body.String(), "ignored: the reviewer")
	assert.Equal(t, ticket.StatusReview, f.status(f.a, aTicket.ID))
	assert.Empty(t, f.deliveries(f.a), "and the deliveries are b's")
}

func TestReceive_RefusesAnOversizedDelivery(t *testing.T) {
	f := newFixture(t)
	ep := f.install(f.a)

	rec := f.deliver(delivery{app: ep, event: "ping", body: `{"x":"` + strings.Repeat("a", 1<<20) + `"}`})
	assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
	assert.Empty(t, f.deliveries(f.a))
}

func TestReceive_RefusesASignedDeliveryWithoutGitHubsHeaders(t *testing.T) {
	f := newFixture(t)
	ep := f.install(f.a)

	rec := f.deliver(delivery{app: ep, event: "", body: `{}`})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	rec = f.deliver(delivery{app: ep, event: "ping", id: strings.Repeat("d", 101), body: `{}`})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Empty(t, f.deliveries(f.a))
}

// The listener serves the receiver and nothing else.
func TestReceiver_ServesNothingElse(t *testing.T) {
	f := newFixture(t)
	for _, target := range []struct{ method, path string }{
		{http.MethodGet, "/hooks/github-app"},
		{http.MethodPost, "/hooks/github/" + uuid.NewString()},
		{http.MethodGet, "/api/tickets"},
		{http.MethodPost, "/hooks/stop"},
		{http.MethodGet, "/healthz"},
		{http.MethodPost, "/hooks/github/"},
	} {
		rec := httptest.NewRecorder()
		f.handler.ServeHTTP(rec, httptest.NewRequest(target.method, target.path, nil))
		assert.Contains(t, []int{http.StatusNotFound, http.StatusMethodNotAllowed}, rec.Code, "%s %s", target.method, target.path)
	}
}

// With no App secrets every delivery is refused.
func TestAShutDoor(t *testing.T) {
	f := newFixture(t)
	app := f.install(f.a)
	shut := newService(t, githubhook.NewPostgresStore(f.database), nil, &githubhook.Config{})
	assert.False(t, shut.Enabled())

	body := withInstallation(`{}`, app.id)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/hooks/github-app", strings.NewReader(body))
	req.Header.Set("X-Hub-Signature-256", signature(app.secret, []byte(body)))
	req.Header.Set("X-GitHub-Event", "ping")
	req.Header.Set("X-GitHub-Delivery", "d")
	githubhook.NewReceiver(shut, slog.DiscardHandler).Handler().ServeHTTP(rec, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}
