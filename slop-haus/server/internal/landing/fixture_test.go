package landing_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-clock"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/agent"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit/audittest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/change"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost/codehosttest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/landing"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/landing/guidance"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/scope"
)

const (
	baseOld  = "1111111111111111111111111111111111111111"
	baseNew  = "2222222222222222222222222222222222222222"
	hunkOne  = "@@ -10,3 +10,4 @@ func one()\n a\n+b\n c\n d\n"
	hostName = "git.example"
	headA    = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	headB    = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

// workspaceFake places every path in one repo, with the drift rules a test sets.
type workspaceFake struct {
	mu   sync.Mutex
	repo scope.WorkspaceRepo
}

func (w *workspaceFake) Place(context.Context, []string) (*ticket.Placement, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	repo := w.repo
	repo.Remote = hostName + "/acme/app"
	return &ticket.Placement{Declared: true, Repos: []scope.WorkspaceRepo{repo}}, nil
}

func (w *workspaceFake) setRepo(repo scope.WorkspaceRepo) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.repo = repo
}

// laneWorkspace is the workspace the ticket store reads the apps of the claim
// rule from: the apps the fixture's batch tests use (app-a/, app-b/, at the
// workspace root), or those of a repo a test set with independent apps.
type laneWorkspace struct{ w *workspaceFake }

func (l laneWorkspace) Place(ctx context.Context, scopes []string) (*ticket.Placement, error) {
	pl, err := l.w.Place(ctx, scopes)
	if err != nil {
		return nil, err
	}
	repo := pl.Repos[0]
	if len(repo.IndependentApps) == 0 {
		repo = scope.WorkspaceRepo{Remote: repo.Remote, IndependentApps: []string{"app-a/", "app-b/"}}
	}
	repo.BatchWithinApps = true
	return &ticket.Placement{Declared: true, All: []scope.WorkspaceRepo{repo}, Repos: []scope.WorkspaceRepo{repo}}, nil
}

type namesFake struct{}

func (namesFake) Agent(context.Context, uuid.UUID) (string, error) { return "someone", nil }
func (namesFake) User(context.Context, uuid.UUID) (string, error)  { return "Alex Approver", nil }

// hostWrap lets a test stand at a point of a landing without timing: the
// real host adapter answers, after the hook has run.
type hostWrap struct {
	*landing.PostgresHost
	onUpdate func(ctx context.Context, head string)
	updates  atomic.Int32
}

func (h *hostWrap) UpdateBranch(ctx context.Context, tenant uuid.UUID, id int64, attempt int, head string) (*codehost.BranchUpdate, error) {
	h.updates.Add(1)
	if h.onUpdate != nil {
		h.onUpdate(ctx, head)
	}
	return h.PostgresHost.UpdateBranch(ctx, tenant, id, attempt, head)
}

// pipesFake is the CI system. Every answer is a field a test sets.
type pipesFake struct {
	mu           sync.Mutex
	timeout      time.Duration
	newest       func(head string) *landing.Pipeline
	passedOnBase bool
	// passedOnCommit answers PassedOnCommit by commit; a commit not in it did not pass.
	passedOnCommit map[string]bool
	retries        []string
	restarts       []int64
	// verdicts answers CheckVerdict by target URL; one not in it is not this CI's.
	verdicts map[string]landing.CheckVerdict
	asked    []string
}

func (p *pipesFake) CheckVerdict(_ context.Context, _, targetURL string) (landing.CheckVerdict, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.asked = append(p.asked, targetURL)
	if v, ok := p.verdicts[targetURL]; ok {
		return v, nil
	}
	return "", landing.ErrUnknown
}

func (p *pipesFake) askedAbout() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.asked...)
}

func (p *pipesFake) Newest(_ context.Context, head string) (*landing.Pipeline, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.newest == nil {
		return nil, nil
	}
	return p.newest(head), nil
}

func (p *pipesFake) RepoTimeout(context.Context) (time.Duration, error) { return p.timeout, nil }

func (p *pipesFake) Restart(_ context.Context, _ string, id int64) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.restarts = append(p.restarts, id)
	return nil
}

func (p *pipesFake) restarted() []int64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]int64(nil), p.restarts...)
}

