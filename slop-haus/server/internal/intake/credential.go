package intake

import (
	"fmt"
	"strconv"

	"github.com/google/uuid"
	"github.com/lowaa/slop-factory/go-auth/auth"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/hookkit"
)

// Credentials are one of two kinds, as the sender's system prefers.
//
// A bearer token is 256 random bits; only its SHA-256 is stored, so a copy
// of the database holds nothing that can be presented. A fast hash is
// right for a secret that is not a password: there is nothing to guess.
//
// An HMAC secret cannot be stored hashed, because checking a signature
// means recomputing it. It is derived from INTAKE_KEY, the source's id and
// its generation instead, and stored nowhere -- rotation raises the
// generation, which orphans the old secret.

// tokenPrefix marks a bearer token so a leaked one is recognisable in logs
// and by secret scanners.
const tokenPrefix = "shi_"

// NewToken is a fresh bearer token and the hash to store for it.
func NewToken() (token string, hash []byte, err error) {
	return auth.NewOpaqueTokenWithPrefix(tokenPrefix)
}

// HashToken is what is stored for a token.
func HashToken(token string) []byte {
	return auth.HashOpaqueTokenBytes(token)
}

// CheckToken reports whether token is the one stored as hash, in constant
// time in the hashes: a stranger learns nothing about the stored one from
// how long a wrong guess takes.
func CheckToken(token string, hash []byte) bool {
	return auth.CheckOpaqueToken(token, hash)
}

// Secrets derives each HMAC source's signing secret.
type Secrets struct {
	deriver *hookkit.SecretDeriver
}

func NewSecrets(key []byte) (*Secrets, error) {
	d, err := hookkit.NewSecretDeriver(key, secretLabel)
	if err != nil {
		return nil, fmt.Errorf("intake: intake %w", err)
	}
	return &Secrets{deriver: d}, nil
}

const secretLabel = "slop-haus intake secret v1\x00"

// For is the secret the sender signs with, as hex: any string works in the
// sender's config.
func (s *Secrets) For(id uuid.UUID, generation int) string {
	return s.deriver.Derive(id.String(), strconv.Itoa(generation))
}

// SignatureHeader is where the sender's signature of the body goes, as
// "sha256=<hex>", the form GitHub uses.
const SignatureHeader = "X-Slophaus-Signature"

// Sign is the header value for body signed with secret.
func Sign(secret string, body []byte) string {
	return hookkit.Sign(secret, body)
}

// Verify reports whether header is the signature of the raw body with the
// source's secret.
func (s *Secrets) Verify(id uuid.UUID, generation int, body []byte, header string) bool {
	return hookkit.VerifySignature(s.For(id, generation), body, header)
}
