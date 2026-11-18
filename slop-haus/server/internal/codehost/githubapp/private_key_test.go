package githubapp_test

import (
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost/githubapp"
)

func TestParsePrivateKey_AcceptsPKCS1AndPKCS8ToTheSameKey(t *testing.T) {
	k := rsaKey(t, 0)
	one, err := githubapp.ParsePrivateKey([]byte(pkcs1PEM(k)))
	require.NoError(t, err)
	eight, err := githubapp.ParsePrivateKey([]byte(pkcs8PEM(t, k)))
	require.NoError(t, err)

	oneFP, err := one.Fingerprint()
	require.NoError(t, err)
	eightFP, err := eight.Fingerprint()
	require.NoError(t, err)
	assert.Equal(t, oneFP, eightFP)
	assert.Len(t, oneFP, 64)

	// Whatever it was given as, what is stored is PKCS#8 and nothing else.
	stored, err := one.PEM()
	require.NoError(t, err)
	block, rest := pem.Decode(stored)
	require.NotNil(t, block)
	assert.Empty(t, rest)
	assert.Equal(t, "PRIVATE KEY", block.Type)
	_, err = x509.ParsePKCS8PrivateKey(block.Bytes)
	assert.NoError(t, err)
}

func TestParsePrivateKey_IgnoresTextAroundTheBlock(t *testing.T) {
	_, err := githubapp.ParsePrivateKey([]byte("downloaded from GitHub\n" + pkcs1PEM(rsaKey(t, 0)) + "\n"))
	assert.NoError(t, err)
}

func TestParsePrivateKey_RefusesWhatIsNotAUsableRSAKey(t *testing.T) {
	pub, err := x509.MarshalPKIXPublicKey(&rsaKey(t, 0).PublicKey)
	require.NoError(t, err)
	cases := map[string]string{
		"empty":         "",
		"garbage":       "not a key",
		"too small":     pkcs1PEM(smallRSAKey(t)),
		"small pkcs8":   pkcs8PEM(t, smallRSAKey(t)),
		"ec key":        pkcs8PEM(t, ecKey(t)),
		"public key":    string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pub})),
		"corrupt body":  string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: []byte("junk")})),
		"corrupt pkcs8": string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("junk")})),
		"oversize":      strings.Repeat("A", githubapp.MaxPEMBytes+1),
	}
	for name, text := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := githubapp.ParsePrivateKey([]byte(text))
			require.ErrorIs(t, err, githubapp.ErrInvalidKey)
		})
	}
}

func TestParsePrivateKey_ErrorsNeverContainTheKeyMaterial(t *testing.T) {
	good := pkcs1PEM(rsaKey(t, 0))
	body := strings.Split(good, "\n")[1]
	// A corrupted copy of a real key: its text is as secret as the key.
	corrupted := strings.Replace(good, body, body[:len(body)-4]+"AAAA", 1)
	_, err := githubapp.ParsePrivateKey([]byte(corrupted))
	require.Error(t, err)
	assert.NotContains(t, err.Error(), body[:20])
}

func TestPrivateKey_NeverPrintsItself(t *testing.T) {
	k, err := githubapp.ParsePrivateKey([]byte(pkcs1PEM(rsaKey(t, 0))))
	require.NoError(t, err)
	secret, err := k.PEM()
	require.NoError(t, err)
	needle := strings.Split(string(secret), "\n")[1]

	var logs strings.Builder
	slog.New(slog.NewJSONHandler(&logs, nil)).Info("key", "key", k, slog.Any("value", *k))
	asJSON, err := json.Marshal(map[string]any{"key": k, "value": *k})
	require.NoError(t, err)
	var logged map[string]any
	require.NoError(t, json.Unmarshal([]byte(logs.String()), &logged))
	assert.Equal(t, "[redacted]", logged["key"], "slog resolves the key's LogValue")
	assert.Equal(t, "[redacted]", logged["value"])
	assert.JSONEq(t, `{"key":"[redacted]","value":"[redacted]"}`, string(asJSON))

	outputs := map[string]string{
		"%v": fmt.Sprintf("%v", k), "%+v": fmt.Sprintf("%+v", k), "%#v": fmt.Sprintf("%#v", k),
		"%s": fmt.Sprintf("%s", k), "%q": fmt.Sprintf("%q", k), "%v value": fmt.Sprintf("%v", *k),
		"%+v value": fmt.Sprintf("%+v", *k), "%v in struct": fmt.Sprintf("%+v", struct{ K *githubapp.PrivateKey }{k}),
		"Println": fmt.Sprintln(k), "error": fmt.Errorf("wrapped: %v", k).Error(),
		"String": k.String(), "slog": logs.String(), "json": string(asJSON),
	}
	for name, out := range outputs {
		assert.NotContains(t, out, needle, name)
		assert.NotContains(t, out, "PRIVATE KEY", name)
		assert.Contains(t, out, "[redacted]", name)
	}
}
