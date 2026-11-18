package consent_test

import (
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
	"github.com/lowaa/slop-factory/go-auth/authn/authntest"
	"github.com/lowaa/slop-factory/go-db/dbtest"
	"github.com/lowaa/slop-factory/go-privacy/consent"
)

const sessionCookieName = "app_session"

// realAuthnServer mounts the consent routes behind go-auth's own
// UserMiddleware, with OptionalUser markers as an app does, so a credential
// is really verified and really rejected. A token "good:<user id>" verifies;
// any other does not.
type realAuthnServer struct {
	f   *fixture
	api huma.API
	mux *http.ServeMux
}

func newRealAuthnServer(t *testing.T) *realAuthnServer {
	t.Helper()
	f := newFixture(t)
	h := consent.NewHandler(&consent.HandlerConfig{Service: f.svc, User: consent.AuthnUser})
	mux := http.NewServeMux()
	api := humago.New(mux, huma.DefaultConfig("test", "1"))
	verify := func(_ context.Context, token string) (*authn.User, error) {
		id, ok := strings.CutPrefix(token, "good:")
		if !ok {
			return nil, errors.New("rejected")
		}
		uid, err := uuid.Parse(id)
		if err != nil {
			return nil, err
		}
		return &authn.User{ID: uid, SessionID: "session-a"}, nil
	}
	api.UseMiddleware(
		authn.NewPolicy().DefaultDeny(api, slog.DiscardHandler),
		authn.UserMiddleware(api, &authn.UserMiddlewareConfig{Verify: verify, Guard: authntest.Guard(), SessionCookie: sessionCookieName}),
		h.Middleware(),
	)
	require.NoError(t, h.RegisterRoutes(api, "", &consent.Operations{Read: authn.OptionalUser(), Write: authn.OptionalUser()}))
	return &realAuthnServer{f: f, api: api, mux: mux}
}

func (s *realAuthnServer) do(method string, headers map[string]string, cookies []*http.Cookie, body any) *httptest.ResponseRecorder {
	var rdr *strings.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = strings.NewReader(string(b))
	} else {
		rdr = strings.NewReader("")
	}
	r := httptest.NewRequest(method, consent.DefaultPath, rdr)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", authntest.AppURL)
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	for _, c := range cookies {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, r)
	return w
}

func sessionOf(token string) *http.Cookie {
	return &http.Cookie{Name: sessionCookieName, Value: token}
}

func putBody(purpose string, granted bool) map[string]any {
	return map[string]any{"source": "banner", "decisions": []any{dec(purpose, granted)}}
}

// eventRows counts every consent event in the database: the guard's claim is
// that nothing at all is recorded, under any identity.
func eventRows(t *testing.T) int {
	t.Helper()
	var n int
	require.NoError(t, dbtest.AppEngine().QueryRowContext(context.Background(),
		`SELECT count(*) FROM global_consent_events`).Scan(&n))
	return n
}

