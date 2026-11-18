package authn

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"
)

// User is a signed-in person, acting within one tenant.
type User struct {
	ID       uuid.UUID
	TenantID uuid.UUID
	// IssuedAt is when the session's access token was issued. Anything
	// that mints a new session from this one checks it against the
	// account's revocation cut-off, as refresh does.
	IssuedAt time.Time
	// AuthTime is when the person last proved who they are interactively
	// -- a sign-in or a re-authentication -- carried unchanged through
	// refreshes and tenant switches. Zero for a session from before auth
	// times were recorded, which counts as long ago. Sensitive changes to
	// an account ask for it to be recent.
	AuthTime time.Time
	// SessionID names the browser session the token was issued for, minted
	// at an interactive sign-in and carried unchanged through refreshes,
	// tenant switches and re-authentications. It is what the session's CSRF
	// token is bound to (see BrowserGuard). Empty for a session from before
	// session ids, which therefore cannot write.
	SessionID string
}

// UserVerifier turns an access token into the user it was issued to, or an
// error for one that is missing, malformed, expired or forged.
type UserVerifier func(ctx context.Context, accessToken string) (*User, error)

type (
	userContextKey        struct{}
	credentialRejectedKey struct{}
)

// CredentialRejectedHeader is set, to CredentialRejectedValue, on the
// response to an OptionalUser request whose credential was refused --
// expired, revoked, forged or malformed, which it does not say apart. The
// request carried on anonymous rather than 401, so this is how a page that
// believes it is signed in learns to refresh its session and retry.
const (
	CredentialRejectedHeader = "X-Auth-Credential"
	CredentialRejectedValue  = "rejected"
)

// CredentialRejected reports whether an OptionalUser request named a
// credential that was refused, so it is anonymous only for want of a good
// one. A handler whose answer differs for a signed-in person -- one that
// would file a decision in the anonymous place, say -- should not act on
// such a request as if it were from a visitor. An operation that changes
// state installs RefuseRejectedCredential rather than checking this itself.
func CredentialRejected(ctx context.Context) bool {
	rejected, _ := ctx.Value(credentialRejectedKey{}).(bool)
	return rejected
}

// ErrNoUser means a route that needs a user was reached without one --
// mounted outside the middleware, a wiring bug.
var ErrNoUser = errors.New("authn: request carries no signed-in user")

// UserFromContext returns the user the middleware resolved.
func UserFromContext(ctx context.Context) (*User, error) {
	found, ok := ctx.Value(userContextKey{}).(*User)
	if !ok || found == nil {
		return nil, ErrNoUser
	}
	return found, nil
}

// NewUserContext records the signed-in user on ctx.
func NewUserContext(ctx context.Context, u *User) context.Context {
	return context.WithValue(ctx, userContextKey{}, u)
}

// UserMiddlewareConfig configures UserMiddleware.
type UserMiddlewareConfig struct {
	Verify UserVerifier
	// Guard checks the writes a session cookie authenticates.
	Guard *BrowserGuard
	// SessionCookie names the httpOnly cookie that carries the access token
	// for the app's own pages.
	SessionCookie string
	// UserMarkers are the app's markers, besides RequireUser, whose
	// operations also need a signed-in user -- an admin marker, say, whose
	// own middleware runs after this one and finds the user on the context.
	UserMarkers []*Marker
	// Scope is applied to a signed-in request's context along with the
	// user, so the two are always set together -- scoping the request to
	// the user's tenant, say. nil scopes nothing more.
	Scope func(ctx context.Context, u *User) context.Context
	// Logger gets each refused credential, at debug.
	Logger slog.Handler
}

