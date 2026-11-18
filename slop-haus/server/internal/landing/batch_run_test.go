package landing_test

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-clock"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/change"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost/codehosttest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/landing"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

// batchWorld is the code host as a batch sees it: the batch host fake, with the
// state the gate reads from the code host kept in step with what the runner
// does to it. It decides nothing about CI or the gate.
type batchWorld struct {
	*batchHostFake
	f    *fixture
	repo codehost.Repo
	// main is main's tip.
	main   string
	mergeN int
	// evidenceRefused makes the gate refuse every batch merge.
	evidenceRefused bool
	merges          []int64
	onBatch         func(branch string)
	// atBatch, when set, runs on each read of the batch's checks, with the reader's context.
	atBatch func(ctx context.Context)
	// checks answers the batch branch's checks; nil is green.
	checks func(head string) *codehost.Checks
	// otherTree makes the nth merge (from 1) have a tree the batch did not test.
	otherTree int
	alerts    []int64
	// asked is the head each batch merge was asked to merge on, by ticket, whether or not the gate took it.
	asked map[int64]string
	// afterMerge, when set, runs once the nth merge (from 1) is on the host, before the gate answers.
	afterMerge func(ctx context.Context, n int)
	// reads counts the reads of the batch's checks.
	reads atomic.Int64
	// nextRef is the reference the code host fake gives the change being opened.
	nextRef    codehost.ChangeRef
	hostChange map[string]codehost.ChangeRef
	// batchHeads are the commits that have been a batch branch's head.
	batchHeads map[string]bool
}

func newBatchWorld(f *fixture) *batchWorld {
	w := &batchWorld{batchHostFake: newBatchHost(), f: f, repo: f.ref(1).InRepo(), main: baseOld}
	w.hostChange = map[string]codehost.ChangeRef{}
	w.batchHeads = map[string]bool{}
	f.host.CI = codehosttest.NewCI()
	// Only the batch branch's checks are the test's to set; the members' own
	// changes keep what the fixture gave them.
	f.host.CI.Script = func(_ codehost.Repo, sha string) ([]codehost.Check, bool) {
		w.mu.Lock()
		isBatch := w.batchHeads[sha]
		w.mu.Unlock()
		if !isBatch {
			return nil, false
		}
		if w.checks == nil {
			return codehosttest.DefaultChecks(), true
		}
		return w.checks(sha).Checks, true
	}
	f.host.OpenedFor = func(req *codehost.OpenRequest) codehost.ChangeRef {
		w.mu.Lock()
		defer w.mu.Unlock()
		if strings.HasPrefix(req.Head, "slop-haus-batch-") {
			return w.nextRef
		}
		return f.host.Opened
	}
	f.host.BranchAt = map[codehosttest.BranchKey]string{{Repo: w.repo, Branch: "main"}: baseOld}
	f.host.OnBranch = map[codehosttest.BranchCommit]bool{}
	f.host.TreeAt = map[codehosttest.RefKey]string{}
	f.wrapChanges = func(inner landing.Changes) landing.Changes { return &batchChanges{Changes: inner, w: w} }
	return w
}

func (w *batchWorld) BaseTip(context.Context, *landing.LandingRow) (string, error) {
	return w.main, nil
}

func (w *batchWorld) CreateBranch(ctx context.Context, row *landing.LandingRow, branch, sha string) error {
	w.f.host.BranchAt[codehosttest.BranchKey{Repo: w.repo, Branch: branch}] = sha
	w.noteBatchHead(sha)
	return w.batchHostFake.CreateBranch(ctx, row, branch, sha)
}

func (w *batchWorld) noteBatchHead(sha string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.batchHeads[sha] = true
}

// UpdateMember is a member queued behind main: its branch contains main's tip
// only once the builder has merged it in. The head keeps its SHA, which the
// fixture's single-head change cannot tell from a new one.
func (w *batchWorld) UpdateMember(ctx context.Context, row *landing.LandingRow, head string) (*codehost.BranchUpdate, error) {
	upd, err := w.batchHostFake.UpdateMember(ctx, row, head)
	if err != nil {
		return nil, err
	}
	w.f.host.OnBranch[codehosttest.BranchCommit{Repo: w.repo, Branch: ticket.BranchName(row.TicketID, row.AttemptNumber), SHA: w.main}] = true
	return upd, nil
}

