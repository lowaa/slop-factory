package user

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/go-auth/auth"
	"github.com/lowaa/slop-factory/go-auth/clientip"
	"github.com/lowaa/slop-factory/go-auth/login"
	"github.com/lowaa/slop-factory/go-auth/ratelimit"

	"github.com/lowaa/slop-factory/go-clock"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
)

// ErrRateLimited means too many attempts for one address from one client.
// It says nothing about whether the address has an account: the limit
// counts attempts either way.
var ErrRateLimited = errors.New("user: too many attempts, try again later")

// Config is the policy Service runs under.
type Config struct {
	// PublicBaseURL is where emailed links point. Configured rather than
	// taken from the request's Host header, which the requester controls:
	// a reset link built from it could be pointed at their own server and
	// the token harvested from the victim's click.
	PublicBaseURL string
	ActivationTTL time.Duration
	ResetTTL      time.Duration
	InviteTTL     time.Duration
	// RegistrationOpen lets anyone sign up. Off, an account starts from an
	// invite; on is for development and the e2e suite.
	RegistrationOpen bool
	// PasswordSignInDisabled turns passwords off: sign-in, sign-up, resets,
	// step-up and setting one are all refused, and an account's password no
	// longer counts as a way in. Where it is off, sign-in inherits the
	// login provider's second factor, which a password here does not have.
	// Named for the off state so the zero Config keeps passwords on.
	PasswordSignInDisabled bool
	// LoginLimit bounds password guesses, and MailLimit the emails one
	// address can be sent. Both count per address *and* client IP: per
	// address alone, anyone could lock the owner out of their own account
	// -- or out of their own reset link -- just by using up the allowance.
	LoginLimit *ratelimit.Config
	MailLimit  *ratelimit.Config
	// LoginClientLimit and MailClientLimit count the same attempts per
	// client alone, whatever the address: one client cycling through
	// addresses never repeats a key above, so without these it could guess
	// one password against every account (and make the server run bcrypt
	// for each), or mail every address it knows.
	LoginClientLimit *ratelimit.Config
	MailClientLimit  *ratelimit.Config
	// InviteLimit bounds the invites one user sends, so a signed-in
	// account cannot be used to mail strangers in bulk.
	InviteLimit *ratelimit.Config
	// TenantCreateLimit bounds the tenants one user may create, on top of
	// TenantOwnerLimit, so the cap can't be cycled through quickly.
	TenantCreateLimit *ratelimit.Config
	// TenantOwnerLimit is the most tenants a user may own before they can no
	// longer create one themselves. An admin's new_tenant invite ignores it.
	// 0 turns self-service creation off.
	TenantOwnerLimit int
	// TenantOwnerTotalLimit is the same count including archived tenants. 0
	// is no limit.
	TenantOwnerTotalLimit int
}

type Service struct {
	store             Store
	signer            *auth.Signer
	mail              Mailer
	passwords         Passwords
	providers         *login.Providers
	loginStates       *login.LoginStateCodec
	csrf              *auth.CSRFTokens
	baseURL           string
	cfg               *Config
	loginLimit        *ratelimit.Limiter
	mailLimit         *ratelimit.Limiter
	loginClientLimit  *ratelimit.Limiter
	mailClientLimit   *ratelimit.Limiter
	inviteLimit       *ratelimit.Limiter
	tenantCreateLimit *ratelimit.Limiter
	events            *audit.Recorder
	agents            TenantAgents
	logger            *slog.Logger
	clock             clock.Clock
}

