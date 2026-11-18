package intake_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-auth/authn/authntest"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/intake"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// The board's side over the wire, as a signed-in member.

func humaMux(resolve func(context.Context, string) (*authn.User, error), svc *intake.Service, tickets *ticket.Service) http.Handler {
	mux := http.NewServeMux()
	api := humago.New(mux, huma.DefaultConfig("test", "0.0.0"))
	api.UseMiddleware(authn.UserMiddleware(api, resolve, authntest.Guard(), slog.New(slog.DiscardHandler)))
	intake.RegisterRoutes(api, intake.NewHandler(svc, slog.DiscardHandler))
	ticket.RegisterRoutes(api, ticket.NewHandler(tickets, slog.DiscardHandler))
	return mux
}

func (f *fixture) boardMux(who *authn.User) http.Handler {
	return humaMux(func(context.Context, string) (*authn.User, error) { return who, nil }, f.svc, f.tickets)
}

func callBoard(t *testing.T, mux http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer test")
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func createBody(mods map[string]any) string {
	m := map[string]any{"name": "desk", "adapter": "native", "trust": "untrusted", "auth": "bearer"}
	for k, v := range mods {
		m[k] = v
	}
	raw, err := json.Marshal(m)
	if err != nil {
		panic(err)
	}
	return string(raw)
}

func decodeCredential(t *testing.T, rec *httptest.ResponseRecorder) intake.IntakeCredentialBody {
	t.Helper()
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var c intake.IntakeCredentialResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &c.Body))
	return c.Body
}

func decodeList(t *testing.T, rec *httptest.ResponseRecorder) intake.IntakeSourcesResponse {
	t.Helper()
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var l intake.IntakeSourcesResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &l.Body))
	return l
}

func TestCreateSource_BearerAnswersATokenOnceThatTheReceiverAccepts(t *testing.T) {
	f := newFixture(t)
	mux := f.boardMux(f.a.who())

	rec := callBoard(t, mux, http.MethodPost, "/api/intake/sources", createBody(nil))
	created := decodeCredential(t, rec)
	assert.True(t, strings.HasPrefix(created.Token, "shi_"), created.Token)
	assert.Empty(t, created.Secret)
	assert.Empty(t, created.Header)
	assert.Equal(t, "http://intake.example/hooks/intake/"+created.Source.ID.String(), created.Source.URL)
	require.NotNil(t, created.Source.CreatedBy)
	assert.Equal(t, f.a.member, *created.Source.CreatedBy)

	sent := f.send((&source{id: created.Source.ID, token: created.Token}).post(event("open", "k1", nil)))
	assert.Equal(t, http.StatusOK, sent.Code, sent.Body.String())

	listed := callBoard(t, mux, http.MethodGet, "/api/intake/sources", "")
	assert.NotContains(t, listed.Body.String(), created.Token)
	assert.NotContains(t, listed.Body.String(), `"token"`)
	assert.NotContains(t, listed.Body.String(), `"secret"`)
	l := decodeList(t, listed)
	require.Len(t, l.Body.Sources, 1)
	assert.Equal(t, created.Source.ID, l.Body.Sources[0].ID)
	assert.NotNil(t, l.Body.Sources[0].LastDeliveryAt)
}

func TestCreateSource_HMACAnswersASecretAndTheHeaderToSignIn(t *testing.T) {
	f := newFixture(t)
	rec := callBoard(t, f.boardMux(f.a.who()), http.MethodPost, "/api/intake/sources", createBody(map[string]any{"auth": "hmac"}))
	created := decodeCredential(t, rec)
	assert.Empty(t, created.Token)
	assert.NotEmpty(t, created.Secret)
	assert.Equal(t, intake.SignatureHeader, created.Header)
	assert.Equal(t, "hmac", created.Source.Auth)
}

func TestCreateSource_NeedsARecentSignIn(t *testing.T) {
	f := newFixture(t)
	for name, authTime := range map[string]time.Time{
		"signed in long ago":   time.Now().Add(-time.Hour),
		"no auth time on file": {},
	} {
		t.Run(name, func(t *testing.T) {
			who := f.a.who()
			who.AuthTime = authTime
			rec := callBoard(t, f.boardMux(who), http.MethodPost, "/api/intake/sources", createBody(nil))
			assert.Equal(t, http.StatusForbidden, rec.Code)
			assert.Contains(t, rec.Body.String(), `"code":"reauth_required"`)
		})
	}
	assert.Empty(t, mustList(t, f))
}