func (w *batchWorld) MergeHead(ctx context.Context, row *landing.LandingRow, branch, head string) (*codehost.BranchMerge, error) {
	m, err := w.batchHostFake.MergeHead(ctx, row, branch, head)
	if err != nil {
		return nil, err
	}
	h := w.f.host
	h.BranchAt[codehosttest.BranchKey{Repo: w.repo, Branch: branch}] = m.SHA
	w.noteBatchHead(m.SHA)
	h.OnBranch[codehosttest.BranchCommit{Repo: w.repo, Branch: branch, SHA: head}] = true
	h.TreeAt[codehosttest.RefKey{Repo: w.repo, Ref: m.SHA}] = "tree-" + m.SHA
	return m, nil
}

// OpenBatchChange opens the batch's change on the code host fake, which decides
// whether CI runs: a branch with no open change has no checks.
func (w *batchWorld) OpenBatchChange(ctx context.Context, row *landing.LandingRow, branch string) (codehost.ChangeRef, error) {
	ref, err := w.batchHostFake.OpenBatchChange(ctx, row, branch)
	if err != nil {
		return ref, err
	}
	w.mu.Lock()
	w.f.nextPR++
	hostRef := w.f.ref(w.f.nextPR)
	w.nextRef = hostRef
	w.mu.Unlock()
	if _, err := w.f.host.OpenChange(ctx, w.f.tenant, &codehost.OpenRequest{Repo: w.repo, Head: branch, Base: "main", Title: "batch", Draft: true}); err != nil {
		return ref, err
	}
	w.mu.Lock()
	w.hostChange[branch] = hostRef
	w.mu.Unlock()
	return ref, nil
}

func (w *batchWorld) CloseBatchChange(ctx context.Context, row *landing.LandingRow, ref codehost.ChangeRef) error {
	w.mu.Lock()
	hostRef, ok := w.hostChange[ref.Repo]
	w.mu.Unlock()
	if ok {
		if err := w.f.host.CloseChange(ctx, w.f.tenant, hostRef); err != nil {
			return err
		}
	}
	return w.batchHostFake.CloseBatchChange(ctx, row, ref)
}

func (w *batchWorld) BranchChecks(ctx context.Context, _ *landing.LandingRow, branch string) (*codehost.Checks, error) {
	w.reads.Add(1)
	if w.atBatch != nil {
		w.atBatch(ctx)
	}
	if w.onBatch != nil {
		w.onBatch(branch)
	}
	c, err := w.f.host.BranchChecks(ctx, w.f.tenant, w.repo, branch)
	if err != nil {
		return nil, err
	}
	c.Base = "main"
	return c, nil
}

// batchChanges is the gate, with the host's world moved on by each merge on
// batch evidence: main's tip becomes the merge commit, whose tree is the
// member's prefix commit's unless the test says otherwise.
type batchChanges struct {
	landing.Changes
	w *batchWorld
}

func (c *batchChanges) Merge(ctx context.Context, tenant, agent uuid.UUID, in *change.MergeInput) (*change.Merged, error) {
	if in.Batch == nil {
		return c.Changes.Merge(ctx, tenant, agent, in)
	}
	w := c.w
	if w.evidenceRefused {
		return nil, change.ErrBatchEvidence
	}
	n := w.mergeN + 1
	pos := 0
	for pos < len(in.Batch.Members) && in.Batch.Members[pos].MergeSHA != "" {
		pos++
	}
	sha := fmt.Sprintf("%040d", n)
	tree := "tree-" + in.Batch.Members[pos].PrefixSHA
	if w.otherTree == n {
		tree = "tree-other"
	}
	if w.asked == nil {
		w.asked = map[int64]string{}
	}
	w.asked[in.Batch.Members[pos].TicketID] = in.ExpectedSHA
	w.f.host.MergedWith = codehost.Merged{SHA: sha}
	w.f.host.TreeAt[codehosttest.RefKey{Repo: w.repo, Ref: sha}] = tree
	merged, err := c.Changes.Merge(ctx, tenant, agent, in)
	if err == nil {
		w.mergeN = n
		w.main = sha
		w.merges = append(w.merges, in.Batch.Members[pos].TicketID)
		w.f.host.BranchAt[codehosttest.BranchKey{Repo: w.repo, Branch: "main"}] = sha
		if w.afterMerge != nil {
			w.afterMerge(ctx, n)
		}
	}
	return merged, err
}

