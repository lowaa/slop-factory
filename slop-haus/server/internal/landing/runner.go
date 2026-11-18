package landing

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/go-clock"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/change"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/landing/guidance"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// Options are what a Runner is built from.
type Options struct {
	Tickets   Tickets
	Changes   Changes
	Host      Host
	Pipelines Pipelines
	Queue     Queue
	Outcomes  Outcomes
	Mode      Mode
	// Tenants lists the tenants a sweep looks at.
	Tenants func(ctx context.Context) ([]uuid.UUID, error)
	Config  Config
	// Guidance names what a conflict or an interaction collided with, and
	// bounds how often a ticket is handed back for one. Nil keeps the generic
	// handbacks.
	Guidance *guidance.Advisor
	Logger   slog.Handler
	// Observer is told of each outcome written; nil observes nothing.
	Observer Observer
	// Clock times the polls, deadlines and heartbeat; nil is the wall clock.
	Clock clock.Clock
}

// Runner lands the head of each tenant's queue, one landing at a time per
// tenant: the lock is one per tenant, and landing in parallel would only queue
// on it.
type Runner struct {
	o       Options
	cfg     Config
	clock   clock.Clock
	log     *slog.Logger
	baseCtx context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup

	// batch, when set, lands rows in batches while the tenant's batch size is
	// above 1. Set before the first Kick.
	batch *BatchRunner

	mu      sync.Mutex
	working map[uuid.UUID]bool
	again   map[uuid.UUID]bool
	// abandoned holds the tickets whose claim was cut off by Close, per tenant.
	// Close releases exactly those: the server agent is one per tenant, so
	// releasing by agent could end another replica's live landing.
	abandoned map[uuid.UUID]map[int64]bool
}

func New(o *Options) (*Runner, error) {
	entries, err := parseKnownFlaky(o.Config.KnownFlaky)
	if err != nil {
		return nil, fmt.Errorf("landing: %w", err)
	}
	if o.Tickets == nil || o.Changes == nil || o.Host == nil || o.Pipelines == nil || o.Queue == nil || o.Outcomes == nil ||
		o.Mode == nil || o.Tenants == nil || o.Logger == nil {
		return nil, errors.New("landing: Tickets, Changes, Host, Pipelines, Queue, Outcomes, Mode, Tenants and Logger are required")
	}
	ctx, cancel := context.WithCancel(context.Background())
	r := &Runner{o: *o, cfg: o.Config.withDefaults(), clock: clock.Or(o.Clock), log: slog.New(o.Logger),
		baseCtx: ctx, cancel: cancel, working: map[uuid.UUID]bool{}, again: map[uuid.UUID]bool{}, abandoned: map[uuid.UUID]map[int64]bool{}}
	r.warnKnownFlaky(entries)
	return r, nil
}

// warnKnownFlaky says at startup which entries lack an owner or are past their
// review, so the warning does not wait for a flake to be retried.
func (r *Runner) warnKnownFlaky(entries []flakyEntry) {
	now := r.clock.Now()
	for _, e := range entries {
		switch {
		case e.bare():
			r.log.Warn("landing: known-flaky entry has no owner ticket or review date; use name:ticket:yyyy-mm-dd", "workflow", e.Name)
		case e.expired(now):
			r.log.Warn("landing: known-flaky entry is past its review date", "workflow", e.Name, "owner", e.Owner, "review", e.Review.Format(flakyReviewLayout))
		}
	}
}

// Run sweeps every tenant's queue until ctx ends. The sweep is what picks up a
// landing whose runner died: its lease runs out and the row is still the head.
func (r *Runner) Run(ctx context.Context) {
	t := r.clock.NewTicker(r.cfg.SweepInterval)
	defer t.Stop()
	for {
		r.Sweep(ctx)
		select {
		case <-ctx.Done():
			return
		case <-r.baseCtx.Done():
			return
		case <-t.C():
		}
	}
}

// Sweep kicks every tenant.
func (r *Runner) Sweep(ctx context.Context) {
	tenants, err := r.o.Tenants(ctx)
	if err != nil {
		r.log.ErrorContext(ctx, "landing: list tenants", "err", err)
		return
	}
	for _, t := range tenants {
		r.Kick(t)
	}
}

