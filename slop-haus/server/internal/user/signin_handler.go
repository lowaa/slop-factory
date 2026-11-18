package user

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"time"

	"github.com/lowaa/slop-factory/go-auth/login"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
)

// LoginStateCookie carries a provider round trip's state from its start to
// its callback. Scoped to that provider's callback path, so no other
// request carries it, and a round trip started at one provider can't be
// finished at another.
const LoginStateCookie = "slop_haus_login_state"

// loginStateTTL is how long a round trip through a provider may take.
const loginStateTTL = 10 * time.Minute

// CallbackPath is where provider redirects back to after a sign-in; its
// OAuth app or OIDC client is registered with PUBLIC_BASE_URL + this.
func CallbackPath(provider string) string {
	return "/api/auth/callback/" + provider
}

func (h *Handler) loginStateCookie(provider, value string) http.Cookie {
	return http.Cookie{
		Name: LoginStateCookie, Value: value, Path: CallbackPath(provider),
		MaxAge: int(loginStateTTL.Seconds()), HttpOnly: true, Secure: h.cookies.Secure,
		// Lax, not Strict: the callback is a top-level navigation arriving
		// from the provider's site, and Strict would withhold the cookie
		// from exactly that request.
		SameSite: http.SameSiteLaxMode,
	}
}

func (h *Handler) sessionCookies(session *Session) []http.Cookie {
	if session == nil {
		return nil
	}
	return []http.Cookie{h.accessCookie(session.Access), h.refreshCookie(session.Refresh)}
}

// sessionUser is who the access cookie says is signed in, or nil. The
// provider routes are Public -- a sign-in has no session yet -- so the
// ones that act on a signed-in user read the session themselves.
func (h *Handler) sessionUser(ctx context.Context, access string) *authn.User {
	if access == "" {
		return nil
	}
	who, err := h.svc.VerifyAccess(ctx, access)
	if err != nil {
		return nil
	}
	return who
}

// RedirectResponse sends the browser on, setting cookies on the way.
type RedirectResponse struct {
	Status       int
	Location     string        `header:"Location"`
	SetCookie    []http.Cookie `header:"Set-Cookie"`
	CacheControl string        `header:"Cache-Control"`
}

func redirect(location string, cookies []http.Cookie) *RedirectResponse {
	return &RedirectResponse{Status: http.StatusFound, Location: location, SetCookie: cookies, CacheControl: "no-store"}
}

// Provider names are plain -- config.ValidateOIDCProviderName -- and the
// path parameter is held to that, since it also goes into the state
// cookie's Path.
type StartSignInRequest struct {
	Provider string `path:"provider" maxLength:"64" pattern:"^[a-z][a-z0-9-]*$"`
	Next     string `query:"next" maxLength:"2048" doc:"a local path to return to afterwards"`
	// Invite and Intent are read only to be refused: see StartSignIn.
	Invite string `query:"invite" hidden:"true"`
	Intent string `query:"intent" hidden:"true"`
}

// StartSignIn starts a plain sign-in. It is a navigation, so it is a GET
// that no CSRF check applies to -- harmless for a sign-in, which only ever
// signs the browser in as whoever it is at the provider. Every round trip
// that acts on an account starts from a POST instead (StartInvite,
// StartReauth, StartLink), which authn.BrowserGuard refuses from another
// site's page. A navigation asking for one of those is refused rather
// than quietly started as a sign-in, so an old link fails loudly and an
// invite token in a URL -- and so in proxy logs and history -- is never
// acted on.
func (h *Handler) StartSignIn(_ context.Context, in *StartSignInRequest) (*RedirectResponse, error) {
	if in.Invite != "" || (in.Intent != "" && login.Intent(in.Intent) != login.IntentLogin) {
		return nil, h.mapError(ErrPostRequired)
	}
	rd, err := h.svc.StartSignIn(in.Provider, in.Next)
	if err != nil {
		return nil, h.mapError(err)
	}
	return redirect(rd.URL, []http.Cookie{h.loginStateCookie(in.Provider, rd.State)}), nil
}

type SignInCallbackRequest struct {
	Provider   string `path:"provider" maxLength:"64" pattern:"^[a-z][a-z0-9-]*$"`
	Code       string `query:"code" maxLength:"4096"`
	State      string `query:"state" maxLength:"256"`
	LoginState string `cookie:"slop_haus_login_state"`
	Session    string `cookie:"slop_haus_session"`
}

