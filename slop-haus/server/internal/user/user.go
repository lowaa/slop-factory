// Package user is the human side of authentication: registration,
// activation, sign-in and password reset. It issues the session the board
// runs on; authn checks that session on every request.
//
// Everything here lives in global_ tables, read and written through the
// database's global handle: a sign-in has to find the user, and their
// tenant, before any tenant is known.
package user

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	goauthn "github.com/lowaa/slop-factory/go-auth/authn"
)

// User is an account. PasswordHash never leaves this package.
type User struct {
	ID    uuid.UUID
	Email string
	// PasswordHash is empty for an account with no password: its owner
	// signs in only through a login provider.
	PasswordHash string
	DisplayName  string
	IsActive     bool
	// IsAdmin may invite people to start tenants of their own.
	IsAdmin bool
	// SessionsValidAfter is when the user's sessions last became invalid;
	// a refresh token issued before it is refused.
	SessionsValidAfter time.Time
	DateJoined         time.Time
}

// Profile is what the signed-in user may see about themselves.
type Profile struct {
	ID          uuid.UUID
	Email       string
	DisplayName string
	TenantID    uuid.UUID
	IsAdmin     bool
}

// TenantSummary is one tenant a user belongs to.
type TenantSummary struct {
	ID   uuid.UUID
	Name string
	// IsOwner is whether the caller owns the tenant, as opposed to having
	// been invited into it.
	IsOwner bool
	// Owners are the display names of the tenant's owners.
	Owners []string
	// Colour is the owner's chosen palette key, nil for the default.
	Colour *string
	// ArchivedAt is when the owner archived the tenant; nil if they have not.
	// Only an owner is ever shown an archived tenant.
	ArchivedAt *time.Time
}

// InviteKind is what accepting an invite does.
type InviteKind string

const (
	// InviteNewTenant is an admin's: the invitee gets a tenant of their own.
	InviteNewTenant InviteKind = "new_tenant"
	// InviteJoinTenant is a member's: the invitee joins the inviter's
	// tenant.
	InviteJoinTenant InviteKind = "join_tenant"
)

// Invite is an outstanding invitation. The token itself is only ever in
// the email.
type Invite struct {
	ID        uuid.UUID
	Email     string
	Kind      InviteKind
	TenantID  *uuid.UUID
	InvitedBy uuid.UUID
	ExpiresAt time.Time
	CreatedAt time.Time
}

// InvitePreview is what the invite page shows before it is accepted.
type InvitePreview struct {
	Email      string
	Kind       InviteKind
	TenantName string
	InvitedBy  string
	// AccountExists decides whether the page asks the invitee to sign in
	// or to choose a password. It is told only to whoever holds the
	// invite, which was sent to that address.
	AccountExists bool
}

// Member is what one member of a tenant may see about another: enough to
// put a name to a comment.
type Member struct {
	ID          uuid.UUID
	DisplayName string
}

// MemberDirectory names the people of a tenant: who belongs now, and who left
// and still has an account. Id and display name only.
type MemberDirectory struct {
	Members []Member
	Former  []Member
}

// TokenPurpose is what an emailed one-time token may be exchanged for.
type TokenPurpose string

const (
	PurposeActivation    TokenPurpose = "activation"
	PurposePasswordReset TokenPurpose = "password_reset"
)