func problemCode(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var p struct {
		Code string `json:"code"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &p), w.Body.String())
	return p.Code
}

func TestPutWithARejectedCredentialIsRefusedAndRecordsNothing(t *testing.T) {
	s := newRealAuthnServer(t)
	u := newUser(t)
	good := "good:" + u.String()

	for _, tc := range []struct {
		name    string
		headers map[string]string
		cookies []*http.Cookie
	}{
		{"forged session cookie", nil, []*http.Cookie{sessionOf("forged")}},
		{"expired-looking session cookie", nil, []*http.Cookie{sessionOf("good:not-a-uuid-so-expired")}},
		{"forged bearer", map[string]string{"Authorization": "Bearer forged"}, nil},
		{"forged bearer beside a good cookie", map[string]string{"Authorization": "Bearer forged"}, []*http.Cookie{sessionOf(good)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := eventRows(t)
			w := s.do(http.MethodPut, tc.headers, tc.cookies, putBody("analytics", true))
			require.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
			assert.Equal(t, authn.CodeCredentialRejected, problemCode(t, w))
			assert.Equal(t, authn.CredentialRejectedValue, w.Header().Get(authn.CredentialRejectedHeader), "the client refreshes on this")
			assert.Empty(t, w.Result().Cookies(), "nothing filed against the anonymous cookie")
			assert.Equal(t, before, eventRows(t), "no event, anonymous or the user's")
			hist, err := s.f.svc.History(context.Background(), u)
			require.NoError(t, err)
			assert.Empty(t, hist)
		})
	}

	t.Run("the retry as the user records the choice once", func(t *testing.T) {
		w := s.do(http.MethodPut, nil, []*http.Cookie{sessionOf("forged")}, putBody("analytics", true))
		require.Equal(t, http.StatusForbidden, w.Code)
		w = s.do(http.MethodPut, map[string]string{"Authorization": "Bearer " + good}, nil, putBody("analytics", true))
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		hist, err := s.f.svc.History(context.Background(), u)
		require.NoError(t, err)
		assert.Len(t, hist, 1)
	})
}

func TestPutStillRecordsForAValidOrAnAbsentCredential(t *testing.T) {
	s := newRealAuthnServer(t)
	u := newUser(t)

	t.Run("valid credential records under the user", func(t *testing.T) {
		w := s.do(http.MethodPut, map[string]string{"Authorization": "Bearer good:" + u.String()}, nil, putBody("analytics", true))
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		assert.Empty(t, w.Header().Get(authn.CredentialRejectedHeader))
		hist, err := s.f.svc.History(context.Background(), u)
		require.NoError(t, err)
		assert.Len(t, hist, 1)
	})
	t.Run("no credential records in the anonymous cookie", func(t *testing.T) {
		before := eventRows(t)
		w := s.do(http.MethodPut, nil, nil, putBody("analytics", true))
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		assert.Empty(t, w.Header().Get(authn.CredentialRejectedHeader))
		assert.NotNil(t, setCookie(t, w, "app_consent"))
		assert.Equal(t, before, eventRows(t), "anonymous decisions live in the cookie")
	})
}

func TestGetWithARejectedCredentialStillAnswersAsAnonymous(t *testing.T) {
	s := newRealAuthnServer(t)
	w := s.do(http.MethodGet, nil, []*http.Cookie{sessionOf("forged")}, nil)
	state := decodeState(t, w)
	assert.False(t, state.SignedIn)
	assert.Equal(t, authn.CredentialRejectedValue, w.Header().Get(authn.CredentialRejectedHeader), "the page still learns to refresh")
}

func TestClientCannotMakeOrAvoidARejection(t *testing.T) {
	s := newRealAuthnServer(t)
	before := eventRows(t)

	// Claiming to be rejected is no more than an anonymous request.
	w := s.do(http.MethodPut, map[string]string{authn.CredentialRejectedHeader: authn.CredentialRejectedValue}, nil, putBody("analytics", true))
	assert.Equal(t, http.StatusOK, w.Code)

	// Claiming not to be does not rescue a request that is.
	for _, h := range []map[string]string{
		{authn.CredentialRejectedHeader: ""},
		{authn.CredentialRejectedHeader: "accepted"},
		{"Authorization": "Bearer forged", "X-Forwarded-User": uuid.NewString()},
	} {
		w := s.do(http.MethodPut, h, []*http.Cookie{sessionOf("forged")}, putBody("analytics", true))
		assert.Equal(t, http.StatusForbidden, w.Code, "%v", h)
	}
	assert.Equal(t, before, eventRows(t))
}

func TestRefusalDoesNotSayWhyTheCredentialFailed(t *testing.T) {
	s := newRealAuthnServer(t)
	a := s.do(http.MethodPut, nil, []*http.Cookie{sessionOf("forged")}, putBody("analytics", true))
	b := s.do(http.MethodPut, nil, []*http.Cookie{sessionOf("good:expired-looking")}, putBody("analytics", true))
	require.Equal(t, http.StatusForbidden, a.Code)
	assert.Equal(t, a.Body.String(), b.Body.String())
}

// A consent write route added later is caught here
// if RegisterRoutes mounts it without the guard: every state-changing
// operation it registers must refuse a rejected credential.
func TestEveryConsentWriteRouteRefusesARejectedCredential(t *testing.T) {
	s := newRealAuthnServer(t)
	checked := 0
	for path, item := range s.api.OpenAPI().Paths {
		for method, op := range map[string]*huma.Operation{
			http.MethodPut: item.Put, http.MethodPost: item.Post, http.MethodPatch: item.Patch, http.MethodDelete: item.Delete,
		} {
			if op == nil {
				continue
			}
			checked++
			r := httptest.NewRequest(method, path, strings.NewReader("{}"))
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Origin", authntest.AppURL)
			r.AddCookie(sessionOf("forged"))
			w := httptest.NewRecorder()
			s.mux.ServeHTTP(w, r)
			assert.Equal(t, http.StatusForbidden, w.Code, "%s %s", method, path)
			assert.Equal(t, authn.CodeCredentialRejected, problemCode(t, w), "%s %s", method, path)
		}
	}
	assert.NotZero(t, checked, "no write route was found to check")
}

// The 403 is part of the PUT's published contract, for generated clients.
func TestPutDocumentsTheRejectedCredentialRefusal(t *testing.T) {
	s := newRealAuthnServer(t)
	put := s.api.OpenAPI().Paths[consent.DefaultPath].Put
	require.NotNil(t, put)
	for _, status := range []string{"400", "403", "409"} {
		assert.Contains(t, put.Responses, status)
	}
}
