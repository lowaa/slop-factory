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

// adminMux mounts one admin route and one user route behind the real
// middleware chain, with the admin answer and its failure under the
// test's control.
func adminMux(isAdmin authn.AdminChecker) *http.ServeMux {
	mux, _ := countingAdminMux(isAdmin)
	return mux
}

// countingAdminMux is adminMux, counting admin refusals.
func countingAdminMux(isAdmin authn.AdminChecker) (*http.ServeMux, *audittest.Counter) {
	mux := http.NewServeMux()
	api := humago.New(mux, huma.DefaultConfig("test", "0.0.0"))
	logger := slog.New(slog.DiscardHandler)
	events, counter := audittest.NewRecorder()
	api.UseMiddleware(authn.DefaultDeny(api, logger))
	api.UseMiddleware(authn.UserMiddleware(api, verify, testGuard(), logger))
	api.UseMiddleware(authn.AdminMiddleware(api, isAdmin, events, logger))
	ok := func(context.Context, *struct{}) (*struct{}, error) { return &struct{}{}, nil }
	huma.Register(api, huma.Operation{Metadata: authn.RequireAdmin(), OperationID: "admin", Method: http.MethodGet, Path: "/admin"}, ok)
	huma.Register(api, huma.Operation{Metadata: authn.RequireUser(), OperationID: "user", Method: http.MethodGet, Path: "/user"}, ok)
	return mux, counter
}

func get(mux *http.ServeMux, path, bearer string) int {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec.Code
}

func TestAdminMiddleware(t *testing.T) {
	yes := func(context.Context, uuid.UUID) (bool, error) { return true, nil }
	no := func(context.Context, uuid.UUID) (bool, error) { return false, nil }
	broken := func(context.Context, uuid.UUID) (bool, error) { return false, errors.New("db down") }

	assert.Equal(t, http.StatusNoContent, get(adminMux(yes), "/admin", goodToken))
	assert.Equal(t, http.StatusForbidden, get(adminMux(no), "/admin", goodToken))
	assert.Equal(t, http.StatusUnauthorized, get(adminMux(yes), "/admin", ""), "an admin route authenticates like a user route")
	assert.Equal(t, http.StatusInternalServerError, get(adminMux(broken), "/admin", goodToken), "a failed check refuses, never lets through")
	assert.Equal(t, http.StatusNoContent, get(adminMux(no), "/user", goodToken), "user routes are not asked about admin")
}

// A signed-in non-admin at an admin route is recorded: the board never
// offers them one, so it is someone probing. Nothing else is.
func TestAdminRefusalsAreRecorded(t *testing.T) {
	no := func(context.Context, uuid.UUID) (bool, error) { return false, nil }
	yes := func(context.Context, uuid.UUID) (bool, error) { return true, nil }

	mux, events := countingAdminMux(no)
	get(mux, "/admin", goodToken)
	get(mux, "/user", goodToken)
	get(mux, "/admin", "")
	assert.Equal(t, 1, events.Count(audit.AdminRefused), "the refused admin request, not the user route or the unauthenticated one")

	mux, events = countingAdminMux(yes)
	get(mux, "/admin", goodToken)
	assert.Zero(t, events.Count(audit.AdminRefused))
}

// The admin question goes to the database with the signed-in user's id,
// not anyone else's.
func TestAdminMiddlewareAsksAboutTheSignedInUser(t *testing.T) {
	var asked uuid.UUID
	check := func(_ context.Context, id uuid.UUID) (bool, error) { asked = id; return true, nil }
	get(adminMux(check), "/admin", goodToken)
	assert.Equal(t, signedIn.ID, asked)
}
