package landing_test

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-clock"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/change"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost/codehosttest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/landing"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/scope"
)

// The soak test drives the real Runner and BatchRunner, over real Postgres and
// a fake clock, through a seeded random sequence of things going wrong, and
// checks invariants after every step. A failure prints the seed and the trace
// of steps, so it reproduces:
//
//	SOAK_SEED=<seed> SOAK_STEPS=<steps> go test ./internal/landing -run TestSoak
//
// SOAK_SEED=random picks a new seed and logs it (the nightly run).
// soakDefaultSeeds are the seeds run when SOAK_SEED is not set. The second was
// picked because reverting the fix for the serial head admitting a batch's first
// member and the fix for the lane check each fails it on the invariants.
var soakDefaultSeeds = []uint64{20261031, 8}

const (
	soakDefaultSteps = 200
	// soakStarvedAfter is how many landings may finish after a refused claim
	// has lapsed, before the ticket ranking above the one landed is starved:
	// a batch lands two between two looks at the queue.
	soakStarvedAfter = 1
)

type soakFate int

const (
	fateGreen soakFate = iota
	fateRed
	// fateFlaky is red until CI is asked to run it again, then green.
	fateFlaky
	// fateNever is a head CI never starts on.
	fateNever
)

func (f soakFate) String() string { return [...]string{"green", "red", "flaky", "never"}[f] }

// soakTicket is the model's view of a ticket: what CI will say of it, and
// where it is. The model decides nothing about landing; it is what the
// invariants are checked against.
type soakTicket struct {
	*finished
	fate   soakFate
	healed bool
	path   string
	head   string
	// behind is whether main has moved past the head.
	behind  bool
	queued  bool
	merges  int
	mergeAt int
	// refuseUntil refuses the landing claim while fewer landings than this
	// have finished, and while refusals remain: a claim that fails "for a
	// while" lapses by either, as nothing else may be landing to count by.
	// refused is whether the refusal has not been followed by a claim yet.
	refuseUntil int
	refusals    int
	refused     bool
	// refusedNow is whether the claim was refused in the pass in hand.
	refusedNow bool
}

func (t *soakTicket) passing() bool {
	return t.fate == fateGreen || (t.fate == fateFlaky && t.healed)
}

type soak struct {
	t     *testing.T
	f     *fixture
	fake  *clock.Fake
	rng   *rand.Rand
	world *batchWorld
	host  *soakHost
	store *landing.BatchStore
	size  atomic.Int32

	runner  *landing.Runner
	tickets []*soakTicket
	byID    map[int64]*soakTicket
	// prepared are finished tickets not yet in the queue: a ticket's author
	// holds its paths until it is finished, so tickets are made while the
	// runner is idle and queued whenever the step says.
	prepared []*soakTicket
	current  int64

	// landings counts outcomes written.
	landings int
	reads    int
	mergeSeq int
	prefix   map[string][]int64
	// tips are the tips main has moved to.
	tips   []string
	branch map[string][]int64

	// events run at a read of CI, in the runner's goroutine: where the world
	// changes in the middle of a landing.
	events  map[int]func(ctx context.Context)
	crashAt int
	crashed bool
	crashes int
	// recovering is whether this pass is the first after a dead process's
	// lease ran out.
	recovering bool
	crashedAt  time.Time

	mu       sync.Mutex
	traceLog []string
	failed   bool
}

func TestSoak(t *testing.T) {
	seeds, steps := soakParams(t)
	for _, seed := range seeds {
		t.Run(fmt.Sprintf("seed_%d", seed), func(t *testing.T) { soakRun(t, seed, steps) })
	}
}

func soakRun(t *testing.T, seed uint64, steps int) {
	s := newSoak(t, seed)
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("soak failed: seed=%d steps=%d; rerun with SOAK_SEED=%d SOAK_STEPS=%d\ntrace:\n%s", seed, steps, seed, steps, strings.Join(s.traceLog, "\n"))
		}
	})
	for i := range steps {
		s.step(i)
		s.checkInvariants(fmt.Sprintf("after step %d", i))
		if s.failed {
			return
		}
	}
	s.drain()
	s.assertFinal()
}

