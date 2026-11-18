// Package runner merges an approved ticket's change on the server's own
// authority, as the tenant's server agent, through the same gate an agent's
// change_merge passes.
package runner

import (
	"context"
	"errors"
	"fmt"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/landing"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/go-clock"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/change"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// Tickets is what the runner needs of the ticket side.
type Tickets interface {
	EnsureServerAgent(ctx context.Context) (uuid.UUID, error)
	ServerClaim(ctx context.Context, server uuid.UUID, ticketID int64, attempt int, ttl time.Duration) (*ticket.ClaimResult, error)
	ServerHandBack(ctx context.Context, server uuid.UUID, ticketID int64, guidance string, affinity time.Duration) error
	Heartbeat(ctx context.Context, agent uuid.UUID, ttl time.Duration) error
	AcquireIntegrationLock(ctx context.Context, agent uuid.UUID) error
	ChangeGateFor(ctx context.Context, agent uuid.UUID) (*ticket.ChangeGate, error)
	Done(ctx context.Context, agent uuid.UUID, p *ticket.DoneParams) (*ticket.Ticket, error)
}

// Changes is the change flow: the same service the agents' change tools
// call, so the gate is not copied here.
type Changes interface {
	Checks(ctx context.Context, tenant, agent uuid.UUID, expectedSHA string) (*change.Report, error)
	Merge(ctx context.Context, tenant, agent uuid.UUID, in *change.MergeInput) (*change.Merged, error)
}

// ChangeTools says whether the context's tenant has a code host credential.
type ChangeTools interface {
	Connected(ctx context.Context) (bool, error)
}

// Routing says whether the landing queue, not this runner, takes an approval
// in the context's tenant.
type Routing interface {
	Enqueues(ctx context.Context) (bool, error)
}

// Outcomes records what each attempt came to.
type Outcomes interface {
	Record(ctx context.Context, r *landing.Record) error
}

// Config bounds an attempt. Zero fields take the defaults.
type Config struct {
	// ChecksTimeout bounds the wait for pending checks.
	ChecksTimeout time.Duration
	// LockTimeout bounds the wait in the integration lock's queue.
	LockTimeout time.Duration
	// PollInterval is how often checks and the lock are asked about.
	PollInterval time.Duration
	// LeaseTTL is the server agent's lease. The heartbeat keeps it fresh, so
	// it is also how long an attempt whose process died holds the ticket.
	LeaseTTL time.Duration
	// HeartbeatInterval must be well under LeaseTTL.
	HeartbeatInterval time.Duration
	// ReapInterval is how often leases of dead attempts are looked for.
	ReapInterval time.Duration
	// Affinity is the window the author gets first claim of a handed-back
	// ticket for.
	Affinity time.Duration
}

func (c Config) withDefaults() Config {
	def := func(d *time.Duration, v time.Duration) {
		if *d <= 0 {
			*d = v
		}
	}
	def(&c.ChecksTimeout, 30*time.Minute)
	def(&c.LockTimeout, 30*time.Minute)
	def(&c.PollInterval, 10*time.Second)
	def(&c.LeaseTTL, 5*time.Minute)
	def(&c.HeartbeatInterval, time.Minute)
	def(&c.ReapInterval, time.Minute)
	def(&c.Affinity, ticket.DefaultResumeAffinity)
	return c
}

// Options are what a Runner is built from.
type Options struct {
	Tickets  Tickets
	Changes  Changes
	Tools    ChangeTools
	Outcomes Outcomes
	// Routing, when set, makes the runner leave the approvals of a tenant the
	// queue enqueues them for.
	Routing Routing
	// Reaper finds the leases of attempts that died; see Runner.Run.
	Reaper Reaper
	Config Config
	Logger slog.Handler
	// Clock times the polls, deadlines and heartbeat; nil is the wall clock.
	Clock clock.Clock
}

// Runner runs one server-merge attempt per approval it is told of.
type Runner struct {
	o       Options
	cfg     Config
	clock   clock.Clock
	log     *slog.Logger
	baseCtx context.Context
	cancel  context.CancelFunc
	// mu makes Approved's check of baseCtx and its wg.Go one step against
	// Close's cancel: otherwise an attempt can start after Close has waited,
	// and wg.Go can run while Wait does.
	mu sync.Mutex
	wg sync.WaitGroup
}

func New(o *Options) (*Runner, error) {
	if o.Tickets == nil || o.Changes == nil || o.Tools == nil || o.Outcomes == nil || o.Logger == nil {
		return nil, errors.New("servermerge: Tickets, Changes, Tools, Outcomes and Logger are required")
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Runner{o: *o, cfg: o.Config.withDefaults(), clock: clock.Or(o.Clock), log: slog.New(o.Logger), baseCtx: ctx, cancel: cancel}, nil
}

// Approved is the approval hook. The hook's own context is bounded to
// seconds and an attempt can wait on CI for much longer, so the attempt
// runs on the runner's own context and this returns at once.
func (r *Runner) Approved(_ context.Context, a *ticket.Approval) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.baseCtx.Err() != nil {
		return nil
	}
	r.wg.Go(func() { r.attempt(db.WithTenant(r.baseCtx, a.TenantID), a) })
	return nil
}

// Wait blocks until the attempts started so far have finished.
func (r *Runner) Wait() { r.wg.Wait() }

// Close abandons running attempts and waits for them. An abandoned attempt
// writes nothing more: its lease runs out and the reaper (of this or any
// other replica) hands the ticket back.
func (r *Runner) Close() {
	r.mu.Lock()
	r.cancel()
	r.mu.Unlock()
	r.wg.Wait()
}

// failure is how an attempt ended short of a merge.
type failure struct {
	outcome  landing.Outcome
	guidance string
	// detail is stored with the outcome; it never holds a host's raw error,
	// which could carry anything the host echoes back.
	detail string
}

func (f *failure) Error() string { return string(f.outcome) + ": " + f.detail }

func (r *Runner) attempt(ctx context.Context, a *ticket.Approval) {
	log := r.log.With("ticket", a.TicketID, "attempt", a.Attempt)
	if r.o.Routing != nil {
		queued, err := r.o.Routing.Enqueues(ctx)
		if err != nil {
			log.ErrorContext(ctx, "server merge: could not read the approval routing", "err", err)
			return
		}
		if queued {
			return
		}
	}
	connected, err := r.o.Tools.Connected(ctx)
	if err != nil {
		log.ErrorContext(ctx, "server merge: could not read the code host credential", "err", err)
		return
	}
	if !connected {
		return
	}
	server, err := r.o.Tickets.EnsureServerAgent(ctx)
	if err != nil {
		log.ErrorContext(ctx, "server merge: no server agent", "err", err)
		return
	}
	// Claimed first, not after the checks: reading a change's checks is an
	// operation of the agent holding the ticket. The claim is also what
	// keeps two runners from both merging it: it refuses a held ticket.
	claim, err := r.o.Tickets.ServerClaim(ctx, server, a.TicketID, a.Attempt, r.cfg.LeaseTTL)
	switch {
	case isNotClaimable(err):
		log.InfoContext(ctx, "server merge: not attempted", "reason", err)
		return
	case err != nil:
		log.ErrorContext(ctx, "server merge: claim failed", "err", err)
		return
	case claim.Ticket == nil:
		r.record(ctx, a, "", nil, landing.MergeRefused, "another ticket holds paths this one needs")
		return
	}

	held, stopBeat := r.heartbeat(ctx, server)
	defer stopBeat()
	rec := &landing.Record{TicketID: a.TicketID, AttemptNumber: a.Attempt}
	fail := r.run(held, server, rec)
	if fail != nil && fail.outcome == landing.Merged {
		// Merged but the ticket is still held: the reaper closes it and
		// records the outcome, once the lease stops being renewed.
		return
	}
	if fail == nil {
		// The merge happened: written down even if the runner is closing.
		rec.Outcome = landing.Merged
		r.write(context.WithoutCancel(ctx), rec)
		return
	}
	if ctx.Err() != nil {
		// Abandoned: the lease is the recovery, so nothing is written here.
		return
	}
	rec.Outcome, rec.Detail = fail.outcome, fail.detail
	// Not on the cancelled context: a failure is still handed back.
	hb, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if err := r.o.Tickets.ServerHandBack(hb, server, a.TicketID, fail.guidance, r.cfg.Affinity); err != nil {
		log.ErrorContext(ctx, "server merge: hand back failed; the reaper will retry", "err", err)
		return
	}
	r.write(hb, rec)
}

// run does the checks, the lock and the merge; nil is a merge.
func (r *Runner) run(ctx context.Context, server uuid.UUID, rec *landing.Record) *failure {
	tenant, err := db.TenantFrom(ctx)
	if err != nil {
		return &failure{landing.MergeRefused, "The server could not tell which tenant this is.", "no tenant"}
	}
	gate, err := r.o.Tickets.ChangeGateFor(ctx, server)
	if err != nil {
		return r.classify(ctx, err)
	}
	rec.ApproverUserID, rec.ApprovedAt = gate.Review.ApprovedByUserID, gate.Review.ApprovedAt

	report, fail := r.awaitChecks(ctx, tenant, server)
	if fail != nil {
		return fail
	}
	rec.ChangeURL, rec.HeadSHA = report.URL, report.HeadSHA

	if fail := r.awaitLock(ctx, server); fail != nil {
		return fail
	}
	// Exactly the head the checks were read for.
	merged, err := r.o.Changes.Merge(ctx, tenant, server, &change.MergeInput{ExpectedSHA: report.HeadSHA})
	if err != nil {
		return r.classify(ctx, err)
	}
	if !merged.Closed {
		// The change is on main; a lease left holding the ticket would only
		// be handed back as a failure.
		// If this fails too, the reaper closes it: it sees the merge recorded.
		if _, err := r.o.Tickets.Done(context.WithoutCancel(ctx), server, &ticket.DoneParams{PRURL: merged.URL}); err != nil {
			r.log.ErrorContext(ctx, "server merge: merged but could not close the ticket", "ticket", rec.TicketID, "change", merged.URL, "err", err)
			return &failure{outcome: landing.Merged, detail: "not closed"}
		}
	}
	return nil
}

func (r *Runner) awaitChecks(ctx context.Context, tenant, server uuid.UUID) (*change.Report, *failure) {
	deadline := r.clock.Now().Add(r.cfg.ChecksTimeout)
	for {
		report, err := r.o.Changes.Checks(ctx, tenant, server, "")
		if err != nil {
			return nil, r.classify(ctx, err)
		}
		switch report.State {
		case "success":
			return report, nil
		case "failure":
			return nil, &failure{landing.ChecksRed,
				fmt.Sprintf("The server tried to merge your approved change at %s and its checks failed. Fix them, push, wait for green, and merge it yourself: the approval stands for the same changes.", report.HeadSHA),
				"checks failed on " + report.HeadSHA}
		}
		// Pending, or none yet: CI may not have started.
		if !r.clock.Now().Before(deadline) {
			return nil, &failure{landing.ChecksPendingTimeout,
				fmt.Sprintf("The server tried to merge your approved change and its checks on %s were still %s after %s. Wait for them and merge it yourself.", report.HeadSHA, report.State, r.cfg.ChecksTimeout),
				"checks " + report.State + " at timeout"}
		}
		if !r.sleep(ctx, r.cfg.PollInterval) {
			return nil, &failure{outcome: landing.MergeRefused, detail: "cancelled"}
		}
	}
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
			return &failure{landing.LockTimeout,
				fmt.Sprintf("The server tried to merge your approved change and the integration lock stayed taken for %s. Merge it yourself when you can take the lock.", r.cfg.LockTimeout),
				"integration lock busy at timeout"}
		}
		if !r.sleep(ctx, r.cfg.PollInterval) {
			return &failure{outcome: landing.MergeRefused, detail: "cancelled"}
		}
	}
}

