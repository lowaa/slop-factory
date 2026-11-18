package user

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
)

// provisionTenant gives a user a tenant of their own, as its first member.
// A tenant with no name is named after the person. Every path that makes a
// tenant comes through here, so the rule that a person's tenants are told
// apart by name holds for all of them.
func provisionTenant(ctx context.Context, q db.Querier, userID uuid.UUID, t *NewTenant) error {
	// Serialises a person's concurrent tenant-making and renaming, so two
	// that each pass the clash check can't both write the same name.
	if _, err := lockUser(ctx, q, userID); err != nil {
		return err
	}
	name := t.Name
	if name == "" {
		var displayName string
		if err := q.QueryRow(ctx, `SELECT display_name FROM global_users WHERE id = $1`, userID).Scan(&displayName); err != nil {
			return fmt.Errorf("user: default tenant name: %w", err)
		}
		name = defaultTenantName(displayName)
	}
	if err := requireNameFree(ctx, q, userID, name, uuid.Nil); err != nil {
		return err
	}
	if _, err := q.Exec(ctx, `INSERT INTO global_tenants (id, name) VALUES ($1, $2)`, t.ID, name); err != nil {
		return fmt.Errorf("user: insert tenant: %w", err)
	}
	if _, err := q.Exec(ctx, `
		INSERT INTO global_tenant_memberships (id, tenant, user_id, role) VALUES ($1, $2, $3, 'owner')`,
		uuid.New(), t.ID, userID); err != nil {
		return fmt.Errorf("user: insert membership: %w", err)
	}
	noteMadeTenant(ctx, t.ID, userID)
	return nil
}

// requireNameFree refuses a name equal, ignoring case, to one of the
// user's other tenants. Only the user's own tenants are looked at: see
// ErrTenantNameTaken.
func requireNameFree(ctx context.Context, q db.Querier, userID uuid.UUID, name string, except uuid.UUID) error {
	var taken bool
	if err := q.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM global_tenant_memberships m JOIN global_tenants t ON t.id = m.tenant
			WHERE m.user_id = $1 AND t.id <> $3 AND lower(t.name) = lower($2))`,
		userID, name, except).Scan(&taken); err != nil {
		return fmt.Errorf("user: tenant name clash: %w", err)
	}
	if taken {
		return ErrTenantNameTaken
	}
	return nil
}

// OwnerLimits are how many tenants a person may own. Total counts archived
// ones too, so that archiving and creating again can't go on for ever; zero
// means no such limit.
type OwnerLimits struct {
	Active int
	Total  int
}

// ownedCounts is how many tenants the user owns, unarchived and in all.
func ownedCounts(ctx context.Context, q db.Querier, userID uuid.UUID) (active, total int, err error) {
	err = q.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE t.archived_at IS NULL), count(*)
		FROM global_tenant_memberships m JOIN global_tenants t ON t.id = m.tenant
		WHERE m.user_id = $1 AND m.role = 'owner'`, userID).Scan(&active, &total)
	if err != nil {
		return 0, 0, fmt.Errorf("user: count owned tenants: %w", err)
	}
	return active, total, nil
}

func (s *PostgresStore) CreateTenant(ctx context.Context, userID uuid.UUID, t *NewTenant, limits OwnerLimits) error {
	return s.inTenantTx(ctx, func(ctx context.Context, q db.Querier) error {
		// provisionTenant takes this lock too; taking it first makes the
		// count below and the insert one step for this user.
		if _, err := lockUser(ctx, q, userID); err != nil {
			return err
		}
		active, total, err := ownedCounts(ctx, q, userID)
		if err != nil {
			return err
		}
		if active >= limits.Active || (limits.Total > 0 && total >= limits.Total) {
			return ErrTenantLimit
		}
		return provisionTenant(ctx, q, userID, t)
	})
}

// lockOwnedTenant takes the user's row lock, so their tenant-making,
// archiving and renaming are one at a time, then the tenant's row, and
// reports when it was archived (nil if it is not). ErrNotOwner for a tenant
// they don't own.
func lockOwnedTenant(ctx context.Context, q db.Querier, userID, tenantID uuid.UUID) (archivedAt *time.Time, err error) {
	if _, err := lockUser(ctx, q, userID); err != nil {
		return nil, err
	}
	owner, err := isOwner(ctx, q, userID, tenantID)
	if err != nil {
		return nil, err
	}
	if !owner {
		return nil, ErrNotOwner
	}
	if err := q.QueryRow(ctx, `SELECT archived_at FROM global_tenants WHERE id = $1 FOR UPDATE`, tenantID).Scan(&archivedAt); err != nil {
		return nil, fmt.Errorf("user: lock tenant: %w", err)
	}
	return archivedAt, nil
}