func soakParams(t *testing.T) (seeds []uint64, steps int) {
	t.Helper()
	seeds, steps = soakDefaultSeeds, soakDefaultSteps
	switch v := os.Getenv("SOAK_SEED"); v {
	case "":
	case "random":
		seeds = []uint64{uint64(time.Now().UnixNano())}
		t.Logf("soak seed %d", seeds[0])
	default:
		n, err := strconv.ParseUint(v, 10, 64)
		require.NoError(t, err, "SOAK_SEED")
		seeds = []uint64{n}
	}
	if v := os.Getenv("SOAK_STEPS"); v != "" {
		n, err := strconv.Atoi(v)
		require.NoError(t, err, "SOAK_STEPS")
		steps = n
	}
	return seeds, steps
}

// soakHost is the batch world's host, remembering which tickets each batch
// commit contains, so the batch's CI can be red because of one member.
type soakHost struct {
	*batchWorld
	s *soak
}

func (h *soakHost) CreateBranch(ctx context.Context, row *landing.LandingRow, branch, sha string) error {
	h.s.branch[branch] = nil
	return h.batchWorld.CreateBranch(ctx, row, branch, sha)
}

func (h *soakHost) MergeHead(ctx context.Context, row *landing.LandingRow, branch, head string) (*codehost.BranchMerge, error) {
	m, err := h.batchWorld.MergeHead(ctx, row, branch, head)
	if err != nil {
		return nil, err
	}
	h.s.branch[branch] = append(slices.Clone(h.s.branch[branch]), row.TicketID)
	h.s.prefix[m.SHA] = slices.Clone(h.s.branch[branch])
	return m, nil
}

// soakPipes is the CI: a run again heals what was a flake.
type soakPipes struct {
	*pipesFake
	s *soak
}

func (p *soakPipes) Retry(ctx context.Context, head string) error {
	s := p.s
	members := s.prefix[head]
	if len(members) == 0 && s.current != 0 {
		members = []int64{s.current}
	}
	for _, id := range members {
		if t := s.byID[id]; t.fate == fateFlaky && !t.healed {
			t.healed = true
			s.publish(t)
		}
	}
	return p.pipesFake.Retry(ctx, head)
}

// soakChanges is the gate, with every merge checked against what CI said.
type soakChanges struct {
	landing.Changes
	s *soak
}

func (c *soakChanges) Merge(ctx context.Context, tenant, agent uuid.UUID, in *change.MergeInput) (*change.Merged, error) {
	s := c.s
	id := s.current
	if in.Batch != nil {
		pos := 0
		for pos < len(in.Batch.Members) && in.Batch.Members[pos].MergeSHA != "" {
			pos++
		}
		id = in.Batch.Members[pos].TicketID
		// Members merge in the order the batch tested them.
		for _, m := range in.Batch.Members[:pos] {
			if s.byID[m.TicketID].mergeAt == 0 {
				s.violate("ticket %d merged before the member ahead of it in its batch, %d", id, m.TicketID)
			}
		}
	}
	t := s.byID[id]
	if !t.passing() {
		s.violate("ticket %d merged without passing CI evidence (fate %s, healed %v)", id, t.fate, t.healed)
	}
	merged, err := c.Changes.Merge(ctx, tenant, agent, in)
	if err == nil {
		t.merges++
		if s.mergeSeq++; t.merges > 1 {
			s.violate("ticket %d merged twice", id)
		}
		t.mergeAt = s.mergeSeq
		s.trace("  merged #%d (%s, seq %d)", id, t.fate, s.mergeSeq)
	}
	return merged, err
}

type soakObserver struct{ s *soak }

func (o soakObserver) LandingOutcome(out string, _ int, _ time.Duration, _ int) {
	o.s.landings++
	o.s.trace("  outcome %s (landings=%d)", out, o.s.landings)
}

func (o soakObserver) LandingBatch(string) {}