func TestRotateSource_NeedsARecentSignIn(t *testing.T) {
	f := newFixture(t)
	s := f.source(f.a, spec())
	who := f.a.who()
	who.AuthTime = time.Now().Add(-time.Hour)

	rec := callBoard(t, f.boardMux(who), http.MethodPost, "/api/intake/sources/"+s.id.String()+"/rotate", "")
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), `"code":"reauth_required"`)
	assert.Equal(t, http.StatusOK, f.send(s.post(event("open", "k", nil))).Code, "the old token still works")
}

func TestCreateSource_RefusesBadSources(t *testing.T) {
	f := newFixture(t)
	mux := f.boardMux(f.a.who())
	for name, mods := range map[string]map[string]any{
		"an untrusted source defaulting to open": {"default_status": "open"},
		"an unknown adapter":                     {"adapter": "zendesk"},
	} {
		t.Run(name, func(t *testing.T) {
			rec := callBoard(t, mux, http.MethodPost, "/api/intake/sources", createBody(mods))
			assert.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
		})
	}
	assert.Empty(t, mustList(t, f))
}

func TestCreateSource_AnAlertmanagerSourceCanBeRegistered(t *testing.T) {
	f := newFixture(t)
	rec := callBoard(t, f.boardMux(f.a.who()), http.MethodPost, "/api/intake/sources", createBody(map[string]any{"adapter": "alertmanager", "trust": "trusted"}))
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}

func TestCreateSource_ShutDoor(t *testing.T) {
	f := newFixture(t)
	f.svc = intake.NewService(intake.NewPostgresStore(f.database), f.tickets, &intake.Config{}, nil, slog.DiscardHandler)
	mux := f.boardMux(f.a.who())

	rec := callBoard(t, mux, http.MethodPost, "/api/intake/sources", createBody(nil))
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.Contains(t, rec.Body.String(), `"code":"intake_disabled"`)

	l := decodeList(t, callBoard(t, mux, http.MethodGet, "/api/intake/sources", ""))
	assert.False(t, l.Body.Enabled)
	assert.False(t, l.Body.HMAC)
}

func TestListSources_ShowsTheTenantsSourcesDeliveriesAndFlags(t *testing.T) {
	f := newFixture(t)
	revoked := f.source(f.a, spec(func(s *intake.Spec) { s.Name = "old" }))
	live := f.source(f.a, spec())
	other := f.source(f.b, spec())
	require.NoError(t, f.svc.Revoke(f.a.ctx, f.a.who(), revoked.id))
	require.Equal(t, http.StatusOK, f.send(live.post(event("open", "k1", nil))).Code)
	require.Equal(t, http.StatusOK, f.send(other.post(event("open", "k2", nil))).Code)

	l := decodeList(t, callBoard(t, f.boardMux(f.a.who()), http.MethodGet, "/api/intake/sources", ""))
	assert.True(t, l.Body.Enabled)
	assert.True(t, l.Body.HMAC)
	ids := map[uuid.UUID]*time.Time{}
	for _, s := range l.Body.Sources {
		ids[s.ID] = s.RevokedAt
	}
	require.Len(t, ids, 2)
	assert.NotNil(t, ids[revoked.id], "revoked sources stay listed")
	assert.Nil(t, ids[live.id])
	assert.NotContains(t, ids, other.id)

	require.Len(t, l.Body.Deliveries, 1)
	d := l.Body.Deliveries[0]
	assert.Equal(t, live.id, d.SourceID)
	assert.Equal(t, "k1", d.Key)
	assert.Equal(t, "applied", d.Outcome)
	assert.NotNil(t, d.TicketID)
}

func TestListSources_HMACIsOfferedOnlyWithAKey(t *testing.T) {
	f := newFixture(t)
	f.svc = intake.NewService(intake.NewPostgresStore(f.database), f.tickets,
		&intake.Config{Enabled: true, PublicBaseURL: "http://intake.example"}, nil, slog.DiscardHandler)
	mux := f.boardMux(f.a.who())

	l := decodeList(t, callBoard(t, mux, http.MethodGet, "/api/intake/sources", ""))
	assert.True(t, l.Body.Enabled)
	assert.False(t, l.Body.HMAC)
	rec := callBoard(t, mux, http.MethodPost, "/api/intake/sources", createBody(map[string]any{"auth": "hmac"}))
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
}