// Deps are what Service is built from.
type Deps struct {
	Store  Store
	Signer *auth.Signer
	Mail   Mailer
	// Passwords nil means bcrypt.
	Passwords Passwords
	// Providers are the login providers people may sign in with besides a
	// password; nil means none.
	Providers *login.Providers
	// LoginStates seals the state cookie of a round trip through a
	// provider, so its callback acts only on a state this server issued.
	// Needed only with providers.
	LoginStates *login.LoginStateCodec
	// CSRF derives each browser session's CSRF token from its session id.
	CSRF *auth.CSRFTokens
	// Audit records sign-ins, refusals and sign-in method changes; nil
	// logs them to the service's log handler and counts nothing.
	Audit *audit.Recorder
	// Agents switches a tenant's agents off when it is archived. Without it
	// no tenant can be archived: the agent package owns that, and this one
	// does not import it.
	Agents TenantAgents
	// Clock is the clock password changes cut sessions off by; nil means the
	// wall clock. It should be the clock the Signer stamps tokens with, since
	// the cut-off is compared with a token's issue time.
	Clock clock.Clock
}

// Passwords hashes and checks passwords: go-auth/auth's bcrypt, behind an
// interface so a test can see which hash a sign-in was checked against.
type Passwords interface {
	Hash(plain string) (string, error)
	Verify(hash, plain string) error
}

type bcryptPasswords struct{}

func (bcryptPasswords) Hash(plain string) (string, error) { return auth.HashPassword(plain) }
func (bcryptPasswords) Verify(hash, plain string) error   { return auth.VerifyPassword(hash, plain) }

func NewService(deps *Deps, cfg *Config, logHandler slog.Handler) *Service {
	// Every session the service issues carries a CSRF token; without the
	// deriver the first sign-in would panic instead of startup failing.
	if deps.CSRF == nil {
		panic("user: NewService needs Deps.CSRF")
	}
	passwords := deps.Passwords
	if passwords == nil {
		passwords = bcryptPasswords{}
	}
	providers := deps.Providers
	if providers == nil {
		providers = login.NewProviders()
	}
	events := deps.Audit
	if events == nil {
		events = audit.New(logHandler, nil)
	}
	return &Service{
		clock:             clock.Or(deps.Clock),
		store:             deps.Store,
		signer:            deps.Signer,
		mail:              deps.Mail,
		passwords:         passwords,
		providers:         providers,
		loginStates:       deps.LoginStates,
		csrf:              deps.CSRF,
		baseURL:           strings.TrimRight(cfg.PublicBaseURL, "/"),
		cfg:               cfg,
		loginLimit:        ratelimit.New(cfg.LoginLimit),
		mailLimit:         ratelimit.New(cfg.MailLimit),
		loginClientLimit:  ratelimit.New(cfg.LoginClientLimit),
		mailClientLimit:   ratelimit.New(cfg.MailClientLimit),
		inviteLimit:       ratelimit.New(cfg.InviteLimit),
		tenantCreateLimit: ratelimit.New(cfg.TenantCreateLimit),
		events:            events,
		agents:            deps.Agents,
		logger:            slog.New(logHandler),
	}
}

// dummyHash stands in for a password hash when the address is unknown or
// its account has no password, so every refused sign-in costs the same
// bcrypt comparison as one for a real password -- otherwise the response
// time alone says which addresses have accounts, and which of those sign
// in only through a provider.
var dummyHash = func() string {
	h, err := auth.HashPassword(uuid.NewString())
	if err != nil {
		panic(fmt.Sprintf("user: dummy hash: %v", err))
	}
	return h
}()

// limitKey is what an attempt is counted under: the address and the client
// it came from (see clientip.RateKey; the zero address when none was
// recorded).
func limitKey(ctx context.Context, address string) string {
	return address + "|" + clientKey(ctx)
}

// clientKey is the client alone, for the limits that count across
// addresses.
func clientKey(ctx context.Context) string {
	return clientip.RateKey(clientip.FromContext(ctx))
}

// allowAttempt counts one attempt against address from this client, under
// both the per-address and the per-client limit.
func allowAttempt(ctx context.Context, perAddress, perClient *ratelimit.Limiter, address string) bool {
	return perClient.Allow(clientKey(ctx)) && perAddress.Allow(limitKey(ctx, address))
}

func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// hashPassword hashes a password for the paths whose request schema
// already bounds its length (sign-up, reset, invites, seeding).
func hashPassword(password string) (string, error) {
	return hashWith(bcryptPasswords{}, password)
}

