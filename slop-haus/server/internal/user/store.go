package user

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
)

// Store is the persistence Service needs. Declared beside Service rather
// than the Postgres implementation so the dependency points inward, as in
// the other packages.
type Store interface {
	FindByEmail(ctx context.Context, email string) (*User, error)
	FindByID(ctx context.Context, id uuid.UUID) (*User, error)
	// Create inserts an inactive user and their activation token together,
	// returning created=false (and writing nothing) when the email is
	// already registered.
	Create(ctx context.Context, u *User, activation *Token) (created bool, err error)
	CreateToken(ctx context.Context, t *Token) error
	// Activate spends an activation token: the user becomes active with a
	// tenant of their own. ErrInvalidToken if the token cannot be spent.
	Activate(ctx context.Context, tokenHash string, tenantID uuid.UUID) error
	// TokenUsable reports whether a token could be spent now, without
	// spending it.
	TokenUsable(ctx context.Context, tokenHash string, purpose TokenPurpose) (bool, error)
	// ResetPassword spends a reset token, replacing the password and
	// ending every existing session.
	ResetPassword(ctx context.Context, tokenHash, passwordHash string, sessionsValidAfter time.Time) error
	// EnsureActive creates u already active, with a tenant, unless the
	// email is taken -- in which case nothing changes.
	EnsureActive(ctx context.Context, u *User, tenantID uuid.UUID) (created bool, err error)
	// DefaultTenant is the tenant a sign-in lands in: never an archived one.
	DefaultTenant(ctx context.Context, userID uuid.UUID) (uuid.UUID, error)
	// OtherActiveTenant is another unarchived tenant the user belongs to,
	// the oldest first, for a session leaving the one named; ErrNotFound if
	// they have none.
	OtherActiveTenant(ctx context.Context, userID, except uuid.UUID) (uuid.UUID, error)
	IsMember(ctx context.Context, userID, tenantID uuid.UUID) (bool, error)
	// TenantArchived reports whether the tenant has been archived. One
	// indexed read, never cached: see Service.TenantArchived.
	TenantArchived(ctx context.Context, tenantID uuid.UUID) (bool, error)
	// IsOwner reports whether the user made the tenant, as opposed to being
	// invited into it.
	IsOwner(ctx context.Context, userID, tenantID uuid.UUID) (bool, error)
	ListMemberDirectory(ctx context.Context, tenantID uuid.UUID) (*MemberDirectory, error)
	// ListTenants lists the tenants the user belongs to. Archived ones are
	// left out unless includeArchived, and then only those the user owns.
	ListTenants(ctx context.Context, userID uuid.UUID, includeArchived bool) ([]TenantSummary, error)
	// CreateTenant makes t, owned by the user, unless they already own
	// limits.Active unarchived tenants or limits.Total tenants in all
	// (ErrTenantLimit). The counts are taken under the user's row lock, so
	// concurrent creates can't both pass.
	CreateTenant(ctx context.Context, userID uuid.UUID, t *NewTenant, limits OwnerLimits) error
	// ArchiveTenant archives a tenant the user owns and revokes the invites
	// into it, in one transaction. ErrNotOwner for a user who is not its
	// owner, ErrLastActiveTenant if it is the only unarchived tenant they
	// own. Archiving an archived tenant changes nothing and reports
	// changed=false.
	ArchiveTenant(ctx context.Context, userID, tenantID uuid.UUID) (changed bool, err error)
	// UnarchiveTenant brings back a tenant the user owns, unless that would
	// leave them owning more than activeLimit unarchived tenants
	// (ErrUnarchiveOverLimit). ErrNotOwner for a user who is not its owner.
	// Unarchiving a tenant that is not archived reports changed=false.
	UnarchiveTenant(ctx context.Context, userID, tenantID uuid.UUID, activeLimit int) (changed bool, err error)
	// RenameTenant renames a tenant the user owns, returning the name it
	// had. ErrNotOwner for a user who is not its owner, ErrTenantNameTaken
	// for a name equal to another of the user's tenants.
	RenameTenant(ctx context.Context, userID, tenantID uuid.UUID, name string) (old string, err error)
	// SetTenantColour sets, or with nil clears, the colour of a tenant the
	// user owns, returning the colour it had. ErrNotOwner for a user who is
	// not its owner.
	SetTenantColour(ctx context.Context, userID, tenantID uuid.UUID, colour *string) (old *string, err error)
	IsAdmin(ctx context.Context, userID uuid.UUID) (bool, error)
	SetAdmin(ctx context.Context, email string) error

	CreateInvite(ctx context.Context, inv *Invite, tokenHash string) error
	// FindInvite returns an invite that can still be accepted, with the
	// names the invite page shows; ErrInvalidToken otherwise.
	FindInvite(ctx context.Context, tokenHash string) (*Invite, *InvitePreview, error)
	// AcceptInviteAsNewAccount spends the invite and creates u, active, at
	// the invite's address -- the link proved the address, as an
	// activation link would. ErrAccountExists if the address has an
	// account.
	AcceptInviteAsNewAccount(ctx context.Context, tokenHash string, u *User, newTenant *NewTenant) (tenantID uuid.UUID, err error)
	// AcceptInvite spends the invite for an existing account, which must
	// be at the invite's address.
	AcceptInvite(ctx context.Context, tokenHash string, userID uuid.UUID, email string, newTenant *NewTenant) (tenantID uuid.UUID, err error)
	// ListInvites lists outstanding invites into a tenant, or with a nil
	// tenant, outstanding invites to start one.
	ListInvites(ctx context.Context, tenantID *uuid.UUID) ([]Invite, error)
	// RevokeInvite withdraws an outstanding invite, within the same scope
	// ListInvites uses; ErrNotFound if there is no such invite there.
	RevokeInvite(ctx context.Context, id uuid.UUID, tenantID *uuid.UUID) error

	IdentityStore
}

