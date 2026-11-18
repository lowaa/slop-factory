package user

import (
	"context"
	"unicode/utf8"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
)

// A person has one account and any number of ways to sign in to it: a
// password, and identities at the configured login providers. Every change
// to them needs a recent interactive authentication (RequireRecentAuth),
// and the last one that can sign in can't be removed.

// IdentityMethod is a linked identity as its account's owner sees it.
type IdentityMethod struct {
	Provider string
	Subject  string
	Login    string
	// Usable is whether it can sign in here: only identities of a
	// configured provider can.
	Usable bool
}

// Methods are an account's ways of signing in.
type Methods struct {
	// Email is the account's address, which a password signs in with.
	Email       string
	HasPassword bool
	Identities  []IdentityMethod
}

func (s *Service) SignInMethods(ctx context.Context, who *authn.User) (*Methods, error) {
	u, err := s.store.FindByID(ctx, who.ID)
	if err != nil {
		return nil, err
	}
	linked, err := s.store.ListIdentities(ctx, who.ID)
	if err != nil {
		return nil, err
	}
	m := &Methods{Email: u.Email, HasPassword: u.PasswordHash != "", Identities: make([]IdentityMethod, 0, len(linked))}
	for _, li := range linked {
		_, usable := s.providers.Get(li.Provider)
		m.Identities = append(m.Identities, IdentityMethod{Provider: li.Provider, Subject: li.Subject, Login: li.Login, Usable: usable})
	}
	return m, nil
}

// UsableMethods are the ways of signing in this deployment accepts, which
// are the only ones that count when deciding whether a removal would leave
// an account no way in.
type UsableMethods struct {
	Password  bool
	Providers []string
}

func (s *Service) usableMethods() *UsableMethods {
	return &UsableMethods{Password: s.PasswordLoginEnabled(), Providers: s.providers.Names()}
}

// checkPassword is the password policy, for the paths whose request schema
// doesn't already enforce it: long enough to resist guessing, and within
// bcrypt's limit, which counts bytes -- go-auth refuses rather than
// truncate, and so does this, with a message that says why.
func checkPassword(password string) error {
	if utf8.RuneCountInString(password) < MinPasswordLength {
		return ErrPasswordTooShort
	}
	if len(password) > MaxPasswordBytes {
		return ErrPasswordTooLong
	}
	return nil
}

// SetPassword gives who's account a password, or changes the one it has.
// Every other session of the account ends -- whoever else is signed in may
// be whoever made the change necessary -- and this one is re-issued, with
// its auth time unchanged, so it survives.
func (s *Service) SetPassword(ctx context.Context, who *authn.User, password string) (*Session, error) {
	if err := s.requirePasswordSignIn(); err != nil {
		return nil, err
	}
	if err := RequireRecentAuth(who); err != nil {
		return nil, err
	}
	if err := checkPassword(password); err != nil {
		return nil, err
	}
	u, err := s.liveSessionUser(ctx, who)
	if err != nil {
		return nil, err
	}
	// Before the change, not after: once sessions_valid_after moves, this
	// session's own token no longer passes liveSessionUser either.
	if err := s.requireMember(ctx, who); err != nil {
		return nil, err
	}
	hash, err := hashWith(s.passwords, password)
	if err != nil {
		return nil, err
	}
	// The cut-off is taken from this clock, not the database's, so the
	// session issued below -- on this clock, after it -- is always on the
	// right side of it.
	if err := s.store.SetPassword(ctx, u.ID, hash, s.clock.Now()); err != nil {
		return nil, err
	}
	action := audit.PasswordChanged
	if u.PasswordHash == "" {
		action = audit.PasswordAdded
	}
	s.audit(ctx, action, "user_id", u.ID, "sessions_revoked", "others")
	return s.sessionFor(u, who.TenantID, sessionOf(who))
}

// RemovePassword deletes who's password, unless it is their only way in.
func (s *Service) RemovePassword(ctx context.Context, who *authn.User) error {
	if err := RequireRecentAuth(who); err != nil {
		return err
	}
	u, err := s.liveSessionUser(ctx, who)
	if err != nil {
		return err
	}
	if err := s.store.RemovePassword(ctx, u.ID, s.usableMethods()); err != nil {
		return err
	}
	s.audit(ctx, audit.SignInMethodRemoved, "user_id", u.ID, "method", "password")
	return nil
}

// RemoveIdentity unlinks one of who's identities, unless it is their only
// way in.
func (s *Service) RemoveIdentity(ctx context.Context, who *authn.User, provider, subject string) error {
	if err := RequireRecentAuth(who); err != nil {
		return err
	}
	u, err := s.liveSessionUser(ctx, who)
	if err != nil {
		return err
	}
	if err := s.store.RemoveIdentity(ctx, u.ID, provider, subject, s.usableMethods()); err != nil {
		return err
	}
	s.audit(ctx, audit.SignInMethodRemoved, "user_id", u.ID, "method", provider, "provider", provider, "subject", subject)
	return nil
}