func (p *pipesFake) Retry(_ context.Context, head string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.retries = append(p.retries, head)
	return nil
}

func (p *pipesFake) PassedOnBase(context.Context, []string) (bool, error) { return p.passedOnBase, nil }

func (p *pipesFake) PassedOnCommit(_ context.Context, sha string, _ []string) (bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.passedOnCommit[sha], nil
}

func (p *pipesFake) retried() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.retries...)
}

// ticketsWrap is the real ticket side, with the close made to fail on demand.
type ticketsWrap struct {
	*ticket.ServerAgent
	f *fixture
}

func (w *ticketsWrap) Done(ctx context.Context, agent uuid.UUID, p *ticket.DoneParams) (*ticket.Ticket, error) {
	if w.f.doneErr != nil {
		return nil, w.f.doneErr
	}
	return w.ServerAgent.Done(ctx, agent, p)
}

// ServerReleaseLanding is skipped when the fixture plays a process that died,
// which releases nothing.
func (w *ticketsWrap) ServerReleaseLanding(ctx context.Context, server uuid.UUID, ticketID int64) error {
	if w.f.diedHoldingLease {
		return nil
	}
	return w.ServerAgent.ServerReleaseLanding(ctx, server, ticketID)
}

// AcquireIntegrationLock is the real lock, counted, and refused with the test's
// lockErr when it sets one.
func (w *ticketsWrap) AcquireIntegrationLock(ctx context.Context, agent uuid.UUID) error {
	w.f.lockAttempts.Add(1)
	if w.f.onLockAttempt != nil {
		w.f.onLockAttempt()
	}
	if w.f.lockErr != nil {
		return w.f.lockErr
	}
	return w.ServerAgent.AcquireIntegrationLock(ctx, agent)
}

// ServerClaimLanding is the real claim, refused first when the test's claimGate says so.
func (w *ticketsWrap) ServerClaimLanding(ctx context.Context, server uuid.UUID, ticketID int64, attempt int, ttl time.Duration) (*ticket.ClaimResult, error) {
	if w.f.claimGate != nil {
		if err := w.f.claimGate(ticketID); err != nil {
			return nil, err
		}
	}
	return w.ServerAgent.ServerClaimLanding(ctx, server, ticketID, attempt, ttl)
}

