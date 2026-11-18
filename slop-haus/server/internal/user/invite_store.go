package user

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
)

func (s *PostgresStore) CreateInvite(ctx context.Context, inv *Invite, tokenHash string) error {
	_, err := s.db.Global().Exec(ctx, `
		INSERT INTO global_invites (id, token_hash, email, kind, tenant, invited_by, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		inv.ID, tokenHash, inv.Email, string(inv.Kind), inv.TenantID, inv.InvitedBy, inv.ExpiresAt)
	if err != nil {
		return fmt.Errorf("user: create invite: %w", err)
	}
	return nil
}

// usableInvite is the condition an invite must meet to be accepted.
const usableInvite = `accepted_at IS NULL AND revoked_at IS NULL AND expires_at > now()`

// inviteTenantLive is the condition that the tenant an invite is into, if it
// has one, is not archived. Archiving revokes the invites outstanding then;
// this holds for any made after.
const inviteTenantLive = `NOT EXISTS (SELECT 1 FROM global_tenants it WHERE it.id = i.tenant AND it.archived_at IS NOT NULL)`

const inviteColumns = `i.id, i.email, i.kind, i.tenant, i.invited_by, i.expires_at, i.created_at`

func scanInvite(row pgx.Row, extra ...any) (*Invite, error) {
	var inv Invite
	var kind string
	dest := append([]any{&inv.ID, &inv.Email, &kind, &inv.TenantID, &inv.InvitedBy, &inv.ExpiresAt, &inv.CreatedAt}, extra...)
	if err := row.Scan(dest...); err != nil {
		return nil, err
	}
	inv.Kind = InviteKind(kind)
	return &inv, nil
}

func (s *PostgresStore) FindInvite(ctx context.Context, tokenHash string) (*Invite, *InvitePreview, error) {
	var preview InvitePreview
	inv, err := scanInvite(s.db.Global().QueryRow(ctx, `
		SELECT `+inviteColumns+`, COALESCE(t.name, ''), inviter.display_name,
			EXISTS (SELECT 1 FROM global_users u WHERE u.email = i.email)
		FROM global_invites i
		JOIN global_users inviter ON inviter.id = i.invited_by
		LEFT JOIN global_tenants t ON t.id = i.tenant
		WHERE i.token_hash = $1 AND i.`+usableInvite+` AND `+inviteTenantLive, tokenHash),
		&preview.TenantName, &preview.InvitedBy, &preview.AccountExists)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, ErrInvalidToken
	}
	if err != nil {
		return nil, nil, fmt.Errorf("user: find invite: %w", err)
	}
	preview.Email = inv.Email
	preview.Kind = inv.Kind
	return inv, &preview, nil
}

// spendInvite marks an invite accepted and returns it, in one statement,
// for the same reason spendToken does: two clicks on one link must not
// both get past a check. emailMust, when set, is the address the invite
// has to be for; an invite for another address is left unspent.
func spendInvite(ctx context.Context, q db.Querier, tokenHash string, emailMust string) (*Invite, error) {
	inv, err := scanInvite(q.QueryRow(ctx, `
		UPDATE global_invites i SET accepted_at = now()
		WHERE i.token_hash = $1 AND i.`+usableInvite+` AND `+inviteTenantLive+` AND ($2 = '' OR i.email = $2)
		RETURNING `+inviteColumns, tokenHash, emailMust))
	if errors.Is(err, pgx.ErrNoRows) {
		if emailMust != "" {
			// Distinguish "not for you" from "no such invite", so a
			// signed-in user who opened someone else's link is told why.
			var exists bool
			if err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM global_invites i
				WHERE i.token_hash = $1 AND i.`+usableInvite+`)`, tokenHash).Scan(&exists); err != nil {
				return nil, fmt.Errorf("user: spend invite: %w", err)
			}
			if exists {
				return nil, ErrWrongAccount
			}
		}
		return nil, ErrInvalidToken
	}
	if err != nil {
		return nil, fmt.Errorf("user: spend invite: %w", err)
	}
	return inv, nil
}

