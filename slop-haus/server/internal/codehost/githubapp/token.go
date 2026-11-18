package githubapp

import (
	"time"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/secretbox"
)

// Token is an installation access token: it acts as the App on the repo
// and permissions it was minted for, until it expires.
type Token struct {
	secretbox.Redacted
	value     string
	expiresAt time.Time
}

var _ codehost.Token = (*Token)(nil)

// Reveal is the token's value, to put in an Authorization header and
// nowhere else.
func (t *Token) Reveal() string { return t.value }

// ExpiresAt is when GitHub stops accepting the token.
func (t *Token) ExpiresAt() time.Time { return t.expiresAt }