var (
	// ErrInvalidCredentials covers an unknown email, a wrong password and
	// an account not yet activated alike, so a sign-in form cannot be used
	// to find out which addresses have accounts.
	ErrInvalidCredentials = errors.New("user: invalid email or password")
	// ErrInvalidToken covers unknown, expired and already-used tokens.
	ErrInvalidToken = errors.New("user: invalid or expired token")
	// ErrInvalidSession means a refresh was refused: expired, forged,
	// revoked by a password reset, or for an account or tenant the user no
	// longer has.
	ErrInvalidSession = errors.New("user: session is no longer valid")
	// ErrPasswordTooLong is bcrypt's limit, which counts bytes, so a
	// maxLength on the request (which counts characters) cannot enforce it.
	ErrPasswordTooLong = errors.New("user: password is longer than 72 bytes")
	ErrNotFound        = errors.New("user: not found")
	// ErrNotOwner means a tenant's owner was needed and the caller is not one.
	ErrNotOwner = errors.New("user: only a tenant's owner can do this")
	// ErrTenantLimit means the user already owns as many tenants as
	// self-service creation allows. Admin invites are not counted against it.
	ErrTenantLimit = errors.New("user: you own the most workspaces you can create yourself; ask an admin for another")
	// ErrLastActiveTenant is archiving the only unarchived tenant a person
	// owns, which would leave them with nowhere to sign in to.
	ErrLastActiveTenant = errors.New("user: this is the only active workspace you own; create another before archiving it")
	// ErrUnarchiveOverLimit is unarchiving a tenant that would take its owner
	// over the number of active workspaces they may own.
	ErrUnarchiveOverLimit = errors.New("user: you own the most active workspaces you can; archive another to bring this one back")
	// ErrAgentsUnavailable is archiving with no way to switch the tenant's
	// agents off; it fails closed rather than archive a tenant whose agents
	// would carry on.
	ErrAgentsUnavailable = errors.New("user: archiving workspaces is not available here")
	// ErrArchiveNameMismatch is an archive whose typed confirmation is not the
	// tenant's exact name.
	ErrArchiveNameMismatch = errors.New("user: the name typed does not match the workspace's name")
	// ErrTenantCreationOff means self-service creation is switched off here.
	ErrTenantCreationOff = errors.New("user: creating your own workspace is turned off here; ask an admin")
	// ErrPasswordSignInOff means this deployment signs in through login
	// providers only, so nothing that sets, checks or resets a password is
	// served.
	ErrPasswordSignInOff = errors.New("user: password sign-in is turned off here; sign in with a login provider")
	// ErrRegistrationClosed means sign-up needs an invite.
	ErrRegistrationClosed = errors.New("user: sign-up is by invitation")
	// ErrAccountExists means an invite was accepted as a new account for
	// an address that already has one; its owner should sign in and
	// accept it from there.
	ErrAccountExists = errors.New("user: that address already has an account; sign in to accept")
	// ErrWrongAccount means a signed-in user tried to accept an invite
	// sent to a different address.
	ErrWrongAccount = errors.New("user: this invite was sent to a different address")
	// ErrAlreadyMember means the invitee is already in the tenant.
	ErrAlreadyMember = errors.New("user: already a member of this tenant")
	// ErrNotMember means a switch to a tenant the user is not in.
	ErrNotMember = errors.New("user: not a member of that tenant")
	// ErrTenantArchived means the tenant was archived: no session starts or
	// continues in it until it is unarchived.
	ErrTenantArchived = errors.New("user: this tenant was archived")
	// ErrPasswordTooShort is the password policy's lower bound.
	ErrPasswordTooShort = fmt.Errorf("user: a password must be at least %d characters", MinPasswordLength)

	// ErrReauthRequired refuses a sensitive change from a session whose
	// last interactive authentication is older than StepUpMaxAge.
	ErrReauthRequired = goauthn.ErrReauthRequired
	// ErrNotSignedIn means an action that needs a session had none.
	ErrNotSignedIn = errors.New("user: sign in first")
	// ErrIdentityTaken means the provider identity belongs to another
	// account. Identities never move between accounts.
	ErrIdentityTaken = errors.New("user: that identity already belongs to another account")
	// ErrAlreadyLinked means the identity is already this account's.
	ErrAlreadyLinked = errors.New("user: that identity is already linked to this account")
	// ErrProviderAlreadyLinked means the account already has a different
	// identity at the same provider.
	ErrProviderAlreadyLinked = errors.New("user: this account already has an identity at that provider; remove it first")
	// ErrLastMethod refuses removing the only way left to sign in.
	ErrLastMethod = errors.New("user: that is the only way left to sign in to this account")
	// ErrNoAccount means a provider vouched for an identity no account
	// has. Accounts start from invites, never from a sign-in.
	ErrNoAccount = errors.New("user: there is no account for that identity here; accept an invite, or sign in another way and link it")
	// ErrSessionMismatch means a link or re-authentication came back to a
	// browser no longer signed in as the account that started it.
	ErrSessionMismatch = errors.New("user: you are no longer signed in as the account that started this; sign in and start again")
	// ErrIdentityMismatch means a re-authentication came back with an
	// identity that is not the signed-in account's.
	ErrIdentityMismatch = errors.New("user: that identity is not linked to the account you are signed in as")
	// ErrUnknownProvider means no login provider of that name is
	// configured here.
	ErrUnknownProvider = errors.New("user: no such login provider here")
	// ErrProviderUnavailable means the provider can't serve sign-ins yet
	// (an OIDC issuer whose discovery hasn't succeeded). Temporary.
	ErrProviderUnavailable = errors.New("user: login provider unavailable; try again shortly")
	// ErrProviderFailed means the provider would not confirm the identity.
	ErrProviderFailed = errors.New("user: could not confirm your identity with the login provider")
	// ErrUnknownIntent means a sign-in was started for something it can't
	// be started for there.
	ErrUnknownIntent = errors.New("user: unknown sign-in intent")
	// ErrLoginExpired covers a callback without the state its browser was
	// given: expired, started elsewhere, or forged.
	ErrLoginExpired = errors.New("user: this sign-in has expired or was not started here; start again")
	// ErrLoginCancelled means the provider sent the browser back without
	// a code: the person cancelled, or the provider refused.
	ErrLoginCancelled = errors.New("user: the sign-in was cancelled or refused")
	// ErrPostRequired refuses a navigation asking to start a round trip
	// that acts on an account: those start from a POST (see
	// Handler.StartSignIn).
	ErrPostRequired = errors.New("user: accepting an invite or re-authenticating through a login provider starts from this site's own pages")
)

const (
	// MinPasswordLength is the shortest password accepted. The request
	// schemas say the same; this is the service's own check, for the
	// paths that don't go through one.
	MinPasswordLength = 10
	// MaxPasswordBytes is bcrypt's limit, in bytes.
	MaxPasswordBytes = 72
	// StepUpMaxAge is how recently a session must have been authenticated
	// interactively for a sensitive change; see authn.StepUpMaxAge.
	StepUpMaxAge = goauthn.StepUpMaxAge
)

// Identity is an identity a login provider vouches for, as this package
// stores it. Subject is the provider's stable ID; Login is display-only
// and may change. Email is only what the provider said: it never decides
// which account anyone is.
type Identity struct {
	Provider    string
	Subject     string
	Login       string
	DisplayName string
	Email       string
}

// LinkedIdentity is an identity attached to an account.
type LinkedIdentity struct {
	Provider    string
	Subject     string
	Login       string
	CreatedAt   time.Time
	LastLoginAt *time.Time
}

// Label is how to name the person on a ticket: their display name, or a
// neutral word when they have set none. Never the address: ticket comments
// are read by every agent and model in the tenant.
func (u *User) Label() string {
	if u.DisplayName != "" {
		return u.DisplayName
	}
	return "a member"
}
