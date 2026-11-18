package user

import (
	"context"
	"crypto/subtle"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/go-auth/auth"
	goauthn "github.com/lowaa/slop-factory/go-auth/authn"
	"github.com/lowaa/slop-factory/go-auth/login"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
)

// Signing in through a login provider (go-auth/login): a round trip that
// starts here with a state cookie and a redirect to the provider, and ends
// at its callback with the identity it vouches for. What that identity is
// then used for travels in the state, as its intent:
//
//   - login: sign in as whoever the identity belongs to. An identity
//     nobody has is refused -- a provider sign-in never creates an account
//     on its own, and is never matched to one by email.
//   - accept_invite: spend an invite on it (see AcceptInviteWithIdentity).
//   - link: attach it to the signed-in account that started the link.
//   - reauth: confirm the signed-in account's owner is present, for
//     step-up.

// maxDisplayNameLength is the display-name bound the sign-up and invite
// schemas put on a name a person types; a provider's name is held to it
// too.
const maxDisplayNameLength = 100

func (s *Service) audit(ctx context.Context, action audit.Action, attrs ...any) {
	s.events.Record(ctx, action, attrs...)
}

// ReauthRequiredAfter is when who's step-up window closes.
func ReauthRequiredAfter(who *authn.User) time.Time { return goauthn.ReauthRequiredAfter(who) }

// RequireRecentAuth refuses a sensitive change from a session whose owner
// last authenticated interactively too long ago.
func RequireRecentAuth(who *authn.User) error { return goauthn.RequireRecentAuth(who) }

// reissue re-issues sess for u in tenantID, checking the membership first
// as a refresh would: re-issuing is minting tokens, and must not outlive a
// removal from the tenant.
func (s *Service) reissue(ctx context.Context, u *User, tenantID uuid.UUID, sess *auth.Session) (*Session, error) {
	member, err := s.store.IsMember(ctx, u.ID, tenantID)
	if err != nil {
		return nil, err
	}
	if !member {
		return nil, ErrInvalidSession
	}
	return s.sessionFor(u, tenantID, sess)
}

// resumeSession is who's session, re-issued with authTime -- and its
// session id unchanged -- if it still stands.
func (s *Service) resumeSession(ctx context.Context, who *authn.User, authTime time.Time) (*Session, error) {
	u, err := s.liveSessionUser(ctx, who)
	if err != nil {
		return nil, err
	}
	return s.reissue(ctx, u, who.TenantID, &auth.Session{AuthTime: authTime, ID: who.SessionID})
}

// Providers is the login providers configured here.
func (s *Service) Providers() *login.Providers { return s.providers }

// ProviderRedirect is where to send the browser, and what to set on it
// before sending it there.
type ProviderRedirect struct {
	URL string
	// State is the state cookie's value: the round trip's secrets and its
	// intent, sealed, which the callback accepts only from this browser.
	State string
	// Session is the signed-in user's session, re-issued with its auth
	// time unchanged, for a round trip whose callback needs the session:
	// an access token near the end of its life would otherwise expire
	// while the browser is at the provider, and the callback arrive signed
	// out. Nil for the other intents.
	Session *Session
}

// StartSignIn begins a plain sign-in through a provider. Anything can
// start one -- it only ever signs the browser in as whoever it is at the
// provider -- so it is the one round trip that starts from a navigation.
func (s *Service) StartSignIn(provider, next string) (*ProviderRedirect, error) {
	ap, ok := s.providers.Get(provider)
	if !ok {
		return nil, ErrUnknownProvider
	}
	return s.startRoundTrip(ap, &login.LoginState{Next: login.LocalRedirect(next), Intent: login.IntentLogin}, nil)
}

// StartInviteAcceptance begins accepting an invite with the identity a
// provider will vouch for.
func (s *Service) StartInviteAcceptance(ctx context.Context, provider, invite, tenantName, next string) (*ProviderRedirect, error) {
	ap, ok := s.providers.Get(provider)
	if !ok {
		return nil, ErrUnknownProvider
	}
	// Checked, not spent: a dead invite fails before the round trip rather
	// than after it.
	if _, _, err := s.store.FindInvite(ctx, auth.HashOpaqueToken(invite)); err != nil {
		return nil, err
	}
	// The name the person typed on the invite page rides in the state
	// cookie, since the callback arrives by redirect and has no form.
	newTenant, err := newTenantNamed(tenantName)
	if err != nil {
		return nil, err
	}
	return s.startRoundTrip(ap, &login.LoginState{Next: login.LocalRedirect(next), Intent: login.IntentAcceptInvite, InviteToken: invite, AppData: newTenant.Name}, nil)
}