func newSoak(t *testing.T, seed uint64) *soak {
	t.Helper()
	f := newFixture(t)
	// Rows are stamped by the database's clock, which the runner compares with
	// its own: the fake one starts where the real one is.
	fake := clock.NewFake(time.Now())
	f.clock = fake
	f.pipes.timeout = 30 * time.Minute
	s := &soak{t: t, f: f, fake: fake, rng: rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15)),
		byID: map[int64]*soakTicket{}, prefix: map[string][]int64{}, branch: map[string][]int64{}, events: map[int]func(context.Context){}}
	s.size.Store(2)
	f.workspace.setRepo(scope.WorkspaceRepo{Path: "repo", IndependentApps: []string{"app-a/", "app-b/"}, SkipRetestAcrossApps: true,
		DeployOnly: []scope.DeployOnlyRule{{Path: "deploy/*/kustomization.yaml", Lines: `\s*newTag: \S+`}}})
	s.world = newBatchWorld(f)
	s.host = &soakHost{batchWorld: s.world, s: s}
	s.store = landing.NewBatchStore(f.database)
	s.world.checks = s.batchChecks
	s.world.atBatch = s.onRead
	f.onChecks = s.onRead
	f.observer = soakObserver{s}
	f.ci = &soakPipes{pipesFake: f.pipes, s: s}
	f.claimGate = s.claimGate
	f.logger = traceLogs{s}
	f.hostWrap.onUpdate = s.onUpdate
	f.host.BranchUpdated = codehost.BranchUpdate{SHA: headB}
	inner := f.wrapChanges
	f.wrapChanges = func(c landing.Changes) landing.Changes { return &soakChanges{Changes: inner(c), s: s} }
	return s
}

func (s *soak) trace(format string, args ...any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.traceLog = append(s.traceLog, fmt.Sprintf(format, args...))
}

// traceLogs puts what the runner logs at warn and above in the trace.
type traceLogs struct{ s *soak }

func (h traceLogs) Enabled(_ context.Context, l slog.Level) bool { return l >= slog.LevelInfo }
func (h traceLogs) Handle(_ context.Context, r slog.Record) error {
	var attrs []string
	r.Attrs(func(a slog.Attr) bool { attrs = append(attrs, a.String()); return true })
	h.s.trace("    log %s: %s %s", r.Level, r.Message, strings.Join(attrs, " "))
	return nil
}
func (h traceLogs) WithAttrs(as []slog.Attr) slog.Handler {
	return &attrLogs{traceLogs: h, attrs: as}
}
func (h traceLogs) WithGroup(string) slog.Handler { return h }

type attrLogs struct {
	traceLogs
	attrs []slog.Attr
}

func (h *attrLogs) Handle(ctx context.Context, r slog.Record) error {
	r.AddAttrs(h.attrs...)
	return h.traceLogs.Handle(ctx, r)
}
func (h *attrLogs) WithAttrs(as []slog.Attr) slog.Handler {
	return &attrLogs{traceLogs: h.traceLogs, attrs: append(slices.Clone(h.attrs), as...)}
}

func (s *soak) violate(format string, args ...any) {
	s.failed = true
	msg := fmt.Sprintf(format, args...)
	s.trace("VIOLATION: %s", msg)
	s.t.Errorf("invariant: %s", msg)
}

// soakPolicy is the batch policy the soak runs under.
var soakPolicy = &landing.PathPolicy{Apps: []string{"repo/app-a/", "repo/app-b/"}}

// checkInvariants reports every rule the queue's tables break, with the trace.
// A rule known to be broken by today's code is skipped only with the bug that
// tracks it, in soakKnownBugs.
func (s *soak) checkInvariants(when string) {
	s.t.Helper()
	vs, err := landing.CheckInvariants(s.f.ctx, s.f.database, s.f.tenant, soakPolicy)
	require.NoError(s.t, err)
	for _, v := range vs {
		if bug := soakKnownBug(&v); bug != 0 {
			continue
		}
		s.violate("%s: %s: ticket %d batch %d: %s [%s]", when, v.Rule, v.TicketID, v.BatchID, v.Detail, s.debugTicket(v.TicketID))
	}
}

// soakKnownBug is the ticket that tracks a violation today's code is known to
// cause, 0 if there is none. A rule is skipped only as far as its bug goes.
func soakKnownBug(v *landing.Violation) int {
	return 0
}

