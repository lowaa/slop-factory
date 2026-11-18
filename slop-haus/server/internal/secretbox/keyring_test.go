package secretbox_test

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/secretbox"
)

func newKey(t *testing.T) []byte {
	t.Helper()
	k := make([]byte, secretbox.KeySize)
	_, err := rand.Read(k)
	require.NoError(t, err)
	return k
}

func newRing(t *testing.T, current, previous []byte) *secretbox.Keyring {
	t.Helper()
	ring, err := secretbox.NewKeyring(current, previous)
	require.NoError(t, err)
	return ring
}

func binding() secretbox.Binding {
	return secretbox.Binding{Tenant: uuid.New(), Row: uuid.New()}
}

func TestKeyring_SealsAndOpens(t *testing.T) {
	ring := newRing(t, newKey(t), nil)
	b := binding()
	secret := []byte("-----BEGIN PRIVATE KEY----- pretend")

	blob, err := ring.Seal(secret, b)
	require.NoError(t, err)
	assert.False(t, bytes.Contains(blob, []byte("pretend")), "the blob is ciphertext")
	assert.Equal(t, ring.CurrentID(), secretbox.BlobKeyID(blob))

	got, err := ring.Open(blob, b)
	require.NoError(t, err)
	assert.Equal(t, secret, got)
}

func TestKeyring_ABlobIsBoundToItsTenantAndRow(t *testing.T) {
	ring := newRing(t, newKey(t), nil)
	b := binding()
	blob, err := ring.Seal([]byte("secret"), b)
	require.NoError(t, err)

	otherTenant := secretbox.Binding{Tenant: uuid.New(), Row: b.Row}
	otherRow := secretbox.Binding{Tenant: b.Tenant, Row: uuid.New()}
	for name, wrong := range map[string]secretbox.Binding{"other tenant": otherTenant, "other row": otherRow} {
		_, err := ring.Open(blob, wrong)
		assert.ErrorIs(t, err, secretbox.ErrCannotOpen, name)
	}
}

func TestKeyring_ABlobIsBoundToItsPurpose(t *testing.T) {
	ring := newRing(t, newKey(t), nil)
	b := binding()
	b.Purpose = "webhook secret"
	blob, err := ring.Seal([]byte("secret"), b)
	require.NoError(t, err)

	plain, err := ring.Open(blob, b)
	require.NoError(t, err)
	assert.Equal(t, "secret", string(plain))
	for name, wrong := range map[string]string{"none": "", "another": "private key"} {
		b.Purpose = wrong
		_, err := ring.Open(blob, b)
		assert.ErrorIs(t, err, secretbox.ErrCannotOpen, name)
	}
}

func TestKeyring_AnAlteredBlobDoesNotOpen(t *testing.T) {
	ring := newRing(t, newKey(t), newKey(t))
	b := binding()
	blob, err := ring.Seal([]byte("secret"), b)
	require.NoError(t, err)

	for _, i := range []int{0, 1, 8, 9, 20, len(blob) - 1} {
		altered := bytes.Clone(blob)
		altered[i] ^= 1
		_, err := ring.Open(altered, b)
		assert.ErrorIs(t, err, secretbox.ErrCannotOpen, "byte %d", i)
	}
	_, err = ring.Open(blob[:10], b)
	assert.ErrorIs(t, err, secretbox.ErrCannotOpen)
	_, err = ring.Open(nil, b)
	assert.ErrorIs(t, err, secretbox.ErrCannotOpen)
}

func TestKeyring_SealsUpToTheSizeBound(t *testing.T) {
	ring := newRing(t, newKey(t), nil)
	b := binding()

	atBound := bytes.Repeat([]byte{'k'}, secretbox.MaxPlaintextSize)
	blob, err := ring.Seal(atBound, b)
	require.NoError(t, err)
	got, err := ring.Open(blob, b)
	require.NoError(t, err)
	assert.Equal(t, atBound, got)

	_, err = ring.Seal(append(atBound, 'k'), b)
	assert.ErrorIs(t, err, secretbox.ErrTooLarge)
}

