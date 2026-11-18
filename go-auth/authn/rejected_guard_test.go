package authn_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-auth/authn"
)

func TestRefuseRejectedCredential(t *testing.T) {
	mux := newMux(t)
	cases := []struct {
		name string
		call call
		// wantRefused: the guard refuses with credential_rejected.
		wantRefused bool
	}{
		{"forged cookie", call{cookie: "forged", origin: board}, true},
		{"expired cookie", call{cookie: "expired", origin: board}, true},
		{"forged bearer", call{bearer: "forged", origin: board}, true},
		{"forged bearer beside a valid cookie", call{bearer: "forged", cookie: goodToken, origin: board}, true},
		{"valid cookie", call{cookie: goodToken, origin: board, csrf: csrfOf(signedIn.SessionID)}, false},
		{"valid bearer", call{bearer: goodToken}, false},
		{"no credential", call{origin: board}, false},
	}
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		for _, tc := range cases {
			t.Run(method+" "+tc.name, func(t *testing.T) {
				tc.call.method, tc.call.path = method, "/guarded"
				rec := serve(mux, tc.call)
				if !tc.wantRefused {
					require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
					assert.Empty(t, rec.Header().Get(authn.CredentialRejectedHeader))
					return
				}
				require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
				assert.Equal(t, authn.CodeCredentialRejected, codeOf(t, rec))
				assert.Equal(t, authn.CredentialRejectedValue, rec.Header().Get(authn.CredentialRejectedHeader), "the client refreshes on the header")
				assert.Equal(t, "application/problem+json", rec.Header().Get("Content-Type"))
			})
		}
	}
}

// The refusal is the same bytes whatever was wrong with the token, so it
// does not tell a caller which tokens are real.
func TestRefusalDoesNotSayWhyTheCredentialFailed(t *testing.T) {
	mux := newMux(t)
	forged := serve(mux, call{method: http.MethodPut, path: "/guarded", cookie: "forged", origin: board})
	expired := serve(mux, call{method: http.MethodPut, path: "/guarded", cookie: "expired", origin: board})
	require.Equal(t, http.StatusForbidden, forged.Code)
	assert.Equal(t, forged.Body.String(), expired.Body.String())
	assert.Equal(t, forged.Header(), expired.Header())
	assert.NotContains(t, strings.ToLower(forged.Body.String()), "expired")
	assert.NotContains(t, strings.ToLower(forged.Body.String()), "forged")
}

// Only the server's own verification sets the flag: nothing a client sends
// -- the header it receives, a look-alike request header, a cookie -- makes a
// good request rejected, nor a rejected one pass.
func TestClientCannotSetOrClearCredentialRejected(t *testing.T) {
	mux := newMux(t)

	t.Run("sending the response header does not get a request refused", func(t *testing.T) {
		req := call{method: http.MethodPut, path: "/guarded", origin: board}
		rec := serveWith(mux, req, map[string]string{authn.CredentialRejectedHeader: authn.CredentialRejectedValue})
		assert.Equal(t, http.StatusOK, rec.Code, "an anonymous request is not rejected by claiming to be")
		assert.Empty(t, rec.Header().Get(authn.CredentialRejectedHeader))
	})
	t.Run("a rejected credential is refused whatever headers say", func(t *testing.T) {
		for _, h := range []map[string]string{
			{authn.CredentialRejectedHeader: ""},
			{authn.CredentialRejectedHeader: "accepted"},
			{"X-Auth-Credential-Rejected": "false"},
			{"X-Forwarded-User": signedIn.ID.String()},
		} {
			rec := serveWith(mux, call{method: http.MethodPut, path: "/guarded", cookie: "forged", origin: board}, h)
			assert.Equal(t, http.StatusForbidden, rec.Code, "%v", h)
			assert.Equal(t, authn.CodeCredentialRejected, codeOf(t, rec))
		}
	})
	t.Run("a second cookie of the same name cannot rescue a bad one", func(t *testing.T) {
		rec := serveWith(mux, call{method: http.MethodPut, path: "/guarded", cookie: "forged", origin: board},
			map[string]string{"Cookie": sessionCookie + "=" + goodToken + "; " + sessionCookie + "=forged"})
		// Whichever one is read, the outcome is decided by verifying it:
		// 200 as the good user or 403 as rejected; never anonymous 200.
		if rec.Code == http.StatusOK {
			assert.Equal(t, signedIn.ID, seenBy(t, rec).user)
		} else {
			assert.Equal(t, http.StatusForbidden, rec.Code)
		}
	})
}
