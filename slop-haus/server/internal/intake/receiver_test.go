package intake_test

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/intake"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

// Who may knock, and what happens to a delivery that is not a good one.

func TestAuth_EveryRefusalLooksTheSame(t *testing.T) {
	f := newFixture(t)
	s := f.source(f.a, spec())
	revoked := f.source(f.a, spec(func(sp *intake.Spec) { sp.Name = "old" }))
	require.NoError(t, f.svc.Revoke(f.a.ctx, f.a.who(), revoked.id))
	body := event("open", "k", nil)

	cases := map[string]*request{
		"no credential":         s.post(body).without("Authorization"),
		"wrong token":           s.post(body).header("Authorization", "Bearer shi_nope"),
		"not a bearer scheme":   s.post(body).header("Authorization", "Basic "+s.token),
		"empty token":           s.post(body).header("Authorization", "Bearer "),
		"blank token":           s.post(body).header("Authorization", "Bearer    "),
		"a signature instead":   s.post(body).without("Authorization").header(intake.SignatureHeader, "sha256=00"),
		"another sources token": {path: "/hooks/intake/" + uuid.NewString(), body: "{}", contentType: "application/json", headers: map[string]string{"Authorization": "Bearer " + s.token}},
		"not a uuid":            {path: "/hooks/intake/nope", body: "{}", contentType: "application/json", headers: map[string]string{"Authorization": "Bearer " + s.token}},
		"revoked source":        revoked.post(body),
	}
	var wantBody string
	for name, r := range cases {
		rec := f.send(r)
		assert.Equal(t, http.StatusUnauthorized, rec.Code, name)
		if wantBody == "" {
			wantBody = rec.Body.String()
		}
		assert.Equal(t, wantBody, rec.Body.String(), "%s: one answer for every refusal, so ids cannot be probed", name)
	}
	assert.Empty(t, f.allTickets(f.a))
	assert.Empty(t, f.deliveries(f.a), "a delivery that did not authenticate is not recorded")
}

func TestAuth_TheBearerSchemeIsCaseInsensitiveAndSpaceTolerant(t *testing.T) {
	f := newFixture(t)
	s := f.source(f.a, spec())
	for i, header := range []string{"bearer " + s.token, "BEARER  " + s.token + " "} {
		rec := f.send(s.post(event("open", fmt.Sprintf("k%d", i), nil)).header("Authorization", header))
		assert.Equal(t, http.StatusOK, rec.Code, header)
	}
}

func TestAuth_OnlyTheSourcesOwnKindOfCredentialWorks(t *testing.T) {
	f := newFixture(t)
	bearer := f.source(f.a, spec())
	hmacSrc := f.source(f.a, spec(func(sp *intake.Spec) { sp.Name = "signed"; sp.AuthKind = intake.AuthHMAC }))
	require.NotEmpty(t, hmacSrc.secret)
	assert.Empty(t, hmacSrc.token)

	raw := `{"key":"k","event":"open","title":"t"}`
	signed := func(secret, body string) *request {
		r := hmacSrc.post(body).without("Authorization")
		return r.header(intake.SignatureHeader, intake.Sign(secret, []byte(body)))
	}

	assert.Equal(t, http.StatusOK, f.send(signed(hmacSrc.secret, raw)).Code)
	assert.Equal(t, http.StatusUnauthorized, f.send(signed("wrong-secret", raw)).Code, "bad HMAC")
	tampered := signed(hmacSrc.secret, raw)
	tampered.body = strings.Replace(raw, `"t"`, `"evil"`, 1)
	assert.Equal(t, http.StatusUnauthorized, f.send(tampered).Code, "a signature of other bytes")
	assert.Equal(t, http.StatusUnauthorized, f.send(hmacSrc.post(raw).header("Authorization", "Bearer "+bearer.token)).Code,
		"a bearer token is not a signature, even a real one")
	assert.Equal(t, http.StatusUnauthorized, f.send(hmacSrc.post(raw).without("Authorization")).Code, "no signature")

	// The reverse: a signature does not do for a bearer source.
	signedForBearer := bearer.post(raw).without("Authorization")
	signedForBearer.headers[intake.SignatureHeader] = intake.Sign(hmacSrc.secret, []byte(raw))
	assert.Equal(t, http.StatusUnauthorized, f.send(signedForBearer).Code)
}