func TestKeyring_EverySealUsesAFreshNonce(t *testing.T) {
	ring := newRing(t, newKey(t), nil)
	b := binding()
	one, err := ring.Seal([]byte("secret"), b)
	require.NoError(t, err)
	two, err := ring.Seal([]byte("secret"), b)
	require.NoError(t, err)
	assert.NotEqual(t, one, two)
}

func TestKeyring_ThePreviousKeyOpensButTheCurrentOneSeals(t *testing.T) {
	oldKey, newKeyBytes := newKey(t), newKey(t)
	b := binding()
	oldBlob, err := newRing(t, oldKey, nil).Seal([]byte("secret"), b)
	require.NoError(t, err)

	rotated := newRing(t, newKeyBytes, oldKey)
	got, err := rotated.Open(oldBlob, b)
	require.NoError(t, err)
	assert.Equal(t, []byte("secret"), got)

	fresh, err := rotated.Seal([]byte("secret"), b)
	require.NoError(t, err)
	assert.Equal(t, rotated.CurrentID(), secretbox.BlobKeyID(fresh))
	assert.NotEqual(t, secretbox.BlobKeyID(oldBlob), secretbox.BlobKeyID(fresh))

	// Once the previous key is dropped, what it sealed is unreadable.
	_, err = newRing(t, newKeyBytes, nil).Open(oldBlob, b)
	assert.ErrorIs(t, err, secretbox.ErrCannotOpen)
}

func TestNewKeyring_RefusesBadKeys(t *testing.T) {
	k := newKey(t)
	_, err := secretbox.NewKeyring(k[:16], nil)
	assert.Error(t, err)
	_, err = secretbox.NewKeyring(k, k[:5])
	assert.Error(t, err)
	_, err = secretbox.NewKeyring(k, k)
	assert.Error(t, err, "the previous key must be a different one")
}

func TestParseKey(t *testing.T) {
	k := newKey(t)
	got, err := secretbox.ParseKey(base64.StdEncoding.EncodeToString(k) + "\n")
	require.NoError(t, err)
	assert.Equal(t, k, got)

	for name, in := range map[string]string{
		"not base64": "!!!", "short": base64.StdEncoding.EncodeToString(k[:16]),
		"long": base64.StdEncoding.EncodeToString(append(k, 1)), "empty": "",
	} {
		_, err := secretbox.ParseKey(in)
		assert.Error(t, err, name)
	}
}

// A blob sealed by the keyring as it was before it moved here from githubapp,
// with its key and binding. Credentials already stored in the database are
// such blobs: if this stops opening, every connected GitHub App credential
// would have to be connected again. Do not regenerate it to make the test pass.
func TestKeyring_OpensABlobSealedBeforeTheMove(t *testing.T) {
	key := make([]byte, secretbox.KeySize)
	for i := range key {
		key[i] = byte(i)
	}
	blob, err := base64.StdEncoding.DecodeString("AWMNzSlmxDNm+2pqjbo8Wz52RDnEJxc4jw4anQVt9Yg18qGyWC6jcJdKCLyeo0l/4VLgApSL1fz7Wjg=")
	require.NoError(t, err)
	b := secretbox.Binding{Tenant: uuid.MustParse("11111111-1111-1111-1111-111111111111"), Row: uuid.MustParse("22222222-2222-2222-2222-222222222222")}
	ring := newRing(t, key, nil)

	assert.Equal(t, "630dcd2966c43366", secretbox.BlobKeyID(blob))
	got, err := ring.Open(blob, b)
	require.NoError(t, err)
	assert.Equal(t, "sealed before the move", string(got))

	_, err = ring.Open(blob, secretbox.Binding{Tenant: b.Tenant, Row: uuid.New()})
	assert.ErrorIs(t, err, secretbox.ErrCannotOpen, "still bound to its row")
}