// SignInCallback finishes a round trip, whatever its outcome clearing the
// state cookie: a round trip is good for one callback.
func (h *Handler) SignInCallback(ctx context.Context, in *SignInCallbackRequest) (*RedirectResponse, error) {
	clearState := h.expired(h.loginStateCookie(in.Provider, ""))
	res, err := h.svc.CompleteSignIn(ctx, &SignInCallback{
		Provider: in.Provider, Code: in.Code, State: in.State, StateCookie: in.LoginState,
		Who: h.sessionUser(ctx, in.Session),
	})
	if err != nil {
		return h.refuseCallback(err, clearState), nil
	}
	return redirect(res.Next, append([]http.Cookie{clearState}, h.sessionCookies(res.Session)...)), nil
}

// The board's pages a refused callback sends the browser back to, by what
// the round trip was for. The callback is a navigation arriving from the
// provider, so an error body would leave the person on a bare JSON page
// outside the board; instead they land where they started, and the page
// explains the code it is handed (?error=<code>).
const (
	LoginPage   = "/login"
	InvitePage  = "/invite"
	AccountPage = "/account"
)

// RefusalPage is where a callback refused for intent lands.
func RefusalPage(intent login.Intent) string {
	switch intent {
	case login.IntentAcceptInvite:
		return InvitePage
	case login.IntentLink, login.IntentReauth:
		return AccountPage
	default:
		return LoginPage
	}
}

// refuseCallback redirects a refused callback back into the board with
// err's code. Without a readable state there is no knowing what the round
// trip was for, so it goes to sign-in.
func (h *Handler) refuseCallback(err error, clearState http.Cookie) *RedirectResponse {
	intent := login.IntentLogin
	var refused *CallbackRefusedError
	if errors.As(err, &refused) {
		intent = refused.Intent
	}
	code := "internal"
	if p := ProblemFor(err); p != nil {
		code = p.Code
	} else {
		h.logger.Error("unexpected sign-in callback error", "err", err)
	}
	return redirect(RefusalPage(intent)+"?"+url.Values{"error": {code}}.Encode(), []http.Cookie{clearState})
}

type RoundTripStartedResponse struct {
	SetCookie []http.Cookie `header:"Set-Cookie"`
	Body      struct {
		RedirectURL string `json:"redirect_url" doc:"where to send the browser to sign in at the provider"`
	}
}

func (h *Handler) roundTripStarted(provider string, rd *ProviderRedirect) *RoundTripStartedResponse {
	resp := &RoundTripStartedResponse{SetCookie: append([]http.Cookie{h.loginStateCookie(provider, rd.State)}, h.sessionCookies(rd.Session)...)}
	resp.Body.RedirectURL = rd.URL
	return resp
}

type StartLinkRequest struct {
	Provider string `path:"provider" maxLength:"64" pattern:"^[a-z][a-z0-9-]*$"`
	Body     struct {
		Next string `json:"next,omitempty" maxLength:"2048" doc:"a local path to return to afterwards"`
	} `required:"false"`
}

// StartLink, StartReauth and StartInvite are POSTs, not navigations, so
// authn.BrowserGuard checks them as it does every write (StartInvite, which
// is Public, by its origin; the other two by the session's CSRF token too);
// the browser is then sent to the URL they answer with.
func (h *Handler) StartLink(ctx context.Context, in *StartLinkRequest) (*RoundTripStartedResponse, error) {
	who, err := h.currentUser(ctx)
	if err != nil {
		return nil, err
	}
	rd, err := h.svc.StartLink(ctx, who, in.Provider, in.Body.Next)
	if err != nil {
		return nil, h.mapError(err)
	}
	return h.roundTripStarted(in.Provider, rd), nil
}

func (h *Handler) StartReauth(ctx context.Context, in *StartLinkRequest) (*RoundTripStartedResponse, error) {
	who, err := h.currentUser(ctx)
	if err != nil {
		return nil, err
	}
	rd, err := h.svc.StartReauth(ctx, who, in.Provider, in.Body.Next)
	if err != nil {
		return nil, h.mapError(err)
	}
	return h.roundTripStarted(in.Provider, rd), nil
}

type StartInviteRequest struct {
	Provider string `path:"provider" maxLength:"64" pattern:"^[a-z][a-z0-9-]*$"`
	Body     struct {
		Token      string `json:"token" maxLength:"256" doc:"the invite link's token"`
		TenantName string `json:"tenant_name,omitempty" maxLength:"200" doc:"what to call the tenant a new_tenant invite makes"`
		Next       string `json:"next,omitempty" maxLength:"2048" doc:"a local path to return to afterwards"`
	}
}

// StartInvite needs no session: the invite's token is the credential, and
// it rides in the body, never a URL.
func (h *Handler) StartInvite(ctx context.Context, in *StartInviteRequest) (*RoundTripStartedResponse, error) {
	rd, err := h.svc.StartInviteAcceptance(ctx, in.Provider, in.Body.Token, in.Body.TenantName, in.Body.Next)
	if err != nil {
		return nil, h.mapError(err)
	}
	return h.roundTripStarted(in.Provider, rd), nil
}

