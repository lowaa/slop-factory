package manifest

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/scope"
)

var _ Mover = (*PostgresStore)(nil)

// terminal tickets keep the paths they finished with.

func (s *PostgresStore) MoveRepo(ctx context.Context, by uuid.UUID, req MoveRequest, dryRun bool) (*MovePlan, error) {
	var plan *MovePlan
	err := s.db.InTx(ctx, func(q db.Querier) error {
		rev, err := lockRevision(ctx, q)
		if err != nil {
			return err
		}
		current, err := currentRepos(ctx, q)
		if err != nil {
			return err
		}
		next, warnings, err := planRepos(current, req)
		if err != nil {
			return err
		}
		plan = &MovePlan{From: req.From, To: req.To, Repos: next, Warnings: warnings,
			Tickets: []MovedTicket{}, IntakeSources: []MovedIntakeSource{}}

		rows, err := ticketPathRows(ctx, q, current, req)
		if err != nil {
			return err
		}
		if err := checkNothingInFlight(ctx, q, plan, rows); err != nil {
			return err
		}
		sources, err := intakeRewrites(ctx, q, current, req)
		if err != nil {
			return err
		}
		plan.IntakeSources = sources.plan
		plan.Tickets = groupTickets(rows)
		if dryRun {
			return nil
		}

		if err := rewriteTicketPaths(ctx, q, rows); err != nil {
			return err
		}
		if err := sources.apply(ctx, q); err != nil {
			return err
		}
		if err := commentOnMoved(ctx, q, by, plan); err != nil {
			return err
		}
		_, err = put(ctx, q, next, by, &rev)
		return err
	})
	if err != nil {
		return nil, err
	}
	return plan, nil
}

func currentRepos(ctx context.Context, q db.Querier) ([]Repo, error) {
	var body []byte
	switch err := q.QueryRow(ctx, `SELECT repos FROM workspace_manifests`).Scan(&body); {
	case errors.Is(err, pgx.ErrNoRows):
		return nil, invalid("the tenant has declared no manifest")
	case err != nil:
		return nil, fmt.Errorf("manifest: read for move: %w", err)
	}
	repos, err := decodeRepos(body)
	if err != nil {
		return nil, fmt.Errorf("manifest: decode for move: %w", err)
	}
	return repos, nil
}

type pathRow struct {
	rowID    int64
	ticketID int64
	title    string
	status   string
	from, to string
}

// ticketPathRows is every scope of a non-terminal ticket that belongs to the
// repo, with where it goes. ticket_paths holds every attempt's scope: the
// attempts of a ticket share it.
//
// The tickets are locked before anything is read. A claim inserts a lease,
// which needs a share lock on its ticket's row, so it either lands before
// this move and is seen by the blocker check, or waits and claims the new
// scopes; and a status change racing the move is decided before the status
// is read, so a ticket that turned terminal is left as it finished.
func ticketPathRows(ctx context.Context, q db.Querier, repos []Repo, req MoveRequest) ([]pathRow, error) {
	if _, err := q.Exec(ctx, `
		SELECT 1 FROM tickets t
		 WHERE t.status NOT IN `+ticket.TerminalStatusesSQL+`
		   AND EXISTS (SELECT 1 FROM ticket_paths p WHERE p.ticket_id = t.id)
		 ORDER BY t.id FOR UPDATE`); err != nil {
		return nil, fmt.Errorf("manifest: lock tickets: %w", err)
	}
	rs, err := q.Query(ctx, `
		SELECT p.id, p.ticket_id, t.title, t.status, p.path
		  FROM ticket_paths p JOIN tickets t ON t.id = p.ticket_id
		 WHERE t.status NOT IN `+ticket.TerminalStatusesSQL+`
		 ORDER BY p.ticket_id, p.path`)
	if err != nil {
		return nil, fmt.Errorf("manifest: read ticket paths: %w", err)
	}
	defer rs.Close()
	var moving []pathRow
	var staying []pathRow
	for rs.Next() {
		var r pathRow
		if err := rs.Scan(&r.rowID, &r.ticketID, &r.title, &r.status, &r.from); err != nil {
			return nil, fmt.Errorf("manifest: scan ticket path: %w", err)
		}
		if !ownedBy(repos, r.from, req.From) {
			staying = append(staying, r)
			continue
		}
		if r.to, err = movePath(r.from, req); err != nil {
			return nil, fmt.Errorf("ticket #%d: %w", r.ticketID, err)
		}
		moving = append(moving, r)
	}
	if err := rs.Err(); err != nil {
		return nil, err
	}
	return moving, noNewOverlap(moving, staying)
}

// noNewOverlap refuses a move that would make a scope overlap another
// repo's ticket's. Scopes keep their relative order within the repo, so only
// a name another repo's tickets already use at the destination can do it.
// It is refused whatever the tenant's scope locking: a move colliding with
// another repo's paths is a namespace collision, not a claim rule.
func noNewOverlap(moving, staying []pathRow) error {
	for _, m := range moving {
		for _, o := range staying {
			if o.ticketID != m.ticketID && scope.Conflicts(m.to, o.from) {
				return invalid("scope %q of ticket #%d would overlap %q of ticket #%d at the new path",
					m.to, m.ticketID, o.from, o.ticketID)
			}
		}
	}
	return nil
}

func groupTickets(rows []pathRow) []MovedTicket {
	out := []MovedTicket{}
	for _, r := range rows {
		if n := len(out); n == 0 || out[n-1].ID != r.ticketID {
			out = append(out, MovedTicket{ID: r.ticketID, Title: r.title, Status: r.status})
		}
		last := &out[len(out)-1]
		last.Paths = append(last.Paths, MovedPath{From: r.from, To: r.to})
	}
	return out
}

