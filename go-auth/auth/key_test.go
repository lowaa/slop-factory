package auth

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDeriveKey_LabelsSeparateKeys(t *testing.T) {
	secret := []byte(strings.Repeat("s", 32))
	a, err := DeriveKey(secret, "a")
	require.NoError(t, err)
	again, err := DeriveKey(secret, "a")
	require.NoError(t, err)
	b, err := DeriveKey(secret, "b")
	require.NoError(t, err)

	assert.Len(t, a, 32)
	assert.Equal(t, a, again, "deterministic")
	assert.NotEqual(t, a, b, "a key per label")
	assert.NotEqual(t, secret, a, "never the secret itself")
}

func TestDeriveKey_Refuses(t *testing.T) {
	_, err := DeriveKey([]byte(strings.Repeat("s", 31)), "a")
	assert.Error(t, err, "a short secret")
	_, err = DeriveKey([]byte(strings.Repeat("s", 32)), "")
	assert.Error(t, err, "no label")
}