func (s *soak) newRunner() *landing.Runner {
	s.t.Helper()
	f := s.f
	r := f.runner(landing.Config{PollInterval: time.Minute, KnownFlaky: []string{"ci/build"}})
	builder, err := landing.NewBatchBuilder(r, &landing.BatchOptions{Store: s.store, Host: s.host, Members: landing.NewPostgresMemberHandBack(f.database)})
	require.NoError(s.t, err)
	br, err := landing.NewBatchRunner(r, &landing.BatchRunnerOptions{
		Builder:  builder,
		Selector: landing.NewBatchSelector(soakPolicy, s.world),
		Queue:    f.queue, Checks: s.world, Recovery: landing.NewPostgresBatchRecovery(f.database),
		Size: func(context.Context) (int, error) { return int(s.size.Load()), nil },
	})
	require.NoError(s.t, err)
	r.UseBatches(br)
	return r
}

// publish makes the host show what CI says of the ticket's head.
func (s *soak) publish(t *soakTicket) {
	var checks []codehost.Check
	switch {
	case t.passing():
		checks = []codehost.Check{green}
	case t.fate != fateNever:
		checks = []codehost.Check{red}
	}
	s.f.setChecks(t.ref, t.head, !t.behind, checks...)
}

func (s *soak) batchChecks(head string) *codehost.Checks {
	members := s.prefix[head]
	var checks []codehost.Check
	switch {
	case len(members) == 0:
	case slices.ContainsFunc(members, func(id int64) bool { return s.byID[id].fate == fateNever }):
	case slices.ContainsFunc(members, func(id int64) bool { return !s.byID[id].passing() }):
		checks = []codehost.Check{red}
	default:
		checks = []codehost.Check{green}
	}
	return codehost.NewChecks(head, "main", checks)
}

// onUpdate is main merged into the head: a new head, behind nothing.
func (s *soak) onUpdate(context.Context, string) {
	t := s.byID[s.current]
	s.f.host.DiffOf[headB] = s.f.diff(baseNew)
	t.head, t.behind = headB, false
	s.publish(t)
}

// onRead runs at each read of CI's word, in the runner's goroutine.
func (s *soak) onRead(ctx context.Context) {
	s.reads++
	s.checkInvariants(fmt.Sprintf("at CI read %d", s.reads))
	if ev, ok := s.events[s.reads]; ok {
		delete(s.events, s.reads)
		ev(ctx)
	}
	if s.crashAt != 0 && s.reads >= s.crashAt {
		s.crashAt = 0
		s.trace("  crash at read %d", s.reads)
		s.crashed, s.crashedAt = true, s.fake.Now()
		s.crashes++
		old := s.runner
		// The process dies in the middle of the landing: nothing it was about
		// to write is written, and its lease is left to run out.
		go old.Close()
		<-ctx.Done()
	}
}

// claimGate refuses the claims the model has made refuse, and checks that a
// ticket whose refusal lapsed is not left behind lower-ranked ones.
func (s *soak) claimGate(id int64) error {
	t := s.byID[id]
	if t.refused && s.landings < t.refuseUntil && t.refusals > 0 {
		t.refusals--
		t.refusedNow = true
		return fmt.Errorf("%w: soak refusal", ticket.ErrAlreadyHolding)
	}
	t.refused = false
	s.checkStarvation(id)
	s.current = id
	return nil
}

func (s *soak) checkStarvation(landed int64) {
	var late []*soakTicket
	for _, t := range s.tickets {
		if t.queued && t.refused && t.ticketID != landed && s.landings-t.refuseUntil > soakStarvedAfter {
			late = append(late, t)
		}
	}
	if len(late) == 0 {
		return
	}
	rows, err := s.f.queue.Ordered(s.f.ctx)
	if err != nil {
		s.violate("read the queue: %v", err)
		return
	}
	// A reserved batch member that is not the batch's first is the batch's to
	// land. Merged members leave gaps, so the first is the lowest position left.
	first := map[int64]int{}
	for _, r := range rows {
		if r.State == landing.Landing && r.BatchID != nil {
			if p, ok := first[*r.BatchID]; !ok || r.BatchPosition < p {
				first[*r.BatchID] = r.BatchPosition
			}
		}
	}
	pos := map[int64]int{}
	for _, r := range rows {
		if r.State == landing.Landing && (r.BatchID == nil || r.BatchPosition == first[*r.BatchID]) {
			pos[r.TicketID] = r.Position
		}
	}
	for _, t := range late {
		if p, ok := pos[t.ticketID]; ok && p < pos[landed] {
			s.violate("ticket %d ranks %d and has been claimable for %d landings, but ticket %d (rank %d) was landed first: starved",
				t.ticketID, p, s.landings-t.refuseUntil, landed, pos[landed])
		}
	}
}

