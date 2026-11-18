package intake_test

import (
	"crypto/sha256"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/intake"
)

func TestTokensCheckAgainstTheirHashOnly(t *testing.T) {
	token, hash, err := intake.NewToken()
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(token, "shi_"))
	assert.True(t, intake.CheckToken(token, hash))
	assert.False(t, intake.CheckToken(token+"x", hash))
	assert.False(t, intake.CheckToken("", hash))
	assert.False(t, intake.CheckToken(token, nil))

	other, otherHash, err := intake.NewToken()
	require.NoError(t, err)
	assert.NotEqual(t, token, other)
	assert.NotEqual(t, hash, otherHash)
	assert.False(t, intake.CheckToken(other, hash))
}

func newSecrets(t *testing.T, key string) *intake.Secrets {
	t.Helper()
	s, err := intake.NewSecrets([]byte(key))
	require.NoError(t, err)
	return s
}

func TestSecretsRefuseAShortKey(t *testing.T) {
	_, err := intake.NewSecrets([]byte(strings.Repeat("k", 31)))
	require.Error(t, err)
	_, err = intake.NewSecrets([]byte(strings.Repeat("k", 32)))
	require.NoError(t, err)
}

func TestDerivedSecretsDifferByEveryInput(t *testing.T) {
	s := newSecrets(t, strings.Repeat("k", 32))
	id := uuid.New()
	base := s.For(id, 1)
	assert.Equal(t, base, s.For(id, 1))
	assert.NotEqual(t, base, s.For(uuid.New(), 1))
	assert.NotEqual(t, base, s.For(id, 2))
	assert.NotEqual(t, base, newSecrets(t, strings.Repeat("j", 32)).For(id, 1))
}

func TestSignaturesVerifyOnlyForTheSameBodySourceAndGeneration(t *testing.T) {
	s := newSecrets(t, strings.Repeat("k", 32))
	id := uuid.New()
	body := []byte(`{"key":"a"}`)
	good := intake.Sign(s.For(id, 1), body)
	require.True(t, strings.HasPrefix(good, "sha256="))

	assert.True(t, s.Verify(id, 1, body, good))
	assert.False(t, s.Verify(id, 1, []byte(`{"key":"b"}`), good), "tampered body")
	assert.False(t, s.Verify(id, 2, body, good), "rotated generation")
	assert.False(t, s.Verify(uuid.New(), 1, body, good), "another source")
	assert.False(t, s.Verify(id, 1, body, strings.TrimPrefix(good, "sha256=")), "no prefix")
	assert.False(t, s.Verify(id, 1, body, "sha256=zz"), "not hex")
	assert.False(t, s.Verify(id, 1, body, "sha256="), "empty digest")
	assert.False(t, s.Verify(id, 1, body, ""), "empty header")
	assert.False(t, newSecrets(t, strings.Repeat("j", 32)).Verify(id, 1, body, good), "another key")
}

// Pinned, computed independently of this code (openssl): sources already
// configured at their senders hold these secrets, so a change to the
// derivation silently breaks every one of them.
func TestSecretsPinnedKnownAnswers(t *testing.T) {
	id := uuid.MustParse("a1b2c3d4-0000-4000-8000-000000000001")
	s := newSecrets(t, strings.Repeat("k", 32))
	body := []byte(`{"zen":"x"}`)

	secret := s.For(id, 3)
	assert.Equal(t, "b47becfed31aeb1495082dbfa5683425eec2a3afe9b1509c4acf0b300648fa74", secret)
	sig := "sha256=e9c86d2775fd0bc19c15e44fd958419e26dbf8bf975300a1192ea9032c54e83c"
	assert.Equal(t, sig, intake.Sign(secret, body))
	assert.True(t, s.Verify(id, 3, body, sig))
	assert.False(t, s.Verify(id, 4, body, sig))
}

// Credentials stored before the hash moved to go-auth are the raw SHA-256 of
// the token; they must keep authenticating.
func TestTokensStoredAsRawSha256StillCheck(t *testing.T) {
	sum := sha256.Sum256([]byte("shi_legacy"))
	assert.True(t, intake.CheckToken("shi_legacy", sum[:]))
	assert.Equal(t, sum[:], intake.HashToken("shi_legacy"))
}