type batchRunFixture struct {
	*fixture
	world  *batchWorld
	store  *landing.BatchStore
	runner *landing.Runner
	size   int
	// cfg is the runner's configuration; zero takes the defaults.
	cfg landing.Config
	// told is what the members without a lease were told.
	told *membersFake
}

// newBatchRun queues one change per path, in order, on a runner that batches
// up to size.
func newBatchRun(t *testing.T, size int, paths ...string) (*batchRunFixture, []*finished) {
	t.Helper()
	return newBatchRunOn(t, newFixture(t), size, paths...)
}

func newBatchRunOn(t *testing.T, f *fixture, size int, paths ...string) (*batchRunFixture, []*finished) {
	t.Helper()
	w := newBatchWorld(f)
	b := &batchRunFixture{fixture: f, world: w, store: landing.NewBatchStore(f.database), size: size, told: &membersFake{}}
	var changes []*finished
	for _, p := range paths {
		c := f.finishedChangeAt(model.PriorityNormal, p)
		f.enqueue(c)
		changes = append(changes, c)
	}
	b.runner = b.newRunner()
	return b, changes
}

// newRunner is a runner wired to batch, as the server is after a restart.
func (b *batchRunFixture) newRunner() *landing.Runner {
	b.t.Helper()
	w := b.world
	r := b.fixture.runner(b.cfg)
	builder, err := landing.NewBatchBuilder(r, &landing.BatchOptions{Store: b.store, Host: w, Members: b.told})
	require.NoError(b.t, err)
	br, err := landing.NewBatchRunner(r, &landing.BatchRunnerOptions{
		Builder:  builder,
		Selector: landing.NewBatchSelector(&landing.PathPolicy{Apps: []string{"app-a/", "app-b/"}}, w),
		Queue:    b.queue, Checks: w, Recovery: landing.NewPostgresBatchRecovery(b.database),
		Size: func(context.Context) (int, error) { return b.size, nil },
		Alert: func(_ context.Context, id, _ int64, _ string) {
			w.alerts = append(w.alerts, id)
		},
	})
	require.NoError(b.t, err)
	r.UseBatches(br)
	return r
}

func ids(cs []*finished) []int64 {
	out := make([]int64, len(cs))
	for i, c := range cs {
		out[i] = c.ticketID
	}
	return out
}

func (b *batchRunFixture) mergedRefs() []int64 {
	var out []int64
	for _, m := range b.host.Merges() {
		out = append(out, m.Change.Number)
	}
	return out
}

func TestBatchRun_MergesInTestedOrderEachAsItsOwnPR(t *testing.T) {
	b, cs := newBatchRun(t, 3, "app-a/server/", "app-a/server/", "app-a/server/")

	b.land(b.runner)

	assert.Equal(t, ids(cs), b.world.merges, "merged in the order the batch tested")
	assert.Equal(t, []int64{1, 2, 3}, b.mergedRefs(), "each member is its own change merged on the host")
	for _, m := range b.host.Merges() {
		assert.Equal(t, headA, m.Merge.HeadSHA, "pinned to the head the batch tested")
	}
	require.Len(t, b.world.created, 1, "one batch branch")
	assert.Len(t, b.world.deleted, 1)
	assert.Contains(t, b.world.created[0], b.world.deleted[0], "and it is deleted once the members are merged")
	assert.Empty(t, b.pipes.retried(), "the runner asks CI for nothing beyond the batch's run: main's own pipeline follows the pushes")
	var batchID *int64
	for i, c := range cs {
		rec := b.outcomeOf(c.ticketID)
		assert.Equal(t, landing.Merged, rec.Outcome)
		assert.Equal(t, 3, rec.BatchSize)
		assert.Equal(t, i+1, rec.BatchPosition)
		require.NotNil(t, rec.BatchID)
		if batchID == nil {
			batchID = rec.BatchID
		}
		assert.Equal(t, *batchID, *rec.BatchID)
		assert.Nil(t, b.row(c.ticketID), "removed from the queue")
	}
	live, err := b.store.Live(b.ctx)
	require.NoError(t, err)
	assert.Empty(t, live, "the batch is done")
}