// joinInvitedTenant does what an invite grants: a tenant of the user's own,
// or a seat in the inviter's.
func joinInvitedTenant(ctx context.Context, q db.Querier, inv *Invite, userID uuid.UUID, newTenant *NewTenant) (uuid.UUID, error) {
	if inv.Kind == InviteNewTenant {
		return newTenant.ID, provisionTenant(ctx, q, userID, newTenant)
	}
	tag, err := q.Exec(ctx, `
		INSERT INTO global_tenant_memberships (id, tenant, user_id) VALUES ($1, $2, $3)
		ON CONFLICT (tenant, user_id) DO NOTHING`,
		uuid.New(), *inv.TenantID, userID)
	if err != nil {
		return uuid.Nil, fmt.Errorf("user: join tenant: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return uuid.Nil, ErrAlreadyMember
	}
	return *inv.TenantID, nil
}

func (s *PostgresStore) AcceptInviteAsNewAccount(ctx context.Context, tokenHash string, u *User, newTenant *NewTenant) (uuid.UUID, error) {
	var tenantID uuid.UUID
	err := s.inTenantTx(ctx, func(ctx context.Context, q db.Querier) error {
		inv, err := spendInvite(ctx, q, tokenHash, "")
		if err != nil {
			return err
		}
		// The invite's address, not one the request names: following the
		// link is the proof of owning it.
		u.Email = inv.Email
		tag, err := q.Exec(ctx, `
			INSERT INTO global_users (id, email, password_hash, display_name, is_active)
			VALUES ($1, $2, $3, $4, true)
			ON CONFLICT (email) DO NOTHING`,
			u.ID, u.Email, u.PasswordHash, u.DisplayName)
		if err != nil {
			return fmt.Errorf("user: insert: %w", err)
		}
		if tag.RowsAffected() == 0 {
			// Rolls the spend back too: the invite is still good for the
			// account that already exists.
			return ErrAccountExists
		}
		tenantID, err = joinInvitedTenant(ctx, q, inv, u.ID, newTenant)
		return err
	})
	return tenantID, err
}

func (s *PostgresStore) AcceptInvite(ctx context.Context, tokenHash string, userID uuid.UUID, email string, newTenant *NewTenant) (uuid.UUID, error) {
	var tenantID uuid.UUID
	err := s.inTenantTx(ctx, func(ctx context.Context, q db.Querier) error {
		inv, err := spendInvite(ctx, q, tokenHash, email)
		if err != nil {
			return err
		}
		tenantID, err = joinInvitedTenant(ctx, q, inv, userID, newTenant)
		return err
	})
	return tenantID, err
}

func (s *PostgresStore) ListInvites(ctx context.Context, tenantID *uuid.UUID) ([]Invite, error) {
	rows, err := s.db.Global().Query(ctx, `
		SELECT `+inviteColumns+` FROM global_invites i
		WHERE `+inviteScope+` AND i.`+usableInvite+`
		ORDER BY i.created_at DESC, i.id`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("user: list invites: %w", err)
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Invite, error) {
		inv, err := scanInvite(row)
		if err != nil {
			return Invite{}, err
		}
		return *inv, nil
	})
}

// inviteScope is which invites a caller may see and revoke: a tenant's
// members see the invites into their tenant, admins the invites to start
// one. $1 is the tenant, or NULL for an admin.
const inviteScope = `(($1::uuid IS NULL AND i.kind = 'new_tenant') OR i.tenant = $1::uuid)`

func (s *PostgresStore) RevokeInvite(ctx context.Context, id uuid.UUID, tenantID *uuid.UUID) error {
	tag, err := s.db.Global().Exec(ctx, `
		UPDATE global_invites i SET revoked_at = now()
		WHERE i.id = $2 AND `+inviteScope+` AND i.`+usableInvite, tenantID, id)
	if err != nil {
		return fmt.Errorf("user: revoke invite: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