func TestAuth_RotationEndsTheOldCredentialAtOnce(t *testing.T) {
	f := newFixture(t)
	s := f.source(f.a, spec())
	h := f.source(f.a, spec(func(sp *intake.Spec) { sp.Name = "signed"; sp.AuthKind = intake.AuthHMAC }))
	raw := `{"key":"k","event":"open","title":"t"}`

	rotated, err := f.svc.Rotate(f.a.ctx, f.a.who(), s.id)
	require.NoError(t, err)
	assert.Equal(t, http.StatusUnauthorized, f.send(s.post(raw)).Code, "the old token")
	s.token = rotated.Token
	assert.Equal(t, http.StatusOK, f.send(s.post(raw)).Code, "the new one")

	oldSecret := h.secret
	rotatedH, err := f.svc.Rotate(f.a.ctx, f.a.who(), h.id)
	require.NoError(t, err)
	assert.NotEqual(t, oldSecret, rotatedH.Secret)
	sign := func(secret string) *request {
		return h.post(raw).without("Authorization").header(intake.SignatureHeader, intake.Sign(secret, []byte(raw)))
	}
	assert.Equal(t, http.StatusUnauthorized, f.send(sign(oldSecret)).Code)
	assert.Equal(t, http.StatusOK, f.send(sign(rotatedH.Secret)).Code)
}

func TestAuth_ARevokedSourceStopsAtOnce(t *testing.T) {
	f := newFixture(t)
	s := f.source(f.a, spec())
	require.Equal(t, http.StatusOK, f.send(s.post(event("open", "k", nil))).Code)
	require.NoError(t, f.svc.Revoke(f.a.ctx, f.a.who(), s.id))
	assert.Equal(t, http.StatusUnauthorized, f.send(s.post(event("update", "k", nil))).Code)

	sources, err := f.svc.List(f.a.ctx)
	require.NoError(t, err)
	require.Len(t, sources, 1)
	assert.NotNil(t, sources[0].RevokedAt, "kept, so the board can still say whose it was")
	_, err = f.svc.Rotate(f.a.ctx, f.a.who(), s.id)
	assert.ErrorIs(t, err, intake.ErrNotFound, "a revoked source cannot be revived by rotating it")
}

func TestCredentials_AreNeitherStoredNorLogged(t *testing.T) {
	f := newFixture(t)
	s := f.source(f.a, spec())
	h := f.source(f.a, spec(func(sp *intake.Spec) { sp.Name = "signed"; sp.AuthKind = intake.AuthHMAC }))
	rotated, err := f.svc.Rotate(f.a.ctx, f.a.who(), s.id)
	require.NoError(t, err)
	f.send(s.post("{}"))
	f.send(s.post(event("open", "k", nil)).header("Authorization", "Bearer "+s.token+"x"))
	f.send(h.post("{}").without("Authorization").header(intake.SignatureHeader, "sha256=aa"))

	var stored int
	require.NoError(t, f.database.Global().QueryRow(context.Background(), `
		SELECT count(*) FROM global_intake_credentials
		 WHERE token_hash = convert_to($1, 'UTF8') OR token_hash = convert_to($2, 'UTF8')
		    OR encode(token_hash, 'escape') LIKE '%shi_%'`, s.token, rotated.Token).Scan(&stored))
	assert.Zero(t, stored, "only a hash is stored")
	var length int
	require.NoError(t, f.database.Global().QueryRow(context.Background(),
		`SELECT length(token_hash) FROM global_intake_credentials WHERE source_id = $1`, s.id).Scan(&length))
	assert.Equal(t, 32, length, "a SHA-256")
	var hmacHash *[]byte
	require.NoError(t, f.database.Global().QueryRow(context.Background(),
		`SELECT token_hash FROM global_intake_credentials WHERE source_id = $1`, h.id).Scan(&hmacHash))
	assert.Nil(t, hmacHash, "an HMAC secret is derived, so nothing about it is stored")

	logs := f.logs.String()
	assert.NotEmpty(t, logs)
	for _, secret := range []string{s.token, rotated.Token, h.secret} {
		assert.NotContains(t, logs, secret)
	}
	assert.NotContains(t, logs, "Bearer")
	assert.Contains(t, logs, "action=intake_source_created")
	assert.Contains(t, logs, "action=intake_source_rotated")
}