func TestBatchRun_OpensAChangeForTheBranchSoCIRunsAndRecordsThePipeline(t *testing.T) {
	b, cs := newBatchRun(t, 2, "app-a/server/", "app-a/server/")
	b.pipes.newest = func(head string) *landing.Pipeline {
		if head == headA {
			// A member's own head: its pipeline is not the batch's.
			return &landing.Pipeline{ID: 5, State: landing.PipelineSuccess}
		}
		return &landing.Pipeline{ID: 77, State: landing.PipelineSuccess}
	}

	b.land(b.runner)

	require.Len(t, b.world.created, 1)
	branch := strings.SplitN(b.world.created[0], "@", 2)[0]
	assert.Equal(t, []string{branch}, b.world.opened, "CI only runs on a change: the batch branch gets one")
	assert.Equal(t, []string{branch}, b.world.closed, "closed when the batch ends")
	rec := b.outcomeOf(cs[0].ticketID)
	require.NotNil(t, rec.BatchID)
	got, err := b.store.Get(b.ctx, *rec.BatchID)
	require.NoError(t, err)
	assert.Equal(t, "77", got.CIPipeline)
}

func TestBatchRun_RecordsThePipelineTheBatchHeadsChecksPointAtWhenTheCICannotNameOne(t *testing.T) {
	b, cs := newBatchRun(t, 2, "app-a/server/", "app-a/server/")
	b.world.checks = func(head string) *codehost.Checks {
		return codehost.NewChecks(head, "main", []codehost.Check{
			{Name: "ci/a", State: codehost.CheckSuccess, TargetURL: "https://ci.example/repos/1/pipeline/41/1"},
			{Name: "ci/b", State: codehost.CheckSuccess, TargetURL: "https://ci.example/repos/1/pipeline/42/2"},
		})
	}

	b.land(b.runner)

	rec := b.outcomeOf(cs[0].ticketID)
	require.NotNil(t, rec.BatchID)
	got, err := b.store.Get(b.ctx, *rec.BatchID)
	require.NoError(t, err)
	assert.Equal(t, "42", got.CIPipeline)
}

func TestBatchRun_ObserverSeesTheBatchSize(t *testing.T) {
	f := &recordingObserver{}
	b, _ := newBatchRunWith(t, 2, f, "app-a/server/", "app-a/server/")

	b.land(b.runner)

	require.Len(t, f.got, 2)
	assert.Equal(t, 2, f.got[0].batchSize)
}

func newBatchRunWith(t *testing.T, size int, obs landing.Observer, paths ...string) (*batchRunFixture, []*finished) {
	t.Helper()
	f := newFixture(t)
	f.observer = obs
	return newBatchRunOn(t, f, size, paths...)
}

func TestBatchRun_BatchesPerAppOnly(t *testing.T) {
	b, cs := newBatchRun(t, 3, "app-a/server/", "app-b/server/")

	b.land(b.runner)

	assert.Empty(t, b.world.created, "two apps share no batch: each lands on its own")
	assert.Equal(t, []int64{1, 2}, b.mergedRefs())
	for _, c := range cs {
		rec := b.outcomeOf(c.ticketID)
		assert.Equal(t, landing.Merged, rec.Outcome)
		assert.Nil(t, rec.BatchID)
	}
}