// Token is an emailed one-time token. Only its hash is stored.
type Token struct {
	ID        uuid.UUID
	UserID    uuid.UUID
	Hash      string
	Purpose   TokenPurpose
	ExpiresAt time.Time
}

type PostgresStore struct {
	db              *db.DB
	onTenantCreated TenantCreatedHook
	logger          *slog.Logger
}

func NewPostgresStore(database *db.DB, opts ...StoreOption) *PostgresStore {
	s := &PostgresStore{db: database}
	for _, o := range opts {
		o(s)
	}
	return s
}

var _ Store = (*PostgresStore)(nil)

// userColumns reads a NULL password_hash -- no password sign-in -- as "".
const userColumns = "id, email, COALESCE(password_hash, ''), display_name, is_active, is_admin, sessions_valid_after, date_joined"

func scanUser(row pgx.Row) (*User, error) {
	var u User
	err := row.Scan(&u.ID, &u.Email, &u.PasswordHash, &u.DisplayName, &u.IsActive, &u.IsAdmin, &u.SessionsValidAfter, &u.DateJoined)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("user: scan: %w", err)
	}
	return &u, nil
}

func (s *PostgresStore) FindByEmail(ctx context.Context, email string) (*User, error) {
	return scanUser(s.db.Global().QueryRow(ctx,
		`SELECT `+userColumns+` FROM global_users WHERE email = $1`, email))
}

func (s *PostgresStore) FindByID(ctx context.Context, id uuid.UUID) (*User, error) {
	return scanUser(s.db.Global().QueryRow(ctx,
		`SELECT `+userColumns+` FROM global_users WHERE id = $1`, id))
}