var soakPaths = []string{"repo/app-a/", "repo/app-a/", "repo/app-b/", "repo/app-b/", "repo/other/"}

var soakFates = []soakFate{fateGreen, fateGreen, fateGreen, fateGreen, fateRed, fateFlaky, fateFlaky, fateNever}

func (s *soak) pick(n int) int { return s.rng.IntN(n) }

// prepare makes a finished ticket, not yet queued.
func (s *soak) prepare() {
	prios := []model.Priority{model.PriorityLow, model.PriorityNormal, model.PriorityNormal, model.PriorityHigh, model.PriorityUrgent}
	// A leaf of its own: a dead landing's lease holds its ticket's paths, and
	// tickets of one app must not stop each other's authors.
	path := fmt.Sprintf("%st%d/", soakPaths[s.pick(len(soakPaths))], len(s.tickets))
	c := s.f.finishedChangeAt(prios[s.pick(len(prios))], path)
	// finishedChangeAt replaces the diffs; the head main is merged into has one too.
	s.f.host.DiffOf[headB] = s.f.diff(baseNew)
	t := &soakTicket{finished: c, fate: soakFates[s.pick(len(soakFates))], path: path, head: headA}
	s.tickets = append(s.tickets, t)
	s.byID[c.ticketID] = t
	s.prepared = append(s.prepared, t)
	s.publish(t)
}

func (s *soak) enqueueNext() {
	if len(s.prepared) == 0 {
		return
	}
	t := s.prepared[0]
	s.prepared = s.prepared[1:]
	t.queued = true
	s.f.enqueue(t.finished)
	// ticket_finish puts the attempt in the merge queue with the row.
	s.f.exec(`UPDATE attempts SET status = 'in_merge_queue' WHERE ticket_id = $1 AND number = $2`, t.ticketID, t.attempt)
	s.trace("  enqueued #%d %s %s", t.ticketID, t.path, t.fate)
}

func (s *soak) queuedCount() int {
	n := 0
	for _, t := range s.tickets {
		if t.queued && s.landingRow(t.ticketID) {
			n++
		}
	}
	return n
}

func (s *soak) landingRow(id int64) bool {
	r := s.f.row(id)
	return r != nil && r.State == landing.Landing
}

func (s *soak) step(i int) {
	s.t.Helper()
	switch n := s.pick(100); {
	case n < 15 && len(s.prepared) < 4:
		s.trace("step %d: prepare", i)
		s.prepare()
		if s.pick(3) > 0 {
			s.enqueueNext()
		}
	case n < 23:
		s.trace("step %d: enqueue", i)
		s.enqueueNext()
	case n < 25:
		// A deep queue is where a ticket skipped for one pass is left behind.
		s.trace("step %d: burst", i)
		for range 3 {
			s.prepare()
			s.enqueueNext()
		}
	case n < 52:
		s.trace("step %d: pass", i)
		if s.pick(3) == 0 && len(s.prepared) > 0 {
			at := s.reads + 1 + s.pick(4)
			s.events[at] = func(context.Context) { s.trace("  arrival mid-landing"); s.enqueueNext() }
		}
		s.pass()
	case n < 60:
		if q := s.queuedTickets(); len(q) > 0 {
			// The head is the one whose refusal can starve it.
			t := q[s.pick(len(q))]
			if s.pick(10) < 7 {
				t = s.byID[s.headOfQueue()]
			}
			t.refuseUntil, t.refusals, t.refused = s.landings+1+s.pick(2), 1+s.pick(2), true
			s.trace("step %d: refuse claims of #%d until %d landings", i, t.ticketID, t.refuseUntil)
		}
	case n < 64:
		s.trace("step %d: claims recover", i)
		for _, t := range s.tickets {
			t.refuseUntil = min(t.refuseUntil, s.landings)
		}
	case n < 67:
		s.trace("step %d: the head's claim fails once, with a deep queue behind it", i)
		for s.queuedCount() < 5 {
			s.prepare()
			s.enqueueNext()
		}
		h := s.byID[s.headOfQueue()]
		h.refuseUntil, h.refusals, h.refused = s.landings+1, 1, true
		s.pass()
	case n < 72:
		if s.crashAt == 0 {
			s.crashAt = s.reads + 1 + s.pick(5)
			s.trace("step %d: plan a crash at read %d", i, s.crashAt)
		}
	case n < 80:
		s.trace("step %d: main moves", i)
		s.moveMain()
	case n < 85:
		s.size.Store(int32(1 + s.pick(2)))
		s.trace("step %d: batch size %d", i, s.size.Load())
	default:
		d := time.Duration(1+s.pick(4)) * time.Minute
		s.trace("step %d: %s passes", i, d)
		s.fake.Advance(d)
	}
}