func TestBatchRun_SizeOneRunsNoBatchCode(t *testing.T) {
	b, _ := newBatchRun(t, 1, "app-a/server/", "app-a/server/")

	b.land(b.runner)

	assert.Empty(t, b.world.created)
	assert.Equal(t, []int64{1, 2}, b.mergedRefs())
}

func TestBatchRun_AnUrgentTicketNeverCancelsARunningBatchAndJoinsTheNextAheadOfItsQueueMates(t *testing.T) {
	b, cs := newBatchRun(t, 2, "app-a/server/", "app-a/server/", "app-a/server/")
	urgent := b.finishedChangeAt(model.PriorityUrgent, "app-a/server/")
	queued := false
	b.world.onBatch = func(string) {
		if !queued {
			queued = true
			b.enqueue(urgent)
		}
	}

	b.land(b.runner)

	require.Len(t, b.world.created, 2, "the running batch finished and the urgent ticket was in the next")
	assert.Equal(t, []int64{cs[0].ticketID, cs[1].ticketID, urgent.ticketID, cs[2].ticketID}, b.world.merges,
		"the running batch merges whole; the urgent ticket is ahead of the one queued before it")
	assert.Equal(t, []int64{1, 2, urgent.ref.Number, 3}, b.mergedRefs())
}

func TestBatchRun_ATreeTheBatchDidNotTestStopsTheBatchAndRequeuesTheRest(t *testing.T) {
	b, cs := newBatchRun(t, 3, "app-a/server/", "app-a/server/", "app-a/server/")
	b.world.otherTree = 1

	b.land(b.runner)

	assert.Equal(t, []int64{cs[0].ticketID}, b.world.alerts, "alerted on the member merged with the other tree")
	assert.Equal(t, landing.Merged, b.outcomeOf(cs[0].ticketID).Outcome)
	var voided []int64
	for _, r := range b.outcomesNow() {
		if r.Outcome == landing.BatchVoided {
			voided = append(voided, r.TicketID)
		}
	}
	assert.ElementsMatch(t, ids(cs[1:]), voided, "the rest were requeued with their place")
	require.Len(t, b.world.created, 2, "and tested again as a batch of the new tip")
	assert.Equal(t, ids(cs), b.world.merges)
	assert.Empty(t, b.world.alerts[1:])
}

func TestBatchRun_TheKillSwitchHandsEveryMemberBackAsMergeRefused(t *testing.T) {
	b, cs := newBatchRun(t, 2, "app-a/server/", "app-a/server/")
	b.world.onBatch = func(string) { b.on.Store(false) }

	b.land(b.runner)

	assert.Empty(t, b.host.Merges(), "the step in hand finished: nothing merges after the switch")
	for _, c := range cs {
		assert.Equal(t, landing.MergeRefused, b.outcomeOf(c.ticketID).Outcome)
	}
}

// landStuck lands with the fake clock moving by step once per read of the
// batch's checks. The clock cannot just be advanced whenever the runner
// waits: the heartbeat's ticker always is, so it would run ahead of the poll
// and past the bound. The tiny advance only wakes the poll's sleep.
func landStuck(b *batchRunFixture, fake *clock.Fake, step time.Duration) {
	b.runner.Kick(b.tenant)
	done := make(chan struct{})
	go func() { b.runner.Wait(); close(done) }()
	var stepped int64
	for {
		select {
		case <-done:
			return
		default:
		}
		if fake.Waiters() > 0 {
			if n := b.world.reads.Load(); n > stepped {
				stepped = n
				fake.Advance(step)
			}
			fake.Advance(time.Millisecond)
		}
		runtime.Gosched()
	}
}

