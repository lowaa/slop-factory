package oauth

import (
	"context"
	"log/slog"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
)

// ClientAdmin makes and revokes read-only clients. Apart from Service
// because nothing in the server does this: an operator does, from a
// command (cmd/smoke_client), which needs the store and the resource and
// none of the server's rate limits or TTLs.
type ClientAdmin struct {
	store    Store
	resource string
	events   *audit.Recorder
	logger   *slog.Logger
}

// NewClientAdmin administers clients of the server whose resource is
// resource (see ResourceFor).
func NewClientAdmin(store Store, resource string, events *audit.Recorder, logHandler slog.Handler) *ClientAdmin {
	if events == nil {
		events = audit.New(logHandler, nil)
	}
	return &ClientAdmin{store: store, resource: resource, events: events, logger: slog.New(logHandler)}
}

// CreateReadOnlyClient makes a confidential client acting as agentID in
// tenantID, limited to ScopeRead, and returns its id and secret. The
// secret is only ever returned here: the store keeps its hash.
func (a *ClientAdmin) CreateReadOnlyClient(ctx context.Context, tenantID, agentID uuid.UUID, name string) (clientID, secret string, err error) {
	secret, secretHash, err := newSecret()
	if err != nil {
		return "", "", err
	}
	c := &Client{ID: "ro-" + uuid.NewString(), Name: name, GrantTypes: []string{GrantClientCredentials}, SecretHash: secretHash}
	g := &Grant{ID: uuid.New(), TenantID: tenantID, AgentID: agentID, ClientID: c.ID, Resource: a.resource, Scope: ScopeRead}
	if err := a.store.CreateReadOnlyClient(ctx, c, g); err != nil {
		return "", "", err
	}
	a.events.Record(ctx, audit.ReadOnlyClientCreated, "client_id", c.ID, "client_name", name, "tenant", tenantID, "agent_id", agentID)
	return c.ID, secret, nil
}

// RevokeClient ends a client's grants and so every token it holds.
func (a *ClientAdmin) RevokeClient(ctx context.Context, clientID string) error {
	if err := a.store.RevokeClient(ctx, clientID); err != nil {
		return err
	}
	a.events.Record(ctx, audit.ReadOnlyClientRevoked, "client_id", clientID)
	return nil
}