type fixture struct {
	workspace *workspaceFake
	// diedHoldingLease makes Runner.Close release no lease, as a killed process.
	diedHoldingLease bool
	// claimGate, when set, runs before each landing claim; an error refuses the claim.
	claimGate func(ticketID int64) error
	// lockAttempts counts the runner's tries at the integration lock.
	lockAttempts atomic.Int32
	// onLockAttempt, when set, runs at each try at the integration lock.
	onLockAttempt func()
	// lockErr, when set, is what every try at the integration lock fails with.
	lockErr error
	// logger, when set, is the runner's log handler in place of a discarded one.
	logger slog.Handler
	// recorder replaces the outcome store as the runner's Outcomes when set.
	recorder landing.Outcomes
	observer landing.Observer
	doneErr  error
	// onChecks, when set, runs before each read of the change's checks.
	onChecks func(ctx context.Context)
	// wrapChanges, when set, wraps the change flow the runner is given.
	wrapChanges func(landing.Changes) landing.Changes
	t           *testing.T
	clock       clock.Clock
	database    *db.DB
	ctx         context.Context
	tenant      uuid.UUID
	host        *codehosttest.Fake
	tickets     *ticket.Service
	agents      *agent.Service
	changes     *change.Service
	store       *ticket.PostgresStore
	outcomes    *landing.Store
	queue       *landing.PostgresQueue
	hostWrap    *hostWrap
	pipes       *pipesFake
	// ci, when set, is the CI the runner uses instead of pipes.
	ci       landing.Pipelines
	on       atomic.Bool
	approver uuid.UUID
	creator  uuid.UUID
	nextPR   int64
	// bound is the collision bound the runner is given; zero takes the default.
	bound  int
	landed *guidance.PostgresSource
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	database := dbtest.WithDB(t)
	tenant := dbtest.NewTenant(t, database)
	f := &fixture{
		t: t, database: database, tenant: tenant, ctx: db.WithTenant(context.Background(), tenant),
		host: codehosttest.NewLinkHost(hostName), approver: dbtest.NewUser(t, database), creator: dbtest.NewUser(t, database),
		pipes: &pipesFake{timeout: time.Hour}, workspace: &workspaceFake{repo: scope.WorkspaceRepo{Path: "repo"}},
	}
	f.on.Store(true)
	f.host.MergedWith = codehost.Merged{SHA: "mergecommit"}
	f.host.ChecksOf = map[codehost.ChangeRef]*codehost.Checks{}
	f.host.CompareOf = map[codehost.ChangeRef]*codehost.BaseComparison{}
	f.store = ticket.NewPostgresStore(database, ticket.WithMergeQueue(landing.NewDequeuer()), ticket.WithStoreWorkspace(laneWorkspace{f.workspace}))
	f.tickets = ticket.NewService(f.store, slog.DiscardHandler, ticket.WithCodeHosts(codehosttest.Registry(f.host)))
	f.agents = agent.NewService(agent.NewPostgresStore(database), f.tickets, nil, slog.DiscardHandler)
	changeStore := change.NewPostgresStore(database)
	svc, err := change.NewService(&change.Options{
		Hosts: codehosttest.Registry(f.host), Tickets: f.tickets, Workspace: f.workspace, Store: changeStore,
		Names: namesFake{}, Detect: codehost.LineNumberChangeDetectionStrategy{},
		Events:   audit.New(slog.DiscardHandler, audittest.NewCounter()),
		BoardURL: "https://board.example", Logger: slog.DiscardHandler, Trees: f.host,
	})
	require.NoError(t, err)
	f.changes = svc
	f.outcomes = landing.NewStore(database)
	f.queue = landing.NewPostgresQueue(database)
	f.hostWrap = &hostWrap{PostgresHost: landing.NewPostgresHost(changeStore, codehosttest.Registry(f.host))}
	f.landed = guidance.NewPostgresSource(database, changeStore, codehosttest.Registry(f.host))
	return f
}

func (f *fixture) runnerLogger() slog.Handler {
	if f.logger != nil {
		return f.logger
	}
	return slog.DiscardHandler
}

func (f *fixture) ref(n int64) codehost.ChangeRef {
	return codehost.ChangeRef{Host: hostName, Owner: "acme", Repo: "app", Number: n}
}

// setChecks makes the change's head and checks, with the head containing main's
// tip or not.
func (f *fixture) setChecks(ref codehost.ChangeRef, head string, containsTip bool, checks ...codehost.Check) {
	f.host.ChecksOf[ref] = codehost.NewChecks(head, "main", checks)
	f.host.CompareOf[ref] = &codehost.BaseComparison{Base: "main", ContainsTip: containsTip}
}

func (f *fixture) runner(cfg landing.Config) *landing.Runner {
	f.t.Helper()
	if cfg.PollInterval == 0 {
		cfg.PollInterval = time.Millisecond
	}
	if cfg.HostBackoff == 0 {
		cfg.HostBackoff = time.Millisecond
	}
	var outcomes landing.Outcomes = f.outcomes
	if f.recorder != nil {
		outcomes = f.recorder
	}
	var changes landing.Changes = &changesWrap{Changes: f.changes, f: f}
	if f.wrapChanges != nil {
		changes = f.wrapChanges(changes)
	}
	r, err := landing.New(&landing.Options{
		Tickets: &ticketsWrap{ServerAgent: ticket.NewServerAgent(f.tickets), f: f}, Changes: changes, Host: f.hostWrap, Pipelines: f.pipelines(),
		Queue: f.queue, Outcomes: outcomes, Config: cfg,
		Guidance: guidance.New(f.landed, guidance.NewPostgresHistory(f.database), f.bound, slog.DiscardHandler), Logger: f.runnerLogger(), Clock: f.clock, Observer: f.observer,
		Mode:    landing.ModeFunc(func(context.Context) (bool, error) { return f.on.Load(), nil }),
		Tenants: func(context.Context) ([]uuid.UUID, error) { return []uuid.UUID{f.tenant}, nil },
	})
	require.NoError(f.t, err)
	f.t.Cleanup(r.Close)
	return r
}