// checkNothingInFlight refuses while a ticket under the repo is held (a
// lease the ticket package counts as live, which includes any lease behind
// the integration lock) or holds the integration lock. The tickets are
// already row-locked by ticketPathRows.
func checkNothingInFlight(ctx context.Context, q db.Querier, plan *MovePlan, rows []pathRow) error {
	ids := []int64{}
	titles := map[int64]string{}
	for _, r := range rows {
		if _, seen := titles[r.ticketID]; !seen {
			ids = append(ids, r.ticketID)
			titles[r.ticketID] = r.title
		}
	}
	if len(ids) == 0 {
		return nil
	}
	rs, err := q.Query(ctx, `
		SELECT l.ticket_id,
		       EXISTS (SELECT 1 FROM locks k WHERE k.holder_lease_id = l.id)
		  FROM leases l
		 WHERE l.ticket_id = ANY($1) AND `+ticket.ActiveLeaseClause+`
		 ORDER BY l.ticket_id, l.id`, ids)
	if err != nil {
		return fmt.Errorf("manifest: read leases: %w", err)
	}
	defer rs.Close()
	var blocked []MoveBlocker
	seen := map[MoveBlocker]bool{}
	for rs.Next() {
		var id int64
		var holdsLock bool
		if err := rs.Scan(&id, &holdsLock); err != nil {
			return fmt.Errorf("manifest: scan lease: %w", err)
		}
		reason := BlockerLease
		if holdsLock {
			reason = BlockerLock
		}
		b := MoveBlocker{TicketID: id, Title: titles[id], Reason: reason}
		if !seen[b] {
			seen[b] = true
			blocked = append(blocked, b)
		}
	}
	if err := rs.Err(); err != nil {
		return err
	}
	if len(blocked) > 0 {
		return &MoveBlockedError{Blockers: blocked}
	}
	return nil
}

// rewriteTicketPaths goes through a placeholder because the new spelling of
// one scope can be the old spelling of another of the same ticket (a
// repo moved from "." into "app" while a ticket also holds "app/x"), and
// the unique index is checked row by row.
func rewriteTicketPaths(ctx context.Context, q db.Querier, rows []pathRow) error {
	if len(rows) == 0 {
		return nil
	}
	ids, tos := make([]int64, len(rows)), make([]string, len(rows))
	for i, r := range rows {
		ids[i], tos[i] = r.rowID, r.to
	}
	if _, err := q.Exec(ctx, `UPDATE ticket_paths SET path = 'moving:' || id WHERE id = ANY($1)`, ids); err != nil {
		return fmt.Errorf("manifest: park ticket paths: %w", err)
	}
	if _, err := q.Exec(ctx, `
		UPDATE ticket_paths t SET path = v.path
		  FROM unnest($1::bigint[], $2::text[]) v(id, path) WHERE t.id = v.id`, ids, tos); err != nil {
		return fmt.Errorf("manifest: rewrite ticket paths: %w", err)
	}
	return nil
}

type intakeRewrite struct {
	plan  []MovedIntakeSource
	paths map[uuid.UUID][]string
}

// intakeRewrites covers revoked sources too: a revoked source's paths are
// history, but nothing reads them wrongly if they follow the repo.
func intakeRewrites(ctx context.Context, q db.Querier, repos []Repo, req MoveRequest) (*intakeRewrite, error) {
	rs, err := q.Query(ctx, `SELECT id, name, paths FROM intake_sources ORDER BY created_at, id`)
	if err != nil {
		return nil, fmt.Errorf("manifest: read intake sources: %w", err)
	}
	defer rs.Close()
	out := &intakeRewrite{plan: []MovedIntakeSource{}, paths: map[uuid.UUID][]string{}}
	for rs.Next() {
		var id uuid.UUID
		var name string
		var paths []string
		if err := rs.Scan(&id, &name, &paths); err != nil {
			return nil, fmt.Errorf("manifest: scan intake source: %w", err)
		}
		moved := MovedIntakeSource{ID: id, Name: name}
		next := make([]string, len(paths))
		for i, p := range paths {
			next[i] = p
			if !ownedBy(repos, p, req.From) {
				continue
			}
			to, err := movePath(p, req)
			if err != nil {
				return nil, fmt.Errorf("intake source %q: %w", name, err)
			}
			next[i] = to
			moved.Paths = append(moved.Paths, MovedPath{From: p, To: to})
		}
		if len(moved.Paths) > 0 {
			out.plan = append(out.plan, moved)
			out.paths[id] = next
		}
	}
	return out, rs.Err()
}

func (r *intakeRewrite) apply(ctx context.Context, q db.Querier) error {
	for id, paths := range r.paths {
		if _, err := q.Exec(ctx, `UPDATE intake_sources SET paths = $2 WHERE id = $1`, id, paths); err != nil {
			return fmt.Errorf("manifest: rewrite intake source paths: %w", err)
		}
	}
	return nil
}

// commentOnMoved records the rewrite on each ticket, as the owner who made
// it: the ticket's own history is where someone wondering why its scope
// changed looks.
func commentOnMoved(ctx context.Context, q db.Querier, by uuid.UUID, plan *MovePlan) error {
	for _, t := range plan.Tickets {
		var b strings.Builder
		fmt.Fprintf(&b, "The workspace repo moved from `%s` to `%s`; this ticket's scope was rewritten with it:\n", plan.From, plan.To)
		for _, p := range t.Paths {
			fmt.Fprintf(&b, "- `%s` -> `%s`\n", p.From, p.To)
		}
		if _, err := ticket.InsertTicketComment(ctx, q, t.ID, ticket.UserAuthor(by), b.String()); err != nil {
			return fmt.Errorf("manifest: record move on ticket #%d: %w", t.ID, err)
		}
	}
	return nil
}