func TestReceive_RefusesAnOversizedDeliveryBeforeParsingIt(t *testing.T) {
	f := newFixture(t)
	s := f.source(f.a, spec())
	big := `{"key":"k","event":"open","title":"t","body":"` + strings.Repeat("x", intake.MaxRequestBody) + `"}`
	rec := f.send(s.post(big))
	assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
	assert.Empty(t, f.allTickets(f.a))

	// Not even a bad credential gets as far as being checked.
	rec = f.send(s.post(strings.Repeat("{", intake.MaxRequestBody+1)).header("Authorization", "Bearer nope"))
	assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
}

func TestReceive_RejectsAnAuthenticatedDeliveryThatIsNotAnEventAndRecordsIt(t *testing.T) {
	f := newFixture(t)
	s := f.source(f.a, spec())

	cases := map[string]*request{
		"malformed json": s.post("{nope"),
		"not an object":  s.post("[1,2]"),
		"unknown event":  s.post(event("close", "k", nil)),
		"no key":         s.post(map[string]any{"event": "open", "title": "t"}),
		"open no title":  s.post(map[string]any{"key": "k", "event": "open"}),
		"long title":     s.post(event("open", "k", map[string]any{"title": strings.Repeat("t", intake.MaxTitle+1)})),
		"long body":      s.post(event("open", "k", map[string]any{"body": strings.Repeat("b", intake.MaxBody+1)})),
		"big context":    s.post(event("open", "k", map[string]any{"context": map[string]any{"a": strings.Repeat("c", intake.MaxContext)}})),
		"bad link":       s.post(event("open", "k", map[string]any{"link": "javascript:alert(1)"})),
		"bad priority":   s.post(event("open", "k", map[string]any{"priority": "now"})),
		"not json":       {path: s.post("{}").path, body: "key=k", contentType: "application/x-www-form-urlencoded", headers: s.post("{}").headers},
	}
	for name, r := range cases {
		rec := f.send(r)
		assert.Equal(t, http.StatusBadRequest, rec.Code, "%s: %s", name, rec.Body.String())
	}
	assert.Empty(t, f.allTickets(f.a))
	ds := f.deliveries(f.a)
	require.Len(t, ds, len(cases)+0)
	for _, d := range ds {
		assert.Equal(t, intake.OutcomeRejected, d.Outcome)
		assert.NotEmpty(t, d.Detail)
	}
}

func TestReceive_AnEventRedeliveredIsHandledOnce(t *testing.T) {
	f := newFixture(t)
	s := f.source(f.a, spec())

	r := func() *request { return s.post(event("open", "k", nil)).header(intake.DeliveryHeader, "evt-1") }
	assert.Equal(t, http.StatusOK, f.send(r()).Code)
	rec := f.send(r())
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "duplicate")
	assert.Len(t, f.external(s, f.ticketFor(s, "k").ID), 1, "the second was not applied")
	assert.Len(t, f.deliveries(f.a), 1)
}