// Kick tells the runner the tenant's queue may have work (a ticket finished).
// It returns at once: a landing waits on CI far longer than any caller may.
func (r *Runner) Kick(tenant uuid.UUID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.baseCtx.Err() != nil {
		return
	}
	if r.working[tenant] {
		r.again[tenant] = true
		return
	}
	r.working[tenant] = true
	r.wg.Go(func() { r.work(tenant) })
}

// Wait blocks until the work started so far has finished.
func (r *Runner) Wait() { r.wg.Wait() }

// Close abandons running landings and waits for them. An abandoned landing
// writes nothing more: the row, still the head, is reconciled from the code
// host by the next sweep. Its lease is released so that sweep, on this replica
// or another, can claim it at once instead of after the lease's TTL.
func (r *Runner) Close() {
	r.cancel()
	r.wg.Wait()
	r.releaseClaims()
}

func (r *Runner) releaseClaims() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	r.mu.Lock()
	abandoned := maps.Clone(r.abandoned)
	r.mu.Unlock()
	for t, tickets := range abandoned {
		tctx := db.WithTenant(ctx, t)
		server, err := r.o.Tickets.EnsureServerAgent(tctx)
		if err != nil {
			r.log.ErrorContext(tctx, "landing: no server agent to release claims for", "tenant", t, "err", err)
			continue
		}
		for id := range tickets {
			if err := r.o.Tickets.ServerReleaseLanding(tctx, server, id); err != nil {
				r.log.ErrorContext(tctx, "landing: release the server's claim; it runs to its TTL", "tenant", t, "ticket", id, "err", err)
			}
		}
	}
}

func (r *Runner) work(tenant uuid.UUID) {
	ctx := db.WithTenant(r.baseCtx, tenant)
	if repaired, err := r.o.Tickets.RepairMergeQueue(ctx, r.cfg.RepairGrace); err != nil {
		r.log.ErrorContext(ctx, "landing: repair the merge queue", "err", err)
	} else if len(repaired) > 0 {
		r.log.WarnContext(ctx, "landing: repaired tickets whose status and queue row disagreed", "tickets", repaired)
	}
	if r.batch != nil {
		r.batch.Reconcile(ctx, tenant)
	}
	var skip []int64
	for ctx.Err() == nil {
		if len(skip) > 0 {
			r.log.InfoContext(ctx, "landing: head selection skips tickets", "tenant", tenant, "skipped", skip)
		}
		row, err := r.o.Queue.Head(ctx, skip)
		if err != nil {
			r.log.ErrorContext(ctx, "landing: read the queue", "err", err)
			break
		}
		if row == nil {
			r.mu.Lock()
			if !r.again[tenant] {
				delete(r.working, tenant)
				r.mu.Unlock()
				return
			}
			delete(r.again, tenant)
			skip = nil
			r.mu.Unlock()
			continue
		}
		if r.batch != nil && row.Step == "" {
			switch r.batch.Run(ctx, tenant, skip) {
			case batchRan:
				skip = nil
				continue
			case batchStuck:
				skip = append(skip, row.TicketID)
				continue
			}
		}
		if r.land(ctx, tenant, row) {
			// A landing that ends frees the claim and changes the queue: what
			// was not claimable a moment ago is tried again, not parked for the
			// rest of a sweep that a busy queue can stretch to hours.
			skip = nil
			continue
		}
		// Not landed and not handed back (not claimable now): look at the rest
		// first, and come back to this one after the next landing.
		skip = append(skip, row.TicketID)
	}
	r.mu.Lock()
	delete(r.working, tenant)
	delete(r.again, tenant)
	r.mu.Unlock()
}

// enabled reads the mode now. An unreadable mode counts as off for the step in
// hand: nothing is merged on a setting nobody could read.
func (r *Runner) enabled(ctx context.Context) (bool, error) { return r.o.Mode.Enabled(ctx) }