// StartReauth begins re-authenticating who through a provider, for
// step-up.
func (s *Service) StartReauth(ctx context.Context, who *authn.User, provider, next string) (*ProviderRedirect, error) {
	ap, ok := s.providers.Get(provider)
	if !ok {
		return nil, ErrUnknownProvider
	}
	session, err := s.resumeSession(ctx, who, who.AuthTime)
	if err != nil {
		return nil, err
	}
	return s.startRoundTrip(ap, &login.LoginState{Next: login.LocalRedirect(next), Intent: login.IntentReauth, UserID: who.ID.String()}, session)
}

// StartLink begins attaching a provider identity to who's account. It
// needs a recent authentication: otherwise a stale session -- or a page
// that sends a signed-in visitor through a link while they are signed in
// at the provider as someone else -- could add a way into the account.
func (s *Service) StartLink(ctx context.Context, who *authn.User, provider, next string) (*ProviderRedirect, error) {
	ap, ok := s.providers.Get(provider)
	if !ok {
		return nil, ErrUnknownProvider
	}
	if err := RequireRecentAuth(who); err != nil {
		return nil, err
	}
	session, err := s.resumeSession(ctx, who, who.AuthTime)
	if err != nil {
		return nil, err
	}
	return s.startRoundTrip(ap, &login.LoginState{
		Next: login.LocalRedirect(next), Intent: login.IntentLink, UserID: who.ID.String(),
	}, session)
}

func (s *Service) startRoundTrip(ap login.AuthProvider, st *login.LoginState, session *Session) (*ProviderRedirect, error) {
	attempt, err := login.NewLoginAttempt()
	if err != nil {
		return nil, err
	}
	target, err := ap.AuthCodeURL(attempt)
	if errors.Is(err, login.ErrProviderUnavailable) {
		return nil, ErrProviderUnavailable
	}
	if err != nil {
		return nil, err
	}
	st.Attempt = attempt
	state, err := s.loginStates.Encode(st)
	if err != nil {
		return nil, err
	}
	return &ProviderRedirect{URL: target, State: state, Session: session}, nil
}

// SignInCallback is the provider's redirect back.
type SignInCallback struct {
	Provider string
	// Code and State are the callback's query parameters.
	Code  string
	State string
	// StateCookie is the state cookie a Start* call set.
	StateCookie string
	// Who is the browser's signed-in user, if it has a session.
	Who *authn.User
}

// SignInResult is what a callback leads to.
type SignInResult struct {
	// Session is the session to set: a new one for a sign-in, or the
	// signed-in user's re-issued with a fresh auth time for a reauth. Nil
	// after a link, which changes no session.
	Session *Session
	Next    string
}

// CallbackRefusedError is a refused callback whose state was read, so what
// the round trip was for is known: the page it goes back to. A refusal
// before that -- no usable state -- is the bare error.
type CallbackRefusedError struct {
	Intent login.Intent
	Err    error
}

func (e *CallbackRefusedError) Error() string { return e.Err.Error() }

func (e *CallbackRefusedError) Unwrap() error { return e.Err }

// CompleteSignIn finishes a round trip through a provider.
func (s *Service) CompleteSignIn(ctx context.Context, in *SignInCallback) (*SignInResult, error) {
	ap, ok := s.providers.Get(in.Provider)
	if !ok {
		return nil, ErrUnknownProvider
	}
	st, err := s.loginStates.Decode(in.StateCookie)
	if err != nil {
		s.logger.DebugContext(ctx, "sign-in callback without a usable state cookie", "err", err)
		return nil, ErrLoginExpired
	}
	res, err := s.completeRoundTrip(ctx, ap, in, st)
	if err != nil {
		return nil, &CallbackRefusedError{Intent: st.Intent, Err: err}
	}
	return res, nil
}