func TestReceive_WithoutADeliveryIdAnIdenticalBodyIsADuplicateOnlyForAWhile(t *testing.T) {
	f := newFixture(t)
	s := f.source(f.a, spec())
	body := event("update", "k", map[string]any{"title": "t", "body": "still firing"})

	assert.Equal(t, http.StatusOK, f.send(s.post(body)).Code)
	rec := f.send(s.post(body))
	assert.Contains(t, rec.Body.String(), "duplicate", "the sender's retry")
	f.advance(time.Hour)
	rec = f.send(s.post(body))
	assert.NotContains(t, rec.Body.String(), "duplicate", "a repeat notification an hour later is news again")
	assert.Len(t, f.external(s, f.ticketFor(s, "k").ID), 2)
}

type allowN struct{ n atomic.Int64 }

func (a *allowN) Allow(uuid.UUID) bool { return a.n.Add(-1) >= 0 }

func TestRateLimit_Gets429AndIsRecordedOncePerMinute(t *testing.T) {
	limiter := &allowN{}
	limiter.n.Store(1)
	f := newFixture(t, withLimiter(limiter))
	s := f.source(f.a, spec())

	assert.Equal(t, http.StatusOK, f.send(s.post(event("open", "k", nil))).Code)
	for i := 0; i < 3; i++ {
		rec := f.send(s.post(event("open", "k", nil)))
		assert.Equal(t, http.StatusTooManyRequests, rec.Code)
		assert.NotEmpty(t, rec.Header().Get("Retry-After"))
	}
	limited := func() []intake.Delivery {
		var out []intake.Delivery
		for _, d := range f.deliveries(f.a) {
			if d.Outcome == intake.OutcomeRateLimited {
				out = append(out, d)
			}
		}
		return out
	}
	require.Len(t, limited(), 1, "recording every refusal would let a flood write as fast as it can send")
	assert.Contains(t, limited()[0].Detail, "1 deliveries")
	assert.Contains(t, f.logs.String(), "action=intake_rate_limited")

	f.advance(2 * time.Minute)
	assert.Equal(t, http.StatusTooManyRequests, f.send(s.post(event("open", "k", nil))).Code)
	require.Len(t, limited(), 2)
	assert.Contains(t, limited()[0].Detail, "3 deliveries", "the ones refused in between are counted")
}

func TestRateLimit_AnUnauthenticatedRequestNeverSpendsASourcesBudget(t *testing.T) {
	limiter := &allowN{}
	limiter.n.Store(1)
	f := newFixture(t, withLimiter(limiter))
	s := f.source(f.a, spec())
	for i := 0; i < 5; i++ {
		assert.Equal(t, http.StatusUnauthorized, f.send(s.post("{}").header("Authorization", "Bearer nope")).Code)
	}
	assert.Equal(t, http.StatusOK, f.send(s.post(event("open", "k", nil))).Code)
}

func TestReceiver_ServesNothingElse(t *testing.T) {
	f := newFixture(t)
	s := f.source(f.a, spec())
	for _, tc := range []struct {
		method, path string
		want         int
	}{
		{http.MethodGet, "/hooks/intake/" + s.id.String(), http.StatusMethodNotAllowed},
		{http.MethodPost, "/", http.StatusNotFound},
		{http.MethodGet, "/api/tickets", http.StatusNotFound},
		{http.MethodPost, "/hooks/github/" + s.id.String(), http.StatusNotFound},
		{http.MethodGet, "/metrics", http.StatusNotFound},
	} {
		rec := httptest.NewRecorder()
		f.handler.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, nil))
		assert.Equal(t, tc.want, rec.Code, tc.method+" "+tc.path)
	}
}

func TestService_AShutDoorRefusesEverything(t *testing.T) {
	f := newFixture(t)
	s := f.source(f.a, spec())
	shut := intake.NewService(intake.NewPostgresStore(f.database), f.tickets, &intake.Config{}, nil, slog.DiscardHandler)
	_, err := shut.Receive(context.Background(), &intake.Incoming{SourceID: s.id.String(), Authorization: "Bearer " + s.token, Body: []byte("{}")})
	assert.ErrorIs(t, err, intake.ErrUnauthorized)
	_, err = shut.Create(f.a.ctx, f.a.who(), spec())
	assert.ErrorIs(t, err, intake.ErrDisabled)
}