// stuckBatch is a batch whose only check the host keeps showing pending, as
// when the CI's final status never reached it.
func stuckBatch(t *testing.T, verdict landing.CheckVerdict) (*batchRunFixture, []*finished, *clock.Fake) {
	t.Helper()
	f := newFixture(t)
	fake := clock.NewFake(time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC))
	f.clock = fake
	f.pipes.timeout = 30 * time.Minute
	f.pipes.verdicts = map[string]landing.CheckVerdict{stuckURL: verdict}
	b, cs := newBatchRunOn(t, f, 2, "app-a/server/", "app-a/server/")
	b.world.checks = func(head string) *codehost.Checks {
		return codehost.NewChecks(head, "main", []codehost.Check{{Name: "ci/build", State: codehost.CheckPending, TargetURL: stuckURL}})
	}
	return b, cs, fake
}

func TestBatchRun_ACheckPendingOnTheHostThatTheCISaysPassedMergesTheBatch(t *testing.T) {
	b, cs, fake := stuckBatch(t, landing.CIPassed)
	start := fake.Now()

	landStuck(b, fake, 5*time.Minute)

	assert.Equal(t, ids(cs), b.world.merges, "merged on the batch's run")
	assert.GreaterOrEqual(t, fake.Now().Sub(start), 10*time.Minute, "the CI is not asked before the short bound")
}

func TestBatchRun_ACheckPendingOnTheHostThatTheCISaysFailedIsRed(t *testing.T) {
	b, _, fake := stuckBatch(t, landing.CIFailed)

	landStuck(b, fake, 5*time.Minute)

	assert.Empty(t, b.world.merges, "the batch is red, so no member merges on its run")
	assert.Contains(t, b.pipes.askedAbout(), stuckURL)
}

func TestBatchRun_ACheckThatTheCISaysIsStillRunningKeepsWaiting(t *testing.T) {
	b, cs, fake := stuckBatch(t, landing.CIRunning)

	landStuck(b, fake, 5*time.Minute)

	assert.Empty(t, b.world.merges)
	for _, c := range cs {
		assert.Equal(t, landing.ChecksPendingTimeout, b.outcomeOf(c.ticketID).Outcome)
	}
	assert.Contains(t, b.pipes.askedAbout(), stuckURL)
}

// CI runs only for a commit with an open change, so a batch whose change was
// never opened sees no checks at all, as one once did -- a run to the end proves
// the batch asked for CI before it waited for it.
func TestBatchRun_TheBatchBranchGetsChecksOnlyFromItsOpenChange(t *testing.T) {
	b, cs := newBatchRun(t, 2, "app-a/server/", "app-a/server/")
	b.cfg.NeverStartedWait = 50 * time.Millisecond

	b.land(b.newRunner())

	assert.Equal(t, ids(cs), b.world.merges, "both members merged on the batch's CI")
	assert.Len(t, b.world.batchHostFake.opened, 1, "the batch's change was opened, which is what starts CI")
}

func TestBatchRun_CountsOneBatchPerRunByHowItEnded(t *testing.T) {
	t.Run("green", func(t *testing.T) {
		obs := &recordingObserver{}
		b, _ := newBatchRunWith(t, 2, obs, "app-a/server/", "app-a/server/")
		b.land(b.runner)
		assert.Equal(t, []string{"green"}, obs.batches)
	})
	t.Run("a red batch whose members then merge alone is one red batch", func(t *testing.T) {
		obs := &recordingObserver{}
		b, _ := newBatchRunWith(t, 2, obs, "app-a/server/", "app-a/server/")
		b.redBatch()
		b.land(b.runner)
		assert.Equal(t, []string{"red"}, obs.batches)
	})
	t.Run("a batch the gate refuses is evidence_refused, not red", func(t *testing.T) {
		obs := &recordingObserver{}
		b, _ := newBatchRunWith(t, 2, obs, "app-a/server/", "app-a/server/")
		b.world.evidenceRefused = true
		b.land(b.runner)
		require.NotEmpty(t, obs.batches)
		assert.Equal(t, "evidence_refused", obs.batches[0])
		assert.NotContains(t, obs.batches, "red")
	})
	t.Run("a handback of the whole batch is failed", func(t *testing.T) {
		obs := &recordingObserver{}
		b, _ := newBatchRunWith(t, 2, obs, "app-a/server/", "app-a/server/")
		b.world.onBatch = func(string) { b.on.Store(false) }
		b.land(b.runner)
		assert.Equal(t, []string{"failed"}, obs.batches)
	})
	t.Run("main moving voids it", func(t *testing.T) {
		obs := &recordingObserver{}
		b, _ := newBatchRunWith(t, 2, obs, "app-a/server/", "app-a/server/")
		b.world.onBatch = func(string) {
			b.world.main = "moved-on"
			b.host.BranchAt[codehosttest.BranchKey{Repo: b.world.repo, Branch: "main"}] = b.world.main
		}
		b.land(b.runner)
		require.NotEmpty(t, obs.batches)
		assert.Equal(t, "voided", obs.batches[0])
	})
}

