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
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
)

// go-auth's authn tests the middleware and the guard themselves; these
// test what slop-haus configures them with: its cookie, its tenancy and
// its markers.

const (
	goodToken = "good-token"
	board     = "https://slop.example"
)

var signedIn = &authn.User{ID: uuid.New(), TenantID: uuid.New(), SessionID: "session-a"}

func verify(_ context.Context, token string) (*authn.User, error) {
	if token == goodToken {
		return signedIn, nil
	}
	return nil, errors.New("bad token")
}

func csrfOf(sessionID string) string { return "csrf-of-" + sessionID }

func validCSRF(sessionID, token string) bool {
	return sessionID != "" && token == csrfOf(sessionID)
}

func testGuard() *authn.BrowserGuard {
	g, err := authn.NewBrowserGuard(board, validCSRF, audit.New(slog.DiscardHandler, nil))
	if err != nil {
		panic(err)
	}
	return g
}

type whoResponse struct {
	Body struct {
		UserID   uuid.UUID `json:"user_id"`
		TenantID uuid.UUID `json:"tenant_id"`
	}
}

func who(ctx context.Context, _ *struct{}) (*whoResponse, error) {
	resp := &whoResponse{}
	if u, err := authn.UserFromContext(ctx); err == nil {
		resp.Body.UserID = u.ID
	}
	if tenant, err := db.TenantFrom(ctx); err == nil {
		resp.Body.TenantID = tenant
	}
	return resp, nil
}

// userMux mounts a user route behind the chain cmd/api installs.
func userMux() *http.ServeMux {
	mux := http.NewServeMux()
	api := humago.New(mux, huma.DefaultConfig("test", "0.0.0"))
	logger := slog.New(slog.DiscardHandler)
	guard := testGuard()
	api.UseMiddleware(authn.DefaultDeny(api, logger))
	api.UseMiddleware(guard.PublicWrites(api))
	api.UseMiddleware(authn.UserMiddleware(api, verify, guard, logger))
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		huma.Register(api, huma.Operation{Metadata: authn.RequireUser(), OperationID: "user" + method, Method: method, Path: "/user"}, who)
	}
	return mux
}

func withSession(req *http.Request) *http.Request {
	req.AddCookie(&http.Cookie{Name: authn.SessionCookie, Value: goodToken})
	return req
}

func TestASignedInRequestIsScopedToTheUsersTenant(t *testing.T) {
	rec := httptest.NewRecorder()
	userMux().ServeHTTP(rec, withSession(httptest.NewRequest(http.MethodGet, "/user", nil)))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), signedIn.ID.String())
	assert.Contains(t, rec.Body.String(), signedIn.TenantID.String(), "the db tenant is set with the user")
}

// The board's session cookie authenticates, and its writes are checked.
func TestTheBoardsSessionCookieAuthenticatesAndIsGuarded(t *testing.T) {
	mux := userMux()
	write := func(csrf string) int {
		req := withSession(httptest.NewRequest(http.MethodPost, "/user", nil))
		req.Header.Set("Origin", board)
		if csrf != "" {
			req.Header.Set(authn.CSRFHeader, csrf)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec.Code
	}
	assert.Equal(t, http.StatusOK, write(csrfOf(signedIn.SessionID)))
	assert.Equal(t, http.StatusForbidden, write(""))
}

func TestNewUserContextScopesToTheTenant(t *testing.T) {
	ctx := authn.NewUserContext(context.Background(), signedIn)
	u, err := authn.UserFromContext(ctx)
	require.NoError(t, err)
	assert.Equal(t, signedIn, u)
	tenant, err := db.TenantFrom(ctx)
	require.NoError(t, err)
	assert.Equal(t, signedIn.TenantID, tenant)
}

func TestNewBrowserGuardRefusesANilRecorder(t *testing.T) {
	_, err := authn.NewBrowserGuard(board, validCSRF, nil)
	assert.Error(t, err)
}

// Every one of slop-haus's four markers is a decision; none, or two, is
// refused.
func TestDefaultDenyKnowsEveryMarker(t *testing.T) {
	for name, metadata := range map[string]map[string]any{
		"agent":  authn.RequireAgent(),
		"user":   authn.RequireUser(),
		"admin":  authn.RequireAdmin(),
		"public": authn.Public(),
	} {
		op := &huma.Operation{Metadata: metadata}
		assert.Equal(t, 1, authn.Markers(op), name)
		assert.Equal(t, name, authn.MarkerName(op))
	}
	both := authn.RequireAgent()
	for k, v := range authn.RequireAdmin() {
		both[k] = v
	}
	assert.Equal(t, 2, authn.Markers(&huma.Operation{Metadata: both}))
	assert.Equal(t, 0, authn.Markers(&huma.Operation{}))

	mux := http.NewServeMux()
	api := humago.New(mux, huma.DefaultConfig("test", "0.0.0"))
	api.UseMiddleware(authn.DefaultDeny(api, slog.New(slog.DiscardHandler)))
	huma.Register(api, huma.Operation{OperationID: "unmarked", Method: http.MethodGet, Path: "/unmarked"}, who)
	huma.Register(api, huma.Operation{Metadata: both, OperationID: "both", Method: http.MethodGet, Path: "/both"}, who)
	huma.Register(api, huma.Operation{Metadata: authn.Public(), OperationID: "public", Method: http.MethodGet, Path: "/public"}, who)
	for path, want := range map[string]int{"/unmarked": http.StatusInternalServerError, "/both": http.StatusInternalServerError, "/public": http.StatusOK} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		assert.Equal(t, want, rec.Code, path)
	}
}

func TestRequirePersonRefusesNobodyAndPersonFromContextIsNilForNoUser(t *testing.T) {
	assert.ErrorIs(t, authn.RequirePerson(nil), authn.ErrNotPerson)
	assert.NoError(t, authn.RequirePerson(&authn.User{ID: uuid.New()}))

	assert.Nil(t, authn.PersonFromContext(context.Background()))
	who := &authn.User{ID: uuid.New(), TenantID: uuid.New()}
	assert.Same(t, who, authn.PersonFromContext(authn.NewUserContext(context.Background(), who)))
}
