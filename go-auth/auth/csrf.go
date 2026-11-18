package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
)

// csrfKeyInfo is the HKDF label the CSRF key is derived under. Distinct from
// every other label keyed from the same secret, so a CSRF token is never a
// valid MAC for anything else.
const csrfKeyInfo = "go-auth/auth: session CSRF token v1"

// NewSessionID returns a fresh random id for an interactive browser session:
// 128 bits, base64url. It names the session, not any one token issued for
// it, so a caller mints one at sign-in and carries it unchanged through
// every refresh and re-issue after.
func NewSessionID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("auth: generate session id: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// CSRFTokens derives a session's synchronizer token from its id, statelessly:
// the token is an HMAC of the session id, so a server with only stateless
// session tokens (JWTs) can still hand a page a per-session secret and check
// it on every write without storing anything.
//
// The token is only as secret as the session id, which travels only inside
// the signed, httpOnly session tokens; the page learns the token (never the
// id) from a same-origin response another site cannot read.
type CSRFTokens struct {
	key []byte
}

// NewCSRFTokens keys a CSRFTokens from secret (at least 32 bytes), through
// DeriveKey under a label of its own, so the secret itself -- typically the
// JWT signing secret -- is never the MAC key.
func NewCSRFTokens(secret []byte) (*CSRFTokens, error) {
	key, err := DeriveKey(secret, csrfKeyInfo)
	if err != nil {
		return nil, err
	}
	return &CSRFTokens{key: key}, nil
}

// Token is sessionID's CSRF token, or "" for a session with no id: there is
// nothing to bind a token to, and no token must match it.
func (c *CSRFTokens) Token(sessionID string) string {
	if sessionID == "" {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(c.mac(sessionID))
}

// Valid reports whether presented is sessionID's CSRF token, in constant
// time. A session with no id has no valid token.
func (c *CSRFTokens) Valid(sessionID, presented string) bool {
	if sessionID == "" || presented == "" {
		return false
	}
	got, err := base64.RawURLEncoding.DecodeString(presented)
	if err != nil {
		return false
	}
	return hmac.Equal(got, c.mac(sessionID))
}

func (c *CSRFTokens) mac(sessionID string) []byte {
	m := hmac.New(sha256.New, c.key)
	m.Write([]byte(sessionID))
	return m.Sum(nil)
}
