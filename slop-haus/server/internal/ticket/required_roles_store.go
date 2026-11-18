package ticket

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/lowaa/slop-factory/go-db/pgerr"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

// meetsRoles is true, for a row of `tickets t`, when the agent whose id is
// the SQL expression agent may claim it as far as roles go: the ticket
// requires none, or the agent holds any (role_match any) or every
// (role_match all) one of them.
//
// Written so that a role_match it doesn't recognise meets nothing: the
// schema's CHECK allows only the two, and if that ever changed without
// this, the new mode would fail closed rather than open.
//
// agent is only ever a constant from this package -- a placeholder or a
// column -- never input.
func meetsRoles(agent string) string {
	return `(NOT EXISTS (SELECT 1 FROM ticket_required_roles rr WHERE rr.ticket_id = t.id)
	     OR (t.role_match = 'any' AND EXISTS (
	            SELECT 1 FROM ticket_required_roles rr
	              JOIN agent_role_members m ON m.tenant_id = rr.tenant_id AND m.role_id = rr.role_id
	             WHERE rr.ticket_id = t.id AND m.agent_id = ` + agent + `))
	     OR (t.role_match = 'all' AND NOT EXISTS (
	            SELECT 1 FROM ticket_required_roles rr
	             WHERE rr.ticket_id = t.id
	               AND NOT EXISTS (SELECT 1 FROM agent_role_members m
	                                WHERE m.tenant_id = rr.tenant_id AND m.role_id = rr.role_id
	                                  AND m.agent_id = ` + agent + `))))`
}

// noEligibleAgent is true, for a row of TicketFrom (its promoted attempt as
// pa), when it requires roles or names a variant version and no enabled agent
// meets both: it will sit in the queue until a person changes something, and
// the board says so. The same predicates the claim path uses, so the warning
// and the queue can't disagree. A ticket that asks for neither is left
// alone, whatever the roster, as it always was.
var noEligibleAgent = `((EXISTS (SELECT 1 FROM ticket_required_roles rr WHERE rr.ticket_id = t.id)
		OR pa.variant_version_id IS NOT NULL)
	AND NOT EXISTS (SELECT 1 FROM agents a WHERE a.disabled_at IS NULL AND ` + meetsRoles("a.id") + `
		AND ` + attemptVariantMatches("pa", "a.id") + `))`

func requiredRolesFor(ctx context.Context, q db.Querier, ticketID int64) ([]model.RoleRef, error) {
	rows, err := q.Query(ctx, `
		SELECT r.id, r.name FROM ticket_required_roles rr
		  JOIN agent_roles r ON r.tenant_id = rr.tenant_id AND r.id = rr.role_id
		 WHERE rr.ticket_id = $1 ORDER BY r.name`, ticketID)
	if err != nil {
		return nil, fmt.Errorf("list required roles: %w", err)
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (model.RoleRef, error) {
		var r model.RoleRef
		err := row.Scan(&r.ID, &r.Name)
		return r, err
	})
}

func attachRequiredRoles(ctx context.Context, q db.Querier, ids []int64, byID map[int64]*Ticket) error {
	rows, err := q.Query(ctx, `
		SELECT rr.ticket_id, r.id, r.name FROM ticket_required_roles rr
		  JOIN agent_roles r ON r.tenant_id = rr.tenant_id AND r.id = rr.role_id
		 WHERE rr.ticket_id = ANY($1) ORDER BY rr.ticket_id, r.name`, ids)
	if err != nil {
		return fmt.Errorf("list required roles: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var ticketID int64
		var r model.RoleRef
		if err := rows.Scan(&ticketID, &r.ID, &r.Name); err != nil {
			return fmt.Errorf("scan required role: %w", err)
		}
		if t, ok := byID[ticketID]; ok {
			t.RequiredRoles = append(t.RequiredRoles, r)
		}
	}
	return rows.Err()
}

// resolveRoles finds the ids of the roles req names, refusing the whole
// requirement if any is not one of the tenant's.
//
// Names match exactly, case included, as role names are unique: matching
// loosely would make "Deploy" ambiguous in a tenant that has both it and
// "deploy". The refusal lists the roles there are, so an agent that got one
// wrong can correct it in one go.
//
// FOR KEY SHARE takes now the lock the inserts' foreign key checks would
// take later. Taken later, it deadlocks against a concurrent role delete
// clearing this ticket: that holds the role and waits on a requirement row
// this transaction has just deleted, while this one waits on the role to
// re-insert it. Same ordering, and reason, as agent.SetAgentRoles.
func resolveRoles(ctx context.Context, q db.Querier, req *RoleRequirement) ([]uuid.UUID, error) {
	rows, err := q.Query(ctx, `
		SELECT id, name FROM agent_roles
		 WHERE id = ANY($1) OR name = ANY($2)
		 ORDER BY id
		 FOR KEY SHARE`, req.IDs, req.Names)
	if err != nil {
		return nil, fmt.Errorf("resolve roles: %w", err)
	}
	foundIDs := map[uuid.UUID]bool{}
	foundNames := map[string]bool{}
	for rows.Next() {
		var id uuid.UUID
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan role: %w", err)
		}
		foundIDs[id] = true
		foundNames[name] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("resolve roles: %w", err)
	}

	for _, id := range req.IDs {
		if !foundIDs[id] {
			return nil, fmt.Errorf("%w: %s", ErrUnknownRole, id)
		}
	}
	var missing []string
	for _, name := range req.Names {
		if !foundNames[name] {
			missing = append(missing, fmt.Sprintf("%q", name))
		}
	}
	if len(missing) > 0 {
		return nil, unknownRoleNames(ctx, q, missing)
	}

	// Every row is a role asked for, by id or by name; one asked for both
	// ways is still one role.
	ids := make([]uuid.UUID, 0, len(foundIDs))
	for id := range foundIDs {
		ids = append(ids, id)
	}
	return ids, nil
}