func hashWith(passwords Passwords, password string) (string, error) {
	h, err := passwords.Hash(password)
	if errors.Is(err, auth.ErrPasswordTooLong) {
		return "", ErrPasswordTooLong
	}
	if err != nil {
		return "", fmt.Errorf("user: hash password: %w", err)
	}
	return h, nil
}

// RegisterInput is a sign-up.
type RegisterInput struct {
	Email       string
	Password    string
	DisplayName string
}

// Register creates an inactive account and emails its activation link.
//
// The response is the same whether or not the address was already
// registered -- the difference is in what arrives in that inbox: an
// activation link, a fresh one for an account never activated, or a note
// that the account already exists. Only the address's owner learns which.
func (s *Service) Register(ctx context.Context, in *RegisterInput) error {
	if !s.cfg.RegistrationOpen {
		return ErrRegistrationClosed
	}
	if err := s.requirePasswordSignIn(); err != nil {
		return err
	}
	address := normalizeEmail(in.Email)
	if !allowAttempt(ctx, s.mailLimit, s.mailClientLimit, address) {
		return ErrRateLimited
	}
	// Hashed before anything is looked up, so every path below costs the
	// same bcrypt round.
	hash, err := hashPassword(in.Password)
	if err != nil {
		return err
	}

	u := &User{ID: uuid.New(), Email: address, PasswordHash: hash, DisplayName: strings.TrimSpace(in.DisplayName)}
	raw, activation, err := s.newToken(u.ID, PurposeActivation, s.cfg.ActivationTTL)
	if err != nil {
		return err
	}
	created, err := s.store.Create(ctx, u, activation)
	if err != nil {
		return err
	}
	if created {
		s.mail.Enqueue(address, "Activate your slop-haus account", s.activationBody(raw))
		return nil
	}

	existing, err := s.store.FindByEmail(ctx, address)
	if err != nil {
		return err
	}
	if existing.IsActive {
		s.mail.Enqueue(address, "You already have a slop-haus account",
			"Someone -- hopefully you -- tried to sign up with this address, but it already has an account.\n\n"+
				"Sign in at "+s.baseURL+"/login, or reset your password at "+s.baseURL+"/forgot-password.\n")
		return nil
	}
	// Never activated: send a fresh link. The password given now is not
	// applied -- whoever registered first chose it, and this request has
	// not proved it owns the inbox any more than that one did.
	raw, activation, err = s.newToken(existing.ID, PurposeActivation, s.cfg.ActivationTTL)
	if err != nil {
		return err
	}
	if err := s.store.CreateToken(ctx, activation); err != nil {
		return err
	}
	s.mail.Enqueue(address, "Activate your slop-haus account", s.activationBody(raw))
	return nil
}

func (s *Service) activationBody(raw string) string {
	return "Activate your slop-haus account:\n\n" + s.link("/activate", raw) +
		"\n\nThe link works once and expires in " + s.cfg.ActivationTTL.String() + ".\n"
}

// link carries the token in the fragment, which a browser never sends to
// a server: not ours, where it would land in access logs, nor anyone's
// the page links to, through the Referer header.
func (s *Service) link(path, raw string) string {
	return s.baseURL + path + "#token=" + url.QueryEscape(raw)
}

func (s *Service) newToken(userID uuid.UUID, purpose TokenPurpose, ttl time.Duration) (string, *Token, error) {
	raw, hash, err := auth.NewOpaqueToken()
	if err != nil {
		return "", nil, fmt.Errorf("user: new token: %w", err)
	}
	return raw, &Token{
		ID:        uuid.New(),
		UserID:    userID,
		Hash:      hash,
		Purpose:   purpose,
		ExpiresAt: s.clock.Now().Add(ttl),
	}, nil
}

// Activate spends an activation link: the account becomes usable, with a
// tenant of its own.
func (s *Service) Activate(ctx context.Context, token string) error {
	return s.store.Activate(ctx, auth.HashOpaqueToken(token), uuid.New())
}