// land lands one row. It returns false when the row was neither landed nor
// handed back, so the caller moves on.
func (r *Runner) land(ctx context.Context, tenant uuid.UUID, row *LandingRow) bool {
	log := r.log.With("ticket", row.TicketID, "attempt", row.AttemptNumber)
	server, err := r.o.Tickets.EnsureServerAgent(ctx)
	if err != nil {
		log.ErrorContext(ctx, "landing: no server agent", "err", err)
		return false
	}
	// Claimed first: reading a change's checks is an operation of the agent
	// holding the ticket, and the claim is what keeps two replicas from landing
	// the same ticket.
	claim, err := r.o.Tickets.ServerClaimLanding(ctx, server, row.TicketID, row.AttemptNumber, r.cfg.LeaseTTL)
	switch {
	case isNotClaimable(err):
		log.InfoContext(ctx, "landing: not claimable", "reason", err)
		return false
	case err != nil:
		log.ErrorContext(ctx, "landing: claim failed", "err", err)
		return false
	case claim.Ticket == nil:
		log.InfoContext(ctx, "landing: another ticket holds paths this one needs")
		return false
	}
	held, stopBeat := r.heartbeat(ctx, server, row.TicketID)
	defer stopBeat()

	rec := &Record{TicketID: row.TicketID, AttemptNumber: row.AttemptNumber, ChangeURL: row.ChangeURL, HeadSHA: row.HeadSHA, QueueDepth: &row.QueueDepth, EnqueuedAt: &row.EnqueuedAt}
	fail := r.run(held, tenant, server, row, rec)
	switch {
	case fail == nil:
		rec.Outcome = Merged
		r.finish(ctx, row, rec)
		return true
	case fail.mergedNotClosed:
		// On the base, so handing the ticket back as a failure would be wrong.
		// The row stays: the next sweep finds the change merged and closes it.
		log.ErrorContext(ctx, "landing: merged but the bookkeeping failed; the next sweep finishes it", "detail", fail.detail)
		return false
	case fail.outcome == AlreadyMerged:
		rec.Outcome, rec.Detail = AlreadyMerged, fail.detail
		r.finish(ctx, row, rec)
		return true
	case ctx.Err() != nil:
		// Abandoned: the lease and the row are the recovery.
		return true
	case held.Err() != nil:
		log.WarnContext(ctx, "landing: lost the lease", "cause", context.Cause(held))
		return true
	}
	return r.handBack(ctx, server, row, fail, rec)
}

// handBack returns the ticket the server holds to its author with the failure's
// guidance, and records the outcome. It is false when the hand back did not
// happen and the next sweep must retry it.
func (r *Runner) handBack(ctx context.Context, server uuid.UUID, row *LandingRow, fail *failure, rec *Record) bool {
	log := r.log.With("ticket", row.TicketID, "attempt", row.AttemptNumber)
	rec.Outcome, rec.Detail = fail.outcome, fail.detail
	hb, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if r.boundReached(hb, server, row, fail, rec) {
		return true
	}
	if err := r.o.Tickets.ServerHandBack(hb, server, row.TicketID, fail.guidance, r.cfg.Affinity); err != nil {
		log.ErrorContext(ctx, "landing: hand back failed; the next sweep retries", "err", err)
		return false
	}
	if err := r.o.Queue.HandBack(hb, row.TicketID, fail.outcome, fail.keepsPlace); err != nil {
		log.ErrorContext(ctx, "landing: mark the row handed back", "err", err)
	}
	r.write(hb, rec)
	return true
}

// boundReached stops re-queueing a ticket that has collided with other landed
// work once too often: it goes to a human instead of round the loop again. It
// reports whether it did.
func (r *Runner) boundReached(ctx context.Context, server uuid.UUID, row *LandingRow, fail *failure, rec *Record) bool {
	if r.o.Guidance == nil || fail.collision == nil || !fail.collision.Counts {
		return false
	}
	prior, atBound := r.o.Guidance.AtBound(ctx, row.TicketID)
	if !atBound {
		return false
	}
	question, handoff := r.o.Guidance.Discussion(prior, fail.collision, fail.guidance)
	if _, err := r.o.Tickets.Discuss(ctx, server, row.TicketID, question, handoff); err != nil {
		// Handed back as usual: a ticket stuck in the queue is worse than one more lap.
		r.log.ErrorContext(ctx, "landing: could not move the ticket to discussion", "ticket", row.TicketID, "err", err)
		return false
	}
	rec.Detail = guidance.BoundPrefix + strings.TrimPrefix(fail.detail, guidance.DetailPrefix)
	if err := r.o.Queue.Remove(ctx, row.TicketID, PhaseDequeued); err != nil {
		r.log.ErrorContext(ctx, "landing: remove the row", "ticket", row.TicketID, "err", err)
	}
	r.write(ctx, rec)
	return true
}

