package user

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/lowaa/slop-factory/go-db/pgerr"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
)

// IdentityStore is the persistence for an account's ways of signing in:
// its password, and the provider identities attached to it.
//
// Every change takes the account's row lock first (lockUser), so changes
// to one account's methods are decided one at a time: two removals racing
// can't each see the other's method still there and leave none.
type IdentityStore interface {
	// IdentityOwner is the account the identity belongs to; ErrNotFound if
	// none.
	IdentityOwner(ctx context.Context, provider, subject string) (uuid.UUID, error)
	// RecordIdentityLogin notes a sign-in with ident, refreshing what the
	// provider last said about it.
	RecordIdentityLogin(ctx context.Context, ident *Identity) error
	ListIdentities(ctx context.Context, userID uuid.UUID) ([]LinkedIdentity, error)
	// LinkIdentity attaches ident to the account. ErrAlreadyLinked if it is
	// already the account's, ErrIdentityTaken if it is anyone else's,
	// ErrProviderAlreadyLinked if the account has another identity at the
	// same provider.
	LinkIdentity(ctx context.Context, userID uuid.UUID, ident *Identity) error
	// AcceptInviteWithIdentity spends an invite on the account ident
	// belongs to -- which must be at the invite's address, as AcceptInvite
	// requires -- or, when ident is nobody's, on a new account (newUser)
	// at the invite's address, with ident attached and no password.
	// ErrIdentityTaken if ident belongs to an account at another address;
	// ErrAccountExists if the address already has an account ident is not
	// attached to. Nothing is spent on a refusal.
	AcceptInviteWithIdentity(ctx context.Context, tokenHash string, ident *Identity, newUser *User, newTenant *NewTenant) (userID, tenantID uuid.UUID, err error)
	// SetPassword sets or replaces the account's password and ends every
	// session issued before sessionsValidAfter.
	SetPassword(ctx context.Context, userID uuid.UUID, passwordHash string, sessionsValidAfter time.Time) error
	// RemovePassword deletes the account's password, unless no identity at
	// one of usable's providers would remain to sign in with
	// (ErrLastMethod). ErrNotFound if it has none.
	RemovePassword(ctx context.Context, userID uuid.UUID, usable *UsableMethods) error
	// RemoveIdentity detaches one of the account's identities, unless
	// nothing usable would remain: another identity at one of usable's
	// providers, or a password where usable.Password (ErrLastMethod).
	// ErrNotFound if it is not the account's.
	RemoveIdentity(ctx context.Context, userID uuid.UUID, provider, subject string, usable *UsableMethods) error
}

var _ IdentityStore = (*PostgresStore)(nil)

// lockUser takes the account's row lock for the rest of the transaction,
// reporting whether it has a password.
func lockUser(ctx context.Context, q db.Querier, userID uuid.UUID) (hasPassword bool, err error) {
	err = q.QueryRow(ctx, `SELECT password_hash IS NOT NULL FROM global_users WHERE id = $1 FOR UPDATE`, userID).Scan(&hasPassword)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, ErrNotFound
	}
	if err != nil {
		return false, fmt.Errorf("user: lock user: %w", err)
	}
	return hasPassword, nil
}

func identityOwner(ctx context.Context, q db.Querier, provider, subject string) (uuid.UUID, error) {
	var owner uuid.UUID
	err := q.QueryRow(ctx, `SELECT user_id FROM global_user_identities WHERE provider = $1 AND subject = $2`,
		provider, subject).Scan(&owner)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, ErrNotFound
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("user: identity owner: %w", err)
	}
	return owner, nil
}

// insertIdentity attaches ident to userID. A unique violation is the
// identity arriving at the same moment for someone else -- the one-per-
// provider rule is checked under the account's lock before this runs.
func insertIdentity(ctx context.Context, q db.Querier, userID uuid.UUID, ident *Identity) error {
	_, err := q.Exec(ctx, `
		INSERT INTO global_user_identities (provider, subject, user_id, login, email, last_login_at)
		VALUES ($1, $2, $3, $4, $5, now())`,
		ident.Provider, ident.Subject, userID, ident.Login, ident.Email)
	if pgerr.IsUniqueViolation(err) {
		return ErrIdentityTaken
	}
	if err != nil {
		return fmt.Errorf("user: insert identity: %w", err)
	}
	return nil
}

