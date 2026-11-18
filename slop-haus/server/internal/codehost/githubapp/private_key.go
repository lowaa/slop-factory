package githubapp

import (
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/secretbox"
)

// MinKeyBits is the smallest RSA key accepted: GitHub issues 2048-bit keys,
// and anything shorter is not one it made.
const MinKeyBits = 2048

// MaxPEMBytes bounds what is parsed: a 4096-bit key is under 4 KiB.
const MaxPEMBytes = 16 << 10

// ErrInvalidKey means the text is not a usable GitHub App private key. It
// never says anything about the text beyond why it was refused: what a
// person pastes here is a secret, and an error is logged.
var ErrInvalidKey = errors.New("githubapp: not a usable RSA private key")

// PrivateKey is a GitHub App's private key. It prints as a placeholder
// however it is formatted, logged or encoded; the one way to its bytes is
// Seal's plaintext, PEM.
type PrivateKey struct {
	secretbox.Redacted
	key *rsa.PrivateKey
}

// ParsePrivateKey reads an RSA private key from PEM: PKCS#1 ("RSA PRIVATE
// KEY", what GitHub hands out) or PKCS#8 ("PRIVATE KEY"). Anything else --
// a certificate, a public key, an encrypted or EC key, an RSA key under
// MinKeyBits -- is refused. Text around the PEM block is ignored.
func ParsePrivateKey(text []byte) (*PrivateKey, error) {
	if len(text) > MaxPEMBytes {
		return nil, fmt.Errorf("%w: more than %d bytes", ErrInvalidKey, MaxPEMBytes)
	}
	block, _ := pem.Decode(text)
	if block == nil {
		return nil, fmt.Errorf("%w: no PEM block", ErrInvalidKey)
	}
	var key *rsa.PrivateKey
	switch block.Type {
	case "RSA PRIVATE KEY":
		k, err := x509.ParsePKCS1PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("%w: malformed PKCS#1 key", ErrInvalidKey)
		}
		key = k
	case "PRIVATE KEY":
		parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("%w: malformed PKCS#8 key", ErrInvalidKey)
		}
		k, ok := parsed.(*rsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("%w: not an RSA key", ErrInvalidKey)
		}
		key = k
	default:
		return nil, fmt.Errorf("%w: PEM type %q", ErrInvalidKey, block.Type)
	}
	if key.N.BitLen() < MinKeyBits {
		return nil, fmt.Errorf("%w: %d bits, at least %d needed", ErrInvalidKey, key.N.BitLen(), MinKeyBits)
	}
	if err := key.Validate(); err != nil {
		return nil, fmt.Errorf("%w: inconsistent key", ErrInvalidKey)
	}
	return &PrivateKey{key: key}, nil
}

// PEM is the key in the one form it is stored in -- PKCS#8 -- whatever it
// was given as, so what is encrypted carries nothing but the key.
func (k *PrivateKey) PEM() ([]byte, error) {
	der, err := x509.MarshalPKCS8PrivateKey(k.key)
	if err != nil {
		return nil, fmt.Errorf("githubapp: encode private key: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), nil
}

// Fingerprint is the SHA-256 of the key's public half in DER, hex: it says
// which key is connected without being any use to someone holding it.
func (k *PrivateKey) Fingerprint() (string, error) {
	der, err := x509.MarshalPKIXPublicKey(&k.key.PublicKey)
	if err != nil {
		return "", fmt.Errorf("githubapp: encode public key: %w", err)
	}
	sum := sha256.Sum256(der)
	return hex.EncodeToString(sum[:]), nil
}
