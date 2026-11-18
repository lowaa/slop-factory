package consent_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-auth/authn"
	"github.com/lowaa/slop-factory/go-privacy/consent"
)

const testUserHeader = "X-Test-User"

// testAuthn stands in for the app's UserMiddleware: it puts the user named
// by a header on the context, the way the real one does.
func testAuthn(ctx huma.Context, next func(huma.Context)) {
	if id := ctx.Header(testUserHeader); id != "" {
		next(huma.WithContext(ctx, authn.NewUserContext(ctx.Context(),
			&authn.User{ID: uuid.MustParse(id), SessionID: "session-a"})))
		return
	}
	next(ctx)
}

type server struct {
	f   *fixture
	mux *http.ServeMux
	log *bytes.Buffer
}

func newServer(t *testing.T, store consent.Store) *server {
	t.Helper()
	return newServerWithHook(t, store, nil)
}

func newServerWithHook(t *testing.T, store consent.Store, hook consent.AfterDecide) *server {
	t.Helper()
	f := newFixture(t)
	logs := &bytes.Buffer{}
	logHandler := slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})
	svc := f.svc
	if store != nil {
		reg, err := consent.NewRegistry(
			consent.Purpose{ID: "analytics", Kind: consent.CookieCategory, Document: "cookie_policy"},
			consent.Purpose{ID: "marketing", Kind: consent.CookieCategory, Document: "cookie_policy", SaleOrShare: true},
		)
		require.NoError(t, err)
		svc, err = consent.NewService(&consent.Config{Registry: reg, Documents: f.docs, Store: store, Cookie: f.codec, Clock: f.clock.Now, Logger: logHandler})
		require.NoError(t, err)
	}
	h := consent.NewHandler(&consent.HandlerConfig{Service: svc, User: consent.AuthnUser, AfterDecide: hook, Logger: logHandler})

	mux := http.NewServeMux()
	api := humago.New(mux, huma.DefaultConfig("test", "1"))
	policy := authn.NewPolicy()
	api.UseMiddleware(policy.DefaultDeny(api, logHandler), testAuthn, h.Middleware())
	require.NoError(t, h.RegisterRoutes(api, "", &consent.Operations{Read: authn.Public(), Write: authn.Public()}))
	huma.Register(api, huma.Operation{OperationID: "probe", Method: http.MethodGet, Path: "/probe", Metadata: authn.Public()},
		func(ctx context.Context, _ *struct{}) (*struct {
			Body struct {
				Analytics bool `json:"analytics"`
				Marketing bool `json:"marketing"`
			}
		}, error) {
			out := &struct {
				Body struct {
					Analytics bool `json:"analytics"`
					Marketing bool `json:"marketing"`
				}
			}{}
			e := consent.FromContext(ctx)
			out.Body.Analytics, out.Body.Marketing = e.Granted("analytics"), e.Granted("marketing")
			return out, nil
		})
	return &server{f: f, mux: mux, log: logs}
}

type call struct {
	method, path string
	body         any
	headers      map[string]string
	cookies      []*http.Cookie
}

func (s *server) do(t *testing.T, c call) *httptest.ResponseRecorder {
	t.Helper()
	var body *bytes.Reader
	if c.body != nil {
		b, err := json.Marshal(c.body)
		require.NoError(t, err)
		body = bytes.NewReader(b)
	} else {
		body = bytes.NewReader(nil)
	}
	if c.path == "" {
		c.path = consent.DefaultPath
	}
	r := httptest.NewRequest(c.method, c.path, body)
	if c.body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	for k, v := range c.headers {
		r.Header.Set(k, v)
	}
	for _, ck := range c.cookies {
		r.AddCookie(ck)
	}
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, r)
	return w
}

func decodeState(t *testing.T, w *httptest.ResponseRecorder) *consent.StateBody {
	t.Helper()
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var b consent.StateBody
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &b))
	return &b
}

func (s *server) putAnon(t *testing.T, headers map[string]string, decisions ...map[string]any) *httptest.ResponseRecorder {
	return s.do(t, call{method: "PUT", headers: headers, body: map[string]any{"source": "banner", "decisions": decisions}})
}

func dec(purpose string, granted bool) map[string]any {
	return map[string]any{"purpose": purpose, "granted": granted, "document_version": "v1", "based_on": 0}
}