// finish writes a landed ticket down even when the runner is closing.
func (r *Runner) finish(ctx context.Context, row *LandingRow, rec *Record) {
	ctx = context.WithoutCancel(ctx)
	if err := r.o.Queue.Remove(ctx, row.TicketID, PhaseMerged); err != nil {
		r.log.ErrorContext(ctx, "landing: remove the row", "ticket", row.TicketID, "err", err)
	}
	r.write(ctx, rec)
}

// run does the landing; nil is a merge.
func (r *Runner) run(ctx context.Context, tenant, server uuid.UUID, row *LandingRow, rec *Record) *failure {
	return r.runBehind(ctx, tenant, server, row, rec, nil)
}

// runBehind is run for a member of a split batch, whose predecessors in the
// batch (ahead) have landed.
func (r *Runner) runBehind(ctx context.Context, tenant, server uuid.UUID, row *LandingRow, rec *Record, ahead []int64) *failure {
	if fail := r.stillOn(ctx); fail != nil {
		return fail
	}
	if fail := r.awaitLock(ctx, server); fail != nil {
		return fail
	}
	gate, err := r.o.Tickets.ChangeGateFor(ctx, server)
	if err != nil {
		return r.classify(ctx, err)
	}
	rec.ApproverUserID, rec.ApprovedAt = gate.Review.ApprovedByUserID, gate.Review.ApprovedAt

	// A restart reconciles from the host: a change already merged is closed,
	// not merged again. A head that moved since the enqueue needs nothing here:
	// the gate below judges the head it finds.
	state, fail := r.reconcile(ctx, tenant, row)
	if fail != nil {
		return fail
	}
	if state.Merged {
		return r.closeMerged(ctx, server, row, state)
	}
	ls := &landing{r: r, tenant: tenant, server: server, row: row, rec: rec, ahead: ahead}
	return ls.drive(ctx)
}

func (r *Runner) reconcile(ctx context.Context, tenant uuid.UUID, row *LandingRow) (*ChangeState, *failure) {
	var state *ChangeState
	err := r.hostCall(ctx, func() (err error) {
		state, err = r.o.Host.State(ctx, tenant, row.TicketID, row.AttemptNumber)
		return err
	})
	if err != nil {
		return nil, r.classify(ctx, err)
	}
	return state, nil
}

// closeMerged finishes the bookkeeping of a change found merged: the store's
// merge, the ticket's close and the lock all follow from Done.
func (r *Runner) closeMerged(ctx context.Context, server uuid.UUID, row *LandingRow, state *ChangeState) *failure {
	ctx = context.WithoutCancel(ctx)
	if err := r.o.Host.MarkMerged(ctx, row.TicketID, row.AttemptNumber, server); err != nil {
		r.log.ErrorContext(ctx, "landing: record the merge found on the host", "ticket", row.TicketID, "err", err)
		return mergedNotClosed("could not record a merge found on the host")
	}
	if _, err := r.o.Tickets.Done(ctx, server, &ticket.DoneParams{PRURL: state.URL}); err != nil {
		r.log.ErrorContext(ctx, "landing: merged but could not close the ticket", "ticket", row.TicketID, "err", err)
		return mergedNotClosed("the ticket could not be closed")
	}
	return &failure{outcome: AlreadyMerged, detail: "found merged on the host"}
}

// stillOn is the check made before each step: the landing in hand finishes the
// step it is in, then yields to a switch back to the worker flow.
func (r *Runner) stillOn(ctx context.Context) *failure {
	on, err := r.enabled(ctx)
	if err != nil {
		r.log.ErrorContext(ctx, "landing: read the merge method", "err", err)
		return hostError("could not read the tenant's merge method")
	}
	if !on {
		return switchedOff()
	}
	return nil
}