// classify maps what the gate or the host said to an outcome and the
// guidance the author is shown. Anything it does not know is a host error,
// reported without the error's text.
func (r *Runner) classify(ctx context.Context, err error) *failure {
	if ctx.Err() != nil {
		return &failure{outcome: landing.MergeRefused, detail: "cancelled"}
	}
	const tail = " Merge the change's base branch (origin/<base>, the branch the PR targets) into your branch if needed, push, wait for the checks, and merge it yourself."
	switch {
	case errors.Is(err, change.ErrBehindBase):
		return &failure{landing.MainMoved, "The server tried to merge your approved change: " + err.Error() + "." + tail, "head does not contain the base's latest commit"}
	case errors.Is(err, change.ErrChecksNotPassed):
		return &failure{landing.ChecksRed, "The server tried to merge your approved change: " + err.Error() + "." + tail, "checks not passed"}
	case errors.Is(err, change.ErrHeadNotReviewed), errors.Is(err, change.ErrHeadMismatch), errors.Is(err, change.ErrHeadMoved):
		return &failure{landing.HeadChanged, "The server did not merge your change: code was pushed after the approval. Hand it over again with ticket_finish so a person approves the new head.", "head is not the approved one"}
	case errors.Is(err, change.ErrNotApproved):
		return &failure{landing.NotApprovedForHead, "The server did not merge your change: no approval covers it now. Hand it over with ticket_finish.", "no approval"}
	case isRefusal(err):
		return &failure{landing.MergeRefused, "The server tried to merge your approved change and the merge gate refused it: " + err.Error() + ". Fix that and merge it yourself.", "refused by the merge gate"}
	}
	r.log.ErrorContext(ctx, "server merge: code host or store error", "err", err)
	return &failure{landing.CodeHostError, "The server tried to merge your approved change and the code host or the store failed. Check the change on the host and merge it yourself.", "code host error"}
}

