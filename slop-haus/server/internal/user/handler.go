package user

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/httperr"
)

// RefreshCookie carries the refresh token. Scoped to the refresh path so
// the browser sends it there and nowhere else: every other request
// carries only the short-lived access token.
const (
	RefreshCookie = "slop_haus_refresh"
	RefreshPath   = "/api/auth/refresh"
)

// CookieConfig is how the session cookies are set.
type CookieConfig struct {
	// Secure is on whenever the board is served over https; off only for
	// plain-http local development, where a Secure cookie would never be
	// sent back.
	Secure     bool
	AccessTTL  time.Duration
	RefreshTTL time.Duration
}

// Handler is the HTTP side of Service: request and response shapes, the
// session cookies, and mapping errors to statuses.
type Handler struct {
	svc     *Service
	cookies *CookieConfig
	logger  *slog.Logger
}

func NewHandler(svc *Service, cookies *CookieConfig, logHandler slog.Handler) *Handler {
	return &Handler{svc: svc, cookies: cookies, logger: slog.New(logHandler)}
}

// mapError answers one of this package's refusals as itself, with its
// code, and anything else as a logged, detail-free 500.
var errTable = httperr.Table{
	Custom: func(err error) error {
		if p := ProblemFor(err); p != nil {
			return p
		}
		return nil
	},
	Unexpected: "unexpected user error",
}

func (h *Handler) mapError(err error) error {
	return httperr.Map(h.logger, err, &errTable)
}

func (h *Handler) accessCookie(token string) http.Cookie {
	return http.Cookie{
		Name: authn.SessionCookie, Value: token, Path: "/",
		MaxAge: int(h.cookies.AccessTTL.Seconds()), HttpOnly: true, Secure: h.cookies.Secure,
		// Lax, not Strict: Strict would arrive signed out from any link
		// into the board -- a ticket URL pasted in a message, say.
		SameSite: http.SameSiteLaxMode,
	}
}

func (h *Handler) refreshCookie(token string) http.Cookie {
	return http.Cookie{
		Name: RefreshCookie, Value: token, Path: RefreshPath,
		MaxAge: int(h.cookies.RefreshTTL.Seconds()), HttpOnly: true, Secure: h.cookies.Secure,
		// Only the board's own scripts ever call refresh; nothing
		// arriving from another site needs it.
		SameSite: http.SameSiteStrictMode,
	}
}

func (h *Handler) expired(c http.Cookie) http.Cookie {
	c.Value = ""
	c.MaxAge = -1
	return c
}

// -- Wire types --

type ProfileBody struct {
	ID          uuid.UUID `json:"id"`
	Email       string    `json:"email"`
	DisplayName string    `json:"display_name"`
	TenantID    uuid.UUID `json:"tenant_id"`
	// IsAdmin decides whether the board links to the admin pages. It
	// grants nothing: every admin operation is checked on the server.
	IsAdmin bool `json:"is_admin"`
	// CSRFToken is this browser session's token, for the X-CSRF-Token
	// header of every write. It is safe in a body only a same-origin page
	// can read, and it is not the session: the cookies stay httpOnly.
	CSRFToken string `json:"csrf_token" doc:"send as X-CSRF-Token on every write this session makes; empty for a session from before CSRF tokens, which must sign in again to write"`
}

func profileToBody(p *Profile, csrfToken string) ProfileBody {
	return ProfileBody{ID: p.ID, Email: p.Email, DisplayName: p.DisplayName, TenantID: p.TenantID, IsAdmin: p.IsAdmin, CSRFToken: csrfToken}
}

// sessionResponse sets a new session's cookies and answers with its
// profile -- sign-in, an accepted invite and a tenant switch all end so.
func (h *Handler) sessionResponse(session *Session) *LoginResponse {
	return &LoginResponse{
		SetCookie: []http.Cookie{h.accessCookie(session.Access), h.refreshCookie(session.Refresh)},
		Body:      profileToBody(session.Profile, session.CSRFToken),
	}
}

type MemberBody struct {
	ID          uuid.UUID `json:"id"`
	DisplayName string    `json:"display_name"`
}

// The password's maxLength counts characters, so it cannot enforce
// bcrypt's 72-byte limit on its own; the service does.
type RegisterRequest struct {
	Body struct {
		Email       string `json:"email" format:"email" maxLength:"254"`
		Password    string `json:"password" minLength:"10" maxLength:"72"`
		DisplayName string `json:"display_name" minLength:"1" maxLength:"100"`
	}
}

type AcceptedResponse struct{}

func (h *Handler) Register(ctx context.Context, in *RegisterRequest) (*AcceptedResponse, error) {
	if err := h.svc.Register(ctx, &RegisterInput{
		Email: in.Body.Email, Password: in.Body.Password, DisplayName: in.Body.DisplayName,
	}); err != nil {
		return nil, h.mapError(err)
	}
	return &AcceptedResponse{}, nil
}

