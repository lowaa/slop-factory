package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	collogs "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/lowaa/slop-factory/go-auth/auth"
	"github.com/lowaa/slop-factory/go-auth/ratelimit"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/agent"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/config"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/githubhook"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/intake"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/oauth"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/oauth/oauthtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/user"
)

// These boot the api the way main does -- config.Load, then serve -- against
// a database of their own, and check it from the outside: the wiring in
// serve is the thing under test, not any one service.

const (
	bootJWTSecret = "a-jwt-secret-long-enough-to-be-accepted-0123456789"
	bootBuildTag  = "20260930T000000Z-deadbeef"
	// bootCredentialKey is 32 bytes of "c", base64.
	bootCredentialKey = "Y2NjY2NjY2NjY2NjY2NjY2NjY2NjY2NjY2NjY2NjY2M="
)

// booted is a running api, and what a test needs to reach it.
type booted struct {
	cfg      *config.Config
	ownerDSN string
	base     string
	appDB    *db.DB
	stop     context.CancelFunc
	stopped  chan error
}

type bootOptions struct {
	metrics  bool
	webhooks bool
	intake   bool
	// logsEndpoint is LOGS_OTLP_ENDPOINT: where the api's logs, and the
	// frontend's error reports it proxies, are exported.
	logsEndpoint string
	// closers is handed to serve, as run hands it the log flush; a fresh
	// stack when nil.
	closers *closers
	// logs receives the api's logs; discarded when nil.
	logs slog.Handler
	// changes stands in for the GitHub host's operations; the real ones
	// when nil.
	changes codehost.Changes
	// setupClaimGate keeps the claim's setup gate on. Without it a tenant that
	// has set up nothing may still claim, as the many tests that claim in a
	// bare tenant need.
	setupClaimGate bool
	// proposalClock is the clock proposals expire by; the wall clock when nil.
	proposalClock func() time.Time
	// resumeAffinity is RESUME_AFFINITY when set (zero value keeps the default).
	resumeAffinity string
	// credentialKey is CREDENTIAL_ENCRYPTION_KEY: the GitHub App's webhook
	// secrets can be opened only when it is set, so the webhook listener
	// starts only with it. A boot with webhooks gets bootCredentialKey unless
	// noCredentialKey says to leave it out.
	credentialKey   string
	noCredentialKey bool
	// attemptsAddOff sets ATTEMPTS_ADD_ENABLED=false; it is on unless set.
	attemptsAddOff bool
	// replays sets REPLAYS_ENABLED=true; it is off unless set.
	replays bool
}

