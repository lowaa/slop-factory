package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
)

// NewOpaqueToken generates a random token for activation/password-reset
// links. raw is what's emailed to the user and never stored; hash is what's
// persisted so a DB compromise doesn't leak usable tokens.
func NewOpaqueToken() (raw string, hash string, err error) {
	raw, sum, err := NewOpaqueTokenWithPrefix("")
	if err != nil {
		return "", "", err
	}
	return raw, hex.EncodeToString(sum), nil
}

// NewOpaqueTokenWithPrefix is a token of prefix followed by 256 random bits,
// with the raw SHA-256 of the whole token (prefix included) as the hash to
// store. A prefix makes a leaked token recognisable in logs and to secret
// scanners.
func NewOpaqueTokenWithPrefix(prefix string) (raw string, hash []byte, err error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", nil, fmt.Errorf("auth: generate token: %w", err)
	}
	raw = prefix + base64.RawURLEncoding.EncodeToString(buf)
	return raw, HashOpaqueTokenBytes(raw), nil
}

func HashOpaqueToken(raw string) string {
	return hex.EncodeToString(HashOpaqueTokenBytes(raw))
}

// HashOpaqueTokenBytes is HashOpaqueToken as the raw digest, for stores that
// keep the hash as bytes. HashOpaqueToken is its hex encoding.
func HashOpaqueTokenBytes(raw string) []byte {
	sum := sha256.Sum256([]byte(raw))
	return sum[:]
}

// CheckOpaqueToken reports whether raw is the token stored as hash, in
// constant time in the hashes.
func CheckOpaqueToken(raw string, hash []byte) bool {
	return subtle.ConstantTimeCompare(HashOpaqueTokenBytes(raw), hash) == 1
}
