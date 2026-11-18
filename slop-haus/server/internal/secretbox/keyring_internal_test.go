package secretbox

import (
	"bytes"
	"crypto/rand"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestRing(t *testing.T) *Keyring {
	t.Helper()
	key := make([]byte, KeySize)
	_, err := rand.Read(key)
	require.NoError(t, err)
	ring, err := NewKeyring(key, nil)
	require.NoError(t, err)
	return ring
}

// The size bounds use the constant; the AEAD must agree with it.
func TestKeyring_TagSizeIsTheAEADsOverhead(t *testing.T) {
	assert.Equal(t, gcmTagSize, newTestRing(t).current.aead.Overhead())
}

// A blob over the bound is refused even when it would authenticate: it is
// sealed here the way Seal would, past Seal's own limit.
func TestKeyring_OpenRefusesAnOversizedBlobThatWouldAuthenticate(t *testing.T) {
	ring := newTestRing(t)
	b := Binding{Tenant: uuid.New(), Row: uuid.New()}
	plaintext := bytes.Repeat([]byte{'k'}, MaxPlaintextSize+1)

	blob := make([]byte, headerSize, headerSize+len(plaintext)+gcmTagSize)
	blob[0] = blobVersion
	copy(blob[1:], ring.current.id[:])
	nonce := blob[1+keyIDSize : headerSize]
	_, err := rand.Read(nonce)
	require.NoError(t, err)
	blob = ring.current.aead.Seal(blob, nonce, plaintext, additionalData(blob[:1+keyIDSize], b))
	require.Greater(t, len(blob), maxBlobSize)

	_, err = ring.Open(blob, b)
	assert.ErrorIs(t, err, ErrCannotOpen)
}