// boot starts serve and waits until /healthz answers. The server is stopped
// by t.Cleanup if the test has not stopped it itself.
func boot(t *testing.T, opts *bootOptions) *booted {
	t.Helper()
	ownerDSN := dbtest.WithOwnerDSN(t)
	role, password := dbtest.AppCredentials()
	bound := map[string]net.Listener{}
	listen := bindAddr(t, bound)
	base := "http://" + listen
	args := []string{
		"--database-url=" + ownerDSN,
		"--app-db-role=" + role,
		"--app-db-password=" + password,
		"--listen-addr=" + listen,
		"--public-base-url=" + base,
		"--jwt-secret=" + bootJWTSecret,
		"--email-backend=log",
		"--build-tag=" + bootBuildTag,
		// Near-immediate so a prune is likely running when the test shuts
		// the server down, and so the shutdown has to wait for it.
		"--prune-interval=1ms",
		// Off: the contract tests release a ticket and claim it again at
		// once. The cooldown is tested where it lives, in internal/ticket.
		"--release-cooldown-min=0", "--release-cooldown-max=0",
	}
	if opts.metrics {
		args = append(args, "--metrics-listen-addr="+bindAddr(t, bound))
	}
	credentialKey := opts.credentialKey
	if opts.webhooks {
		args = append(args, "--webhook-listen-addr="+bindAddr(t, bound), "--webhook-public-base-url=http://127.0.0.1")
		if credentialKey == "" && !opts.noCredentialKey {
			credentialKey = bootCredentialKey
		}
	}
	if credentialKey != "" {
		args = append(args, "--credential-encryption-key="+credentialKey)
	}
	if opts.intake {
		args = append(args, "--intake-listen-addr="+bindAddr(t, bound), "--intake-public-base-url=http://127.0.0.1")
	}
	if opts.logsEndpoint != "" {
		args = append(args, "--logs-otlp-endpoint="+opts.logsEndpoint)
	}
	if opts.attemptsAddOff {
		args = append(args, "--attempts-add-enabled=false")
	}
	if opts.replays {
		args = append(args, "--replays-enabled=true")
	}
	if !opts.setupClaimGate {
		args = append(args, "--setup-claim-gate=false")
	}
	if opts.resumeAffinity != "" {
		args = append(args, "--resume-affinity="+opts.resumeAffinity)
	}
	cfg, err := config.Load(args)
	require.NoError(t, err)

	ctx, stop := context.WithCancel(context.Background())
	b := &booted{cfg: cfg, ownerDSN: ownerDSN, base: base, stop: stop, stopped: make(chan error, 1)}
	cl := opts.closers
	if cl == nil {
		cl = &closers{}
	}
	logs := opts.logs
	if logs == nil {
		logs = slog.DiscardHandler
	}
	seams := []serveOption{withBoundListeners(bound)}
	if opts.changes != nil {
		seams = append(seams, withChanges(opts.changes))
	}
	if opts.proposalClock != nil {
		seams = append(seams, withProposalClock(opts.proposalClock))
	}
	go func() { b.stopped <- serve(ctx, cfg, cl, logs, seams...) }()
	t.Cleanup(func() {
		stop()
		<-b.stopped
	})

	// Polled rather than signalled: serve says nothing when it is ready,
	// and adding a hook to say so would change what is under test.
	healthy := func() bool {
		resp, err := http.Get(base + "/healthz")
		if err != nil {
			return false
		}
		_ = resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}
	deadline := time.After(30 * time.Second)
	for !healthy() {
		select {
		case err := <-b.stopped:
			b.stopped <- err
			t.Fatalf("serve stopped before it was ready: %v", err)
		case <-deadline:
			t.Fatal("the api never became healthy")
		case <-time.After(20 * time.Millisecond):
		}
	}

	appDSN, err := db.WithCredentials(ownerDSN, role, password)
	require.NoError(t, err)
	b.appDB, err = db.New(context.Background(), appDSN)
	require.NoError(t, err)
	t.Cleanup(b.appDB.Close)
	return b
}

// bindAddr opens a loopback listener and records it in bound under its
// address, for serve to use. It is held open until serve takes it over, so
// no other process or test can be handed the port in between; the cleanup
// closes it should serve never get that far.
func bindAddr(t *testing.T, bound map[string]net.Listener) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	addr := ln.Addr().String()
	bound[addr] = ln
	return addr
}

type request struct {
	method string
	url    string
	token  string
	body   string
	header map[string]string
}

// response is an answer, read in full.
type response struct {
	code   int
	body   string
	header http.Header
}

