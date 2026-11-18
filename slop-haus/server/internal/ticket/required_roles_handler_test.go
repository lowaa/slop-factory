package ticket_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-auth/authn/authntest"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

const (
	rolesUserToken  = "user-token"
	rolesAgentToken = "agent-token"
)

// rolesMux is the ticket routes behind the auth middlewares cmd/api
// installs, in the same order, against the real stores: a signed-in person
// and an agent of the fixture's tenant each have a bearer token.
func (f *roleFixture) rolesMux(t *testing.T, withUserMiddleware bool) (http.Handler, uuid.UUID) {
	t.Helper()
	caller, err := f.agents.Create(f.ctx, "caller")
	require.NoError(t, err)

	mux := http.NewServeMux()
	api := humago.New(mux, huma.DefaultConfig("test", "0.0.0"))
	logger := slog.New(slog.DiscardHandler)
	api.UseMiddleware(authn.DefaultDeny(api, logger))
	api.UseMiddleware(authn.HumaMiddleware(api, func(_ context.Context, token string) (authn.Identity, error) {
		if token == rolesAgentToken {
			return caller, nil
		}
		return nil, errors.New("unknown agent token")
	}, audit.New(slog.DiscardHandler, nil), logger))
	if withUserMiddleware {
		api.UseMiddleware(authn.UserMiddleware(api, func(_ context.Context, token string) (*authn.User, error) {
			if token == rolesUserToken {
				return &authn.User{ID: f.human, TenantID: f.tenant}, nil
			}
			return nil, errors.New("unknown user token")
		}, authntest.Guard(), logger))
	}
	ticket.RegisterRoutes(api, ticket.NewHandler(f.svc, slog.DiscardHandler))
	return mux, caller.ID
}

func sendAs(t *testing.T, mux http.Handler, method, path, bearer, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestTicketRolesWire_APersonFilesAndReplacesThem(t *testing.T) {
	t.Parallel()
	f := newRoleFixture(t)
	mux, _ := f.rolesMux(t, true)
	deploy, review := f.role(t, "deploy"), f.role(t, "review")

	rec := sendAs(t, mux, http.MethodPost, "/api/tickets", rolesUserToken,
		fmt.Sprintf(`{"title": "Scoped", "role_ids": [%q, %q], "role_match": "all"}`, deploy, review))
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	created := decodeTicket(t, rec.Body.Bytes())
	assert.Equal(t, []ticket.RoleRefBody{{ID: deploy.String(), Name: "deploy"}, {ID: review.String(), Name: "review"}},
		created.RequiredRoles)
	assert.Equal(t, "all", created.RoleMatch)
	assert.True(t, created.NoEligibleAgent, "nobody has both")

	path := fmt.Sprintf("/api/tickets/%d/roles", created.ID)
	rec = sendAs(t, mux, http.MethodPut, path, rolesUserToken, fmt.Sprintf(`{"role_ids": [%q]}`, review))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	replaced := decodeTicket(t, rec.Body.Bytes())
	assert.Equal(t, []ticket.RoleRefBody{{ID: review.String(), Name: "review"}}, replaced.RequiredRoles)
	assert.Equal(t, "any", replaced.RoleMatch)

	rec = sendAs(t, mux, http.MethodPut, path, rolesUserToken, `{"role_ids": []}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"required_roles":[]`, "cleared, and [] rather than null")
	assert.Contains(t, rec.Body.String(), `"no_eligible_agent":false`)

	rec = sendAs(t, mux, http.MethodPut, path, rolesUserToken, fmt.Sprintf(`{"role_ids": [%q]}`, uuid.New()))
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code, "unknown role: %s", rec.Body.String())
	rec = sendAs(t, mux, http.MethodPut, path, rolesUserToken, `{"role_ids": ["not-a-uuid"]}`)
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
	rec = sendAs(t, mux, http.MethodPut, path, rolesUserToken, fmt.Sprintf(`{"role_ids": [%q], "role_match": "some"}`, review))
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
	rec = sendAs(t, mux, http.MethodPut, "/api/tickets/9999/roles", rolesUserToken, `{"role_ids": []}`)
	assert.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
}

// Only a person may change a ticket's required roles: clearing them, or
// switching all to any, opens the ticket to agents it was closed to. An
// agent's token is refused, and nothing changed.
func TestTicketRolesWire_AnAgentCannotChangeThem(t *testing.T) {
	t.Parallel()
	f := newRoleFixture(t)
	mux, caller := f.rolesMux(t, true)
	deploy := f.role(t, "deploy")
	scoped := f.requiring(t, "Scoped", model.RoleMatchAny, deploy)

	path := fmt.Sprintf("/api/tickets/%d/roles", scoped.ID)
	rec := sendAs(t, mux, http.MethodPut, path, rolesAgentToken, `{"role_ids": []}`)
	assert.Equal(t, http.StatusUnauthorized, rec.Code, rec.Body.String())

	// Nor can it loosen one through the board's PATCH, which has no role
	// field to carry it.
	rec = sendAs(t, mux, http.MethodPatch, fmt.Sprintf("/api/tickets/%d", scoped.ID), rolesAgentToken,
		`{"required_roles": [], "role_match": "any"}`)
	assert.Equal(t, http.StatusUnauthorized, rec.Code, rec.Body.String())

	assert.Equal(t, []string{"deploy"}, roleRefNames(f.get(t, scoped.ID).RequiredRoles), "still required")
	assert.Zero(t, f.claimed(t, caller))

	// The token itself works: it is refused for what it is.
	rec = sendAs(t, mux, http.MethodPost, "/api/claims", rolesAgentToken, `{}`)
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}

// The handler refuses a change with no signed-in person behind it, even if
// the route's auth marker were ever wrong and let the request through.
func TestTicketRolesWire_TheHandlerRefusesWithNoSignedInPerson(t *testing.T) {
	t.Parallel()
	f := newRoleFixture(t)
	mux, _ := f.rolesMux(t, false)
	scoped := f.requiring(t, "Scoped", model.RoleMatchAny, f.role(t, "deploy"))

	rec := sendAs(t, mux, http.MethodPut, fmt.Sprintf("/api/tickets/%d/roles", scoped.ID), "", `{"role_ids": []}`)
	assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	assert.Len(t, f.get(t, scoped.ID).RequiredRoles, 1)
}

// A directed claim of a ticket the agent lacks the role for is a 403 that
// names the role, not an empty queue.
func TestTicketRolesWire_ADirectedClaimWithoutTheRoleIs403(t *testing.T) {
	t.Parallel()
	f := newRoleFixture(t)
	mux, _ := f.rolesMux(t, true)
	scoped := f.requiring(t, "Scoped", model.RoleMatchAny, f.role(t, "deploy"))

	rec := sendAs(t, mux, http.MethodPost, "/api/claims", rolesAgentToken, fmt.Sprintf(`{"ticket_id": %d}`, scoped.ID))
	assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), fmt.Sprintf("#%d needs role deploy", scoped.ID))
}
