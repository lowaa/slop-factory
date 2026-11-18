package auth

import (
	"encoding/hex"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewOpaqueToken_RawAndHashDiffer(t *testing.T) {
	raw, hash, err := NewOpaqueToken()
	require.NoError(t, err)
	assert.NotEmpty(t, raw)
	assert.NotEmpty(t, hash)
	assert.NotEqual(t, raw, hash)
}

func TestNewOpaqueToken_HashMatchesHashOpaqueToken(t *testing.T) {
	raw, hash, err := NewOpaqueToken()
	require.NoError(t, err)
	assert.Equal(t, HashOpaqueToken(raw), hash)
}

func TestHashOpaqueToken_Deterministic(t *testing.T) {
	assert.Equal(t, HashOpaqueToken("some-raw-token"), HashOpaqueToken("some-raw-token"))
}

func TestNewOpaqueToken_UniquePerCall(t *testing.T) {
	raw1, hash1, err := NewOpaqueToken()
	require.NoError(t, err)
	raw2, hash2, err := NewOpaqueToken()
	require.NoError(t, err)

	assert.NotEqual(t, raw1, raw2)
	assert.NotEqual(t, hash1, hash2)
}

func TestNewOpaqueTokenWithPrefix(t *testing.T) {
	raw, hash, err := NewOpaqueTokenWithPrefix("shi_")
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(raw, "shi_"))
	assert.Regexp(t, `^[A-Za-z0-9_-]{43}$`, strings.TrimPrefix(raw, "shi_"))
	assert.Equal(t, HashOpaqueTokenBytes(raw), hash)
	assert.Equal(t, HashOpaqueToken(raw), hex.EncodeToString(hash))
}

func TestHashOpaqueTokenBytes_KnownDigest(t *testing.T) {
	// sha256("abc"); pins the stored form so existing rows keep matching.
	assert.Equal(t, "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad", hex.EncodeToString(HashOpaqueTokenBytes("abc")))
}

func TestCheckOpaqueToken(t *testing.T) {
	raw, hash, err := NewOpaqueTokenWithPrefix("p_")
	require.NoError(t, err)
	assert.True(t, CheckOpaqueToken(raw, hash))
	assert.False(t, CheckOpaqueToken(raw+"x", hash))
	assert.False(t, CheckOpaqueToken("", hash))
	assert.False(t, CheckOpaqueToken(raw, nil))
}
