package auth

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var testCSRFSecret = []byte(strings.Repeat("s", 32))

func testCSRFTokens(t *testing.T) *CSRFTokens {
	t.Helper()
	c, err := NewCSRFTokens(testCSRFSecret)
	require.NoError(t, err)
	return c
}

func TestNewSessionID_IsLongAndUnique(t *testing.T) {
	a, err := NewSessionID()
	require.NoError(t, err)
	b, err := NewSessionID()
	require.NoError(t, err)
	assert.NotEqual(t, a, b)
	raw, err := base64.RawURLEncoding.DecodeString(a)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(raw)*8, 128, "at least 128 bits")
}

func TestCSRFTokens_ASessionsTokenIsValidForItAlone(t *testing.T) {
	c := testCSRFTokens(t)
	token := c.Token("session-a")
	require.NotEmpty(t, token)
	assert.Equal(t, token, c.Token("session-a"), "stable for the life of the session")
	assert.True(t, c.Valid("session-a", token))
	assert.False(t, c.Valid("session-b", token), "another session's token")
	assert.False(t, c.Valid("session-a", ""), "no token")
	assert.False(t, c.Valid("session-a", token+"x"), "tampered")
	assert.False(t, c.Valid("session-a", "!!not base64!!"))
}

// A session without an id -- one issued before ids were -- has no token,
// and nothing is valid for it, not even the empty string.
func TestCSRFTokens_NoSessionIDNoToken(t *testing.T) {
	c := testCSRFTokens(t)
	assert.Empty(t, c.Token(""))
	assert.False(t, c.Valid("", ""))
	assert.False(t, c.Valid("", c.Token("x")))
}

// The token is keyed from a key derived for it, never the raw secret: a
// token computed with the raw secret (which also signs the session JWTs)
// is not valid, and another secret's tokens are not either.
func TestCSRFTokens_KeyIsDerivedNotTheRawSecret(t *testing.T) {
	c := testCSRFTokens(t)
	assert.NotEqual(t, testCSRFSecret, c.key)
	withRawSecret := &CSRFTokens{key: testCSRFSecret}
	assert.False(t, c.Valid("session-a", withRawSecret.Token("session-a")))

	other, err := NewCSRFTokens([]byte(strings.Repeat("o", 32)))
	require.NoError(t, err)
	assert.False(t, c.Valid("session-a", other.Token("session-a")))
}

func TestNewCSRFTokens_RefusesAShortSecret(t *testing.T) {
	_, err := NewCSRFTokens([]byte(strings.Repeat("s", 31)))
	assert.Error(t, err)
}