func TestService_RegisteringASourceNeedsARecentSignIn(t *testing.T) {
	f := newFixture(t)
	stale := &authn.User{ID: f.a.member, TenantID: f.a.id}
	_, err := f.svc.Create(f.a.ctx, stale, spec())
	assert.Error(t, err)
	_, err = f.svc.Rotate(f.a.ctx, stale, uuid.New())
	assert.Error(t, err)
	assert.Empty(t, mustList(t, f))
}

func mustList(t *testing.T, f *fixture) []intake.Source {
	t.Helper()
	sources, err := f.svc.List(f.a.ctx)
	require.NoError(t, err)
	return sources
}

func TestService_RefusesBadSources(t *testing.T) {
	f := newFixture(t)
	noKey := intake.NewService(intake.NewPostgresStore(f.database), f.tickets,
		&intake.Config{Enabled: true}, nil, slog.DiscardHandler)
	_, err := noKey.Create(f.a.ctx, f.a.who(), spec(func(sp *intake.Spec) { sp.AuthKind = intake.AuthHMAC }))
	assert.ErrorIs(t, err, intake.ErrHMACDisabled)

	for name, mod := range map[string]func(*intake.Spec){
		"no name":                func(sp *intake.Spec) { sp.Name = "  " },
		"unknown adapter":        func(sp *intake.Spec) { sp.Adapter = "zendesk" },
		"unknown trust":          func(sp *intake.Spec) { sp.Trust = "kinda" },
		"unknown auth":           func(sp *intake.Spec) { sp.AuthKind = "none" },
		"default above max":      func(sp *intake.Spec) { sp.DefaultPriority, sp.MaxPriority = model.PriorityUrgent, model.PriorityLow },
		"untrusted default open": func(sp *intake.Spec) { sp.DefaultStatus = ticket.StatusOpen },
		"in_progress default":    func(sp *intake.Spec) { sp.Trust = intake.TrustTrusted; sp.DefaultStatus = ticket.StatusInProgress },
		"bad path":               func(sp *intake.Spec) { sp.Paths = []string{"../etc"} },
		"bad reopen policy":      func(sp *intake.Spec) { sp.ReopenPolicy = "sometimes" },
		"bad role match":         func(sp *intake.Spec) { sp.RoleMatch = "most" },
	} {
		_, err := f.svc.Create(f.a.ctx, f.a.who(), spec(mod))
		assert.Error(t, err, name)
	}

	f.source(f.a, spec())
	_, err = f.svc.Create(f.a.ctx, f.a.who(), spec())
	assert.ErrorIs(t, err, intake.ErrInvalidSource, "two live sources with one name")
	assert.Len(t, mustList(t, f), 1)
}

// A delivery handled twice because the first attempt was cut off between the
// ticket write and its record is applied once.
func TestReceive_AnAbandonedDeliveryAppliedBeforeItWasRecordedIsNotAppliedTwice(t *testing.T) {
	f := newFixture(t)
	s := f.source(f.a, spec())
	r := func() *request { return s.post(event("open", "k", nil)).header(intake.DeliveryHeader, "evt-1") }
	require.Equal(t, http.StatusOK, f.send(r()).Code)

	// As if the process died before FinishDelivery, long enough ago to be
	// taken as abandoned.
	_, err := f.database.Exec(f.a.ctx, `
		UPDATE intake_deliveries SET outcome = 'processing', detail = '', ticket_id = NULL,
		       received_at = now() - interval '1 hour'`)
	require.NoError(t, err)

	rec := f.send(r())
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "already")
	assert.Len(t, f.allTickets(f.a), 1)
	assert.Len(t, f.external(s, f.ticketFor(s, "k").ID), 1, "one event for one delivery")
	ds := f.deliveries(f.a)
	require.Len(t, ds, 1)
	assert.Equal(t, intake.OutcomeApplied, ds[0].Outcome)
	assert.NotNil(t, ds[0].TicketID)
}

