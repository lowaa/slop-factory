// Package authn resolves who a request is from, once, at the edge: an
// agent, by its bearer token, or a signed-in user, by their session. Either
// way the answer includes a tenant, and the request is scoped to it --
// every tenant-scoped query downstream runs as that tenant.
//
// It is slop-haus's policy over go-auth/authn, which does the work for
// signed-in users and browsers: this package adds agents and admins, the
// session cookie's name, and tenant scoping, and re-exports go-auth's
// vocabulary so the rest of slop-haus has one authn to import.
//
// Every transport goes through it -- REST, MCP, and the hook endpoints --
// so "who is this" has one answer rather than three. The agent token is
// what makes a ticket comment's author trustworthy: when two agents
// disagree about who broke the build, an unauthenticated log is unreliable
// exactly when it is needed most.
package authn

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
)

// Identity is all this package needs to know about whoever is calling.
//
// Deliberately not agent.Agent: the agent package's handler needs the
// ticket package (to show what an agent is holding), and ticket's handlers
// need authentication -- so an authn that imported agent would close a
// cycle. Keeping the identity abstract means auth can sit under both.
type Identity interface {
	AgentID() uuid.UUID
	AgentName() string
	// TenantID is the tenant the agent belongs to, which is the only
	// tenant its requests may touch.
	TenantID() uuid.UUID
	// ReadOnly reports a credential that may read and nothing else (the
	// smoke test's: see oauth.ScopeRead). Part of the interface rather
	// than an optional extra so every identity has to answer it -- one
	// that forgot would otherwise read as allowed to write.
	ReadOnly() bool
}

// Authenticator resolves a bearer token. It is a func rather than an
// interface because the concrete implementation returns *agent.Agent, and
// Go will not treat that as satisfying a method returning Identity.
type Authenticator func(ctx context.Context, token string) (Identity, error)

type contextKey struct{}

// ErrNoAgent is returned by FromContext when the request was never
// authenticated. It means a route was mounted outside the middleware, not
// that a caller sent a bad token -- a wiring bug, so it must fail loudly
// rather than degrade to anonymous.
var ErrNoAgent = errors.New("authn: request carries no authenticated agent")

// FromContext returns the agent the middleware resolved.
func FromContext(ctx context.Context) (Identity, error) {
	found, ok := ctx.Value(contextKey{}).(Identity)
	if !ok || found == nil {
		return nil, ErrNoAgent
	}
	return found, nil
}

// NewContext records the authenticated agent and scopes ctx to its tenant.
// The two happen together, here, so no transport can establish who a
// request is from without also fixing which tenant it may touch.
func NewContext(ctx context.Context, id Identity) context.Context {
	return db.WithTenant(context.WithValue(ctx, contextKey{}, id), id.TenantID())
}