func (s *soak) headOfQueue() int64 {
	rows, err := s.f.queue.Ordered(s.f.ctx)
	require.NoError(s.t, err)
	for _, r := range rows {
		if r.State == landing.Landing {
			return r.TicketID
		}
	}
	return 0
}

func (s *soak) queuedTickets() []*soakTicket {
	var out []*soakTicket
	for _, t := range s.tickets {
		if t.queued && s.landingRow(t.ticketID) {
			out = append(out, t)
		}
	}
	return out
}

// soakMoves are what main can change, by the files it touches: the drift rules
// accept a move in another app or in deploy-only files for a batch, and refuse
// one in its own app or in a path no app owns.
var soakMoves = []struct {
	name string
	file codehost.FileDiff
}{
	{"in app-a", codehost.FileDiff{Path: "app-a/x.go", Status: codehost.DiffModified, Patch: hunkOne}},
	{"in app-b", codehost.FileDiff{Path: "app-b/x.go", Status: codehost.DiffModified, Patch: hunkOne}},
	{"deploy-only", codehost.FileDiff{Path: "deploy/test/kustomization.yaml", Status: codehost.DiffModified,
		Patch: "@@ -5,3 +5,3 @@ images:\n   - name: api\n-    newTag: 20261011T1\n+    newTag: 20261011T2\n   - name: web"}},
	{"in a shared path", codehost.FileDiff{Path: "shared/x.go", Status: codehost.DiffModified, Patch: hunkOne}},
}

// soakMergeCommits is how many of the host fake's numbered merge commits a move is
// described for.
const soakMergeCommits = 600

// moveMain moves main's tip, and says which paths the move changed from every
// tip a batch or a merge may have predicted.
func (s *soak) moveMain() {
	move := soakMoves[s.pick(len(soakMoves))]
	tip := fmt.Sprintf("%040d", 900000+s.pick(1000000))
	s.trace("  main moves %s", move.name)
	h := s.f.host
	if h.CommitsOf == nil {
		h.CommitsOf = map[codehosttest.CommitRange]*codehost.Diff{}
	}
	froms := append([]string{baseOld, s.world.main, "mergecommit"}, s.tips...)
	for n := 1; n <= soakMergeCommits; n++ {
		froms = append(froms, fmt.Sprintf("%040d", n))
	}
	for _, from := range froms {
		h.CommitsOf[codehosttest.CommitRange{From: from, To: tip}] = &codehost.Diff{BaseSHA: from, MergeBaseSHA: from, Files: []codehost.FileDiff{move.file}}
	}
	// What each member's merge adds to the new tip is the member's own file.
	for n := 1; n <= soakMergeCommits; n++ {
		h.CommitsOf[codehosttest.CommitRange{From: tip, To: fmt.Sprintf("%040d", n)}] = &codehost.Diff{BaseSHA: tip, MergeBaseSHA: tip,
			Files: []codehost.FileDiff{{Path: "a.go", Status: codehost.DiffModified, Patch: hunkOne}}}
	}
	s.tips = append(s.tips, tip)
	s.world.main = tip
	h.BranchAt[codehosttest.BranchKey{Repo: s.world.repo, Branch: "main"}] = tip
	for _, t := range s.tickets {
		if t.queued {
			t.behind = true
			s.publish(t)
		}
	}
}

