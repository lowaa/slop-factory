package auth

import (
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testSigner() *Signer {
	return NewSigner([]byte("test-secret"), time.Hour, 24*time.Hour)
}

func TestSigner_AccessTokenRoundTrip(t *testing.T) {
	s := testSigner()
	userID, tenantID := uuid.New(), uuid.New()

	raw, err := s.IssueAccessToken(userID, tenantID)
	require.NoError(t, err)

	claims, err := s.ParseAccessToken(raw)
	require.NoError(t, err)
	assert.Equal(t, userID, claims.UserID)
	assert.Equal(t, tenantID, claims.TenantID)
}

func TestSigner_RefreshTokenRoundTrip(t *testing.T) {
	s := testSigner()
	userID, tenantID := uuid.New(), uuid.New()

	raw, err := s.IssueRefreshToken(userID, tenantID)
	require.NoError(t, err)

	claims, err := s.ParseRefreshToken(raw)
	require.NoError(t, err)
	assert.Equal(t, userID, claims.UserID)
	assert.Equal(t, tenantID, claims.TenantID)
}

func TestSigner_RefreshTokenRejectedAsAccess(t *testing.T) {
	s := testSigner()
	raw, err := s.IssueRefreshToken(uuid.New(), uuid.New())
	require.NoError(t, err)

	_, err = s.ParseAccessToken(raw)
	assert.ErrorIs(t, err, ErrInvalidToken)
}

func TestSigner_AccessTokenRejectedAsRefresh(t *testing.T) {
	s := testSigner()
	raw, err := s.IssueAccessToken(uuid.New(), uuid.New())
	require.NoError(t, err)

	_, err = s.ParseRefreshToken(raw)
	assert.ErrorIs(t, err, ErrInvalidToken)
}

func TestSigner_ExpiredTokenRejected(t *testing.T) {
	// A negative TTL issues a token whose exp is already in the past,
	// avoiding a real sleep to observe expiry.
	s := NewSigner([]byte("test-secret"), -time.Minute, -time.Minute)
	raw, err := s.IssueAccessToken(uuid.New(), uuid.New())
	require.NoError(t, err)

	_, err = s.ParseAccessToken(raw)
	assert.ErrorIs(t, err, ErrInvalidToken)
}

func TestSigner_TamperedSignatureRejected(t *testing.T) {
	s := testSigner()
	raw, err := s.IssueAccessToken(uuid.New(), uuid.New())
	require.NoError(t, err)

	parts := strings.Split(raw, ".")
	require.Len(t, parts, 3)
	// Change the signature's first character, which is six whole bits of
	// the MAC. The last one would not do: it carries padding bits a
	// lenient decoder ignores, and was sometimes already the replacement.
	first := "A"
	if parts[2][0] == 'A' {
		first = "B"
	}
	tampered := parts[0] + "." + parts[1] + "." + first + parts[2][1:]

	_, err = s.ParseAccessToken(tampered)
	assert.ErrorIs(t, err, ErrInvalidToken)
}

func TestSigner_WrongSecretRejected(t *testing.T) {
	issuer := NewSigner([]byte("secret-a"), time.Hour, 24*time.Hour)
	verifier := NewSigner([]byte("secret-b"), time.Hour, 24*time.Hour)

	raw, err := issuer.IssueAccessToken(uuid.New(), uuid.New())
	require.NoError(t, err)

	_, err = verifier.ParseAccessToken(raw)
	assert.ErrorIs(t, err, ErrInvalidToken)
}

// Only HS256 is accepted. A token claiming another algorithm -- even
// another HMAC keyed with the same secret, or none at all -- is refused:
// letting the token choose how it is verified is how algorithm-confusion
// attacks work.
func TestSigner_OtherAlgorithmsRejected(t *testing.T) {
	s := testSigner()
	claims := Claims{
		UserID:   uuid.New(),
		TenantID: uuid.New(),
		Type:     tokenTypeAccess,
		RegisteredClaims: jwt.RegisteredClaims{
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		},
	}

	hs512, err := jwt.NewWithClaims(jwt.SigningMethodHS512, claims).SignedString([]byte("test-secret"))
	require.NoError(t, err)
	_, err = s.ParseAccessToken(hs512)
	assert.ErrorIs(t, err, ErrInvalidToken, "HS512 with the same secret")

	none, err := jwt.NewWithClaims(jwt.SigningMethodNone, claims).SignedString(jwt.UnsafeAllowNoneSignatureType)
	require.NoError(t, err)
	_, err = s.ParseAccessToken(none)
	assert.ErrorIs(t, err, ErrInvalidToken, "alg none")
}

// The session's claims -- auth time and id -- come back out of both
// token types exactly as they went in, so a refresh or re-issue can carry
// them forward unchanged.
func TestSigner_SessionClaimsRoundTrip(t *testing.T) {
	s := testSigner()
	userID, tenantID := uuid.New(), uuid.New()
	sess := &Session{AuthTime: time.Now().Add(-42 * time.Minute).Truncate(time.Second), ID: "the-session"}

	access, err := s.IssueSessionAccessToken(userID, tenantID, sess)
	require.NoError(t, err)
	claims, err := s.ParseAccessToken(access)
	require.NoError(t, err)
	assert.Equal(t, sess, claims.Session(), "access")
	assert.True(t, claims.IssuedAt.After(sess.AuthTime), "issued now, not at the authentication")

	refresh, err := s.IssueSessionRefreshToken(userID, tenantID, sess)
	require.NoError(t, err)
	claims, err = s.ParseRefreshToken(refresh)
	require.NoError(t, err)
	assert.Equal(t, sess, claims.Session(), "refresh")
}

// Tokens issued without a session -- by the original methods, with a nil
// session, or with a zero one -- carry neither claim at all, so services
// that never ask for them see exactly the tokens they always did.
func TestSigner_NoSessionLeavesTheClaimsOut(t *testing.T) {
	s := testSigner()
	for name, issue := range map[string]func() (string, error){
		"IssueAccessToken": func() (string, error) { return s.IssueAccessToken(uuid.New(), uuid.New()) },
		"nil session":      func() (string, error) { return s.IssueSessionAccessToken(uuid.New(), uuid.New(), nil) },
		"zero session":     func() (string, error) { return s.IssueSessionAccessToken(uuid.New(), uuid.New(), &Session{}) },
	} {
		raw, err := issue()
		require.NoError(t, err, name)
		payload, err := jwt.NewParser().DecodeSegment(strings.Split(raw, ".")[1])
		require.NoError(t, err, name)
		assert.NotContains(t, string(payload), "auth_time", name)
		assert.NotContains(t, string(payload), "sid", name)
		claims, err := s.ParseAccessToken(raw)
		require.NoError(t, err, name)
		assert.Nil(t, claims.AuthTime, name)
		assert.Empty(t, claims.SessionID, name)
		assert.Equal(t, &Session{}, claims.Session(), name)
	}
}