func do(t *testing.T, r *request) *response {
	t.Helper()
	req, err := http.NewRequest(r.method, r.url, strings.NewReader(r.body))
	require.NoError(t, err)
	if r.body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if r.token != "" {
		req.Header.Set("Authorization", "Bearer "+r.token)
	}
	for k, v := range r.header {
		req.Header.Set(k, v)
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return &response{code: resp.StatusCode, body: string(body), header: resp.Header}
}

func status(t *testing.T, r *request) int {
	t.Helper()
	return do(t, r).code
}

// refused reports whether nothing is listening at addr.
func refused(addr string) bool {
	conn, err := net.Dial("tcp", addr)
	if err == nil {
		_ = conn.Close()
		return false
	}
	return errors.Is(err, syscall.ECONNREFUSED)
}

// signedInUser is a session access token for a new user in a tenant of
// their own, signed with the api's secret as sign-in would sign it.
func (b *booted) signedInUser(t *testing.T) (*authn.User, string) {
	t.Helper()
	who := &authn.User{ID: dbtest.NewUser(t, b.appDB), TenantID: dbtest.NewTenant(t, b.appDB)}
	token, err := auth.NewSigner([]byte(bootJWTSecret), b.cfg.AccessTokenTTL, b.cfg.RefreshTokenTTL).
		IssueAccessToken(who.ID, who.TenantID)
	require.NoError(t, err)
	return who, token
}

// agentToken runs the OAuth code flow a coding agent's client runs:
// registration, the authorize redirect and the token exchange go to the
// api over HTTP; the user's consent -- a board page behind a session and a
// CSRF token -- is recorded through the service directly.
func (b *booted) agentToken(t *testing.T) string {
	t.Helper()
	who, _ := b.signedInUser(t)
	return b.agentTokenFor(t, who, &oauth.Decision{Approve: true, AgentName: "booted-agent"})
}

// agentTokenFor is agentToken for the agent the given user consents to.
func (b *booted) agentTokenFor(t *testing.T, who *authn.User, decision *oauth.Decision) string {
	t.Helper()
	_, tokens := b.agentCredentialsFor(t, who, decision)
	return tokens.AccessToken
}

// agentCredentialsFor is agentTokenFor, keeping the OAuth client and the
// refresh token for a test that exchanges it.
func (b *booted) agentCredentialsFor(t *testing.T, who *authn.User, decision *oauth.Decision) (clientID string, tokens *oauth.TokenResponse) {
	t.Helper()
	const redirect = "http://127.0.0.1:33418/callback"
	resource := oauth.ResourceFor(b.cfg.PublicBaseURL)

	resp := do(t, &request{method: http.MethodPost, url: b.base + "/oauth/register",
		body: `{"client_name":"test","redirect_uris":["` + redirect + `"]}`})
	require.Equal(t, http.StatusCreated, resp.code, resp.body)
	var client struct {
		ClientID string `json:"client_id"`
	}
	require.NoError(t, json.Unmarshal([]byte(resp.body), &client))

	raw := make([]byte, 32)
	_, err := rand.Read(raw)
	require.NoError(t, err)
	verifier := base64.RawURLEncoding.EncodeToString(raw)
	sum := sha256.Sum256([]byte(verifier))
	authorize := url.Values{
		"response_type": {"code"}, "client_id": {client.ClientID}, "redirect_uri": {redirect},
		"code_challenge": {base64.RawURLEncoding.EncodeToString(sum[:])}, "code_challenge_method": {"S256"},
		"state": {"st"}, "resource": {resource},
	}
	resp = do(t, &request{method: http.MethodGet, url: b.base + "/oauth/authorize?" + authorize.Encode()})
	require.Equal(t, http.StatusFound, resp.code, resp.body)
	consent, err := url.Parse(resp.header.Get("Location"))
	require.NoError(t, err)
	requestID, err := uuid.Parse(consent.Query().Get("request"))
	require.NoError(t, err)

	back, err := b.consent().DecideAuthRequest(authn.NewUserContext(context.Background(), who), who, requestID,
		decision)
	require.NoError(t, err)
	callback, err := url.Parse(back)
	require.NoError(t, err)

	resp = do(t, &request{method: http.MethodPost, url: b.base + "/oauth/token", header: map[string]string{
		"Content-Type": "application/x-www-form-urlencoded",
	}, body: url.Values{
		"grant_type": {"authorization_code"}, "client_id": {client.ClientID}, "code": {callback.Query().Get("code")},
		"redirect_uri": {redirect}, "code_verifier": {verifier}, "resource": {resource},
	}.Encode()})
	require.Equal(t, http.StatusOK, resp.code, resp.body)
	tokens = &oauth.TokenResponse{}
	require.NoError(t, json.Unmarshal([]byte(resp.body), tokens))
	require.NotEmpty(t, tokens.AccessToken)
	return client.ClientID, tokens
}

// consent is an OAuth service over the api's database, standing in for
// the board's consent page.
func (b *booted) consent() *oauth.Service {
	grants := &oauthtest.DeferredGrants{}
	agents := agent.NewService(agent.NewPostgresStore(b.appDB), noLeases{}, grants, slog.DiscardHandler,
		agent.WithMembers(storeMembers{user.NewPostgresStore(b.appDB)}))
	generous := &ratelimit.Config{Limit: 1000, Window: time.Minute, MaxKeys: 1000}
	svc := oauth.NewService(oauth.NewPostgresStore(b.appDB), agents, &oauth.Config{
		Issuer: b.cfg.PublicBaseURL, CodeTTL: time.Minute,
		RegisterLimit: generous, DeviceLookupLimit: generous, DeviceStartLimit: generous,
	}, nil, slog.DiscardHandler)
	grants.OAuth = svc
	return svc
}

// storeMembers answers the membership question consent asks when a user picks
// a conductor.
type storeMembers struct{ store *user.PostgresStore }

func (m storeMembers) IsTenantMember(ctx context.Context, userID, tenantID uuid.UUID) (bool, error) {
	return m.store.IsMember(ctx, userID, tenantID)
}

type noLeases struct{}

func (noLeases) ReleaseLeasesFor(context.Context, uuid.UUID, string, time.Time) error { return nil }

func TestServe_HealthAndVersion(t *testing.T) {
	t.Parallel()
	b := boot(t, &bootOptions{})

	assert.Equal(t, http.StatusOK, status(t, &request{method: http.MethodGet, url: b.base + "/healthz"}))
	resp := do(t, &request{method: http.MethodGet, url: b.base + "/api/version"})
	assert.Equal(t, http.StatusOK, resp.code)
	assert.JSONEq(t, `{"version":"`+bootBuildTag+`"}`, resp.body)
}

// No credential, no route: every agent and user route is refused before
// its handler, and so is one with the other kind's credential.
func TestServe_RoutesRefuseTheUnauthenticated(t *testing.T) {
	t.Parallel()
	b := boot(t, &bootOptions{})
	_, userToken := b.signedInUser(t)
	agentToken := b.agentToken(t)

	for name, r := range map[string]*request{
		"user route, no credential":       {method: http.MethodGet, url: b.base + "/api/auth/me"},
		"user route, garbage token":       {method: http.MethodGet, url: b.base + "/api/auth/me", token: "garbage"},
		"agent route, no credential":      {method: http.MethodPost, url: b.base + "/api/claims", body: `{}`},
		"agent route, a user's token":     {method: http.MethodPost, url: b.base + "/api/claims", body: `{}`, token: userToken},
		"worker state, no credential":     {method: http.MethodGet, url: b.base + "/api/worker/state"},
		"worker state, a user's token":    {method: http.MethodGet, url: b.base + "/api/worker/state", token: userToken},
		"worker poll, no credential":      {method: http.MethodPost, url: b.base + "/api/worker/poll", body: `{}`},
		"worker poll, a user's token":     {method: http.MethodPost, url: b.base + "/api/worker/poll", body: `{}`, token: userToken},
		"board route, no credential":      {method: http.MethodGet, url: b.base + "/api/queue/blockage"},
		"board route, an agent's token":   {method: http.MethodGet, url: b.base + "/api/queue/blockage", token: agentToken},
		"mcp, no credential":              {method: http.MethodPost, url: b.base + "/mcp", body: mcpInitialize},
		"admin route, no credential":      {method: http.MethodGet, url: b.base + "/api/auth/admin-check"},
		"admin route, a non-admin's user": {method: http.MethodGet, url: b.base + "/api/auth/admin-check", token: userToken},
	} {
		got := status(t, r)
		assert.Contains(t, []int{http.StatusUnauthorized, http.StatusForbidden}, got, name)
	}
}

func TestServe_SignedInUserAndAgentRoutes(t *testing.T) {
	t.Parallel()
	b := boot(t, &bootOptions{})

	who, userToken := b.signedInUser(t)
	resp := do(t, &request{method: http.MethodGet, url: b.base + "/api/auth/me", token: userToken})
	require.Equal(t, http.StatusOK, resp.code, resp.body)
	assert.Contains(t, resp.body, who.ID.String())

	resp = do(t, &request{method: http.MethodGet, url: b.base + "/api/queue/blockage", token: userToken})
	require.Equal(t, http.StatusOK, resp.code, resp.body)
	assert.JSONEq(t, `{"blockage":null}`, resp.body)

	agentToken := b.agentToken(t)
	resp = do(t, &request{method: http.MethodPost, url: b.base + "/api/claims", body: `{}`, token: agentToken})
	assert.Equal(t, http.StatusOK, resp.code, resp.body)
}

const mcpInitialize = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"0"}}}`

// A session started on /mcp carries on at /mcp/: one handler behind both
// spellings, so one set of sessions.
func TestServe_MCPIsOneHandlerAtBothPaths(t *testing.T) {
	t.Parallel()
	b := boot(t, &bootOptions{})
	token := b.agentToken(t)
	mcp := func(path, sessionID, body string) *response {
		header := map[string]string{"Accept": "application/json, text/event-stream"}
		if sessionID != "" {
			header["Mcp-Session-Id"] = sessionID
		}
		return do(t, &request{method: http.MethodPost, url: b.base + path, token: token, body: body, header: header})
	}

	resp := mcp("/mcp", "", mcpInitialize)
	require.Equal(t, http.StatusOK, resp.code, resp.body)
	sessionID := resp.header.Get("Mcp-Session-Id")
	require.NotEmpty(t, sessionID)

	resp = mcp("/mcp/", sessionID, `{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	require.Equal(t, http.StatusAccepted, resp.code, resp.body)
	resp = mcp("/mcp/", sessionID, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	require.Equal(t, http.StatusOK, resp.code, resp.body)
	assert.Contains(t, resp.body, "ticket_next")
}

// The metrics and webhook listeners are started only when configured, and
// each serves only its own thing.
func TestServe_OptionalListeners(t *testing.T) {
	t.Parallel()
	t.Run("configured", func(t *testing.T) {
		b := boot(t, &bootOptions{metrics: true, webhooks: true, intake: true})

		metrics := do(t, &request{method: http.MethodGet, url: "http://" + b.cfg.MetricsListenAddr + "/metrics"})
		assert.Equal(t, http.StatusOK, metrics.code)
		assert.Contains(t, metrics.body, `slophaus_rate_limit_refusals_total{limiter="device_start"} 0`,
			"the metrics the limiters were built to report to")
		assert.Equal(t, http.StatusNotFound, status(t, &request{method: http.MethodGet, url: b.base + "/metrics"}),
			"the board's listener has no /metrics")

		// A delivery naming an installation nobody here connected reaches
		// the receiver and is ignored; anything else is not its route.
		who, _ := b.signedInUser(t)
		hookURL := "http://" + b.cfg.Webhook.ListenAddr
		app := do(t, &request{method: http.MethodPost, url: hookURL + githubhook.AppReceivePath, body: `{"installation":{"id":1}}`,
			header: map[string]string{"X-GitHub-Event": "ping", "X-GitHub-Delivery": uuid.NewString()}})
		assert.Equal(t, http.StatusOK, app.code)
		assert.Contains(t, app.body, "no GitHub App installation here")
		assert.Equal(t, http.StatusNotFound,
			status(t, &request{method: http.MethodPost, url: hookURL + "/hooks/github/" + uuid.NewString(), body: `{}`}),
			"the repo endpoints' route is gone")
		assert.Equal(t, http.StatusNotFound,
			status(t, &request{method: http.MethodGet, url: hookURL + "/healthz"}),
			"the webhook listener serves the receiver and nothing else")

		// The intake door is its own listener too: a source's bearer token
		// opens a ticket on it, and neither the board nor the GitHub door
		// answers the route.
		src := uuid.New()
		token, hash, err := intake.NewToken()
		require.NoError(t, err)
		_, err = intake.NewPostgresStore(b.appDB).CreateSource(db.WithTenant(context.Background(), who.TenantID), src,
			&intake.Spec{Name: "boot", Adapter: "native", Trust: intake.TrustUntrusted, AuthKind: intake.AuthBearer,
				DefaultStatus: ticket.StatusPreparing, DefaultPriority: model.PriorityNormal, MaxPriority: model.PriorityNormal,
				RoleMatch: model.RoleMatchAny, ReopenPolicy: intake.ReopenNew},
			&intake.NewCredential{Kind: intake.AuthBearer, TokenHash: hash}, who.ID)
		require.NoError(t, err)
		post := func(base, bearer string) int {
			return status(t, &request{method: http.MethodPost, url: base + "/hooks/intake/" + src.String(),
				body:   `{"key":"k","event":"open","title":"boot"}`,
				header: map[string]string{"Authorization": "Bearer " + bearer, "Content-Type": "application/json"}})
		}
		intakeBase := "http://" + b.cfg.Intake.ListenAddr
		assert.Equal(t, http.StatusUnauthorized, post(intakeBase, "wrong"))
		opened := do(t, &request{method: http.MethodPost, url: intakeBase + "/hooks/intake/" + src.String(),
			body:   `{"key":"k2","event":"open","title":"boot"}`,
			header: map[string]string{"Authorization": "Bearer " + token, "Content-Type": "application/json"}})
		assert.Equal(t, http.StatusOK, opened.code)
		assert.Contains(t, opened.body, "created as preparing")
		tickets, err := ticket.NewService(ticket.NewPostgresStore(b.appDB), slog.DiscardHandler).
			List(db.WithTenant(context.Background(), who.TenantID), &ticket.ListFilter{})
		require.NoError(t, err)
		require.Len(t, tickets, 1)
		require.NotNil(t, tickets[0].Intake)
		assert.Equal(t, "k2", tickets[0].Intake.Key)
		assert.Equal(t, http.StatusNotFound, post(b.base, token), "not on the board's listener")
		assert.Equal(t, http.StatusNotFound, post("http://"+b.cfg.Webhook.ListenAddr, token), "nor on the GitHub door's")
		assert.Equal(t, http.StatusNotFound,
			status(t, &request{method: http.MethodGet, url: intakeBase + "/healthz"}), "the intake listener serves the receiver and nothing else")
	})

	t.Run("webhooks without the credential key", func(t *testing.T) {
		b := boot(t, &bootOptions{webhooks: true, noCredentialKey: true})
		require.NotNil(t, b.cfg.Webhook)
		assert.True(t, refused(b.cfg.Webhook.ListenAddr), "the App's secrets cannot be opened, so nothing listens")
	})

	t.Run("not configured", func(t *testing.T) {
		b := boot(t, &bootOptions{})
		assert.Empty(t, b.cfg.MetricsListenAddr)
		assert.Nil(t, b.cfg.Webhook)
		assert.Nil(t, b.cfg.Intake)
		assert.Equal(t, http.StatusNotFound, status(t, &request{method: http.MethodGet, url: b.base + "/metrics"}))
		assert.Equal(t, http.StatusNotFound,
			status(t, &request{method: http.MethodPost, url: b.base + "/hooks/github/" + uuid.NewString(), body: `{}`}),
			"no receiver on the board's listener either")
	})
}

// waitStopped waits for serve to return after b.stop, and requires it to
// have returned cleanly.
func (b *booted) waitStopped(t *testing.T) {
	t.Helper()
	select {
	case err := <-b.stopped:
		b.stopped <- err
		require.NoError(t, err)
	case <-time.After(30 * time.Second):
		t.Fatal("serve did not return after its context ended")
	}
}

// Cancelling the context -- a SIGTERM, in main -- stops every listener,
// closes the database pool and returns cleanly. The order things are
// closed in is TestServe_ShutdownOrder's.
func TestServe_ShutsDownCleanlyWhenTheContextEnds(t *testing.T) {
	t.Parallel()
	b := boot(t, &bootOptions{metrics: true, webhooks: true})
	addrs := []string{b.cfg.ListenAddr, b.cfg.MetricsListenAddr, b.cfg.Webhook.ListenAddr}
	for _, addr := range addrs {
		require.False(t, refused(addr), addr)
	}

	b.stop()
	b.waitStopped(t)
	for _, addr := range addrs {
		assert.True(t, refused(addr), "%s is still listening", addr)
	}
	// Its database pool is closed: once this test's own pool is too, no
	// connection as the app role is left on the database. A closed
	// connection's backend exits after its client has gone, so that is
	// waited for rather than read once.
	b.appDB.Close()
	owner, err := db.New(context.Background(), b.ownerDSN)
	require.NoError(t, err)
	defer owner.Close()
	role, _ := dbtest.AppCredentials()
	assert.Eventually(t, func() bool {
		var n int
		err := owner.Global().QueryRow(context.Background(), `
			SELECT count(*) FROM pg_stat_activity WHERE datname = current_database() AND usename = $1`, role).Scan(&n)
		return err == nil && n == 0
	}, 10*time.Second, 20*time.Millisecond, "connections serve left open")
}

// What serve opened is closed only once the servers have stopped -- a
// request still in flight as the shutdown begins is answered, by a
// database that is still open -- and then in reverse: the jobs, stopped
// and waited for before the database they query is closed (see
// TestScheduler_StopWaitsForTheRunToReturn for the wait); the login
// providers and the outbox, so a sign-up in the last moments still gets its
// link; the database; and last what the caller pushed before calling serve,
// which in run is the log flush.
func TestServe_ShutdownOrder(t *testing.T) {
	t.Parallel()
	type closing struct {
		name string
		// Whether the request held open across the shutdown had been let
		// finish when this closer ran.
		inFlightDone bool
	}
	var (
		mu           sync.Mutex
		closed       []closing
		inFlightDone bool
	)
	cl := &closers{closing: func(name string) {
		mu.Lock()
		defer mu.Unlock()
		closed = append(closed, closing{name: name, inFlightDone: inFlightDone})
	}}
	cl.push("caller's", func() {})
	b := boot(t, &bootOptions{closers: cl})

	// A request held in flight: its headers are in and its handler is
	// reading the body, which the server's 100 Continue says. Only the
	// body still to come holds it open across the shutdown.
	const body = `{"client_name":"last-moment","redirect_uris":["http://127.0.0.1:1/cb"]}`
	conn, err := net.Dial("tcp", b.cfg.ListenAddr)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()
	_, err = fmt.Fprintf(conn, "POST /oauth/register HTTP/1.1\r\nHost: %s\r\nContent-Type: application/json\r\n"+
		"Content-Length: %d\r\nExpect: 100-continue\r\n\r\n", b.cfg.ListenAddr, len(body))
	require.NoError(t, err)
	replies := bufio.NewReader(conn)
	cont, err := http.ReadResponse(replies, nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusContinue, cont.StatusCode)

	b.stop()
	// The shutdown has begun once the listener refuses new connections;
	// only then is the held request let finish. It is marked done just
	// before its body goes, not once its answer is read: serve may close
	// everything between the server sending the answer and this test
	// reading it, and a mark set after the read would fail a correct serve.
	// So a closer that runs before the mark is caught every time, while
	// one that slipped into the moment between the mark and the answer
	// would not be -- only the 201 below speaks for that moment, and for
	// the database alone.
	require.Eventually(t, func() bool { return refused(b.cfg.ListenAddr) }, 10*time.Second, 5*time.Millisecond)
	mu.Lock()
	inFlightDone = true
	mu.Unlock()
	_, err = io.WriteString(conn, body)
	require.NoError(t, err)
	resp, err := http.ReadResponse(replies, nil)
	require.NoError(t, err)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusCreated, resp.StatusCode, "the request in flight is answered, with the database still open")

	b.waitStopped(t)
	mu.Lock()
	defer mu.Unlock()
	var names []string
	for _, c := range closed {
		names = append(names, c.name)
		assert.True(t, c.inFlightDone, "%s closed while a request was still in flight", c.name)
	}
	assert.Equal(t, []string{"landing runner", "approval hooks", "servermerge_reaper", "server merge", "webhook_reconcile", "closing_rule_reconciler", "steward_discussion_apply_due", "steward_apply_due", "cooldown_sweep", "prune", "promotion carries", "discussion hooks", "login providers", "outbox", "database", "caller's"}, names)
}