func (s *Service) completeRoundTrip(ctx context.Context, ap login.AuthProvider, in *SignInCallback, st *login.LoginState) (*SignInResult, error) {
	// The state is what stops a login CSRF -- an attacker finishing their
	// own sign-in in the victim's browser -- since only the browser that
	// started the round trip holds the cookie it must match.
	if subtle.ConstantTimeCompare([]byte(st.Attempt.State), []byte(in.State)) != 1 {
		return nil, ErrLoginExpired
	}
	if in.Code == "" {
		return nil, ErrLoginCancelled
	}
	vouched, err := ap.Exchange(ctx, in.Code, st.Attempt)
	if errors.Is(err, login.ErrProviderUnavailable) {
		return nil, ErrProviderUnavailable
	}
	if err != nil {
		// The provider's own error can carry details a stranger shouldn't
		// see; it is logged, and the answer is a generic one.
		s.logger.WarnContext(ctx, "provider refused the code exchange", "provider", in.Provider, "err", err)
		return nil, ErrProviderFailed
	}
	ident := identityFrom(vouched)
	res := &SignInResult{Next: st.Next}
	switch st.Intent {
	case login.IntentLogin:
		res.Session, err = s.signInWithIdentity(ctx, ident)
	case login.IntentAcceptInvite:
		res.Session, err = s.acceptInviteWithIdentity(ctx, st.InviteToken, st.AppData, ident)
	case login.IntentLink:
		err = s.linkIdentity(ctx, in.Who, st.UserID, ident)
	case login.IntentReauth:
		res.Session, err = s.reauthWithIdentity(ctx, in.Who, st.UserID, ident)
	default:
		// LoginStateCodec.Decode refuses any other intent; this is unreachable
		// unless the two drift apart, and then it must fail closed.
		err = ErrUnknownIntent
	}
	if err != nil {
		return nil, err
	}
	return res, nil
}

// identityFrom is the one place go-auth/login's Identity becomes this
// package's: everything past the callback deals in Identity.
func identityFrom(li *login.Identity) *Identity {
	return &Identity{Provider: li.Provider, Subject: li.Subject, Login: li.Login, DisplayName: li.DisplayName, Email: li.Email}
}

func identityAttrs(ident *Identity) []any {
	return []any{"provider", ident.Provider, "subject", ident.Subject, "login", ident.Login}
}

func (s *Service) signInWithIdentity(ctx context.Context, ident *Identity) (*Session, error) {
	owner, err := s.store.IdentityOwner(ctx, ident.Provider, ident.Subject)
	if errors.Is(err, ErrNotFound) {
		s.audit(ctx, audit.SignInRefused, append(identityAttrs(ident), "method", ident.Provider, "reason", "no account")...)
		return nil, ErrNoAccount
	}
	if err != nil {
		return nil, err
	}
	u, err := s.store.FindByID(ctx, owner)
	if err != nil {
		return nil, err
	}
	if !u.IsActive {
		s.audit(ctx, audit.SignInRefused, append(identityAttrs(ident), "method", ident.Provider, "user_id", u.ID, "reason", "account inactive")...)
		return nil, ErrNoAccount
	}
	if err := s.store.RecordIdentityLogin(ctx, ident); err != nil {
		return nil, err
	}
	return s.signIn(ctx, u, append([]any{"method", ident.Provider}, identityAttrs(ident)...)...)
}

// acceptInviteWithIdentity spends an invite with the identity a provider
// just vouched for, and signs in in the tenant it grants.
func (s *Service) acceptInviteWithIdentity(ctx context.Context, token, tenantName string, ident *Identity) (*Session, error) {
	newUser := &User{ID: uuid.New(), DisplayName: displayNameFor(ident), IsActive: true}
	// Checked again: AppData is only what the start request claimed.
	newTenant, err := newTenantNamed(tenantName)
	if err != nil {
		return nil, err
	}
	userID, tenantID, err := s.store.AcceptInviteWithIdentity(ctx, auth.HashOpaqueToken(token), ident, newUser, newTenant)
	if errors.Is(err, ErrIdentityTaken) || errors.Is(err, ErrAccountExists) {
		s.audit(ctx, audit.InviteRefused, append(identityAttrs(ident), "method", ident.Provider, "reason", err.Error())...)
	}
	if err != nil {
		return nil, err
	}
	u, err := s.store.FindByID(ctx, userID)
	if err != nil {
		return nil, err
	}
	if err := s.store.RecordIdentityLogin(ctx, ident); err != nil {
		return nil, err
	}
	s.audit(ctx, audit.InviteAccepted, append([]any{"user_id", u.ID, "method", ident.Provider, "new_account", userID == newUser.ID}, identityAttrs(ident)...)...)
	s.audit(ctx, audit.SignIn, append([]any{"user_id", u.ID, "method", ident.Provider}, identityAttrs(ident)...)...)
	return s.newSessionFor(u, tenantID)
}

