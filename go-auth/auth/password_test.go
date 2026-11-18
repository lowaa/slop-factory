package auth

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHashPassword_VerifyPassword_RoundTrip(t *testing.T) {
	hash, err := HashPassword("correct horse battery staple")
	require.NoError(t, err)
	assert.NoError(t, VerifyPassword(hash, "correct horse battery staple"))
}

func TestVerifyPassword_WrongPassword(t *testing.T) {
	hash, err := HashPassword("correct horse battery staple")
	require.NoError(t, err)
	assert.Error(t, VerifyPassword(hash, "wrong password"))
}

// bcrypt (as vendored here, golang.org/x/crypto v0.54.0) rejects passwords
// over 72 bytes outright rather than silently truncating them -- worth
// pinning explicitly, since older bcrypt implementations/other languages'
// ports do truncate, and a caller relying on that would silently weaken a
// long passphrase to its first 72 bytes.
func TestHashPassword_RejectsOver72Bytes(t *testing.T) {
	tooLong := strings.Repeat("a", 73)
	_, err := HashPassword(tooLong)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrPasswordTooLong)
}

func TestHashPassword_Accepts72Bytes(t *testing.T) {
	exactly72 := strings.Repeat("a", 72)
	_, err := HashPassword(exactly72)
	assert.NoError(t, err)
}
