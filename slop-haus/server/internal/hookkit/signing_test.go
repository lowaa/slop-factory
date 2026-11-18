package hookkit_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/hookkit"
)

// GitHub's own test values for validating webhook deliveries
// (docs.github.com, "Validating webhook deliveries"): what GitHub sends is
// what this computes.
func TestSignMatchesGitHubsTestVector(t *testing.T) {
	const want = "sha256=757107ea0eb2509fc211221cce984b8a37570b6d7586c22c46f4379c8b043e17"
	secret, body := "It's a Secret to Everybody", []byte("Hello, World!")
	assert.Equal(t, want, hookkit.Sign(secret, body))
	assert.True(t, hookkit.VerifySignature(secret, body, want))
}

func TestVerifySignatureRefusesWhatIsNotTheSignature(t *testing.T) {
	good := hookkit.Sign("s", []byte("b"))
	digest := strings.TrimPrefix(good, hookkit.SignaturePrefix)
	assert.True(t, hookkit.VerifySignature("s", []byte("b"), good))
	for name, header := range map[string]string{
		"empty":      "",
		"no prefix":  digest,
		"sha1":       "sha1=" + digest,
		"not hex":    "sha256=zz",
		"no digest":  "sha256=",
		"truncated":  good[:len(good)-2],
		"upper case": "SHA256=" + digest,
	} {
		assert.False(t, hookkit.VerifySignature("s", []byte("b"), header), name)
	}
	assert.False(t, hookkit.VerifySignature("t", []byte("b"), good), "another secret")
	assert.False(t, hookkit.VerifySignature("s", []byte("c"), good), "another body")
}

func TestSecretDeriver(t *testing.T) {
	key := []byte(strings.Repeat("k", hookkit.MinKeyBytes))
	_, err := hookkit.NewSecretDeriver(key[:hookkit.MinKeyBytes-1], "l")
	require.ErrorIs(t, err, hookkit.ErrKeyTooShort)

	d, err := hookkit.NewSecretDeriver(key, "label\x00")
	require.NoError(t, err)
	base := d.Derive("a", "b")
	assert.Equal(t, base, d.Derive("a", "b"))
	assert.Len(t, base, 64)
	assert.NotEqual(t, base, d.Derive("a", "c"))
	assert.NotEqual(t, d.Derive("ab", "c"), d.Derive("a", "bc"), "part boundaries matter")
	other, err := hookkit.NewSecretDeriver(key, "other\x00")
	require.NoError(t, err)
	assert.NotEqual(t, base, other.Derive("a", "b"), "the label binds the use")

	// the key is copied: the caller reusing its buffer must not change secrets
	mutable := append([]byte(nil), key...)
	d2, err := hookkit.NewSecretDeriver(mutable, "label\x00")
	require.NoError(t, err)
	mutable[0] = 'x'
	assert.Equal(t, base, d2.Derive("a", "b"))
}