// The usage hook, end to end: an agent's real token reports a transcript,
// and the llm_* counters on the metrics listener count it once, however
// often the same totals are reported.
func TestServe_UsageHookCountsInTheMetrics(t *testing.T) {
	t.Parallel()
	b := boot(t, &bootOptions{metrics: true})
	token := b.agentToken(t)
	report := `{"transcript_key":"pi-1","harness":"pi","session_id":"pi-1","scope":"main","records":[{
		"schema_version":1,"provider":"anthropic","model":"claude-opus-5-5","harness":"pi","billing_mode":"subscription",
		"requests":3,"input_tokens":10,"output_tokens":5,"cache_read_tokens":0,"cache_write_tokens":0,"reasoning_tokens":0,
		"started_at":"2026-10-01T09:00:00Z","ended_at":"2026-10-01T09:05:00Z",
		"tools":[{"tool":"bash","command":"git status","calls":2,"errors":0,"duration_ms":5,"result_bytes":10}]}]}`
	for range 2 {
		resp := do(t, &request{method: http.MethodPost, url: b.base + "/hooks/usage", token: token, body: report})
		require.Equal(t, http.StatusOK, resp.code, resp.body)
		assert.JSONEq(t, `{"scope":"overhead"}`, resp.body)
	}

	metrics := do(t, &request{method: http.MethodGet, url: "http://" + b.cfg.MetricsListenAddr + "/metrics"})
	require.Equal(t, http.StatusOK, metrics.code)
	assert.Contains(t, metrics.body,
		`llm_requests_total{billing_mode="subscription",harness="pi",model="claude-opus-5-5",provider="anthropic"} 3`)
	assert.Contains(t, metrics.body, `llm_tool_calls_total{`)
	assert.Contains(t, metrics.body, `slophaus_rate_limit_refusals_total{limiter="llm_usage"} 0`)
}