type ReauthPasswordRequest struct {
	Body struct {
		Password string `json:"password" maxLength:"256"`
	}
}

// ReauthPassword is step-up by password: the session's cookies come back
// re-issued with a fresh auth time.
func (h *Handler) ReauthPassword(ctx context.Context, in *ReauthPasswordRequest) (*SessionCookieResponse, error) {
	who, err := h.currentUser(ctx)
	if err != nil {
		return nil, err
	}
	session, err := h.svc.ReauthenticateWithPassword(ctx, who, in.Body.Password)
	if err != nil {
		return nil, h.mapError(err)
	}
	return &SessionCookieResponse{SetCookie: h.sessionCookies(session)}, nil
}

type PasswordMethodBody struct {
	Email    string `json:"email"`
	Verified bool   `json:"verified" doc:"always true here: an account's address is verified when the account is made"`
}

type IdentityMethodBody struct {
	Provider string `json:"provider"`
	Subject  string `json:"subject" doc:"the provider's stable id for the identity"`
	Login    string `json:"login"`
	Usable   bool   `json:"usable" doc:"whether it can sign in here: only identities of a configured login provider can"`
}

type SignInMethodsResponse struct {
	Body struct {
		Password             *PasswordMethodBody  `json:"password,omitempty" doc:"absent when the account has no password"`
		Identities           []IdentityMethodBody `json:"identities"`
		PasswordLoginEnabled bool                 `json:"password_login_enabled"`
		ReauthRequiredAfter  time.Time            `json:"reauth_required_after" doc:"when this session's step-up window closes; after it, changes answer 403 with code reauth_required"`
	}
}

func (h *Handler) SignInMethods(ctx context.Context, _ *struct{}) (*SignInMethodsResponse, error) {
	who, err := h.currentUser(ctx)
	if err != nil {
		return nil, err
	}
	m, err := h.svc.SignInMethods(ctx, who)
	if err != nil {
		return nil, h.mapError(err)
	}
	resp := &SignInMethodsResponse{}
	if m.HasPassword {
		resp.Body.Password = &PasswordMethodBody{Email: m.Email, Verified: true}
	}
	resp.Body.Identities = make([]IdentityMethodBody, 0, len(m.Identities))
	for _, id := range m.Identities {
		resp.Body.Identities = append(resp.Body.Identities, IdentityMethodBody{Provider: id.Provider, Subject: id.Subject, Login: id.Login, Usable: id.Usable})
	}
	resp.Body.PasswordLoginEnabled = h.svc.PasswordLoginEnabled()
	resp.Body.ReauthRequiredAfter = ReauthRequiredAfter(who)
	return resp, nil
}

// The password's maxLength counts characters, so it cannot enforce
// bcrypt's 72-byte limit on its own; the service does.
type SetPasswordRequest struct {
	Body struct {
		NewPassword string `json:"new_password" minLength:"10" maxLength:"72"`
	}
}

func (h *Handler) SetPassword(ctx context.Context, in *SetPasswordRequest) (*SessionCookieResponse, error) {
	who, err := h.currentUser(ctx)
	if err != nil {
		return nil, err
	}
	session, err := h.svc.SetPassword(ctx, who, in.Body.NewPassword)
	if err != nil {
		return nil, h.mapError(err)
	}
	return &SessionCookieResponse{SetCookie: h.sessionCookies(session)}, nil
}

func (h *Handler) RemovePassword(ctx context.Context, _ *struct{}) (*NoContentResponse, error) {
	who, err := h.currentUser(ctx)
	if err != nil {
		return nil, err
	}
	if err := h.svc.RemovePassword(ctx, who); err != nil {
		return nil, h.mapError(err)
	}
	return &NoContentResponse{}, nil
}

// RemoveIdentityRequest names the identity by query parameters rather
// than path segments: an OIDC subject is any string, slashes included.
type RemoveIdentityRequest struct {
	Provider string `query:"provider" required:"true" maxLength:"64"`
	Subject  string `query:"subject" required:"true" maxLength:"512"`
}

func (h *Handler) RemoveIdentity(ctx context.Context, in *RemoveIdentityRequest) (*NoContentResponse, error) {
	who, err := h.currentUser(ctx)
	if err != nil {
		return nil, err
	}
	if err := h.svc.RemoveIdentity(ctx, who, in.Provider, in.Subject); err != nil {
		return nil, h.mapError(err)
	}
	return &NoContentResponse{}, nil
}