func (s *PostgresStore) ArchiveTenant(ctx context.Context, userID, tenantID uuid.UUID) (bool, error) {
	changed := false
	err := s.db.InGlobalTx(ctx, func(q db.Querier) error {
		archivedAt, err := lockOwnedTenant(ctx, q, userID, tenantID)
		if err != nil {
			return err
		}
		if archivedAt != nil {
			return nil
		}
		active, _, err := ownedCounts(ctx, q, userID)
		if err != nil {
			return err
		}
		if active <= 1 {
			return ErrLastActiveTenant
		}
		if _, err := q.Exec(ctx, `UPDATE global_tenants SET archived_at = now(), archived_by = $2 WHERE id = $1`, tenantID, userID); err != nil {
			return fmt.Errorf("user: archive tenant: %w", err)
		}
		if _, err := q.Exec(ctx, `UPDATE global_invites SET revoked_at = now() WHERE tenant = $1 AND `+usableInvite, tenantID); err != nil {
			return fmt.Errorf("user: revoke tenant invites: %w", err)
		}
		changed = true
		return nil
	})
	return changed, err
}

func (s *PostgresStore) UnarchiveTenant(ctx context.Context, userID, tenantID uuid.UUID, activeLimit int) (bool, error) {
	changed := false
	err := s.db.InGlobalTx(ctx, func(q db.Querier) error {
		archivedAt, err := lockOwnedTenant(ctx, q, userID, tenantID)
		if err != nil {
			return err
		}
		if archivedAt == nil {
			return nil
		}
		active, _, err := ownedCounts(ctx, q, userID)
		if err != nil {
			return err
		}
		if active >= activeLimit {
			return ErrUnarchiveOverLimit
		}
		if _, err := q.Exec(ctx, `UPDATE global_tenants SET archived_at = NULL, archived_by = NULL, archived_by_deleted = false WHERE id = $1`, tenantID); err != nil {
			return fmt.Errorf("user: unarchive tenant: %w", err)
		}
		changed = true
		return nil
	})
	return changed, err
}

func (s *PostgresStore) RenameTenant(ctx context.Context, userID, tenantID uuid.UUID, name string) (string, error) {
	var old string
	err := s.db.InGlobalTx(ctx, func(q db.Querier) error {
		if _, err := lockUser(ctx, q, userID); err != nil {
			return err
		}
		// Inside the transaction: an owner demoted a moment ago must not
		// rename on the strength of an earlier check.
		owner, err := isOwner(ctx, q, userID, tenantID)
		if err != nil {
			return err
		}
		if !owner {
			return ErrNotOwner
		}
		if err := requireNameFree(ctx, q, userID, name, tenantID); err != nil {
			return err
		}
		if err := q.QueryRow(ctx, `SELECT name FROM global_tenants WHERE id = $1 FOR UPDATE`, tenantID).Scan(&old); err != nil {
			return fmt.Errorf("user: read tenant name: %w", err)
		}
		if _, err := q.Exec(ctx, `UPDATE global_tenants SET name = $2 WHERE id = $1`, tenantID, name); err != nil {
			return fmt.Errorf("user: rename tenant: %w", err)
		}
		return nil
	})
	return old, err
}

func (s *PostgresStore) SetTenantColour(ctx context.Context, userID, tenantID uuid.UUID, colour *string) (*string, error) {
	var old *string
	err := s.db.InGlobalTx(ctx, func(q db.Querier) error {
		if _, err := lockUser(ctx, q, userID); err != nil {
			return err
		}
		// Inside the transaction, as RenameTenant does: an owner demoted a
		// moment ago must not recolour on the strength of an earlier check.
		owner, err := isOwner(ctx, q, userID, tenantID)
		if err != nil {
			return err
		}
		if !owner {
			return ErrNotOwner
		}
		if err := q.QueryRow(ctx, `SELECT colour FROM global_tenants WHERE id = $1 FOR UPDATE`, tenantID).Scan(&old); err != nil {
			return fmt.Errorf("user: read tenant colour: %w", err)
		}
		if _, err := q.Exec(ctx, `UPDATE global_tenants SET colour = $2 WHERE id = $1`, tenantID, colour); err != nil {
			return fmt.Errorf("user: set tenant colour: %w", err)
		}
		return nil
	})
	return old, err
}

func (s *PostgresStore) DefaultTenant(ctx context.Context, userID uuid.UUID) (uuid.UUID, error) {
	var tenantID uuid.UUID
	// The oldest unarchived membership: the tenant made at activation, until
	// there is a way to choose.
	err := s.db.Global().QueryRow(ctx, `
		SELECT m.tenant FROM global_tenant_memberships m JOIN global_tenants t ON t.id = m.tenant
		WHERE m.user_id = $1 AND t.archived_at IS NULL ORDER BY m.created_at, m.id LIMIT 1`, userID).Scan(&tenantID)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, ErrNotFound
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("user: default tenant: %w", err)
	}
	return tenantID, nil
}

