package authn_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-auth/audit"
	"github.com/lowaa/slop-factory/go-auth/authn"
)

// seen is what an OptionalUser handler saw of a request.
type seen struct {
	user     uuid.UUID
	tenant   uuid.UUID
	rejected bool
}

func (s seen) anonymous() bool { return s.user == uuid.Nil }

func seenBy(t *testing.T, rec *httptest.ResponseRecorder) seen {
	t.Helper()
	var body struct {
		UserID   uuid.UUID `json:"user_id"`
		TenantID uuid.UUID `json:"tenant_id"`
		Rejected bool      `json:"rejected"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body), rec.Body.String())
	return seen{user: body.UserID, tenant: body.TenantID, rejected: body.Rejected}
}

func TestOptionalUserReads(t *testing.T) {
	mux := newMux(t)
	cases := []struct {
		name         string
		call         call
		wantUser     bool
		wantRejected bool
	}{
		{"no credential", call{}, false, false},
		{"valid cookie", call{cookie: goodToken}, true, false},
		{"valid bearer", call{bearer: goodToken}, true, false},
		{"forged cookie", call{cookie: "forged"}, false, true},
		{"expired or revoked cookie", call{cookie: "expired"}, false, true},
		{"forged bearer", call{bearer: "forged"}, false, true},
		// The one credential a request names is the bearer token: a bad one
		// is not rescued by a good cookie, as for RequireUser.
		{"forged bearer, valid cookie", call{bearer: "forged", cookie: goodToken}, false, true},
		{"valid bearer, forged cookie", call{bearer: goodToken, cookie: "forged"}, true, false},
		{"a Basic Authorization header, no cookie", call{authorization: "Basic dXNlcjpwYXNz"}, false, false},
		{"an empty bearer", call{authorization: "Bearer   "}, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.call.method, tc.call.path = http.MethodGet, "/optional"
			rec := serve(mux, tc.call)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			got := seenBy(t, rec)
			assert.Equal(t, !tc.wantUser, got.anonymous(), "user on the context")
			assert.Equal(t, tc.wantRejected, got.rejected, "handler told the credential was refused")
			if tc.wantUser {
				assert.Equal(t, signedIn.ID, got.user)
				assert.Equal(t, signedIn.TenantID, got.tenant, "the scope is set with the user")
			} else {
				assert.Equal(t, uuid.Nil, got.tenant, "no scope without a user")
			}
			wantHeader := ""
			if tc.wantRejected {
				wantHeader = authn.CredentialRejectedValue
			}
			assert.Equal(t, wantHeader, rec.Header().Get(authn.CredentialRejectedHeader))
		})
	}
}

// Whatever the credential, whatever the method, an OptionalUser route
// never answers 401.
func TestOptionalUserNeverAnswers401(t *testing.T) {
	mux := newMux(t)
	creds := []call{
		{}, {cookie: goodToken}, {bearer: goodToken}, {cookie: "forged"}, {bearer: "forged"},
		{bearer: "forged", cookie: goodToken}, {cookie: "expired", origin: board},
	}
	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		for _, c := range creds {
			c.method, c.path = method, "/optional"
			rec := serve(mux, c)
			assert.NotEqual(t, http.StatusUnauthorized, rec.Code, "%s %+v", method, c)
		}
	}
}

// Two people's requests never see each other: each is the user its own
// credential names.
func TestOptionalUserSeesOnlyTheCredentialsOwnUser(t *testing.T) {
	mux := newMux(t)
	a := seenBy(t, serve(mux, call{method: http.MethodGet, path: "/optional", cookie: goodToken}))
	b := seenBy(t, serve(mux, call{method: http.MethodGet, path: "/optional", cookie: otherPersonToken}))
	anon := seenBy(t, serve(mux, call{method: http.MethodGet, path: "/optional"}))
	assert.Equal(t, signedIn.ID, a.user)
	assert.Equal(t, anotherPerson.ID, b.user)
	assert.NotEqual(t, a.user, b.user)
	assert.True(t, anon.anonymous(), "a request after two signed-in ones is still anonymous")
	// A bearer token wins over a cookie of someone else.
	both := seenBy(t, serve(mux, call{method: http.MethodGet, path: "/optional", bearer: otherPersonToken, cookie: goodToken}))
	assert.Equal(t, anotherPerson.ID, both.user)
}

// A write needs the origin whatever the request carries, and a session
// cookie's CSRF token too: omitting or spoiling the credential must not
// skip a check that would have applied to a good one.
func TestOptionalUserWrites(t *testing.T) {
	mux, events := newCountingMux(t)
	good := csrfOf(signedIn.SessionID)
	cases := []struct {
		name         string
		call         call
		want         int
		wantCode     string
		wantUser     bool
		wantRejected bool
	}{
		{"no credential, the board's origin", call{origin: board}, http.StatusOK, "", false, false},
		{"no credential, another origin", call{origin: "https://evil.example"}, http.StatusForbidden, authn.CodeCrossOrigin, false, false},
		{"no credential, no origin", call{}, http.StatusForbidden, authn.CodeCrossOrigin, false, false},
		{"no credential, Origin: null", call{origin: "null"}, http.StatusForbidden, authn.CodeCrossOrigin, false, false},
		{"no credential, a Referer from the board", call{referer: board + "/x"}, http.StatusOK, "", false, false},

		{"cookie, origin and token", call{cookie: goodToken, origin: board, csrf: good}, http.StatusOK, "", true, false},
		{"cookie, no token", call{cookie: goodToken, origin: board}, http.StatusForbidden, authn.CodeCSRF, false, false},
		{"cookie, another session's token", call{cookie: goodToken, origin: board, csrf: csrfOf(otherSession.SessionID)}, http.StatusForbidden, authn.CodeCSRF, false, false},
		{"cookie, a legacy session", call{cookie: legacyToken, origin: board}, http.StatusForbidden, authn.CodeCSRF, false, false},
		{"cookie, another origin", call{cookie: goodToken, origin: "https://evil.example", csrf: good}, http.StatusForbidden, authn.CodeCrossOrigin, false, false},
		{"cookie, no origin", call{cookie: goodToken, csrf: good}, http.StatusForbidden, authn.CodeCrossOrigin, false, false},

		// A cross-site form carrying the visitor's expired or forged
		// cookie is checked, not waved through as "anonymous".
		{"forged cookie, another origin", call{cookie: "forged", origin: "https://evil.example"}, http.StatusForbidden, authn.CodeCrossOrigin, false, false},
		{"forged cookie, no origin", call{cookie: "forged"}, http.StatusForbidden, authn.CodeCrossOrigin, false, false},
		{"expired cookie, another origin", call{cookie: "expired", origin: "https://evil.example", csrf: good}, http.StatusForbidden, authn.CodeCrossOrigin, false, false},
		{"expired cookie, the board's origin", call{cookie: "expired", origin: board}, http.StatusOK, "", false, true},

		{"bearer, nothing else", call{bearer: goodToken}, http.StatusOK, "", true, false},
		{"bearer, another origin", call{bearer: goodToken, origin: "https://evil.example"}, http.StatusOK, "", true, false},
		{"bearer, a cookie of its own and no token", call{bearer: goodToken, cookie: otherSessionToken}, http.StatusOK, "", true, false},
		// A forged bearer verifies nothing, so it buys no exemption -- and
		// the valid cookie beside it is not used.
		{"forged bearer, another origin", call{bearer: "forged", origin: "https://evil.example"}, http.StatusForbidden, authn.CodeCrossOrigin, false, false},
		{"forged bearer, valid cookie, no token, another origin", call{bearer: "forged", cookie: goodToken, origin: "https://evil.example"}, http.StatusForbidden, authn.CodeCrossOrigin, false, false},
		{"forged bearer, valid cookie, the board's origin", call{bearer: "forged", cookie: goodToken, origin: board}, http.StatusOK, "", false, true},
	}
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		for _, tc := range cases {
			t.Run(method+" "+tc.name, func(t *testing.T) {
				tc.call.method, tc.call.path = method, "/optional"
				csrfBefore, originBefore := events.Count(audit.CSRFRefused), events.Count(audit.CrossOriginRefused)
				rec := serve(mux, tc.call)
				require.Equal(t, tc.want, rec.Code, rec.Body.String())
				if tc.wantCode != "" {
					assert.Equal(t, tc.wantCode, codeOf(t, rec))
					if tc.call.bearer == "forged" || (tc.call.bearer == "" && (tc.call.cookie == "forged" || tc.call.cookie == "expired")) {
						assert.Equal(t, authn.CredentialRejectedValue, rec.Header().Get(authn.CredentialRejectedHeader), "a refused credential is flagged even on a refused write")
					}
					return
				}
				got := seenBy(t, rec)
				if tc.wantUser {
					assert.Equal(t, signedIn.ID, got.user)
					assert.Equal(t, signedIn.TenantID, got.tenant)
				}
				assert.Equal(t, !tc.wantUser, got.anonymous())
				assert.Equal(t, tc.wantRejected, got.rejected)
				assert.Zero(t, events.Count(audit.CSRFRefused)-csrfBefore+events.Count(audit.CrossOriginRefused)-originBefore, "nothing refused")
			})
		}
	}
}

func TestOptionalUserIsAnExplicitMarker(t *testing.T) {
	p := authn.NewPolicy()
	assert.Equal(t, "optional-user", p.MarkerName(opWith(authn.OptionalUser())))
	// Neither of the other modes is optional-user by implication.
	assert.NotEqual(t, "optional-user", p.MarkerName(opWith(authn.Public())))
	assert.NotEqual(t, "optional-user", p.MarkerName(opWith(authn.RequireUser())))
	assert.NotEqual(t, "optional-user", p.MarkerName(opWith()))
	// Two decisions are refused, not resolved in favour of either.
	assert.Equal(t, "multiple", p.MarkerName(opWith(authn.OptionalUser(), authn.RequireUser())))
	assert.Equal(t, 2, p.Markers(opWith(authn.OptionalUser(), authn.Public())))
}

// A RequireUser route is unchanged by the new marker: still 401, and a
// rejected credential is not flagged as if anonymous were an answer.
func TestRequireUserStillAnswers401(t *testing.T) {
	mux := newMux(t)
	for _, c := range []call{{}, {cookie: "expired"}, {bearer: "forged"}} {
		c.method, c.path = http.MethodGet, "/user"
		rec := serve(mux, c)
		assert.Equal(t, http.StatusUnauthorized, rec.Code)
		assert.Empty(t, rec.Header().Get(authn.CredentialRejectedHeader))
	}
}

// The answer depends on who asks, on a route open to anyone: it must not
// be served from a shared cache to someone else.
func TestOptionalUserResponsesAreNotSharedCacheable(t *testing.T) {
	mux := newMux(t)
	for _, c := range []call{{}, {cookie: goodToken}, {bearer: goodToken}, {cookie: "forged"}} {
		c.method, c.path = http.MethodGet, "/optional"
		rec := serve(mux, c)
		assert.Equal(t, "private", rec.Header().Get("Cache-Control"))
		assert.Contains(t, rec.Header().Values("Vary"), "Cookie")
		assert.Contains(t, rec.Header().Values("Vary"), "Authorization")
	}
}