// UserMiddleware enforces a signed-in user on operations marked
// RequireUser (or one of cfg.UserMarkers), and resolves one, if there is
// one, on operations marked OptionalUser.
//
// An OptionalUser operation is never refused for want of a user, and never
// answered 401. With no credential the request is anonymous. With a good
// one it is exactly a RequireUser request: the user on the context, the
// scope applied, a cookie-authenticated write checked by the guard. With a
// credential that is refused it is anonymous too, flagged: the response
// carries CredentialRejectedHeader and CredentialRejected is true. It is
// not silently anonymous because the session refresh flow is driven by the
// 401 a RequireUser route answers; here there is none, so an expired access
// token would otherwise look like a visitor to both the page and the
// handler. Writes that no verified user authenticates are checked for the
// origin, as a Public write is; only a verified bearer token is exempt.
//
// The access token comes from the session cookie (the app's pages) or an
// Authorization bearer header (anything else -- a script, a delegated
// OAuth2 client); both are the same token, verified the same way. Only a
// cookie-authenticated write is checked by the guard: a browser attaches a
// cookie to a cross-site request by itself, but never an Authorization
// header. A request that names a bearer token is authenticated by it alone
// -- never by a cookie it also carries -- so skipping the check for it
// cannot let a cookie through unchecked.
//
// It panics on a config missing its verifier, guard or cookie name: each
// would leave every user route open or broken, and the configuration is
// the program's own, so a bad one is a bug to find at startup.
func UserMiddleware(api huma.API, cfg *UserMiddlewareConfig) func(huma.Context, func(huma.Context)) {
	if cfg.Verify == nil || cfg.Guard == nil || cfg.SessionCookie == "" {
		panic("authn: a user middleware needs a verifier, a browser guard and a session cookie name")
	}
	markers := append([]*Marker{userMarker}, cfg.UserMarkers...)
	scope := cfg.Scope
	if scope == nil {
		scope = func(ctx context.Context, _ *User) context.Context { return ctx }
	}
	logHandler := cfg.Logger
	if logHandler == nil {
		logHandler = slog.DiscardHandler
	}
	logger := slog.New(logHandler)

	return func(ctx huma.Context, next func(huma.Context)) {
		op := ctx.Operation()
		required := anyOn(markers, op)
		if !required && !optionalUserMarker.On(op) {
			next(ctx)
			return
		}

		cred := credentialOf(ctx, cfg.SessionCookie)
		if !required {
			// The answer can depend on who is asking, on a route that is also
			// open to anyone, so a shared cache keyed on the URL must not
			// serve one visitor's answer to another. A handler may still
			// override these.
			ctx.AppendHeader("Vary", "Cookie")
			ctx.AppendHeader("Vary", "Authorization")
			ctx.SetHeader("Cache-Control", "private")
		}
		if !required && cred.token == "" {
			if p := checkAnonymousWrite(cfg.Guard, ctx); p != nil {
				writeProblem(api, ctx, p)
				return
			}
			next(ctx)
			return
		}

		found, err := cfg.Verify(ctx.Context(), cred.token)
		if err != nil {
			logger.Debug("rejected unauthenticated request", "path", op.Path, "method", op.Method, "err", err)
			if required {
				// A bare 401 whatever the reason: saying a token was expired
				// rather than forged tells a caller which tokens are real.
				_ = huma.WriteErr(api, ctx, http.StatusUnauthorized, "unauthorized")
				return
			}
			// Anonymous, but never silently: a signed-in person whose access
			// token has expired is not told to refresh by a 401 here, so the
			// response says the credential was refused and the handler can see
			// it. The write checks still apply: with no verified user there is
			// no session to take a CSRF token from, so only the origin check
			// is left, and a bearer header that failed to verify buys no
			// exemption from it.
			ctx.SetHeader(CredentialRejectedHeader, CredentialRejectedValue)
			if p := checkAnonymousWrite(cfg.Guard, ctx); p != nil {
				writeProblem(api, ctx, p)
				return
			}
			next(huma.WithContext(ctx, context.WithValue(ctx.Context(), credentialRejectedKey{}, true)))
			return
		}
		if cred.fromCookie && !isSafeMethod(ctx.Method()) {
			if p := cfg.Guard.checkSessionWrite(ctx, found); p != nil {
				writeProblem(api, ctx, p)
				return
			}
		}
		next(huma.WithContext(ctx, scope(NewUserContext(ctx.Context(), found), found)))
	}
}

// credential is the access token a request names, and where it came from.
type credential struct {
	token      string
	fromCookie bool
}

// credentialOf picks the one credential a request is authenticated by: a
// bearer token if it names one, else the session cookie. A request that
// names a bearer token is never rescued by, nor judged by, a cookie.
func credentialOf(ctx huma.Context, sessionCookie string) credential {
	if token := BearerToken(ctx.Header("Authorization")); token != "" {
		return credential{token: token}
	}
	if c, err := huma.ReadCookie(ctx, sessionCookie); err == nil {
		return credential{token: c.Value, fromCookie: true}
	}
	return credential{}
}

// checkAnonymousWrite is the origin check on a state-changing request with
// no verified user behind it -- the same check a Public write gets.
func checkAnonymousWrite(g *BrowserGuard, ctx huma.Context) *Problem {
	if isSafeMethod(ctx.Method()) {
		return nil
	}
	return g.checkOrigin(ctx)
}

func anyOn(markers []*Marker, op *huma.Operation) bool {
	for _, m := range markers {
		if m.On(op) {
			return true
		}
	}
	return false
}

func isSafeMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}
	return false
}