// The prompt hook resets a safety counter, so it is the agent's route and
// nobody else's.
func TestServe_UserPromptSubmitHookNeedsAnAgentToken(t *testing.T) {
	t.Parallel()
	b := boot(t, &bootOptions{})
	url := b.base + "/hooks/user-prompt-submit"
	body := `{"session_id":"s1","cwd":"/w"}`

	anonymous := do(t, &request{method: http.MethodPost, url: url, body: body})
	assert.Equal(t, http.StatusUnauthorized, anonymous.code, anonymous.body)

	agent := do(t, &request{method: http.MethodPost, url: url, token: b.agentToken(t), body: body})
	assert.Equal(t, http.StatusOK, agent.code, agent.body)
	assert.Empty(t, agent.body)
}

// The frontend's error reports reach the log store through the api's own
// /api/otel route, forced to the frontend's service name, with only the
// operational attributes.
func TestServe_OtelProxyForwardsFrontendErrorReports(t *testing.T) {
	t.Parallel()
	received := make(chan string, 1)
	store := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		// VictoriaLogs takes OTLP protobuf only.
		var msg collogs.ExportLogsServiceRequest
		if r.Header.Get("Content-Type") != "application/x-protobuf" || proto.Unmarshal(b, &msg) != nil {
			received <- "not protobuf"
			return
		}
		received <- protojson.Format(&msg)
	}))
	defer store.Close()
	b := boot(t, &bootOptions{logsEndpoint: store.URL})

	report := `{"resourceLogs":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"slop-haus-api"}}]},` +
		`"scopeLogs":[{"logRecords":[{"severityNumber":17,"body":{"stringValue":"render error"},"attributes":[` +
		`{"key":"privacy.tier","value":{"stringValue":"operational"}},` +
		`{"key":"error.type","value":{"stringValue":"TypeError"}},` +
		`{"key":"form.title","value":{"stringValue":"a patient name"}}]}]}]}]}`
	resp := do(t, &request{method: http.MethodPost, url: b.base + "/api/otel/v1/logs", body: report,
		header: map[string]string{"Origin": b.base}})

	require.Equal(t, http.StatusNoContent, resp.code, resp.body)
	got := <-received
	assert.Contains(t, got, "slop-haus-frontend")
	assert.NotContains(t, got, "slop-haus-api")
	assert.Contains(t, got, "TypeError")
	assert.NotContains(t, got, "a patient name")
}

// onlyCode reduces an error body to its "code", so a test pins the machine-readable reason apart from huma's wording.
func onlyCode(t *testing.T, body string) string {
	t.Helper()
	var e struct {
		Code string `json:"code"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &e))
	out, err := json.Marshal(e)
	require.NoError(t, err)
	return string(out)
}