func TestBatchRun_ARunTheServerStoppedIsCountedOnceWhenRecoveryEndsIt(t *testing.T) {
	obs := &recordingObserver{}
	b, _ := newBatchRunWith(t, 3, obs, "app-a/server/", "app-a/server/", "app-a/server/")
	b.crashAfterMerge(1)
	assert.Empty(t, obs.batches, "a stopped run has not ended the batch")

	b.land(b.newRunner())

	assert.Equal(t, []string{"green"}, obs.batches)
}

func TestBatchRun_ARunnerThatLostTheBatchsLeaseNeverLandsAMemberOutsideTheBatch(t *testing.T) {
	b, cs := newBatchRun(t, 2, "app-a/server/", "app-a/server/")
	b.cfg = landing.Config{HeartbeatInterval: time.Millisecond}
	b.runner = b.newRunner()
	var once sync.Once
	b.world.atBatch = func(ctx context.Context) {
		once.Do(func() {
			b.revokeServerLease()
			<-ctx.Done()
		})
	}

	// The run abandons the batch, still testing, and the same loop looks at the
	// queue again: the members' own checks are green.
	b.land(b.runner)

	assert.Empty(t, b.host.Merges(), "no member is landed outside the batch while the batch is live")
	live, err := b.store.Live(b.ctx)
	require.NoError(t, err)
	require.Len(t, live, 1, "the batch is left for recovery")

	b.world.atBatch = nil
	b.ageBatches()
	b.land(b.newRunner())

	assert.Equal(t, ids(cs), b.world.merges, "recovery merges both through the batch, in order")
	assert.Len(t, b.host.Merges(), 2)
}

func TestBatchRun_ABatchRecordsEveryMemberItWasBuiltWithEvenWhenVoided(t *testing.T) {
	b, cs := newBatchRun(t, 2, "app-a/server/", "app-a/server/")
	b.world.onBatch = func(string) { b.on.Store(false) }

	b.land(b.runner)

	recent, err := b.store.Recent(b.ctx, 10, nil)
	require.NoError(t, err)
	require.Len(t, recent, 1)
	assert.Equal(t, ids(cs), recent[0].Members, "both members, in tested order")
	assert.Len(t, recent[0].Heads, 2, "each with its tested head")
}

func TestBatchRun_AKnownFlakyRetryOfTheBatchRunIsRecorded(t *testing.T) {
	for _, tc := range []struct {
		name    string
		rescued bool
		want    string
	}{{"rescued", true, landing.FlakyRescued}, {"confirmed", false, landing.FlakyConfirmed}} {
		t.Run(tc.name, func(t *testing.T) {
			obs := &flakyObserver{}
			f := newFixture(t)
			f.observer = obs
			b, _ := newBatchRunOn(t, f, 2, "app-a/server/", "app-a/server/")
			b.cfg.KnownFlaky = []string{"build:1566:2999-01-01"}
			b.runner = b.newRunner()
			reads := 0
			b.world.checks = func(head string) *codehost.Checks {
				reads++
				if reads > 1 && tc.rescued {
					return codehost.NewChecks(head, "main", []codehost.Check{green})
				}
				return codehost.NewChecks(head, "main", []codehost.Check{red})
			}

			b.land(b.runner)

			require.NotEmpty(t, obs.retries)
			assert.Equal(t, flakyRetry{"build", tc.want, false}, obs.retries[0])
		})
	}
}
