package control

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"

	"github.com/lowaa/slop-factory/slop-bot/internal/ident"
)

// TokenEnv is the environment variable an agent's token reaches its harness
// in, and from there every `slop-bot` call the agent makes.
const TokenEnv = "SLOP_BOT_AGENT_TOKEN"

// ErrUnknownToken is a token no live agent holds: a stale token from an
// ended agent, or a made-up one. It is refused, never treated as the user.
var ErrUnknownToken = errors.New("control: unknown agent token")

// TokenResolver finds the agent a token belongs to, by the token's hash.
type TokenResolver interface {
	AgentForTokenHash(ctx context.Context, hash string) (ident.Principal, error)
}

// NewToken makes an agent token: 32 random bytes, URL-safe.
func NewToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// HashToken is what state.db stores instead of the token itself.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

type principalKey struct{}

// WithPrincipal stores who is calling in ctx.
func WithPrincipal(ctx context.Context, p ident.Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

// PrincipalFrom returns who is calling. Handlers only run behind Authenticate,
// so a missing principal is a wiring bug and is reported as the zero
// (invalid) principal, which every permission check refuses.
func PrincipalFrom(ctx context.Context) ident.Principal {
	p, _ := ctx.Value(principalKey{}).(ident.Principal)
	return p
}

// Authenticate resolves each request's principal: no token is the user, and
// a token must belong to a live agent.
//
// This tells a confused agent apart from the user; it is not a security
// boundary. An agent runs as the user's uid and can drop its token. The
// user accepted that risk (design doc, "Control socket").
func Authenticate(resolver TokenResolver, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := bearer(r)
		if !ok {
			next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), ident.UserPrincipal())))
			return
		}
		p, err := resolver.AgentForTokenHash(r.Context(), HashToken(token))
		if err != nil || !p.Valid() || p.IsUser() {
			WriteError(w, http.StatusUnauthorized, ErrUnknownToken)
			return
		}
		next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), p)))
	})
}

func bearer(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	if h == "" {
		return "", false
	}
	token, ok := strings.CutPrefix(h, "Bearer ")
	return token, ok && token != ""
}