// finished is an author's gated ticket, with its change opened and handed over
// at headA, approved by a person and queued for landing.
type finished struct {
	author   uuid.UUID
	ticketID int64
	ref      codehost.ChangeRef
	attempt  int
}

func (f *fixture) finishedChange(priority model.Priority) *finished {
	f.t.Helper()
	c := f.finishedChangeAt(priority, "app/server/")
	f.enqueue(c)
	return c
}

func (f *fixture) enqueue(c *finished) {
	f.t.Helper()
	require.NoError(f.t, f.queue.Enqueue(f.ctx, &landing.LandingRow{
		TicketID: c.ticketID, AttemptNumber: c.attempt, ChangeURL: f.hostWrapURL(c.ref), HeadSHA: headA,
	}))
}

// finishedChangeAt is finishedChange for a ticket on path, not yet queued.
func (f *fixture) finishedChangeAt(priority model.Priority, path string) *finished {
	f.t.Helper()
	f.nextPR++
	ref := f.ref(f.nextPR)
	f.host.Opened = ref
	f.host.DiffOf = map[string]*codehost.Diff{headA: f.diff(baseOld)}
	f.setChecks(ref, headA, true, codehost.Check{Name: "ci/build", State: codehost.CheckSuccess})
	author, err := f.agents.Create(f.ctx, fmt.Sprintf("author-%d", f.nextPR))
	require.NoError(f.t, err)
	created, err := f.tickets.Create(f.ctx, &ticket.CreateParams{
		Title: "Add the thing", Paths: []model.Path{{Path: path}}, Status: ticket.StatusOpen,
		CreatedByUserID: &f.creator, RequiresApproval: true, Priority: priority,
	})
	require.NoError(f.t, err)
	claimed, err := f.tickets.Claim(f.ctx, author.ID, &created.ID)
	require.NoError(f.t, err)
	require.NotNil(f.t, claimed.Ticket)
	_, err = f.changes.Open(f.ctx, f.tenant, author.ID, &change.OpenInput{Branch: ticket.BranchName(created.ID, claimed.Attempt.Number), Title: "Add the thing", Body: "It adds the thing."})
	require.NoError(f.t, err)
	_, err = f.changes.Review(f.ctx, f.tenant, author.ID, &ticket.ReviewParams{Reason: "reviewed by a person", PRURL: fmt.Sprintf("https://git.example/acme/app/pull/%d", ref.Number), Handoff: "ready"})
	require.NoError(f.t, err)
	_, err = f.tickets.Approve(f.ctx, created.ID, f.approver)
	require.NoError(f.t, err)
	return &finished{author: author.ID, ticketID: created.ID, ref: ref, attempt: claimed.Attempt.Number}
}

func (f *fixture) hostWrapURL(ref codehost.ChangeRef) string {
	return fmt.Sprintf("https://git.example/%s/%s/pull/%d", ref.Owner, ref.Repo, ref.Number)
}

// land kicks the tenant and waits until the runner is idle.
func (f *fixture) land(r *landing.Runner) {
	f.t.Helper()
	r.Kick(f.tenant)
	r.Wait()
}

// landAdvancing is land, moving the fake clock by step whenever the runner is
// waiting on it, until it is idle.
func (f *fixture) landAdvancing(r *landing.Runner, fake *clock.Fake, step time.Duration) {
	f.t.Helper()
	r.Kick(f.tenant)
	done := make(chan struct{})
	go func() { r.Wait(); close(done) }()
	for {
		select {
		case <-done:
			return
		default:
		}
		if fake.Waiters() > 0 {
			fake.Advance(step)
		}
		runtime.Gosched()
	}
}

func (f *fixture) outcomesNow() []landing.Record {
	f.t.Helper()
	recs, err := f.outcomes.List(f.ctx, time.Now().Add(-time.Hour), time.Now().Add(time.Hour), 50)
	require.NoError(f.t, err)
	return recs
}