// Session is a signed-in user's tokens. Both are JWTs; the refresh token
// is exchanged for a new access token when the access token expires.
type Session struct {
	Access  string
	Refresh string
	Profile *Profile
	// CSRFToken is the session's CSRF token, for the page to send back on
	// its writes.
	CSRFToken string
}

// Login checks a password and starts a session.
func (s *Service) Login(ctx context.Context, rawEmail, password string) (*Session, error) {
	if err := s.requirePasswordSignIn(); err != nil {
		return nil, err
	}
	address := normalizeEmail(rawEmail)
	if !allowAttempt(ctx, s.loginLimit, s.loginClientLimit, address) {
		return nil, ErrRateLimited
	}

	u, err := s.store.FindByEmail(ctx, address)
	if errors.Is(err, ErrNotFound) {
		_ = s.passwords.Verify(dummyHash, password)
		s.audit(ctx, audit.SignInRefused, "method", "password", "reason", "no account")
		return nil, ErrInvalidCredentials
	}
	if err != nil {
		return nil, err
	}
	if u.PasswordHash == "" {
		// An account that signs in only through a provider: refused
		// exactly as an unknown address is, at the same cost.
		_ = s.passwords.Verify(dummyHash, password)
		s.audit(ctx, audit.SignInRefused, "method", "password", "user_id", u.ID, "reason", "no password")
		return nil, ErrInvalidCredentials
	}
	if err := s.passwords.Verify(u.PasswordHash, password); err != nil {
		s.audit(ctx, audit.SignInRefused, "method", "password", "user_id", u.ID, "reason", "wrong password")
		return nil, ErrInvalidCredentials
	}
	if !u.IsActive {
		s.audit(ctx, audit.SignInRefused, "method", "password", "user_id", u.ID, "reason", "account inactive")
		return nil, ErrInvalidCredentials
	}
	return s.signIn(ctx, u, "method", "password")
}

// signIn starts a new session for u after an interactive sign-in, however
// it was made -- a password or a provider -- so every way in lands in the
// same tenant, with the same fresh auth time, and is recorded the same way.
func (s *Service) signIn(ctx context.Context, u *User, detail ...any) (*Session, error) {
	tenantID, err := s.store.DefaultTenant(ctx, u.ID)
	if err != nil {
		// Every way an account becomes active -- activation, an accepted
		// invite, seeding -- gives it a tenant in the same transaction, so
		// this is a bug, not a caller error.
		return nil, fmt.Errorf("user: active user %s has no tenant: %w", u.ID, err)
	}
	session, err := s.newSessionFor(u, tenantID)
	if err != nil {
		return nil, err
	}
	s.audit(ctx, audit.SignIn, append([]any{"user_id", u.ID}, detail...)...)
	return session, nil
}

// sessionLive reports whether a session issued at issuedAt still stands
// for u: the account is active and the session postdates its last
// revocation (a password reset moves that cut-off forward).
//
// Compared in whole seconds, because a JWT's iat is: otherwise a session
// signed in during the same second as the cut-off -- the sign-in right
// after activation or a reset -- would be refused. A session from earlier
// in that second survives; that is the price.
func sessionLive(u *User, issuedAt time.Time) bool {
	return u.IsActive && !issuedAt.Before(u.SessionsValidAfter.Truncate(time.Second))
}

// liveSessionUser loads the signed-in user, refusing a session that has
// been revoked since it was issued. Every path that mints a new session
// from an existing one goes through here: an access token lives on
// unchecked for its short life, but it must not be exchangeable for a
// fresh, long-lived session after a reset or deactivation.
func (s *Service) liveSessionUser(ctx context.Context, who *authn.User) (*User, error) {
	u, err := s.store.FindByID(ctx, who.ID)
	if errors.Is(err, ErrNotFound) {
		return nil, ErrInvalidSession
	}
	if err != nil {
		return nil, err
	}
	if !sessionLive(u, who.IssuedAt) {
		return nil, ErrInvalidSession
	}
	return u, nil
}