func (s *PostgresStore) OtherActiveTenant(ctx context.Context, userID, except uuid.UUID) (uuid.UUID, error) {
	var tenantID uuid.UUID
	err := s.db.Global().QueryRow(ctx, `
		SELECT m.tenant FROM global_tenant_memberships m JOIN global_tenants t ON t.id = m.tenant
		WHERE m.user_id = $1 AND m.tenant <> $2 AND t.archived_at IS NULL ORDER BY m.created_at, m.id LIMIT 1`,
		userID, except).Scan(&tenantID)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, ErrNotFound
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("user: other active tenant: %w", err)
	}
	return tenantID, nil
}

func (s *PostgresStore) IsMember(ctx context.Context, userID, tenantID uuid.UUID) (bool, error) {
	var member bool
	err := s.db.Global().QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM global_tenant_memberships WHERE user_id = $1 AND tenant = $2)`,
		userID, tenantID).Scan(&member)
	if err != nil {
		return false, fmt.Errorf("user: membership: %w", err)
	}
	return member, nil
}

func (s *PostgresStore) TenantArchived(ctx context.Context, tenantID uuid.UUID) (bool, error) {
	var archived bool
	err := s.db.Global().QueryRow(ctx, `SELECT archived_at IS NOT NULL FROM global_tenants WHERE id = $1`, tenantID).Scan(&archived)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, ErrNotFound
	}
	if err != nil {
		return false, fmt.Errorf("user: tenant archived: %w", err)
	}
	return archived, nil
}

// IsTenantOwner lets the store stand as the owner lookup the gates in other
// packages ask, in tests as in the board.
func (s *PostgresStore) IsTenantOwner(ctx context.Context, userID, tenantID uuid.UUID) (bool, error) {
	return s.IsOwner(ctx, userID, tenantID)
}

func (s *PostgresStore) IsOwner(ctx context.Context, userID, tenantID uuid.UUID) (bool, error) {
	return isOwner(ctx, s.db.Global(), userID, tenantID)
}

func isOwner(ctx context.Context, q db.Querier, userID, tenantID uuid.UUID) (bool, error) {
	var owner bool
	err := q.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM global_tenant_memberships WHERE user_id = $1 AND tenant = $2 AND role = 'owner')`,
		userID, tenantID).Scan(&owner)
	if err != nil {
		return false, fmt.Errorf("user: ownership: %w", err)
	}
	return owner, nil
}

// ListMemberDirectory is one statement so the two lists come from one
// snapshot: a member who leaves between two queries would otherwise be in
// both lists, or in neither. The tenant filter on each side is the only thing
// keeping another tenant's people out. A former row of someone who has since
// rejoined is skipped (the trigger removes it anyway; this is a guard).
func (s *PostgresStore) ListMemberDirectory(ctx context.Context, tenantID uuid.UUID) (*MemberDirectory, error) {
	rows, err := s.db.Global().Query(ctx, `
		SELECT u.id, u.display_name, true AS current
		FROM global_tenant_memberships m JOIN global_users u ON u.id = m.user_id
		WHERE m.tenant = $1
		UNION ALL
		SELECT u.id, u.display_name, false
		FROM global_former_memberships f JOIN global_users u ON u.id = f.user_id
		WHERE f.tenant = $1
		  AND NOT EXISTS (SELECT 1 FROM global_tenant_memberships m WHERE m.tenant = f.tenant AND m.user_id = f.user_id)
		ORDER BY display_name, id`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("user: list members: %w", err)
	}
	defer rows.Close()
	dir := &MemberDirectory{}
	for rows.Next() {
		var m Member
		var current bool
		if err := rows.Scan(&m.ID, &m.DisplayName, &current); err != nil {
			return nil, fmt.Errorf("user: list members: %w", err)
		}
		if current {
			dir.Members = append(dir.Members, m)
		} else {
			dir.Former = append(dir.Former, m)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("user: list members: %w", err)
	}
	return dir, nil
}

func (s *PostgresStore) ListTenants(ctx context.Context, userID uuid.UUID, includeArchived bool) ([]TenantSummary, error) {
	// Owners are read through the caller's own membership rows, so only
	// tenants the caller belongs to are described.
	rows, err := s.db.Global().Query(ctx, `
		SELECT t.id, t.name, m.role = 'owner', t.colour, t.archived_at,
			COALESCE((SELECT array_agg(u.display_name ORDER BY om.created_at, om.id)
				FROM global_tenant_memberships om JOIN global_users u ON u.id = om.user_id
				WHERE om.tenant = t.id AND om.role = 'owner'), '{}')
		FROM global_tenant_memberships m JOIN global_tenants t ON t.id = m.tenant
		WHERE m.user_id = $1 AND (t.archived_at IS NULL OR ($2 AND m.role = 'owner'))
		ORDER BY m.created_at, m.id`, userID, includeArchived)
	if err != nil {
		return nil, fmt.Errorf("user: list tenants: %w", err)
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (TenantSummary, error) {
		var t TenantSummary
		err := row.Scan(&t.ID, &t.Name, &t.IsOwner, &t.Colour, &t.ArchivedAt, &t.Owners)
		return t, err
	})
}