func TestReceive_SimultaneousRedeliveriesApplyOnce(t *testing.T) {
	f := newFixture(t)
	s := f.source(f.a, spec())
	const copies = 8
	start := make(chan struct{})
	bodies := make(chan string, copies)
	var wg sync.WaitGroup
	for i := 0; i < copies; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			rec := f.send(s.post(event("open", "k", nil)).header(intake.DeliveryHeader, "same"))
			bodies <- rec.Body.String()
		}()
	}
	close(start)
	wg.Wait()
	close(bodies)

	applied, duplicates := 0, 0
	for b := range bodies {
		switch {
		case strings.HasPrefix(b, "duplicate"):
			duplicates++
		case strings.HasPrefix(b, "applied"):
			applied++
		default:
			t.Errorf("unexpected answer %q", b)
		}
	}
	assert.Equal(t, 1, applied)
	assert.Equal(t, copies-1, duplicates)
	assert.Len(t, f.allTickets(f.a), 1)
	assert.Len(t, f.external(s, f.ticketFor(s, "k").ID), 1)
	assert.Len(t, f.deliveries(f.a), 1)
}

func TestRateLimiter_GivesEachSourceItsOwnBurstAndNoMore(t *testing.T) {
	// A rate so slow that no token comes back during the test: only the
	// burst is spent.
	l := intake.NewRateLimiter(1, 2)
	a, b := uuid.New(), uuid.New()
	assert.True(t, l.Allow(a))
	assert.True(t, l.Allow(a))
	assert.False(t, l.Allow(a), "the burst is spent")
	assert.True(t, l.Allow(b), "another source's bucket is its own")
	assert.True(t, l.Allow(b))
	assert.False(t, l.Allow(b))
}

func TestService_RotatingAnHMACSourceNeedsTheKeyThatDerivesItsSecret(t *testing.T) {
	f := newFixture(t)
	h := f.source(f.a, spec(func(sp *intake.Spec) { sp.AuthKind = intake.AuthHMAC }))
	noKey := intake.NewService(intake.NewPostgresStore(f.database), f.tickets,
		&intake.Config{Enabled: true}, nil, slog.DiscardHandler)

	_, err := noKey.Rotate(f.a.ctx, f.a.who(), h.id)
	assert.ErrorIs(t, err, intake.ErrHMACDisabled)

	raw := `{"key":"k","event":"open","title":"t"}`
	signed := h.post(raw).without("Authorization").header(intake.SignatureHeader, intake.Sign(h.secret, []byte(raw)))
	assert.Equal(t, http.StatusOK, f.send(signed).Code, "the refused rotation left the secret as it was")
}

// What the tickets service reads back, which is what an agent is shown.
func TestDetail_CarriesWhatTheSourceSentForRendering(t *testing.T) {
	f := newFixture(t)
	s := f.source(f.a, spec())
	require.Equal(t, http.StatusOK, f.send(s.post(event("open", "k", map[string]any{"body": "details"}))).Code)
	id := f.ticketFor(s, "k").ID

	detail, err := f.tickets.Detail(s.tenant.ctx, id)
	require.NoError(t, err)
	require.NotNil(t, detail.Ticket.Intake)
	require.Len(t, detail.External, 1)
	rendered := ticket.RenderExternal(detail.External, nil)
	assert.Contains(t, rendered, "Printer on fire")
	assert.Contains(t, rendered, "details")
	assert.Contains(t, rendered, ticket.ExternalHeading+` from source "desk"`)

	plain, err := f.tickets.Create(s.tenant.ctx, &ticket.CreateParams{Title: "ordinary"})
	require.NoError(t, err)
	d2, err := f.tickets.Detail(s.tenant.ctx, plain.ID)
	require.NoError(t, err)
	assert.Empty(t, d2.External)
}