// usableIdentities counts userID's identities at usableProviders, leaving
// out the one named by exceptProvider/exceptSubject.
func usableIdentities(ctx context.Context, q db.Querier, userID uuid.UUID, usableProviders []string, exceptProvider, exceptSubject string) (int, error) {
	var n int
	err := q.QueryRow(ctx, `
		SELECT count(*) FROM global_user_identities
		WHERE user_id = $1 AND provider = ANY($2::text[]) AND NOT (provider = $3 AND subject = $4)`,
		userID, usableProviders, exceptProvider, exceptSubject).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("user: count identities: %w", err)
	}
	return n, nil
}

// expireResetTokens spends every outstanding reset link of userID's: one
// sitting in an inbox would otherwise set a password the account's owner
// has since changed or removed.
func expireResetTokens(ctx context.Context, q db.Querier, userID uuid.UUID) error {
	if _, err := q.Exec(ctx, `
		UPDATE global_auth_tokens SET used_at = now()
		WHERE user_id = $1 AND purpose = $2 AND used_at IS NULL`,
		userID, string(PurposePasswordReset)); err != nil {
		return fmt.Errorf("user: expire reset tokens: %w", err)
	}
	return nil
}

func (s *PostgresStore) IdentityOwner(ctx context.Context, provider, subject string) (uuid.UUID, error) {
	return identityOwner(ctx, s.db.Global(), provider, subject)
}

func (s *PostgresStore) RecordIdentityLogin(ctx context.Context, ident *Identity) error {
	if _, err := s.db.Global().Exec(ctx, `
		UPDATE global_user_identities SET login = $3, email = $4, last_login_at = now()
		WHERE provider = $1 AND subject = $2`,
		ident.Provider, ident.Subject, ident.Login, ident.Email); err != nil {
		return fmt.Errorf("user: record identity login: %w", err)
	}
	return nil
}

func (s *PostgresStore) ListIdentities(ctx context.Context, userID uuid.UUID) ([]LinkedIdentity, error) {
	rows, err := s.db.Global().Query(ctx, `
		SELECT provider, subject, login, created_at, last_login_at
		FROM global_user_identities WHERE user_id = $1
		ORDER BY provider, subject`, userID)
	if err != nil {
		return nil, fmt.Errorf("user: list identities: %w", err)
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (LinkedIdentity, error) {
		var li LinkedIdentity
		err := row.Scan(&li.Provider, &li.Subject, &li.Login, &li.CreatedAt, &li.LastLoginAt)
		return li, err
	})
}

func (s *PostgresStore) LinkIdentity(ctx context.Context, userID uuid.UUID, ident *Identity) error {
	return s.db.InGlobalTx(ctx, func(q db.Querier) error {
		if _, err := lockUser(ctx, q, userID); err != nil {
			return err
		}
		owner, err := identityOwner(ctx, q, ident.Provider, ident.Subject)
		switch {
		case err == nil && owner == userID:
			return ErrAlreadyLinked
		case err == nil:
			return ErrIdentityTaken
		case !errors.Is(err, ErrNotFound):
			return err
		}
		var atProvider bool
		if err := q.QueryRow(ctx, `
			SELECT EXISTS (SELECT 1 FROM global_user_identities WHERE user_id = $1 AND provider = $2)`,
			userID, ident.Provider).Scan(&atProvider); err != nil {
			return fmt.Errorf("user: identity at provider: %w", err)
		}
		if atProvider {
			return ErrProviderAlreadyLinked
		}
		return insertIdentity(ctx, q, userID, ident)
	})
}

