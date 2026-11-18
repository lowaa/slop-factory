package authn

import (
	"context"
	"errors"
	"log/slog"

	"github.com/danielgtaylor/huma/v2"

	goauthn "github.com/lowaa/slop-factory/go-auth/authn"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
)

// User is a signed-in person, acting within one tenant.
type User = goauthn.User

// UserVerifier turns an access token into the user it was issued to, or an
// error for one that is missing, malformed, expired or forged.
type UserVerifier = goauthn.UserVerifier

// SessionCookie carries the access token for the board. httpOnly, so no
// script on the page -- including one injected through text an agent wrote
// -- can read it, and the board's change stream authenticates with it, which an
// Authorization header could not do: EventSource cannot set one.
const SessionCookie = "slop_haus_session"

// RequireUser marks an operation as needing a signed-in user.
func RequireUser() map[string]any {
	return goauthn.RequireUser()
}

// OptionalUser marks an operation open to anyone that uses the signed-in
// user when there is one (see go-auth's OptionalUser, including how a
// refused credential is treated).
func OptionalUser() map[string]any {
	return goauthn.OptionalUser()
}

// CredentialRejected reports whether an OptionalUser request named a
// credential that was refused, so it is anonymous only for want of a good
// one.
func CredentialRejected(ctx context.Context) bool {
	return goauthn.CredentialRejected(ctx)
}

// ErrNoUser means a route that needs a user was reached without one --
// mounted outside the middleware, a wiring bug.
var ErrNoUser = goauthn.ErrNoUser

// UserFromContext returns the user the middleware resolved.
func UserFromContext(ctx context.Context) (*User, error) {
	return goauthn.UserFromContext(ctx)
}

// NewUserContext records the signed-in user and scopes ctx to their tenant,
// together, for the same reason NewContext does for agents.
func NewUserContext(ctx context.Context, u *User) context.Context {
	return scopeToTenant(goauthn.NewUserContext(ctx, u), u)
}

func scopeToTenant(ctx context.Context, u *User) context.Context {
	return db.WithTenant(ctx, u.TenantID)
}

// UserMiddleware enforces a signed-in user on operations marked
// RequireUser or RequireAdmin, from the session cookie or a bearer token,
// scoping each request to the user's tenant (see go-auth's
// UserMiddleware).
func UserMiddleware(api huma.API, verify UserVerifier, guard *BrowserGuard, logger *slog.Logger) func(huma.Context, func(huma.Context)) {
	return goauthn.UserMiddleware(api, &goauthn.UserMiddlewareConfig{
		Verify:        verify,
		Guard:         guard,
		SessionCookie: SessionCookie,
		UserMarkers:   []*goauthn.Marker{adminMarker},
		Scope:         scopeToTenant,
		Logger:        logger.Handler(),
	})
}

// ErrNotPerson means an action only a signed-in person may take was
// attempted without one. Services return it so every transport is refused
// alike, not just the one whose handler remembered to check.
var ErrNotPerson = errors.New("authn: only a signed-in person can do this")

// PersonFromContext is the signed-in person behind the request, or nil when
// the caller is anything else. It is for handlers handing identity to a
// service: the service decides, via RequirePerson, whether nil is allowed.
func PersonFromContext(ctx context.Context) *User {
	who, err := goauthn.UserFromContext(ctx)
	if err != nil {
		return nil
	}
	return who
}

// RequirePerson refuses a nil caller with ErrNotPerson.
func RequirePerson(who *User) error {
	if who == nil {
		return ErrNotPerson
	}
	return nil
}

type serverActorKey struct{}

// AsServer marks ctx as work the server does on a tenant's behalf (a
// background job or the steward's own act), not a person's request. Only
// in-process code can set it; it grants nothing by itself, the services that
// honour it decide what the server may do.
func AsServer(ctx context.Context) context.Context {
	return context.WithValue(ctx, serverActorKey{}, true)
}

// IsServer says whether ctx was marked by AsServer.
func IsServer(ctx context.Context) bool {
	v, _ := ctx.Value(serverActorKey{}).(bool)
	return v
}