func TestRotateSource_AnswersANewTokenAndEndsTheOldOne(t *testing.T) {
	f := newFixture(t)
	s := f.source(f.a, spec())
	mux := f.boardMux(f.a.who())

	rotated := decodeCredential(t, callBoard(t, mux, http.MethodPost, "/api/intake/sources/"+s.id.String()+"/rotate", ""))
	assert.True(t, strings.HasPrefix(rotated.Token, "shi_"))
	assert.NotEqual(t, s.token, rotated.Token)
	assert.Equal(t, s.id, rotated.Source.ID)

	assert.Equal(t, http.StatusUnauthorized, f.send(s.post(event("open", "k", nil))).Code, "old token")
	fresh := &source{id: s.id, token: rotated.Token}
	assert.Equal(t, http.StatusOK, f.send(fresh.post(event("open", "k", nil))).Code, "new token")
}

func TestSources_EachTenantRotatesAndRevokesOnlyItsOwn(t *testing.T) {
	f := newFixture(t)
	a := f.source(f.a, spec())
	bMux := f.boardMux(f.b.who())

	rec := callBoard(t, bMux, http.MethodPost, "/api/intake/sources/"+a.id.String()+"/rotate", "")
	assert.Equal(t, http.StatusNotFound, rec.Code)
	rec = callBoard(t, bMux, http.MethodDelete, "/api/intake/sources/"+a.id.String(), "")
	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Equal(t, http.StatusOK, f.send(a.post(event("open", "k", nil))).Code, "b changed nothing of a's")

	assert.Equal(t, http.StatusNotFound,
		callBoard(t, bMux, http.MethodDelete, "/api/intake/sources/"+uuid.NewString(), "").Code, "unknown id")
}

func TestRevokeSource_StopsTheSourceAtTheReceiver(t *testing.T) {
	f := newFixture(t)
	s := f.source(f.a, spec())
	mux := f.boardMux(f.a.who())

	rec := callBoard(t, mux, http.MethodDelete, "/api/intake/sources/"+s.id.String(), "")
	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.Equal(t, http.StatusUnauthorized, f.send(s.post(event("open", "k", nil))).Code)

	l := decodeList(t, callBoard(t, mux, http.MethodGet, "/api/intake/sources", ""))
	require.Len(t, l.Body.Sources, 1)
	assert.NotNil(t, l.Body.Sources[0].RevokedAt, "still listed, as revoked")
}

// Agents may not manage sources: there is no agent route to them.
func TestSources_NeedASignedInUser(t *testing.T) {
	f := newFixture(t)
	mux := humaMux(func(context.Context, string) (*authn.User, error) { return nil, authn.ErrNoUser }, f.svc, f.tickets)

	for _, target := range []struct{ method, path string }{
		{http.MethodGet, "/api/intake/sources"},
		{http.MethodPost, "/api/intake/sources"},
		{http.MethodPost, "/api/intake/sources/" + uuid.NewString() + "/rotate"},
		{http.MethodDelete, "/api/intake/sources/" + uuid.NewString()},
	} {
		body := ""
		if target.method == http.MethodPost && !strings.HasSuffix(target.path, "/rotate") {
			body = createBody(nil)
		}
		rec := callBoard(t, mux, target.method, target.path, body)
		assert.Equal(t, http.StatusUnauthorized, rec.Code, "%s %s", target.method, target.path)
	}
	assert.Empty(t, mustList(t, f))
}