func (s *PostgresStore) AcceptInviteWithIdentity(ctx context.Context, tokenHash string, ident *Identity, newUser *User, newTenant *NewTenant) (uuid.UUID, uuid.UUID, error) {
	var userID, tenantID uuid.UUID
	err := s.inTenantTx(ctx, func(ctx context.Context, q db.Querier) error {
		owner, err := identityOwner(ctx, q, ident.Provider, ident.Subject)
		if err == nil {
			userID = owner
			return acceptInviteAsIdentityOwner(ctx, q, tokenHash, owner, newTenant, &tenantID)
		}
		if !errors.Is(err, ErrNotFound) {
			return err
		}
		inv, err := spendInvite(ctx, q, tokenHash, "")
		if err != nil {
			return err
		}
		// The invite's address, as AcceptInviteAsNewAccount uses: having
		// the emailed link is the proof of owning it. The provider's own
		// idea of the person's email plays no part.
		newUser.Email = inv.Email
		tag, err := q.Exec(ctx, `
			INSERT INTO global_users (id, email, password_hash, display_name, is_active)
			VALUES ($1, $2, NULL, $3, true)
			ON CONFLICT (email) DO NOTHING`,
			newUser.ID, newUser.Email, newUser.DisplayName)
		if err != nil {
			return fmt.Errorf("user: insert: %w", err)
		}
		if tag.RowsAffected() == 0 {
			// Never attached by matching the address: its owner signs in
			// and links the identity from there.
			return ErrAccountExists
		}
		if err := insertIdentity(ctx, q, newUser.ID, ident); err != nil {
			return err
		}
		userID = newUser.ID
		tenantID, err = joinInvitedTenant(ctx, q, inv, newUser.ID, newTenant)
		return err
	})
	return userID, tenantID, err
}

// acceptInviteAsIdentityOwner is AcceptInvite for the account an identity
// already belongs to: the invite must be for that account's address.
func acceptInviteAsIdentityOwner(ctx context.Context, q db.Querier, tokenHash string, owner uuid.UUID, newTenant *NewTenant, tenantID *uuid.UUID) error {
	var email string
	var active bool
	err := q.QueryRow(ctx, `SELECT email, is_active FROM global_users WHERE id = $1 FOR UPDATE`, owner).Scan(&email, &active)
	if err != nil {
		return fmt.Errorf("user: identity owner: %w", err)
	}
	if !active {
		return ErrNoAccount
	}
	inv, err := spendInvite(ctx, q, tokenHash, email)
	if errors.Is(err, ErrWrongAccount) {
		// The identity is someone else's than the invite's: it is never
		// moved, and never a second way into the invite either.
		return ErrIdentityTaken
	}
	if err != nil {
		return err
	}
	*tenantID, err = joinInvitedTenant(ctx, q, inv, owner, newTenant)
	return err
}

func (s *PostgresStore) SetPassword(ctx context.Context, userID uuid.UUID, passwordHash string, sessionsValidAfter time.Time) error {
	return s.db.InGlobalTx(ctx, func(q db.Querier) error {
		if _, err := lockUser(ctx, q, userID); err != nil {
			return err
		}
		if _, err := q.Exec(ctx, `
			UPDATE global_users SET password_hash = $2, sessions_valid_after = $3 WHERE id = $1`,
			userID, passwordHash, sessionsValidAfter); err != nil {
			return fmt.Errorf("user: set password: %w", err)
		}
		return expireResetTokens(ctx, q, userID)
	})
}

func (s *PostgresStore) RemovePassword(ctx context.Context, userID uuid.UUID, usable *UsableMethods) error {
	return s.db.InGlobalTx(ctx, func(q db.Querier) error {
		hasPassword, err := lockUser(ctx, q, userID)
		if err != nil {
			return err
		}
		if !hasPassword {
			return ErrNotFound
		}
		left, err := usableIdentities(ctx, q, userID, usable.Providers, "", "")
		if err != nil {
			return err
		}
		if left == 0 {
			return ErrLastMethod
		}
		if _, err := q.Exec(ctx, `UPDATE global_users SET password_hash = NULL WHERE id = $1`, userID); err != nil {
			return fmt.Errorf("user: remove password: %w", err)
		}
		return expireResetTokens(ctx, q, userID)
	})
}

func (s *PostgresStore) RemoveIdentity(ctx context.Context, userID uuid.UUID, provider, subject string, usable *UsableMethods) error {
	return s.db.InGlobalTx(ctx, func(q db.Querier) error {
		hasPassword, err := lockUser(ctx, q, userID)
		if err != nil {
			return err
		}
		owner, err := identityOwner(ctx, q, provider, subject)
		if err != nil {
			return err
		}
		if owner != userID {
			return ErrNotFound
		}
		left, err := usableIdentities(ctx, q, userID, usable.Providers, provider, subject)
		if err != nil {
			return err
		}
		if hasPassword && usable.Password {
			left++
		}
		if left == 0 {
			return ErrLastMethod
		}
		if _, err := q.Exec(ctx, `DELETE FROM global_user_identities WHERE provider = $1 AND subject = $2`, provider, subject); err != nil {
			return fmt.Errorf("user: remove identity: %w", err)
		}
		return nil
	})
}