func isRefusal(err error) bool {
	for _, target := range []error{
		change.ErrLockNotHeld, change.ErrDraft, change.ErrAlreadyMerged, change.ErrWrongRepo, change.ErrWrongBase,
		change.ErrNoRepo, change.ErrNoHost, change.ErrNoChange, change.ErrMergeInFlight, ticket.ErrNeedsDiscussion,
		ticket.ErrAttemptAwaitsChoice, ticket.ErrNoLease,
	} {
		if errors.Is(err, target) {
			return true
		}
	}
	return false
}

func isNotClaimable(err error) bool {
	for _, target := range []error{
		ticket.ErrServerClaimNotPromoted, ticket.ErrServerClaimNotApproved, ticket.ErrServerClaimNotQueued,
		ticket.ErrServerClaimHeld, ticket.ErrAlreadyHolding, ticket.ErrServerAgentDisabled, ticket.ErrServerAgentTaken,
	} {
		if errors.Is(err, target) {
			return true
		}
	}
	return false
}

// heartbeat keeps the server's lease fresh until stop. It cancels the
// returned context if the lease is gone, so an attempt does not carry on
// with a ticket it no longer holds.
func (r *Runner) heartbeat(ctx context.Context, server uuid.UUID) (context.Context, func()) {
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
		once.Do(func() { cancel(nil); <-stopped })
	}
}

func (r *Runner) record(ctx context.Context, a *ticket.Approval, url string, rec *landing.Record, o landing.Outcome, detail string) {
	if rec == nil {
		rec = &landing.Record{TicketID: a.TicketID, AttemptNumber: a.Attempt, ChangeURL: url}
	}
	rec.Outcome, rec.Detail = o, detail
	r.write(ctx, rec)
}

func (r *Runner) write(ctx context.Context, rec *landing.Record) {
	if err := r.o.Outcomes.Record(ctx, rec); err != nil {
		r.log.ErrorContext(ctx, "server merge: could not record the outcome", "ticket", rec.TicketID, "outcome", rec.Outcome, "err", err)
	}
}

func (r *Runner) sleep(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-r.clock.After(d):
		return true
	}
}
