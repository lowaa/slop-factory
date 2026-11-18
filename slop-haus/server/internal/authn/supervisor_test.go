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

	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit/audittest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
)

type supervisorIdentity struct{ agentIdentity }

func (supervisorIdentity) IsSupervisor() bool { return true }

// A supervisor creates workers and mints their credentials, so an agent route
// that never heard of supervisors is closed to one; a supervisor's own routes
// are closed to everyone else; and the few routes both use say so.
func TestSupervisorsAndOtherAgentsAreEachKeptToTheirRoutes(t *testing.T) {
	tenant := uuid.New()
	authenticate := func(_ context.Context, token string) (authn.Identity, error) {
		switch token {
		case "worker":
			return agentIdentity{id: uuid.New(), tenant: tenant}, nil
		case "supervisor":
			return supervisorIdentity{agentIdentity{id: uuid.New(), tenant: tenant}}, nil
		}
		return nil, errors.New("unknown token")
	}
	mux := http.NewServeMux()
	api := humago.New(mux, huma.DefaultConfig("test", "0.0.0"))
	logger := slog.New(slog.DiscardHandler)
	api.UseMiddleware(authn.DefaultDeny(api, logger))
	events, counted := audittest.NewRecorder()
	api.UseMiddleware(authn.HumaMiddleware(api, authenticate, events, logger))
	reached := map[string]int{}
	for path, md := range map[string]map[string]any{
		"/plain":      authn.RequireAgent(),
		"/supervisor": authn.RequireSupervisor(),
		"/shared":     authn.RequireAgentOrSupervisor(),
	} {
		huma.Register(api, huma.Operation{Metadata: md, OperationID: path[1:], Method: http.MethodGet, Path: path},
			func(context.Context, *struct{}) (*struct{}, error) {
				reached[path]++
				return nil, nil
			})
	}
	get := func(path, token string) int {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec.Code
	}

	// refusals counts the audit events so far, so each request is held to
	// having recorded one exactly when it was refused.
	refusals := 0
	expect := func(want int, path, token string, audited bool, msg ...any) {
		t.Helper()
		assert.Equal(t, want, get(path, token), msg...)
		if audited {
			refusals++
		}
		assert.Equal(t, refusals, counted.Count(audit.SupervisorRouteRefused), "%s as %s", path, token)
	}
	expect(http.StatusNoContent, "/plain", "worker", false)
	expect(http.StatusForbidden, "/plain", "supervisor", true, "a route that never heard of supervisors is closed to one")
	expect(http.StatusForbidden, "/supervisor", "worker", true, "the supervisor's own routes are no other agent's")
	expect(http.StatusNoContent, "/supervisor", "supervisor", false)
	expect(http.StatusNoContent, "/shared", "worker", false)
	expect(http.StatusNoContent, "/shared", "supervisor", false)
	expect(http.StatusUnauthorized, "/supervisor", "forged", false)

	assert.Equal(t, map[string]int{"/plain": 1, "/supervisor": 1, "/shared": 2}, reached)
	assert.Equal(t, 1, authn.Markers(&huma.Operation{Metadata: authn.RequireSupervisor()}), "still one marker, so DefaultDeny serves it")
}