// newSessionFor starts a new browser session for u, acting within
// tenantID: an interactive sign-in, so a fresh auth time and a fresh
// session id -- and so a CSRF token no earlier session had.
func (s *Service) newSessionFor(u *User, tenantID uuid.UUID) (*Session, error) {
	id, err := auth.NewSessionID()
	if err != nil {
		return nil, err
	}
	return s.sessionFor(u, tenantID, &auth.Session{AuthTime: s.clock.Now(), ID: id})
}

// sessionOf is the session who's token was issued for, to re-issue as it
// is.
func sessionOf(who *authn.User) *auth.Session {
	return &auth.Session{AuthTime: who.AuthTime, ID: who.SessionID}
}

// sessionFor issues tokens for sess, for u acting within tenantID. Only a
// sign-in (newSessionFor) starts a new sess, and only it or a
// re-authentication moves its auth time; anything that re-issues an
// existing session (a tenant switch, an accepted invite) carries both
// unchanged -- or a step-up could be had without proving anything, and the
// page's CSRF token would stop matching the session it is still in.
func (s *Service) sessionFor(u *User, tenantID uuid.UUID, sess *auth.Session) (*Session, error) {
	access, err := s.signer.IssueSessionAccessToken(u.ID, tenantID, sess)
	if err != nil {
		return nil, fmt.Errorf("user: issue access token: %w", err)
	}
	refresh, err := s.signer.IssueSessionRefreshToken(u.ID, tenantID, sess)
	if err != nil {
		return nil, fmt.Errorf("user: issue refresh token: %w", err)
	}
	return &Session{Access: access, Refresh: refresh, Profile: profileOf(u, tenantID), CSRFToken: s.csrf.Token(sess.ID)}, nil
}

// CSRFToken is the CSRF token of who's browser session: what the board
// sends back on every write (see authn.BrowserGuard). Empty for a session
// from before session ids, which cannot write.
func (s *Service) CSRFToken(who *authn.User) string {
	return s.csrf.Token(who.SessionID)
}

// ValidCSRF is authn.CSRFValidator.
func (s *Service) ValidCSRF(sessionID, token string) bool {
	return s.csrf.Valid(sessionID, token)
}

func profileOf(u *User, tenantID uuid.UUID) *Profile {
	return &Profile{ID: u.ID, Email: u.Email, DisplayName: u.DisplayName, TenantID: tenantID, IsAdmin: u.IsAdmin}
}

// Refresh exchanges a refresh token for a new access token.
//
// This is the one place a session is checked against the database, which
// is what bounds how long a stateless access token can outlive a password
// reset, a deactivation or a removed membership: one access-token TTL.
func (s *Service) Refresh(ctx context.Context, refreshToken string) (string, error) {
	claims, err := s.signer.ParseRefreshToken(refreshToken)
	if err != nil {
		return "", ErrInvalidSession
	}
	u, err := s.store.FindByID(ctx, claims.UserID)
	if errors.Is(err, ErrNotFound) {
		return "", ErrInvalidSession
	}
	if err != nil {
		return "", err
	}
	if claims.IssuedAt == nil || !sessionLive(u, claims.IssuedAt.Time) {
		return "", ErrInvalidSession
	}
	member, err := s.store.IsMember(ctx, u.ID, claims.TenantID)
	if err != nil {
		return "", err
	}
	if !member {
		return "", ErrInvalidSession
	}
	if err := s.requireLiveTenant(ctx, claims.TenantID); err != nil {
		return "", err
	}
	// The auth time and session id are the refresh token's, unchanged: a
	// refresh proves the browser still holds the session, not that its
	// person is present, and it is still the same session. A session from
	// before session ids stays without one -- minting one here would hand
	// a refresh the power to make a session that can write, which only a
	// sign-in should have.
	access, err := s.signer.IssueSessionAccessToken(u.ID, claims.TenantID, claims.Session())
	if err != nil {
		return "", fmt.Errorf("user: issue access token: %w", err)
	}
	return access, nil
}

