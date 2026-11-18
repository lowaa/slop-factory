package hookkit

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
)

// MinKeyBytes is HMAC-SHA256's key size: a shorter key is weaker than the
// MAC it keys. config refuses a shorter key; this is the backstop.
const MinKeyBytes = 32

// ErrKeyTooShort is a key under MinKeyBytes.
var ErrKeyTooShort = errors.New("key is too short")

// SecretDeriver derives a door's per-endpoint signing secrets from one key.
// Nothing derived is stored, so a copy of the database forges nothing.
//
// The label binds a secret to one use of the key and to a version, so the
// derivation can change without an old secret meaning anything under the
// new one. Changing a door's label, or how parts are joined, orphans every
// secret already handed to a sender; the doors pin known answers for that.
type SecretDeriver struct {
	key   []byte
	label string
}

func NewSecretDeriver(key []byte, label string) (*SecretDeriver, error) {
	if len(key) < MinKeyBytes {
		return nil, ErrKeyTooShort
	}
	return &SecretDeriver{key: append([]byte(nil), key...), label: label}, nil
}

// Derive is the hex secret for the endpoint named by parts (an id, and for
// a door that rotates, a generation). Parts are joined with NUL, which keeps
// ("ab","c") apart from ("a","bc") as long as no part holds a NUL itself:
// callers pass ids and numbers, never free text.
func (d *SecretDeriver) Derive(parts ...string) string {
	mac := hmac.New(sha256.New, d.key)
	mac.Write([]byte(d.label))
	mac.Write([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(mac.Sum(nil))
}

// SignaturePrefix is how a signature header names its algorithm.
const SignaturePrefix = "sha256="

// Sign is the header value, "sha256=<hex>", for body signed with secret:
// HMAC-SHA256 keyed with the secret's bytes as typed into the sender's form.
func Sign(secret string, body []byte) string {
	return SignaturePrefix + hex.EncodeToString(mac(secret, body))
}

// VerifySignature reports whether header is Sign(secret, body). Constant
// time in what matters: the comparison of the MACs.
func VerifySignature(secret string, body []byte, header string) bool {
	hexSig, ok := strings.CutPrefix(header, SignaturePrefix)
	if !ok {
		return false
	}
	got, err := hex.DecodeString(hexSig)
	if err != nil {
		return false
	}
	return hmac.Equal(got, mac(secret, body))
}

func mac(secret string, body []byte) []byte {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write(body)
	return m.Sum(nil)
}