// pass runs the runner until it is idle, moving the fake clock one minute
// whenever it is asleep in a poll.
func (s *soak) pass() {
	s.t.Helper()
	s.recovering = false
	if s.crashed {
		s.recoverLease()
	}
	if s.runner == nil {
		s.runner = s.newRunner()
	}
	r := s.runner
	for _, t := range s.tickets {
		t.refusedNow = false
	}
	crashes := s.crashes
	r.Kick(s.f.tenant)
	done := make(chan struct{})
	go func() { r.Wait(); close(done) }()
	// The landing's heartbeat is one waiter, its poll sleep the other.
	asleep := make(chan struct{})
	go func() {
		for {
			s.fake.BlockUntil(2)
			select {
			case asleep <- struct{}{}:
			case <-done:
				return
			}
		}
	}()
	for waiting := true; waiting; {
		select {
		case <-done:
			waiting = false
		case <-asleep:
			s.fake.Advance(time.Minute)
		}
	}
	if s.crashes != crashes {
		s.runner = nil
	}
	s.assertIdle()
}

// recoverLease is the lease of the dead process running out. Leases expire by
// the database's clock, which the fake clock does not move, so it is made to
// agree: once a lease's worth of fake time has passed since the crash.
func (s *soak) recoverLease() {
	if s.fake.Now().Sub(s.crashedAt) < 5*time.Minute {
		return
	}
	s.f.expireServerLease()
	require.NoError(s.t, s.f.reaper().Reap(context.Background()))
	// A restarted process reconciles what the dead one left.
	s.crashed, s.runner, s.recovering = false, nil, true
	s.trace("  the dead process's lease has run out")
}

// assertIdle checks what must hold once the runner has nothing more to do.
func (s *soak) assertIdle() {
	s.t.Helper()
	if s.crashed || s.runner == nil {
		// A crashed landing leaves its claim, its lock and its batch to the
		// recovery that follows the lease.
		return
	}
	// A landing that died holding the lock keeps the server's claim pinned
	// until its own row is claimed again: while that claim is refused, every
	// other ticket waits behind it.
	pinned := false
	// Settling a batch left by a dead process needs its member's claim, which a
	// refusal, or the dead lease still pinned on another ticket, delays to the
	// next pass.
	refused := s.recovering || slices.ContainsFunc(s.tickets, func(t *soakTicket) bool { return t.refusedNow })
	if h := s.f.lockHolder(); h != nil {
		pinned = s.byID[h.TicketID] != nil && s.byID[h.TicketID].refusedNow
		if !pinned {
			s.violate("the integration lock is held by ticket %d with the runner idle", h.TicketID)
		}
	}
	rows, err := s.f.queue.Ordered(s.f.ctx)
	require.NoError(s.t, err)
	for _, r := range rows {
		if r.State != landing.Landing || pinned || (refused && r.BatchID != nil) {
			continue
		}
		if t := s.byID[r.TicketID]; !t.refusedNow {
			s.violate("ticket %d is still queued after the runner went idle with nothing to stop it landing: %+v; %s", r.TicketID, r, s.dbState())
		}
	}
	if refused {
		return
	}
	live, err := s.store.Live(s.f.ctx)
	require.NoError(s.t, err)
	if len(live) > 0 {
		s.violate("batch %d is live with the runner idle", live[0].ID)
	}
	// Rows handed back keep a stale reservation today, so only the rows
	// still in the queue are counted.
	var reserved int
	require.NoError(s.t, s.f.database.QueryRow(s.f.ctx, `SELECT count(*) FROM landings WHERE batch_id IS NOT NULL AND queue_state <> 'back_with_agent'`).Scan(&reserved))
	if reserved > 0 {
		s.violate("%d landings rows are reserved by a batch with the runner idle: %s", reserved, s.reservations())
	}
}

// drain lets everything outstanding settle: claims recover, the dead
// process's lease runs out, and every prepared ticket is queued.
func (s *soak) drain() {
	s.t.Helper()
	s.trace("drain")
	s.events, s.crashAt = map[int]func(context.Context){}, 0
	for _, t := range s.tickets {
		t.refused = false
	}
	for len(s.prepared) > 0 {
		s.enqueueNext()
	}
	for range 10 {
		if s.crashed {
			s.fake.Advance(5 * time.Minute)
		}
		s.pass()
		s.checkInvariants("in the drain")
		if s.failed {
			return
		}
		if s.queuedCount() == 0 && !s.crashed {
			return
		}
	}
	s.violate("%d tickets are still queued after the drain", s.queuedCount())
}