func (r *Runner) awaitLock(ctx context.Context, server uuid.UUID) *failure {
	deadline := r.clock.Now().Add(r.cfg.LockTimeout)
	for {
		err := r.o.Tickets.AcquireIntegrationLock(ctx, server)
		if err == nil {
			return nil
		}
		if !errors.Is(err, ticket.ErrLockHeld) {
			return r.classify(ctx, err)
		}
		if !r.clock.Now().Before(deadline) {
			return lockTimeout(r.cfg.LockTimeout.String())
		}
		if !r.sleep(ctx, r.cfg.PollInterval) {
			return r.cancelled()
		}
	}
}

func (r *Runner) cancelled() *failure {
	return &failure{outcome: MergeRefused, detail: "cancelled"}
}

// classify is known() for an error that may be a host error, which is reported
// without the error's text.
func (r *Runner) classify(ctx context.Context, err error) *failure {
	if ctx.Err() != nil {
		return r.cancelled()
	}
	if f := known(err); f != nil {
		return f
	}
	r.log.ErrorContext(ctx, "landing: code host or store error", "err", err)
	return hostError("code host or store error")
}

// hostCall tries fn again, with backoff, while it fails with what the runner
// does not know: a host that errors is not a change that is wrong.
func (r *Runner) hostCall(ctx context.Context, fn func() error) error {
	var err error
	for attempt := 1; ; attempt++ {
		// An update still pending may yet land: the caller reads the head again
		// rather than asking for it a second time.
		if err = fn(); err == nil || known(err) != nil || errors.Is(err, codehost.ErrAlreadyMerged) || errors.Is(err, change.ErrAlreadyMerged) ||
			errors.Is(err, codehost.ErrUpdatePending) {
			return err
		}
		if attempt >= r.cfg.HostRetries || !r.sleep(ctx, time.Duration(attempt)*r.cfg.HostBackoff) {
			return err
		}
	}
}

// heartbeat keeps the server's lease on ticketID fresh until stop, and cancels
// the returned context if the lease is gone. A stop after the runner was closed
// leaves the ticket marked for Close to release.
func (r *Runner) heartbeat(ctx context.Context, server uuid.UUID, ticketID int64) (context.Context, func()) {
	parent := ctx
	ctx, cancel := context.WithCancelCause(ctx)
	stopped := make(chan struct{})
	var once sync.Once
	go func() {
		defer close(stopped)
		t := r.clock.NewTicker(r.cfg.HeartbeatInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C():
				if err := r.o.Tickets.Heartbeat(ctx, server, r.cfg.LeaseTTL); errors.Is(err, ticket.ErrNoLease) {
					cancel(ticket.ErrNoLease)
					return
				}
			}
		}
	}()
	return ctx, func() {
		once.Do(func() {
			cancel(nil)
			<-stopped
			if parent.Err() != nil {
				if t, err := db.TenantFrom(parent); err == nil {
					r.mu.Lock()
					if r.abandoned[t] == nil {
						r.abandoned[t] = map[int64]bool{}
					}
					r.abandoned[t][ticketID] = true
					r.mu.Unlock()
				}
			}
		})
	}
}

func (r *Runner) write(ctx context.Context, rec *Record) {
	if err := r.o.Outcomes.Record(ctx, rec); err != nil {
		r.log.ErrorContext(ctx, "landing: could not record the outcome", "ticket", rec.TicketID, "outcome", rec.Outcome, "err", err)
		return
	}
	r.observe(rec)
}

func (r *Runner) sleep(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-r.clock.After(d):
		return true
	}
}

// observe tells the Observer of an outcome that is in the table, so the series
// agree with the report queries.
func (r *Runner) observe(rec *Record) {
	if r.o.Observer == nil {
		return
	}
	depth, waited := -1, time.Duration(0)
	if rec.QueueDepth != nil {
		depth = *rec.QueueDepth
	}
	if rec.EnqueuedAt != nil {
		waited = r.clock.Now().Sub(*rec.EnqueuedAt)
	}
	size := max(rec.BatchSize, 1)
	r.o.Observer.LandingOutcome(string(rec.Outcome), depth, waited, size)
}