// displayNameFor is what a new account made from ident is called: what
// the provider calls the person, or their login there.
func displayNameFor(ident *Identity) string {
	name := strings.TrimSpace(ident.DisplayName)
	if name == "" {
		name = ident.Login
	}
	if r := []rune(name); len(r) > maxDisplayNameLength {
		name = string(r[:maxDisplayNameLength])
	}
	return name
}

// linkIdentity attaches ident to the signed-in user who started the link
// -- only if the session this callback arrives with is still theirs. The
// state cookie only says who started it; a session that ended, or changed
// hands, since then must not have an identity attached to it.
func (s *Service) linkIdentity(ctx context.Context, who *authn.User, startedBy string, ident *Identity) error {
	if who == nil || who.ID.String() != startedBy {
		s.audit(ctx, audit.LinkRefused, append(identityAttrs(ident), "method", ident.Provider, "started_by", startedBy, "reason", "session does not match the user who started the link")...)
		return ErrSessionMismatch
	}
	u, err := s.liveSessionUser(ctx, who)
	if err != nil {
		return err
	}
	err = s.store.LinkIdentity(ctx, u.ID, ident)
	if errors.Is(err, ErrAlreadyLinked) {
		return nil
	}
	if err != nil {
		s.audit(ctx, audit.LinkRefused, append(identityAttrs(ident), "method", ident.Provider, "user_id", u.ID, "reason", err.Error())...)
		return err
	}
	s.audit(ctx, audit.IdentityLinked, append([]any{"user_id", u.ID, "method", ident.Provider}, identityAttrs(ident)...)...)
	return nil
}

// reauthWithIdentity refreshes the session's auth time -- only if the
// identity the provider just vouched for is the signed-in user's own.
// Anyone else's, or one nobody has, proves nothing about who is at the
// keyboard of this session.
func (s *Service) reauthWithIdentity(ctx context.Context, who *authn.User, startedBy string, ident *Identity) (*Session, error) {
	if who == nil || who.ID.String() != startedBy {
		return nil, ErrSessionMismatch
	}
	owner, err := s.store.IdentityOwner(ctx, ident.Provider, ident.Subject)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	if err != nil || owner != who.ID {
		s.audit(ctx, audit.ReauthRefused, append(identityAttrs(ident), "method", ident.Provider, "user_id", who.ID, "reason", "identity is not this user's")...)
		return nil, ErrIdentityMismatch
	}
	session, err := s.resumeSession(ctx, who, s.clock.Now())
	if err != nil {
		return nil, err
	}
	if err := s.store.RecordIdentityLogin(ctx, ident); err != nil {
		return nil, err
	}
	s.audit(ctx, audit.Reauth, append([]any{"user_id", who.ID, "method", ident.Provider}, identityAttrs(ident)...)...)
	return session, nil
}

// ReauthenticateWithPassword is step-up by the signed-in user's own
// password: their session, re-issued with a fresh auth time. It shares the
// sign-in guess limits, so a stolen session can't guess the password any
// faster than the sign-in form allows: per client, and per account alone
// -- not account and client, which a session thief could reset by moving
// to another address. Only someone already holding one of the account's
// sessions can use up the per-account allowance.
func (s *Service) ReauthenticateWithPassword(ctx context.Context, who *authn.User, password string) (*Session, error) {
	if err := s.requirePasswordSignIn(); err != nil {
		return nil, err
	}
	if !s.loginClientLimit.Allow(clientKey(ctx)) || !s.loginLimit.Allow("reauth|"+who.ID.String()) {
		return nil, ErrRateLimited
	}
	u, err := s.liveSessionUser(ctx, who)
	if err != nil {
		return nil, err
	}
	hash := u.PasswordHash
	if hash == "" {
		hash = dummyHash
	}
	if s.passwords.Verify(hash, password) != nil || u.PasswordHash == "" {
		s.audit(ctx, audit.ReauthRefused, "user_id", u.ID, "method", "password")
		return nil, ErrInvalidCredentials
	}
	session, err := s.reissue(ctx, u, who.TenantID, &auth.Session{AuthTime: s.clock.Now(), ID: who.SessionID})
	if err != nil {
		return nil, err
	}
	s.audit(ctx, audit.Reauth, "user_id", u.ID, "method", "password")
	return session, nil
}