func setCookie(t *testing.T, w *httptest.ResponseRecorder, name string) *http.Cookie {
	t.Helper()
	for _, c := range w.Result().Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

func TestRoutesAreMarkedForTheAppsPolicy(t *testing.T) {
	s := newServer(t, nil)
	// DefaultDeny answers 500 to an operation without exactly one marker, so
	// a 200 on each route shows it carries the one the app gave.
	assert.Equal(t, http.StatusOK, s.do(t, call{method: "GET"}).Code)
	assert.Equal(t, http.StatusOK, s.putAnon(t, nil, dec("analytics", true)).Code)

	h := consent.NewHandler(&consent.HandlerConfig{Service: s.f.svc, User: consent.AuthnUser})
	api := humago.New(http.NewServeMux(), huma.DefaultConfig("t", "1"))
	assert.Error(t, h.RegisterRoutes(api, "", &consent.Operations{Read: authn.Public()}), "a route can't be mounted without a marker")
	assert.Error(t, h.RegisterRoutes(api, "", nil))
}

func TestAnonymousVisitorFlow(t *testing.T) {
	s := newServer(t, nil)

	first := s.do(t, call{method: "GET"})
	assert.Empty(t, first.Header().Values("Set-Cookie"), "a visit stores nothing")
	assert.Equal(t, "no-store", first.Header().Get("Cache-Control"))
	state := decodeState(t, first)
	assert.False(t, state.SignedIn)
	var ids []string
	for _, p := range state.Purposes {
		ids = append(ids, p.ID)
	}
	assert.Equal(t, []string{"necessary", "analytics", "interaction_recording", "marketing"}, ids, "no processing purpose for a visitor")
	assert.Equal(t, []string{"analytics", "interaction_recording", "marketing"}, state.NeedsDecision)

	w := s.putAnon(t, nil, dec("analytics", true))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	ck := setCookie(t, w, "app_consent")
	require.NotNil(t, ck)
	assert.True(t, ck.HttpOnly)
	assert.True(t, ck.Secure)
	assert.Equal(t, http.SameSiteLaxMode, ck.SameSite)

	probe := func(cookies ...*http.Cookie) map[string]bool {
		w := s.do(t, call{method: "GET", path: "/probe", cookies: cookies})
		return decodeProbe(t, w)
	}
	assert.False(t, probe()["analytics"], "no cookie, no consent")
	assert.True(t, probe(ck)["analytics"], "the middleware carries the cookie's consent to any handler")

	forged := &http.Cookie{Name: "app_consent", Value: ck.Value[:len(ck.Value)-3] + "AAA"}
	assert.False(t, probe(forged)["analytics"], "a cookie that doesn't verify counts as none")

	again := decodeState(t, s.do(t, call{method: "GET", cookies: []*http.Cookie{ck}}))
	assert.Equal(t, []string{"interaction_recording", "marketing"}, again.NeedsDecision)
}

func TestDecisionRefusalsCarryACode(t *testing.T) {
	s := newServer(t, nil)
	code := func(w *httptest.ResponseRecorder) string {
		var p struct {
			Code string `json:"code"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &p))
		return p.Code
	}

	w := s.putAnon(t, map[string]string{"Sec-GPC": "1"}, dec("marketing", true))
	assert.Equal(t, http.StatusConflict, w.Code)
	assert.Equal(t, consent.CodeGPCActive, code(w))
	assert.Nil(t, setCookie(t, w, "app_consent"))

	w = s.putAnon(t, nil, dec("health_data_processing", true))
	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Equal(t, consent.CodeSignInRequired, code(w))

	w = s.putAnon(t, nil, map[string]any{"purpose": "analytics", "granted": true, "document_version": "v0", "based_on": 0})
	assert.Equal(t, http.StatusConflict, w.Code)
	assert.Equal(t, consent.CodeDocumentChanged, code(w))

	w = s.do(t, call{method: "PUT", body: map[string]any{"source": "gpc", "decisions": []any{dec("analytics", true)}}})
	assert.Equal(t, http.StatusUnprocessableEntity, w.Code, "gpc isn't a source a client can claim")
}

func TestSignedInFlowAndMerge(t *testing.T) {
	s := newServer(t, nil)
	u := newUser(t)
	asUser := map[string]string{testUserHeader: u.String()}

	// A visitor decides, then signs in: the first consent request merges.
	w := s.putAnon(t, nil, dec("analytics", true))
	anonCookie := setCookie(t, w, "app_consent")
	require.NotNil(t, anonCookie)

	w = s.do(t, call{method: "GET", headers: asUser, cookies: []*http.Cookie{anonCookie}})
	state := decodeState(t, w)
	assert.True(t, state.SignedIn)
	for _, p := range state.Purposes {
		if p.ID == "analytics" {
			assert.True(t, p.Granted)
			assert.NotZero(t, p.BasedOn)
		}
	}
	rewritten := setCookie(t, w, "app_consent")
	require.NotNil(t, rewritten, "the cookie is rewritten without its token")
	got, err := s.f.codec.Decode(rewritten.Value)
	require.NoError(t, err)
	assert.Empty(t, got.MergeToken)

	// A health-data consent is offered to a signed-in user, and a stale
	// write to it is refused over HTTP too.
	put := func(basedOn int64) *httptest.ResponseRecorder {
		return s.do(t, call{method: "PUT", headers: asUser, body: map[string]any{"source": "sign_up", "decisions": []any{
			map[string]any{"purpose": "health_data_processing", "granted": true, "document_version": "v1", "based_on": basedOn}}}})
	}
	assert.Equal(t, http.StatusOK, put(0).Code)
	assert.Equal(t, http.StatusConflict, put(0).Code)
}

func TestMiddlewareRecordsGPCOnceForASignedInBrowser(t *testing.T) {
	s := newServer(t, nil)
	u := newUser(t)
	asUser := func(extra map[string]string) map[string]string {
		h := map[string]string{testUserHeader: u.String()}
		for k, v := range extra {
			h[k] = v
		}
		return h
	}
	_, err := s.f.svc.Decide(ctx, user(u), in(consent.SourceBanner, grant("marketing", true)))
	require.NoError(t, err)

	probe := func(h map[string]string) map[string]bool {
		w := s.do(t, call{method: "GET", path: "/probe", headers: h})
		return decodeProbe(t, w)
	}
	history := func() int {
		hist, err := s.f.svc.History(ctx, u)
		require.NoError(t, err)
		return len(hist)
	}

	t.Run("only the value 1 is the signal", func(t *testing.T) {
		for _, v := range []string{"0", "true", ""} {
			assert.True(t, probe(asUser(map[string]string{"Sec-GPC": v}))["marketing"], v)
		}
		assert.Equal(t, 1, history())
	})
	t.Run("a script's request, with an Authorization header, never records one", func(t *testing.T) {
		assert.True(t, probe(asUser(map[string]string{"Sec-GPC": "1", "Authorization": "Bearer x"}))["marketing"])
		assert.Equal(t, 1, history())
	})
	t.Run("a script's request doesn't read the consent cookie either", func(t *testing.T) {
		put := s.putAnon(t, nil, dec("analytics", true))
		ck := setCookie(t, put, "app_consent")
		require.NotNil(t, ck)
		withCookie := func(h map[string]string) map[string]bool {
			w := s.do(t, call{method: "GET", path: "/probe", headers: h, cookies: []*http.Cookie{ck}})
			return decodeProbe(t, w)
		}
		assert.True(t, withCookie(nil)["analytics"], "a browser's cookie counts")
		assert.False(t, withCookie(map[string]string{"Authorization": "Bearer x"})["analytics"])
	})
	t.Run("a browser's does, once", func(t *testing.T) {
		for i := 0; i < 3; i++ {
			assert.False(t, probe(asUser(map[string]string{"Sec-GPC": "1"}))["marketing"])
		}
		hist, err := s.f.svc.History(ctx, u)
		require.NoError(t, err)
		require.Len(t, hist, 2)
		assert.Equal(t, consent.SourceGPC, hist[1].Source)
	})
}

// failingStore stands in for a database that is down. It returns what the
// test gives it.
type failingStore struct{ err error }

func (f *failingStore) Current(context.Context, uuid.UUID) (map[string]*consent.Event, error) {
	return nil, f.err
}
func (f *failingStore) History(context.Context, uuid.UUID) ([]*consent.Event, error) {
	return nil, f.err
}
func (f *failingStore) Apply(context.Context, uuid.UUID, func(map[string]*consent.Event) (*consent.Plan, error)) (*consent.Applied, error) {
	return nil, f.err
}

func TestAnUnavailableStoreFailsClosedWithoutLeaking(t *testing.T) {
	u := uuid.New()
	s := newServer(t, &failingStore{err: errors.New("boom for " + u.String())})
	headers := map[string]string{testUserHeader: u.String(), "Sec-GPC": "1"}

	w := s.do(t, call{method: "GET", path: "/probe", headers: headers})
	assert.Equal(t, map[string]bool{"analytics": false, "marketing": false}, decodeProbe(t, w), "nothing granted, and the request still served")

	w = s.do(t, call{method: "GET", headers: headers})
	assert.Equal(t, http.StatusInternalServerError, w.Code)
	assert.NotContains(t, w.Body.String(), "boom")
	assert.NotContains(t, w.Body.String(), u.String())

	w = s.do(t, call{method: "PUT", headers: headers, body: map[string]any{"source": "banner", "decisions": []any{dec("analytics", true)}}})
	assert.Equal(t, http.StatusInternalServerError, w.Code)
	assert.NotContains(t, w.Body.String(), u.String())

	logged := s.log.String()
	assert.Contains(t, logged, "consent unavailable", "the failure is logged")
	assert.NotContains(t, logged, u.String(), "but not with who it was for")
	assert.NotContains(t, logged, "boom")
}

func TestLogsCarryNoPersonalData(t *testing.T) {
	s := newServer(t, nil)
	u := newUser(t)
	w := s.putAnon(t, nil, dec("analytics", true))
	ck := setCookie(t, w, "app_consent")
	state, err := s.f.codec.Decode(ck.Value)
	require.NoError(t, err)

	for _, c := range []call{
		{method: "GET", headers: map[string]string{testUserHeader: u.String(), "Sec-GPC": "1"}, cookies: []*http.Cookie{ck}},
		{method: "PUT", headers: map[string]string{testUserHeader: u.String()}, body: map[string]any{"source": "banner", "decisions": []any{dec("marketing", true)}}},
		{method: "GET", path: "/probe", headers: map[string]string{testUserHeader: u.String()}, cookies: []*http.Cookie{{Name: "app_consent", Value: "forged"}}},
	} {
		s.do(t, c)
	}
	logged := s.log.String()
	for _, secret := range []string{u.String(), ck.Value, state.MergeToken} {
		assert.False(t, strings.Contains(logged, secret), "a log line carried %q", secret)
	}
}

func decodeProbe(t *testing.T, w *httptest.ResponseRecorder) map[string]bool {
	t.Helper()
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var out struct {
		Analytics bool `json:"analytics"`
		Marketing bool `json:"marketing"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &out))
	return map[string]bool{"analytics": out.Analytics, "marketing": out.Marketing}
}

func TestAfterDecideRunsForASignedInDecisionAndCanFailIt(t *testing.T) {
	var gotUser uuid.UUID
	var gotPurposes []string
	calls := 0
	var hookErr error
	s := newServerWithHook(t, nil, func(_ context.Context, userID uuid.UUID, in *consent.DecideInput, res *consent.Resolved) error {
		calls++
		gotUser = userID
		for _, d := range in.Decisions {
			gotPurposes = append(gotPurposes, d.Purpose)
		}
		assert.True(t, res.Effective.Granted("health_data_processing"), "the hook sees the decision in force")
		return hookErr
	})
	u := newUser(t)
	asUser := map[string]string{testUserHeader: u.String()}
	put := func(basedOn int64) *httptest.ResponseRecorder {
		return s.do(t, call{method: "PUT", headers: asUser, body: map[string]any{"source": "settings", "decisions": []any{
			map[string]any{"purpose": "health_data_processing", "granted": true, "document_version": "v1", "based_on": basedOn}}}})
	}

	assert.Equal(t, http.StatusOK, put(0).Code)
	assert.Equal(t, 1, calls)
	assert.Equal(t, u, gotUser)
	assert.Equal(t, []string{"health_data_processing"}, gotPurposes)

	// An anonymous decision and a refused one don't reach the hook.
	s.putAnon(t, nil, dec("analytics", true))
	assert.Equal(t, http.StatusConflict, put(0).Code)
	assert.Equal(t, 1, calls)

	hookErr = errors.New("boom")
	var basedOn int64
	for _, p := range decodeState(t, s.do(t, call{method: "GET", headers: asUser})).Purposes {
		if p.ID == "health_data_processing" {
			basedOn = p.BasedOn
		}
	}
	require.NotZero(t, basedOn)
	w := put(basedOn)
	assert.Equal(t, http.StatusInternalServerError, w.Code)
	assert.NotContains(t, w.Body.String(), "boom")
}