func (f *fixture) outcomeOf(id int64) landing.Record {
	f.t.Helper()
	for _, r := range f.outcomesNow() {
		if r.TicketID == id {
			return r
		}
	}
	f.t.Fatalf("no outcome for ticket %d", id)
	return landing.Record{}
}

func (f *fixture) lockHolder() *ticket.LockHolder {
	f.t.Helper()
	st, err := f.tickets.IntegrationLockState(f.ctx)
	require.NoError(f.t, err)
	return st.Holder
}

func (f *fixture) row(id int64) *landing.LandingRow {
	f.t.Helper()
	r, err := f.queue.Get(f.ctx, id)
	if err != nil {
		return nil
	}
	return r
}

func (f *fixture) comments(id int64) string {
	f.t.Helper()
	rows, err := f.database.Query(f.ctx, `SELECT body FROM ticket_comments WHERE ticket_id = $1 ORDER BY id`, id)
	require.NoError(f.t, err)
	defer rows.Close()
	var all []string
	for rows.Next() {
		var b string
		require.NoError(f.t, rows.Scan(&b))
		all = append(all, b)
	}
	return strings.Join(all, "\n")
}

// diff is the change's diff against a base tip that the head contains: the
// same changes whatever the base, which is what keeps an approval valid across
// a clean merge of main.
func (f *fixture) diff(base string) *codehost.Diff {
	return &codehost.Diff{Base: "main", BaseSHA: base, MergeBaseSHA: base,
		Files: []codehost.FileDiff{{Path: "a.go", Status: codehost.DiffModified, Patch: hunkOne}}}
}

// markMergedOnHost makes the host say the change's head branch has nothing the
// base lacks, which is what a merged change looks like.
func (f *fixture) markMergedOnHost(c *finished) {
	f.t.Helper()
	rec, err := change.NewPostgresStore(f.database).Current(f.ctx, c.ticketID, 1)
	require.NoError(f.t, err)
	if f.host.ComparisonOf == nil {
		f.host.ComparisonOf = map[codehosttest.BranchKey]*codehost.BranchComparison{}
	}
	f.host.ComparisonOf[codehosttest.BranchKey{Repo: rec.Change.InRepo(), Branch: rec.Head}] = &codehost.BranchComparison{Ahead: 0, Behind: 3}
}

// landPerRead lands with the fake clock moved one minute per read of the checks,
// so a bound is measured in polls and not in how fast this machine is. It
// returns the time of the last read.
func (f *fixture) landPerRead(r *landing.Runner, fake *clock.Fake) time.Time {
	f.t.Helper()
	reads := make(chan struct{}, 10000)
	f.host.AfterChecks = func() { reads <- struct{}{} }
	r.Kick(f.tenant)
	done := make(chan struct{})
	go func() { r.Wait(); close(done) }()
	var last time.Time
	for {
		select {
		case <-done:
			return last
		case <-reads:
			// The heartbeat and the poll sleep are the two waiters.
			for fake.Waiters() < 2 {
				select {
				case <-done:
					return last
				default:
				}
			}
			last = fake.Now()
			fake.Advance(time.Minute)
		}
	}
}

func (f *fixture) pipelines() landing.Pipelines {
	if f.ci != nil {
		return f.ci
	}
	return f.pipes
}

// observed is an Observer that keeps what it was told.
type observed struct {
	outcome    string
	depth      int
	enqueuedIn time.Duration
	batchSize  int
}

type recordingObserver struct {
	got     []observed
	batches []string
}

func (o *recordingObserver) LandingBatch(result string) { o.batches = append(o.batches, result) }

func (o *recordingObserver) LandingOutcome(out string, depth int, d time.Duration, size int) {
	o.got = append(o.got, observed{out, depth, d, size})
}

type failingOutcomes struct{}

func (failingOutcomes) Record(context.Context, *landing.Record) error { return errors.New("db down") }

// reserveNow creates a batch of ticketIDs and reserves its members at once,
// for tests that need a reserved batch without building one.
func reserveNow(ctx context.Context, s *landing.BatchStore, baseSHA string, ticketIDs []int64) (*landing.Batch, error) {
	created, err := s.Create(ctx, baseSHA, ticketIDs)
	if err != nil {
		return nil, err
	}
	return s.Reserve(ctx, created.ID)
}