type TokenRequest struct {
	Body struct {
		Token string `json:"token" minLength:"1" maxLength:"256"`
	}
}

type NoContentResponse struct{}

func (h *Handler) Activate(ctx context.Context, in *TokenRequest) (*NoContentResponse, error) {
	if err := h.svc.Activate(ctx, in.Body.Token); err != nil {
		return nil, h.mapError(err)
	}
	return &NoContentResponse{}, nil
}

type LoginRequest struct {
	Body struct {
		Email    string `json:"email" maxLength:"254"`
		Password string `json:"password" maxLength:"256"`
	}
}

type LoginResponse struct {
	SetCookie []http.Cookie `header:"Set-Cookie"`
	Body      ProfileBody
}

// Login answers with the profile and sets the session cookies. The tokens
// are never in the body: a script that could read them could send them
// anywhere, which is what httpOnly cookies are there to prevent.
func (h *Handler) Login(ctx context.Context, in *LoginRequest) (*LoginResponse, error) {
	session, err := h.svc.Login(ctx, in.Body.Email, in.Body.Password)
	if err != nil {
		return nil, h.mapError(err)
	}
	return h.sessionResponse(session), nil
}

// RefreshRequest is Public -- its access token may have expired -- so it is
// held to the board's origin like every public write (see
// authn.BrowserGuard), and its cookie is SameSite=Strict besides. A
// refresh re-issues the session it was given, so it needs no CSRF token:
// the most a forged one could do is extend a session its victim already has.
type RefreshRequest struct {
	Refresh string `cookie:"slop_haus_refresh"`
}

type SessionCookieResponse struct {
	SetCookie []http.Cookie `header:"Set-Cookie"`
}

func (h *Handler) Refresh(ctx context.Context, in *RefreshRequest) (*SessionCookieResponse, error) {
	access, err := h.svc.Refresh(ctx, in.Refresh)
	if err != nil {
		return nil, h.mapError(err)
	}
	return &SessionCookieResponse{SetCookie: []http.Cookie{h.accessCookie(access)}}, nil
}

// Logout clears the cookies. The tokens themselves stay valid until they
// expire -- they are stateless -- so this ends the session in this
// browser; a password reset is what ends it everywhere.
func (h *Handler) Logout(_ context.Context, _ *struct{}) (*SessionCookieResponse, error) {
	return &SessionCookieResponse{SetCookie: []http.Cookie{
		h.expired(h.accessCookie("")), h.expired(h.refreshCookie("")),
	}}, nil
}

type PasswordResetRequest struct {
	Body struct {
		Email string `json:"email" maxLength:"254"`
	}
}

func (h *Handler) RequestPasswordReset(ctx context.Context, in *PasswordResetRequest) (*AcceptedResponse, error) {
	if err := h.svc.RequestPasswordReset(ctx, in.Body.Email); err != nil {
		return nil, h.mapError(err)
	}
	return &AcceptedResponse{}, nil
}

type ResetPasswordRequest struct {
	Body struct {
		Token    string `json:"token" minLength:"1" maxLength:"256"`
		Password string `json:"password" minLength:"10" maxLength:"72"`
	}
}

func (h *Handler) ResetPassword(ctx context.Context, in *ResetPasswordRequest) (*NoContentResponse, error) {
	if err := h.svc.ResetPassword(ctx, in.Body.Token, in.Body.Password); err != nil {
		return nil, h.mapError(err)
	}
	return &NoContentResponse{}, nil
}

type MeResponse struct {
	Body ProfileBody
}

func (h *Handler) Me(ctx context.Context, _ *struct{}) (*MeResponse, error) {
	who, err := authn.UserFromContext(ctx)
	if err != nil {
		return nil, h.mapError(err)
	}
	p, err := h.svc.Me(ctx, who)
	if err != nil {
		return nil, h.mapError(err)
	}
	return &MeResponse{Body: profileToBody(p, h.svc.CSRFToken(who))}, nil
}

type MembersResponse struct {
	Body struct {
		Members []MemberBody `json:"members"`
		// Former are people who left this tenant and still have an account.
		Former []MemberBody `json:"former"`
	}
}

func (h *Handler) Members(ctx context.Context, _ *struct{}) (*MembersResponse, error) {
	who, err := authn.UserFromContext(ctx)
	if err != nil {
		return nil, h.mapError(err)
	}
	dir, err := h.svc.MemberDirectory(ctx, who)
	if err != nil {
		return nil, h.mapError(err)
	}
	resp := &MembersResponse{}
	resp.Body.Members = memberBodies(dir.Members)
	resp.Body.Former = memberBodies(dir.Former)
	return resp, nil
}

func memberBodies(members []Member) []MemberBody {
	out := make([]MemberBody, 0, len(members))
	for _, m := range members {
		out = append(out, MemberBody{ID: m.ID, DisplayName: m.DisplayName})
	}
	return out
}