// RequestPasswordReset emails a reset link if the address has an active
// account, and does nothing visible otherwise.
func (s *Service) RequestPasswordReset(ctx context.Context, rawEmail string) error {
	if err := s.requirePasswordSignIn(); err != nil {
		return err
	}
	address := normalizeEmail(rawEmail)
	if !allowAttempt(ctx, s.mailLimit, s.mailClientLimit, address) {
		return ErrRateLimited
	}
	u, err := s.store.FindByEmail(ctx, address)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if !u.IsActive {
		return nil
	}
	if u.PasswordHash == "" {
		// Only a password can be reset. Adding one is a sign-in method
		// change, made signed in and stepped up, not from an inbox. The
		// note goes only to the address's owner, so it tells nobody else
		// anything.
		s.mail.Enqueue(address, "Your slop-haus account has no password",
			"Someone -- hopefully you -- asked to reset the password of the slop-haus account at this address, "+
				"but it has no password: it signs in with a login provider.\n\n"+
				"Sign in at "+s.baseURL+"/login with the provider you used before. Once signed in, you can add a password.\n")
		return nil
	}
	raw, token, err := s.newToken(u.ID, PurposePasswordReset, s.cfg.ResetTTL)
	if err != nil {
		return err
	}
	if err := s.store.CreateToken(ctx, token); err != nil {
		return err
	}
	s.mail.Enqueue(address, "Reset your slop-haus password",
		"Choose a new slop-haus password:\n\n"+s.link("/reset-password", raw)+
			"\n\nThe link works once and expires in "+s.cfg.ResetTTL.String()+
			". If you did not ask for this, ignore this email; your password is unchanged.\n")
	return nil
}

// ResetPassword spends a reset link. Every existing session ends with it:
// a reset is often a response to someone else having the password.
//
// The token is checked before the password is hashed, so a made-up token
// costs a lookup rather than a bcrypt round: this route is public, and
// unchecked it would be a way to make the server hash on demand. It is
// checked again when spent, atomically, so the early look is only a
// filter.
func (s *Service) ResetPassword(ctx context.Context, token, password string) error {
	if err := s.requirePasswordSignIn(); err != nil {
		return err
	}
	tokenHash := auth.HashOpaqueToken(token)
	usable, err := s.store.TokenUsable(ctx, tokenHash, PurposePasswordReset)
	if err != nil {
		return err
	}
	if !usable {
		return ErrInvalidToken
	}
	hash, err := hashPassword(password)
	if err != nil {
		return err
	}
	return s.store.ResetPassword(ctx, tokenHash, hash, s.clock.Now())
}

// VerifyAccess is authn.UserVerifier: a valid access token is its own
// proof, without a database read -- which is why its TTL is short.
func (s *Service) VerifyAccess(_ context.Context, accessToken string) (*authn.User, error) {
	claims, err := s.signer.ParseAccessToken(accessToken)
	if err != nil {
		return nil, err
	}
	who := &authn.User{ID: claims.UserID, TenantID: claims.TenantID}
	if claims.IssuedAt != nil {
		who.IssuedAt = claims.IssuedAt.Time
	}
	if claims.AuthTime != nil {
		who.AuthTime = claims.AuthTime.Time
	}
	who.SessionID = claims.SessionID
	return who, nil
}

// Me is the signed-in user's own profile.
func (s *Service) Me(ctx context.Context, who *authn.User) (*Profile, error) {
	u, err := s.store.FindByID(ctx, who.ID)
	if err != nil {
		return nil, err
	}
	return profileOf(u, who.TenantID), nil
}

// DisplayName is what to call a person where an agent acts for them.
func (s *Service) DisplayName(ctx context.Context, userID uuid.UUID) (string, error) {
	u, err := s.store.FindByID(ctx, userID)
	if err != nil {
		return "", err
	}
	return u.Label(), nil
}