func unknownRoleNames(ctx context.Context, q db.Querier, missing []string) error {
	rows, err := q.Query(ctx, `SELECT name FROM agent_roles ORDER BY name`)
	if err != nil {
		return fmt.Errorf("list roles: %w", err)
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return fmt.Errorf("list roles: %w", err)
	}
	have := "this tenant has no roles"
	if len(names) > 0 {
		have = "roles are named exactly: " + strings.Join(names, ", ")
	}
	return fmt.Errorf("%w: %s (%s)", ErrUnknownRole, strings.Join(missing, ", "), have)
}

// writeRequiredRoles replaces the ticket's requirement with req. The
// caller has locked the ticket's row, or created it in this transaction.
func writeRequiredRoles(ctx context.Context, q db.Querier, ticketID int64, req *RoleRequirement) error {
	ids, err := resolveRoles(ctx, q, req)
	if err != nil {
		return err
	}
	if _, err := q.Exec(ctx, `DELETE FROM ticket_required_roles WHERE ticket_id = $1`, ticketID); err != nil {
		return fmt.Errorf("clear required roles: %w", err)
	}
	if _, err := q.Exec(ctx, `
		INSERT INTO ticket_required_roles (ticket_id, role_id)
		SELECT $1, unnest($2::uuid[])`, ticketID, ids); err != nil {
		if pgerr.IsForeignKeyViolation(err) {
			// Not reachable while resolveRoles holds the roles; kept so a
			// change to that locking reads as "no such role", not a 500.
			return ErrUnknownRole
		}
		return fmt.Errorf("add required roles: %w", err)
	}
	if _, err := q.Exec(ctx, `
		UPDATE tickets SET role_match = $2, updated_at = now() WHERE id = $1`,
		ticketID, string(req.Match)); err != nil {
		return fmt.Errorf("set role match: %w", err)
	}
	return nil
}

// setRequiredRoles replaces the ticket's required roles and match mode,
// inside a transition: a claim chooses its candidates by them under the
// claim lock, and a change landing between that choice and the grant would
// grant on a requirement no longer there.
func setRequiredRoles(ctx context.Context, q db.Querier, ticketID int64, req *RoleRequirement) error {
	// NO KEY UPDATE rather than UPDATE: writeRequiredRoles wants the row
	// locked, and this does it without also blocking every comment and
	// lease insert whose foreign key checks share-lock the ticket.
	var locked int64
	err := q.QueryRow(ctx, `SELECT id FROM tickets WHERE id = $1 FOR NO KEY UPDATE`, ticketID).Scan(&locked)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%w: #%d", ErrNotFound, ticketID)
	}
	if err != nil {
		return fmt.Errorf("lock ticket: %w", err)
	}
	return writeRequiredRoles(ctx, q, ticketID, req)
}

// missingRole reports why the agent may not claim ticketID as far as roles
// go, or nil if roles are not what stands in its way. Only for an open
// ticket: one that is preparing, blocked or finished isn't claimable by
// anyone, and blaming a role for that would send the agent after the
// wrong fix.
func missingRole(ctx context.Context, q db.Querier, agentID uuid.UUID, ticketID int64) (*MissingRoleError, error) {
	var match string
	err := q.QueryRow(ctx, `
		SELECT t.role_match FROM tickets t
		 WHERE t.id = $1 AND t.status = 'open' AND NOT `+meetsRoles("$2"), ticketID, agentID).Scan(&match)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("check required roles: %w", err)
	}
	roles, err := requiredRolesFor(ctx, q, ticketID)
	if err != nil {
		return nil, err
	}
	names := make([]string, len(roles))
	for i, r := range roles {
		names[i] = r.Name
	}
	return &MissingRoleError{TicketID: ticketID, Roles: names, Match: model.RoleMatch(match)}, nil
}

// TicketsRequiringRole lists the tickets that require roleID, for a role
// delete to refuse or clear. Through the delete's own transaction, which
// holds the role locked: no new requirement can reference it meanwhile.
func TicketsRequiringRole(ctx context.Context, q db.Querier, roleID uuid.UUID) ([]model.TicketRef, error) {
	rows, err := q.Query(ctx, `
		SELECT t.id, t.title, t.status FROM ticket_required_roles rr
		  JOIN tickets t ON t.tenant_id = rr.tenant_id AND t.id = rr.ticket_id
		 WHERE rr.role_id = $1 ORDER BY t.id`, roleID)
	if err != nil {
		return nil, fmt.Errorf("tickets requiring role: %w", err)
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (model.TicketRef, error) {
		var ref model.TicketRef
		var status string
		err := row.Scan(&ref.ID, &ref.Title, &status)
		ref.Status = Status(status)
		return ref, err
	})
}

// ClearRequiredRole removes roleID from every ticket that requires it. A
// ticket whose only requirement it was is then open to every agent; that
// loosening is why only a person's role delete, having been told which
// tickets, may do this.
func ClearRequiredRole(ctx context.Context, q db.Querier, roleID uuid.UUID) error {
	if _, err := q.Exec(ctx, `DELETE FROM ticket_required_roles WHERE role_id = $1`, roleID); err != nil {
		return fmt.Errorf("clear role from tickets: %w", err)
	}
	return nil
}