// assertFinal checks that nothing was lost: every ticket queued ended merged
// or handed back, once (a voided batch's members go back to the queue).
func (s *soak) assertFinal() {
	s.t.Helper()
	if s.failed {
		return
	}
	recs, err := s.f.outcomes.List(s.f.ctx, time.Now().Add(-24*time.Hour), time.Now().Add(24*time.Hour), 100000)
	require.NoError(s.t, err)
	got := map[int64][]landing.Outcome{}
	for _, r := range recs {
		got[r.TicketID] = append(got[r.TicketID], r.Outcome)
	}
	merged := 0
	for _, t := range s.tickets {
		row := s.f.row(t.ticketID)
		wins := 0
		for _, o := range got[t.ticketID] {
			if o == landing.Merged || o == landing.AlreadyMerged {
				wins++
			}
		}
		switch {
		case !t.queued:
			s.violate("ticket %d was never queued", t.ticketID)
		case t.merges > 0 && (wins != 1 || row != nil):
			s.violate("ticket %d merged but has %d merged outcomes and row %+v", t.ticketID, wins, row)
		case t.merges == 0 && (wins != 0 || row == nil || row.State != landing.BackWithAgent || len(got[t.ticketID]) == 0):
			s.violate("ticket %d (%s) was neither merged nor handed back with an outcome: outcomes %v, row %+v", t.ticketID, t.fate, got[t.ticketID], row)
		}
		if t.merges > 0 {
			merged++
		}
	}
	// Handing everything back would otherwise satisfy every check above. Which
	// green tickets land depends on conflicts with main, so only a floor is asserted.
	if greens := s.count(fateGreen); greens > 0 && merged == 0 {
		s.violate("%d tickets had green CI and none merged", greens)
	}
	if n := len(s.f.host.Merges()); n != merged {
		s.violate("the host merged %d changes, the model %d", n, merged)
	}
	s.t.Logf("soak: %d tickets, %d merged, %d landings, %d CI reads", len(s.tickets), merged, s.landings, s.reads)
}

func (s *soak) dbState() string {
	var out []string
	rows, err := s.f.database.Query(s.f.ctx, `SELECT ticket_id, attempt_number, expires_at > now(), released_at IS NOT NULL FROM leases ORDER BY id DESC LIMIT 4`)
	require.NoError(s.t, err)
	defer rows.Close()
	for rows.Next() {
		var id int64
		var n int
		var live, rel bool
		require.NoError(s.t, rows.Scan(&id, &n, &live, &rel))
		out = append(out, fmt.Sprintf("lease #%d.%d unexpired=%v released=%v", id, n, live, rel))
	}
	if h := s.f.lockHolder(); h != nil {
		out = append(out, fmt.Sprintf("lock held by #%d", h.TicketID))
	}
	return strings.Join(out, "; ")
}

func (s *soak) reservations() string {
	rows, err := s.f.database.Query(s.f.ctx, `SELECT ticket_id, queue_state, batch_id, batch_position FROM landings WHERE batch_id IS NOT NULL AND queue_state <> 'back_with_agent'`)
	require.NoError(s.t, err)
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id, batch int64
		var pos int
		var state string
		require.NoError(s.t, rows.Scan(&id, &state, &batch, &pos))
		var bs string
		_ = s.f.database.QueryRow(s.f.ctx, `SELECT state FROM batches WHERE id = $1`, batch).Scan(&bs)
		out = append(out, fmt.Sprintf("#%d %s in batch %d (%s) at %d", id, state, batch, bs, pos))
	}
	return strings.Join(out, "; ")
}

func (s *soak) count(f soakFate) int {
	n := 0
	for _, t := range s.tickets {
		if t.fate == f {
			n++
		}
	}
	return n
}

func (s *soak) debugTicket(id int64) string {
	var st, status string
	var batch *int64
	_ = s.f.database.QueryRow(s.f.ctx, `SELECT coalesce((SELECT queue_state FROM landings WHERE ticket_id = $1), 'none'), coalesce((SELECT status FROM attempts WHERE ticket_id = $1 ORDER BY number DESC LIMIT 1), ''), (SELECT batch_id FROM landings WHERE ticket_id = $1)`, id).Scan(&st, &status, &batch)
	return fmt.Sprintf("row %s batch %v, attempt %s", st, batch, status)
}
