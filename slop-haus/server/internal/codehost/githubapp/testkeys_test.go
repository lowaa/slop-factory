package githubapp_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// Generated keys only: the tests never read, print or commit a real one.
var (
	keyOnce  [2]sync.Once
	keyCache [2]*rsa.PrivateKey
)

// rsaKey is the n-th shared 2048-bit test key; generating one takes long
// enough that each test binary makes two at most.
func rsaKey(t *testing.T, n int) *rsa.PrivateKey {
	t.Helper()
	keyOnce[n].Do(func() {
		k, err := rsa.GenerateKey(rand.Reader, 2048)
		require.NoError(t, err)
		keyCache[n] = k
	})
	return keyCache[n]
}

func pkcs1PEM(k *rsa.PrivateKey) string {
	return string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)}))
}

func pkcs8PEM(t *testing.T, key any) string {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(key)
	require.NoError(t, err)
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

func smallRSAKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 1024)
	require.NoError(t, err)
	return k
}

func ecKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	return k
}
