package agent

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/lowaa/slop-factory/go-db/pgerr"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// PostgresRoleStore keeps roles and their members. Every query runs in
// the context's tenant, and row level security hides every other tenant's
// roles: an id from another tenant reads as not found.
type PostgresRoleStore struct {
	db *db.DB
}

func NewPostgresRoleStore(database *db.DB) *PostgresRoleStore {
	return &PostgresRoleStore{db: database}
}

var _ RoleStore = (*PostgresRoleStore)(nil)

const roleColumns = `r.id, r.name, r.description, r.created_at`

func scanRole(row pgx.Row) (*Role, error) {
	var r Role
	if err := row.Scan(&r.ID, &r.Name, &r.Description, &r.CreatedAt); err != nil {
		return nil, err
	}
	return &r, nil
}

func (s *PostgresRoleStore) ListRoles(ctx context.Context) ([]Role, error) {
	rows, err := s.db.Query(ctx, `SELECT `+roleColumns+` FROM agent_roles r ORDER BY r.name`)
	if err != nil {
		return nil, fmt.Errorf("agent role: list: %w", err)
	}
	return collectRoles(rows)
}

func collectRoles(rows pgx.Rows) ([]Role, error) {
	defer rows.Close()
	roles := []Role{}
	for rows.Next() {
		r, err := scanRole(rows)
		if err != nil {
			return nil, fmt.Errorf("agent role: scan: %w", err)
		}
		roles = append(roles, *r)
	}
	return roles, rows.Err()
}

func (s *PostgresRoleStore) CreateRole(ctx context.Context, id uuid.UUID, name, description string) (*Role, error) {
	created, err := scanRole(s.db.QueryRow(ctx, `
		INSERT INTO agent_roles AS r (id, name, description) VALUES ($1, $2, $3)
		RETURNING `+roleColumns, id, name, description))
	if pgerr.IsUniqueViolation(err) {
		return nil, fmt.Errorf("%w: %q", ErrRoleNameTaken, name)
	}
	if err != nil {
		return nil, fmt.Errorf("agent role: create: %w", err)
	}
	return created, nil
}

func (s *PostgresRoleStore) UpdateRole(ctx context.Context, id uuid.UUID, params *RoleParams) (*Role, error) {
	updated, err := scanRole(s.db.QueryRow(ctx, `
		UPDATE agent_roles AS r
		   SET name = COALESCE($2, r.name),
		       description = COALESCE($3, r.description)
		 WHERE r.id = $1
		RETURNING `+roleColumns, id, params.Name, params.Description))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrRoleNotFound
	}
	if pgerr.IsUniqueViolation(err) {
		return nil, fmt.Errorf("%w: %q", ErrRoleNameTaken, *params.Name)
	}
	if err != nil {
		return nil, fmt.Errorf("agent role: update: %w", err)
	}
	return updated, nil
}

// DeleteRole locks the role before deleting it, so the tickets requiring
// it are checked, and cleared, in this same transaction without a new
// requirement slipping in between the check and the delete: a new
// requirement's foreign key check waits on this lock.
//
// Lock order: the role, then the rows referring to it. Every writer of
// those rows -- agent.SetAgentRoles, a ticket's required roles -- locks the
// roles it names first too, which is what keeps this deadlock-free.
func (s *PostgresRoleStore) DeleteRole(ctx context.Context, id uuid.UUID, clearTickets bool) error {
	return s.db.InTx(ctx, func(q db.Querier) error {
		var locked uuid.UUID
		err := q.QueryRow(ctx, `SELECT id FROM agent_roles WHERE id = $1 FOR UPDATE`, id).Scan(&locked)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrRoleNotFound
		}
		if err != nil {
			return fmt.Errorf("agent role: lock for delete: %w", err)
		}
		requiring, err := ticket.TicketsRequiringRole(ctx, q, id)
		if err != nil {
			return err
		}
		if len(requiring) > 0 {
			if !clearTickets {
				return &RoleInUseError{Tickets: requiring}
			}
			if err := ticket.ClearRequiredRole(ctx, q, id); err != nil {
				return err
			}
		}
		if _, err := q.Exec(ctx, `DELETE FROM agent_roles WHERE id = $1`, id); err != nil {
			return fmt.Errorf("agent role: delete: %w", err)
		}
		return nil
	})
}