func TestListSources_NewestFirst(t *testing.T) {
	f := newFixture(t)
	older := f.source(f.a, spec(func(s *intake.Spec) { s.Name = "older" }))
	newer := f.source(f.a, spec(func(s *intake.Spec) { s.Name = "newer" }))
	require.Equal(t, http.StatusOK, f.send(older.post(event("open", "first", nil))).Code)
	require.Equal(t, http.StatusOK, f.send(older.post(event("open", "second", nil))).Code)
	_, err := f.database.Exec(f.a.ctx, `UPDATE intake_sources SET created_at = now() - interval '1 hour' WHERE id = $1`, older.id)
	require.NoError(t, err)
	_, err = f.database.Exec(f.a.ctx, `UPDATE intake_deliveries SET received_at = now() - interval '1 hour' WHERE key = 'first'`)
	require.NoError(t, err)

	l := decodeList(t, callBoard(t, f.boardMux(f.a.who()), http.MethodGet, "/api/intake/sources", ""))
	require.Len(t, l.Body.Sources, 2)
	assert.Equal(t, newer.id, l.Body.Sources[0].ID)
	assert.Equal(t, older.id, l.Body.Sources[1].ID)
	require.Len(t, l.Body.Deliveries, 2)
	assert.Equal(t, "second", l.Body.Deliveries[0].Key)
	assert.Equal(t, "first", l.Body.Deliveries[1].Key)
}

// What the board's detail page reads: the ticket says it is external, and the
// sender's words come from their own route, by event, as the sender's.
func TestTicketRoutes_ServeWhatTheSourceSentAsExternal(t *testing.T) {
	f := newFixture(t)
	s := f.source(f.a, spec())
	require.Equal(t, http.StatusOK, f.send(s.post(event("open", "k", map[string]any{"body": "details"}))).Code)
	require.Equal(t, http.StatusOK, f.send(s.post(event("resolve", "k", map[string]any{"body": "fixed"}))).Code)
	id := f.ticketFor(s, "k").ID
	mux := f.boardMux(f.a.who())

	rec := callBoard(t, mux, http.MethodGet, "/api/tickets/"+strconv.FormatInt(id, 10), "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var got struct {
		Title    string `json:"title"`
		External *struct {
			SourceID string `json:"source_id"`
			Key      string `json:"key"`
			Title    string `json:"title"`
		} `json:"external"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.Equal(t, "[desk] external report", got.Title)
	require.NotNil(t, got.External)
	assert.Equal(t, s.id.String(), got.External.SourceID)
	assert.Equal(t, "k", got.External.Key)
	assert.Equal(t, "Printer on fire", got.External.Title)

	rec = callBoard(t, mux, http.MethodGet, "/api/tickets/"+strconv.FormatInt(id, 10)+"/external", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var events struct {
		Events []struct {
			Event      string `json:"event"`
			Body       string `json:"body"`
			SourceName string `json:"source_name"`
		} `json:"events"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &events))
	require.Len(t, events.Events, 2)
	assert.Equal(t, "open", events.Events[0].Event)
	assert.Equal(t, "details", events.Events[0].Body)
	assert.Equal(t, "resolve", events.Events[1].Event)
	assert.Equal(t, "desk", events.Events[1].SourceName)

	assert.Equal(t, http.StatusNotFound, callBoard(t, mux, http.MethodGet, "/api/tickets/99999/external", "").Code)
	other := f.boardMux(f.b.who())
	assert.Equal(t, http.StatusNotFound, callBoard(t, other, http.MethodGet, "/api/tickets/"+strconv.FormatInt(id, 10)+"/external", "").Code,
		"another tenant's ticket's external content is not served")
}

// A source outlives the account of the person who set it up, without their id.
func TestListSources_ASourceOfADeletedPersonIsFlaggedAndNamesNoId(t *testing.T) {
	f := newFixture(t)
	f.source(f.a, spec())
	liveList := decodeList(t, callBoard(t, f.boardMux(f.a.who()), http.MethodGet, "/api/intake/sources", ""))
	require.Len(t, liveList.Body.Sources, 1)
	require.NotNil(t, liveList.Body.Sources[0].CreatedBy)
	assert.Equal(t, f.a.member, *liveList.Body.Sources[0].CreatedBy)
	assert.False(t, liveList.Body.Sources[0].CreatedByDeleted)
	_, err := f.database.Global().Exec(context.Background(), `DELETE FROM global_users WHERE id = $1`, f.a.member)
	require.NoError(t, err)

	rec := callBoard(t, f.boardMux(f.a.who()), http.MethodGet, "/api/intake/sources", "")
	assert.NotContains(t, rec.Body.String(), f.a.member.String())
	l := decodeList(t, rec)
	require.Len(t, l.Body.Sources, 1)
	assert.Nil(t, l.Body.Sources[0].CreatedBy)
	assert.True(t, l.Body.Sources[0].CreatedByDeleted)
}
