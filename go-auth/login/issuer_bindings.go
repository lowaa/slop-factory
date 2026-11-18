package login

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// GitHubIssuer is what GitHubProviderName binds to: a GitHub OAuth app has
// no per-deployment issuer to configure, so every deployment shares it.
const GitHubIssuer = "https://github.com"

// NormalizeIssuer canonicalises an OIDC issuer URL for comparison and
// storage: a trailing slash is insignificant to discovery, but would
// otherwise make one issuer look like two.
func NormalizeIssuer(raw string) string {
	return strings.TrimSuffix(strings.TrimSpace(raw), "/")
}

// Querier runs IssuerBindings' statements. It is pgx's shape, which each
// service's own database package adapts to.
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// IssuerBindings binds each login provider's name to the issuer it was
// first registered with.
//
// Identities are keyed (provider, subject), and a subject is only unique
// within the issuer that minted it. If an operator later pointed a
// provider name at a different issuer, the new issuer's subjects would be
// looked up inside the old issuer's namespace, and a subject the new
// issuer happened to reuse would sign in as whichever account the old one
// belonged to -- an account takeover, not just a mix-up. A service
// registers every configured provider at startup, and Register refuses to
// let a name change issuer, so that mistake fails loudly at startup
// instead of silently at whoever's sign-in collides with it.
//
// The bindings live in the service's own schema, in a table it creates:
//
//	CREATE TABLE global_auth_providers (
//	    name   TEXT PRIMARY KEY,
//	    issuer TEXT NOT NULL
//	);
//
// The queries run with no tenant: the table is global, and read before any
// tenant is known.
type IssuerBindings struct {
	q Querier
}

// NewIssuerBindings stores bindings through q.
func NewIssuerBindings(q Querier) *IssuerBindings {
	return &IssuerBindings{q: q}
}

// Register binds name to issuer, compared and stored as NormalizeIssuer
// has it. The first call for a name records it; a later call with the same
// issuer (an ordinary restart) is a no-op, and one with a different issuer
// is refused, naming both.
func (b *IssuerBindings) Register(ctx context.Context, name, issuer string) error {
	issuer = NormalizeIssuer(issuer)
	if _, err := b.q.Exec(ctx, `
		INSERT INTO global_auth_providers (name, issuer) VALUES ($1, $2)
		ON CONFLICT (name) DO NOTHING`, name, issuer); err != nil {
		return fmt.Errorf("login: register provider %q: %w", name, err)
	}
	var stored string
	if err := b.q.QueryRow(ctx, `SELECT issuer FROM global_auth_providers WHERE name = $1`, name).Scan(&stored); err != nil {
		return fmt.Errorf("login: register provider %q: %w", name, err)
	}
	if stored != issuer {
		return fmt.Errorf(
			"login: provider %q is already registered to issuer %q, but this deployment is configured with issuer %q; "+
				"a provider name can never move to a different issuer -- subjects are only unique per issuer -- "+
				"give the new issuer its own provider name instead",
			name, stored, issuer)
	}
	return nil
}