// SetAgentRoles replaces the agent's memberships in one transaction.
//
// The agent's row is locked first, so two replacements of the same
// agent's roles run one after the other and the last one wins whole,
// rather than interleaving their deletes and inserts.
func (s *PostgresRoleStore) SetAgentRoles(ctx context.Context, agentID uuid.UUID, roleIDs []uuid.UUID) ([]Role, error) {
	var roles []Role
	err := s.db.InTx(ctx, func(q db.Querier) error {
		var locked uuid.UUID
		err := q.QueryRow(ctx, `SELECT id FROM agents WHERE id = $1 FOR UPDATE`, agentID).Scan(&locked)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("agent role: lock agent: %w", err)
		}

		// Read under RLS, so another tenant's role id is simply not found
		// here -- and the composite foreign key would refuse it anyway if
		// this check were ever skipped.
		//
		// FOR KEY SHARE takes now the lock the inserts' foreign key checks
		// would take later. Taken later, it deadlocks against a concurrent
		// DeleteRole: that holds the role and waits on a membership row
		// this transaction has just deleted, while this one waits on the
		// role to re-insert it.
		held, err := q.Query(ctx, `SELECT id FROM agent_roles WHERE id = ANY($1) FOR KEY SHARE`, roleIDs)
		if err != nil {
			return fmt.Errorf("agent role: check roles: %w", err)
		}
		found := 0
		for held.Next() {
			found++
		}
		held.Close()
		if err := held.Err(); err != nil {
			return fmt.Errorf("agent role: check roles: %w", err)
		}
		if found != len(roleIDs) {
			return ErrRoleNotFound
		}

		if _, err := q.Exec(ctx, `DELETE FROM agent_role_members WHERE agent_id = $1`, agentID); err != nil {
			return fmt.Errorf("agent role: clear members: %w", err)
		}
		_, err = q.Exec(ctx, `
			INSERT INTO agent_role_members (agent_id, role_id)
			SELECT $1, unnest($2::uuid[])`, agentID, roleIDs)
		if pgerr.IsForeignKeyViolation(err) {
			// Not reachable while the roles are locked above; kept so a
			// change to that locking fails as "no such role", not a 500.
			return ErrRoleNotFound
		}
		if err != nil {
			return fmt.Errorf("agent role: add members: %w", err)
		}

		rows, err := q.Query(ctx, rolesForQuery, agentID)
		if err != nil {
			return fmt.Errorf("agent role: read back: %w", err)
		}
		roles, err = collectRoles(rows)
		return err
	})
	if err != nil {
		return nil, err
	}
	return roles, nil
}

const rolesForQuery = `
	SELECT ` + roleColumns + `
	  FROM agent_roles r
	  JOIN agent_role_members m ON m.tenant_id = r.tenant_id AND m.role_id = r.id
	 WHERE m.agent_id = $1
	 ORDER BY r.name`

func (s *PostgresRoleStore) RolesFor(ctx context.Context, agentID uuid.UUID) ([]Role, error) {
	rows, err := s.db.Query(ctx, rolesForQuery, agentID)
	if err != nil {
		return nil, fmt.Errorf("agent role: roles for agent: %w", err)
	}
	return collectRoles(rows)
}

func (s *PostgresRoleStore) RolesByAgent(ctx context.Context) (map[uuid.UUID][]Role, error) {
	rows, err := s.db.Query(ctx, `
		SELECT m.agent_id, `+roleColumns+`
		  FROM agent_roles r
		  JOIN agent_role_members m ON m.tenant_id = r.tenant_id AND m.role_id = r.id
		 ORDER BY r.name`)
	if err != nil {
		return nil, fmt.Errorf("agent role: roles by agent: %w", err)
	}
	defer rows.Close()

	byAgent := map[uuid.UUID][]Role{}
	for rows.Next() {
		var agentID uuid.UUID
		var r Role
		if err := rows.Scan(&agentID, &r.ID, &r.Name, &r.Description, &r.CreatedAt); err != nil {
			return nil, fmt.Errorf("agent role: scan: %w", err)
		}
		byAgent[agentID] = append(byAgent[agentID], r)
	}
	return byAgent, rows.Err()
}