func (s *PostgresStore) Create(ctx context.Context, u *User, activation *Token) (bool, error) {
	created := false
	err := s.db.InGlobalTx(ctx, func(q db.Querier) error {
		// ON CONFLICT rather than look-then-insert: two registrations for
		// one address racing each other must not both get past a check.
		tag, err := q.Exec(ctx, `
			INSERT INTO global_users (id, email, password_hash, display_name)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT (email) DO NOTHING`,
			u.ID, u.Email, u.PasswordHash, u.DisplayName)
		if err != nil {
			return fmt.Errorf("user: insert: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return nil
		}
		created = true
		return insertToken(ctx, q, activation)
	})
	return created, err
}

func (s *PostgresStore) CreateToken(ctx context.Context, t *Token) error {
	return insertToken(ctx, s.db.Global(), t)
}

func insertToken(ctx context.Context, q db.Querier, t *Token) error {
	_, err := q.Exec(ctx, `
		INSERT INTO global_auth_tokens (id, user_id, token_hash, purpose, expires_at)
		VALUES ($1, $2, $3, $4, $5)`,
		t.ID, t.UserID, t.Hash, string(t.Purpose), t.ExpiresAt)
	if err != nil {
		return fmt.Errorf("user: insert token: %w", err)
	}
	return nil
}

func (s *PostgresStore) TokenUsable(ctx context.Context, tokenHash string, purpose TokenPurpose) (bool, error) {
	var usable bool
	err := s.db.Global().QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM global_auth_tokens
			WHERE token_hash = $1 AND purpose = $2 AND used_at IS NULL AND expires_at > now())`,
		tokenHash, string(purpose)).Scan(&usable)
	if err != nil {
		return false, fmt.Errorf("user: check token: %w", err)
	}
	return usable, nil
}

// spendToken marks a token used and returns whose it was, in one
// statement: a find-then-mark would let two requests with the same link
// both get past the find -- two activations, two tenants.
func spendToken(ctx context.Context, q db.Querier, tokenHash string, purpose TokenPurpose) (uuid.UUID, error) {
	var userID uuid.UUID
	err := q.QueryRow(ctx, `
		UPDATE global_auth_tokens SET used_at = now()
		WHERE token_hash = $1 AND purpose = $2 AND used_at IS NULL AND expires_at > now()
		RETURNING user_id`,
		tokenHash, string(purpose)).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, ErrInvalidToken
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("user: spend token: %w", err)
	}
	return userID, nil
}

func (s *PostgresStore) Activate(ctx context.Context, tokenHash string, tenantID uuid.UUID) error {
	return s.inTenantTx(ctx, func(ctx context.Context, q db.Querier) error {
		userID, err := spendToken(ctx, q, tokenHash, PurposeActivation)
		if err != nil {
			return err
		}
		tag, err := q.Exec(ctx, `UPDATE global_users SET is_active = true WHERE id = $1 AND NOT is_active`, userID)
		if err != nil {
			return fmt.Errorf("user: activate: %w", err)
		}
		if tag.RowsAffected() == 0 {
			// Already active through some other path (the seed command);
			// a second tenant would be a surprise, not a feature.
			return ErrInvalidToken
		}
		return provisionTenant(ctx, q, userID, &NewTenant{ID: tenantID})
	})
}

func (s *PostgresStore) ResetPassword(ctx context.Context, tokenHash, passwordHash string, sessionsValidAfter time.Time) error {
	return s.db.InGlobalTx(ctx, func(q db.Querier) error {
		userID, err := spendToken(ctx, q, tokenHash, PurposePasswordReset)
		if err != nil {
			return err
		}
		if _, err := q.Exec(ctx, `
			UPDATE global_users
			SET password_hash = $2, sessions_valid_after = $3
			WHERE id = $1`,
			userID, passwordHash, sessionsValidAfter); err != nil {
			return fmt.Errorf("user: update password: %w", err)
		}
		// Any other reset link already mailed is now stale; one that sits
		// in an inbox is a way back in for whoever reads that inbox next.
		return expireResetTokens(ctx, q, userID)
	})
}

func (s *PostgresStore) EnsureActive(ctx context.Context, u *User, tenantID uuid.UUID) (bool, error) {
	created := false
	err := s.inTenantTx(ctx, func(ctx context.Context, q db.Querier) error {
		tag, err := q.Exec(ctx, `
			INSERT INTO global_users (id, email, password_hash, display_name, is_active)
			VALUES ($1, $2, $3, $4, true)
			ON CONFLICT (email) DO NOTHING`,
			u.ID, u.Email, u.PasswordHash, u.DisplayName)
		if err != nil {
			return fmt.Errorf("user: insert: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return nil
		}
		created = true
		return provisionTenant(ctx, q, u.ID, &NewTenant{ID: tenantID})
	})
	return created, err
}

func (s *PostgresStore) IsAdmin(ctx context.Context, userID uuid.UUID) (bool, error) {
	var admin bool
	err := s.db.Global().QueryRow(ctx,
		`SELECT is_admin AND is_active FROM global_users WHERE id = $1`, userID).Scan(&admin)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("user: is admin: %w", err)
	}
	return admin, nil
}

func (s *PostgresStore) SetAdmin(ctx context.Context, email string) error {
	tag, err := s.db.Global().Exec(ctx, `UPDATE global_users SET is_admin = true WHERE email = $1`, email)
	if err != nil {
		return fmt.Errorf("user: set admin: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
