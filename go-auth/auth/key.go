package auth

import (
	"crypto/sha256"
	"fmt"
	"io"

	"golang.org/x/crypto/hkdf"
)

// MinDerivedKeySecret is the least secret DeriveKey accepts: HMAC-SHA256's
// own key size.
const MinDerivedKeySecret = 32

// DeriveKey derives a 32-byte key from secret with HKDF-SHA256 under label.
//
// It is how one service secret (slop-haus's JWT signing secret, say) keys
// several unrelated MACs without any of them using the secret itself: a key
// derived under one label is useless for anything keyed under another, so a
// MAC one feature hands out can never be replayed as another's -- or as a
// token signature.
func DeriveKey(secret []byte, label string) ([]byte, error) {
	if len(secret) < MinDerivedKeySecret {
		return nil, fmt.Errorf("auth: a key derivation secret must be at least %d bytes", MinDerivedKeySecret)
	}
	if label == "" {
		return nil, fmt.Errorf("auth: a derived key needs a label of its own")
	}
	key := make([]byte, sha256.Size)
	if _, err := io.ReadFull(hkdf.New(sha256.New, secret, nil, []byte(label)), key); err != nil {
		return nil, fmt.Errorf("auth: derive key: %w", err)
	}
	return key, nil
}
