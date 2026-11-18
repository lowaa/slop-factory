package authn_test

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit/audittest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
)

type agentIdentity struct{ id, tenant uuid.UUID }

func (a agentIdentity) AgentID() uuid.UUID  { return a.id }
func (a agentIdentity) AgentName() string   { return "builder" }
func (a agentIdentity) TenantID() uuid.UUID { return a.tenant }
func (a agentIdentity) ReadOnly() bool      { return false }

// An agent's request is scoped to the agent's tenant by the same step that
// authenticates it; a handler never has to remember to do it.
func TestAnAgentRequestIsScopedToTheAgentsTenant(t *testing.T) {
	agent := agentIdentity{id: uuid.New(), tenant: uuid.New()}
	authenticate := func(_ context.Context, token string) (authn.Identity, error) {
		if token == "agent-token" {
			return agent, nil
		}
		return nil, errors.New("unknown token")
	}

	mux := http.NewServeMux()
	api := humago.New(mux, huma.DefaultConfig("test", "0.0.0"))
	logger := slog.New(slog.DiscardHandler)
	api.UseMiddleware(authn.DefaultDeny(api, logger))
	api.UseMiddleware(authn.HumaMiddleware(api, authenticate, audit.New(slog.DiscardHandler, nil), logger))
	huma.Register(api, huma.Operation{Metadata: authn.RequireAgent(), OperationID: "agent", Method: http.MethodGet, Path: "/agent"},
		func(ctx context.Context, _ *struct{}) (*whoResponse, error) {
			resp := &whoResponse{}
			if id, err := authn.FromContext(ctx); err == nil {
				resp.Body.UserID = id.AgentID()
			}
			if tenant, err := db.TenantFrom(ctx); err == nil {
				resp.Body.TenantID = tenant
			}
			return resp, nil
		})

	serveAs := func(token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/agent", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	rec := serveAs("agent-token")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), agent.id.String())
	assert.Contains(t, rec.Body.String(), agent.tenant.String())

	assert.Equal(t, http.StatusUnauthorized, serveAs("forged").Code)
}

type readOnlyIdentity struct{ agentIdentity }

func (readOnlyIdentity) ReadOnly() bool { return true }

// A read-only credential authenticates, but no agent route will serve it:
// every one of them writes or records activity, so the refusal is the
// middleware's rather than each handler's.
func TestAReadOnlyAgentIsRefusedAgentRoutes(t *testing.T) {
	authenticate := func(_ context.Context, token string) (authn.Identity, error) {
		if token == "read-only" {
			return readOnlyIdentity{agentIdentity{id: uuid.New(), tenant: uuid.New()}}, nil
		}
		return nil, errors.New("unknown token")
	}

	mux := http.NewServeMux()
	api := humago.New(mux, huma.DefaultConfig("test", "0.0.0"))
	logger := slog.New(slog.DiscardHandler)
	api.UseMiddleware(authn.DefaultDeny(api, logger))
	events, counted := audittest.NewRecorder()
	api.UseMiddleware(authn.HumaMiddleware(api, authenticate, events, logger))
	reached := false
	huma.Register(api, huma.Operation{Metadata: authn.RequireAgent(), OperationID: "agent", Method: http.MethodGet, Path: "/agent"},
		func(context.Context, *struct{}) (*struct{}, error) {
			reached = true
			return nil, nil
		})

	req := httptest.NewRequest(http.MethodGet, "/agent", nil)
	req.Header.Set("Authorization", "Bearer read-only")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	assert.False(t, reached)
	assert.Equal(t, 1, counted.Count(audit.ReadOnlyRefused))
}
