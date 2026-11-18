package runner

import (
	"context"
	"errors"
	"fmt"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/landing"
	"time"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/jobs"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/tenantstate"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// inFlightBound is how long a merge asked of the host may still land: the
// change flow's own bound on a merge claim.
const inFlightBound = "5 minutes"

// Stale is a server lease nobody has renewed within its TTL: the process
// that ran the attempt is gone.
type Stale struct {
	Tenant   uuid.UUID
	Agent    uuid.UUID
	TicketID int64
	Attempt  int
	// Merged is a change the host merged and slop-haus recorded, whose
	// ticket was not closed before the process died. InFlight is a merge
	// asked of the host recently enough that it may still land.
	Merged, InFlight bool
	ChangeURL        string
	ApproverUserID   *uuid.UUID
	ApprovedAt       *time.Time
}

// Reaper finds stale server leases.
type Reaper interface {
	Stale(ctx context.Context) ([]Stale, error)
}

// PostgresReaper reads them from the database, across tenants.
type PostgresReaper struct{ db *db.DB }

func NewPostgresReaper(database *db.DB) *PostgresReaper { return &PostgresReaper{db: database} }

// Stale lists, for every active tenant, the server agent's lease that has run
// past its deadline. A lease holding the integration lock is pinned against the
// ordinary expiry sweep, so this is what frees it when the process that held
// it dies; a live attempt renews its lease and never shows up here. A ticket
// in the landing queue is the landing runner's: its lease is left for the
// runner to reclaim and reconcile from the code host, so that one owner
// recovers a landing and a wait on CI is never bounced. A tenant
// that cannot be read does not hide the others' leases: they are returned
// along with the error.
func (p *PostgresReaper) Stale(ctx context.Context) ([]Stale, error) {
	var out []Stale
	err := tenantstate.Each(ctx, tenantstate.Active(p.db.Global()), func(tctx context.Context, tenant uuid.UUID) error {
		trows, err := p.db.Query(tctx, `
			SELECT l.agent_id, l.ticket_id, l.attempt_number,
			       EXISTS (SELECT 1 FROM ticket_changes c WHERE c.ticket_id = l.ticket_id AND c.attempt_number = l.attempt_number AND c.merged_at IS NOT NULL),
			       EXISTS (SELECT 1 FROM ticket_changes c WHERE c.ticket_id = l.ticket_id AND c.attempt_number = l.attempt_number
			                AND c.merged_at IS NULL AND c.merging_since > now() - interval '`+inFlightBound+`'),
			       COALESCE(at.pr_url, ''), at.approved_by_user_id, at.approved_at
			  FROM leases l JOIN agents a ON a.id = l.agent_id
			  JOIN attempts at ON at.ticket_id = l.ticket_id AND at.number = l.attempt_number
			 WHERE a.name = $1 AND a.kind = 'console'
			   AND l.released_at IS NULL AND l.expires_at <= now()
			   AND NOT EXISTS (SELECT 1 FROM landings w WHERE w.ticket_id = l.ticket_id AND w.queue_state <> 'back_with_agent')`, ticket.ServerAgentName)
		if err != nil {
			return fmt.Errorf("servermerge: stale leases: %w", err)
		}
		defer trows.Close()
		for trows.Next() {
			s := Stale{Tenant: tenant}
			if err := trows.Scan(&s.Agent, &s.TicketID, &s.Attempt, &s.Merged, &s.InFlight, &s.ChangeURL, &s.ApproverUserID, &s.ApprovedAt); err != nil {
				return fmt.Errorf("servermerge: scan stale lease: %w", err)
			}
			out = append(out, s)
		}
		return trows.Err()
	})
	return out, err
}

// Reap hands back every ticket whose server attempt died, so it returns to
// the queue for its author and the integration lock goes on. Any replica may
// do it: the hand-back is under the claim lock and a second one finds no
// lease. What an attempt that never finished leaves recorded is a
// merge_refused outcome written here, naming the lease expiry.
func (r *Runner) Reap(ctx context.Context) error {
	if r.o.Reaper == nil {
		return nil
	}
	stale, err := r.o.Reaper.Stale(ctx)
	var errs []error
	if err != nil {
		errs = append(errs, err)
	}
	for _, s := range stale {
		tctx := db.WithTenant(ctx, s.Tenant)
		switch {
		case s.InFlight:
			// The host may yet accept it; looked at again next time.
			continue
		case s.Merged:
			err = r.closeMerged(tctx, &s)
		default:
			err = r.handBack(tctx, &s)
		}
		if err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Job is the reap as a background job. A failed pass is logged by the
// scheduler and the next one still runs.
func (r *Runner) Job() *jobs.Job {
	return &jobs.Job{Name: "servermerge_reaper", Interval: r.cfg.ReapInterval, Run: r.Reap}
}

func (r *Runner) handBack(ctx context.Context, s *Stale) error {
	err := r.o.Tickets.ServerHandBack(ctx, s.Agent, s.TicketID,
		"The server started to merge your approved change and stopped before it finished (the server restarted, or lost the attempt). "+
			"Nothing was recorded as merged, but check the change on the code host before merging it yourself.", r.cfg.Affinity)
	if errors.Is(err, ticket.ErrNoLease) {
		return nil
	}
	if err != nil {
		return err
	}
	r.write(ctx, &landing.Record{TicketID: s.TicketID, AttemptNumber: s.Attempt, ChangeURL: s.ChangeURL, Outcome: landing.MergeRefused, Detail: "attempt did not finish: its lease expired"})
	return nil
}

// closeMerged finishes a ticket whose change was merged but whose attempt
// died before closing it: handing it back would send the author to redo a
// merge that happened.
func (r *Runner) closeMerged(ctx context.Context, s *Stale) error {
	if _, err := r.o.Tickets.Done(ctx, s.Agent, &ticket.DoneParams{PRURL: s.ChangeURL}); err != nil {
		if errors.Is(err, ticket.ErrNoLease) {
			return nil
		}
		return err
	}
	r.write(ctx, &landing.Record{TicketID: s.TicketID, AttemptNumber: s.Attempt, ChangeURL: s.ChangeURL,
		ApproverUserID: s.ApproverUserID, ApprovedAt: s.ApprovedAt, Outcome: landing.Merged})
	return nil
}
