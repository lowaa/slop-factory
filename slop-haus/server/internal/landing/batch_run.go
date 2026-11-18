package landing

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/change"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/landing/guidance"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// BatchQueue lists the rows a batch can be formed from.
type BatchQueue interface {
	Candidates(ctx context.Context) ([]Candidate, error)
}

// BatchChecks reads the checks CI reports for a batch branch's head.
type BatchChecks interface {
	BranchChecks(ctx context.Context, row *LandingRow, branch string) (*codehost.Checks, error)
}

// BatchRunnerOptions are what a BatchRunner is built from.
type BatchRunnerOptions struct {
	Builder  *BatchBuilder
	Selector *BatchSelector
	Queue    BatchQueue
	Checks   BatchChecks
	// Recovery reads what a restarted runner cannot take from memory.
	Recovery BatchRecovery
	// Size is the tenant's merge_queue_batch_size. A batch is run only above 1.
	Size func(ctx context.Context) (int, error)
	// Alert is told of a batch stopped because a member's merge on the host
	// did not have the tree the batch tested: that member is on the base. Nil
	// only logs it.
	Alert func(ctx context.Context, ticketID, batchID int64, why string)
}

// BatchRunner lands several queued rows from one CI run on a batch branch,
// merging the members one by one, in the tested order, each as its own change
// through the gate with the batch's run as the evidence.
//
// A red batch (after the flaky retry) is split: its members are landed one at a
// time, in order, through the serial path, and the culprit is handed back.
// Reconcile finishes or voids a batch a stopped runner left behind.
type BatchRunner struct {
	r *Runner
	o BatchRunnerOptions

	mu    sync.Mutex
	swept map[uuid.UUID]bool
}

func NewBatchRunner(r *Runner, o *BatchRunnerOptions) (*BatchRunner, error) {
	if r == nil || o.Builder == nil || o.Selector == nil || o.Queue == nil || o.Checks == nil || o.Recovery == nil || o.Size == nil {
		return nil, errors.New("landing: a batch runner needs a Runner, Builder, Selector, Queue, Checks, Recovery and Size")
	}
	return &BatchRunner{r: r, o: *o, swept: map[uuid.UUID]bool{}}, nil
}

// UseBatches makes the runner land in batches while the batch size is above 1.
// It is called before the first Kick.
func (r *Runner) UseBatches(b *BatchRunner) { r.batch = b }

type batchResult int

const (
	// batchNone: no batch was formed, so the head is landed on its own.
	batchNone batchResult = iota
	// batchRan: a batch was formed and run, whatever came of it.
	batchRan
	// batchStuck: a batch could not be formed from the head; the caller moves
	// on to the rest of the queue.
	batchStuck
)

// Run forms a batch from the queue, minus skip, and runs it.
func (b *BatchRunner) Run(ctx context.Context, tenant uuid.UUID, skip []int64) batchResult {
	r := b.r
	size, err := b.o.Size(ctx)
	if err != nil {
		r.log.ErrorContext(ctx, "landing: read the batch size", "err", err)
		return batchNone
	}
	if size <= 1 {
		return batchNone
	}
	all, err := b.o.Queue.Candidates(ctx)
	if err != nil {
		r.log.ErrorContext(ctx, "landing: read the batch candidates", "err", err)
		return batchNone
	}
	cands := slices.DeleteFunc(all, func(c Candidate) bool { return slices.Contains(skip, c.Row.TicketID) })
	picked, err := b.o.Selector.Select(ctx, cands, size)
	if err != nil {
		r.log.ErrorContext(ctx, "landing: select a batch", "err", err)
		return batchNone
	}
	if len(picked) < 2 {
		// A batch of one is the serial landing.
		return batchNone
	}
	built, err := b.o.Builder.Build(ctx, picked)
	if err != nil {
		r.log.ErrorContext(ctx, "landing: build a batch", "err", err)
		return batchStuck
	}
	if built == nil {
		return batchStuck
	}
	(&batchRun{b: b, r: r, tenant: tenant, built: built}).run(ctx)
	return batchRan
}

// batchRun is one built batch being tested and merged.
type batchRun struct {
	b      *BatchRunner
	r      *Runner
	tenant uuid.UUID
	built  *Built
	ev     *change.BatchEvidence
	// leased is the member the server's lease is on, -1 when none.
	leased int
	held   context.Context
	stop   func()
	// pipelineSet is whether the batch's CI pipeline is recorded.
	pipelineSet bool
	// result is how the batch ended, set by the first step that decides it so a
	// red batch whose members then void or merge alone stays red.
	result string
}

// Batch results, as counted by the batch series.
const (
	batchGreen           = "green"
	batchRed             = "red"
	batchVoidedResult    = "voided"
	batchEvidenceRefused = "evidence_refused"
	batchFailed          = "failed"
)

// ended records how the batch ended, unless something already did.
func (br *batchRun) ended(result string) {
	if br.result == "" {
		br.result = result
	}
}

// observe tells the Observer how the batch ended. A batch a stopped runner
// left (no result) is Reconcile's, and is not counted.
func (br *batchRun) observe() {
	if br.result != "" && br.r.o.Observer != nil {
		br.r.o.Observer.LandingBatch(br.result)
	}
}

func (br *batchRun) run(ctx context.Context) {
	r, built := br.r, br.built
	defer func() { br.stop() }()
	defer br.observe()
	br.leased, br.held, br.stop = 0, built.Held, built.Close
	ids := make([]string, len(built.Members))
	for i, m := range built.Members {
		ids[i] = fmt.Sprintf("#%d", m.Row.TicketID)
	}
	r.log.InfoContext(ctx, "landing: batch built", "batch", built.Batch.ID, "members", strings.Join(ids, ","))

	if fail := r.stillOn(br.held); fail != nil {
		br.handBackFrom(ctx, 0, fail)
		return
	}
	if fail := r.awaitLock(br.held, built.Server); fail != nil {
		br.handBackFrom(ctx, 0, fail)
		return
	}
	if err := br.b.o.Builder.o.Store.SetTested(ctx, built.Batch.ID, prefixesOf(built.Members), headsOf(built.Members)); err != nil {
		br.fault(ctx, "record the batch's commits", err)
		return
	}
	if err := br.b.o.Builder.o.Store.SetState(ctx, built.Batch.ID, BatchTesting); err != nil {
		br.fault(ctx, "set the batch testing", err)
		return
	}
	br.awaitAndMerge(ctx)
}

// awaitAndMerge waits for CI on the batch branch's head and merges the members
// if it is green. The leased member is the first and the lock is held.
func (br *batchRun) awaitAndMerge(ctx context.Context) {
	built := br.built
	last := built.Members[len(built.Members)-1].PrefixSHA
	checks, ciPassed, fail := br.awaitCI(ctx, last)
	if fail != nil {
		if fail.outcome == ChecksRed && ctx.Err() == nil && br.held.Err() == nil {
			br.ended(batchRed)
			br.splitRed(ctx)
			return
		}
		br.handBackFrom(ctx, 0, fail)
		return
	}
	// Whether main moving while the batch was tested matters is the gate's
	// call, member by member, with the drift rules of a serial merge: moves in
	// other independent apps or deploy-only paths are allowed, the rest void.
	br.ev = evidenceOf(built, checks)
	br.ev.CIPassed = ciPassed
	if err := br.b.o.Builder.o.Store.SetState(ctx, built.Batch.ID, BatchMerging); err != nil {
		br.fault(ctx, "set the batch merging", err)
		return
	}
	br.mergeAll(ctx, 0)
}

// awaitCI waits for the checks of the batch branch's head, with the serial
// landing's wait: the newest pipeline is followed and a known flake is
// retried once. ciPassed are the checks the host showed pending that the CI
// said had passed, which the gate is told of.
func (br *batchRun) awaitCI(ctx context.Context, head string) (checks *codehost.Checks, ciPassed []change.CheckLine, _ *failure) {
	r, built := br.r, br.built
	holder := built.holder
	var latest *codehost.Checks
	l := &landing{r: r, tenant: br.tenant, server: built.Server, row: holder, rec: &Record{}}
	l.read = func(ctx context.Context) (*change.Report, *failure) {
		var c *codehost.Checks
		err := r.hostCall(ctx, func() (err error) {
			c, err = br.b.o.Checks.BranchChecks(ctx, holder, built.Batch.Branch)
			return err
		})
		if err != nil {
			return nil, r.classify(ctx, err)
		}
		latest = c
		br.recordPipeline(ctx, c)
		rep := &change.Report{URL: holder.ChangeURL, HeadSHA: c.HeadSHA, State: c.State.String(), Passed: c.Passed(), Base: c.Base}
		for _, k := range c.Checks {
			rep.Checks = append(rep.Checks, change.CheckLine{Name: k.Name, State: k.State.String(), TargetURL: k.TargetURL})
		}
		return rep, nil
	}
	if err := r.o.Queue.SetStep(br.held, holder.TicketID, WaitingChecks); err != nil {
		return nil, nil, r.classify(br.held, err)
	}
	if _, fail := l.awaitChecks(br.held, head, "", nil); fail != nil {
		return nil, nil, fail
	}
	return latest, l.ciPassed(), nil
}

// recordPipeline notes the CI pipeline testing the batch's head, once one
// exists, so that a batch CI never ran for is told apart from one it did. The
// CI's own list is asked first; a head it cannot name a pipeline for (the
// list failed, or a status it has no word for) falls back on the pipeline the
// head's checks point at.
func (br *batchRun) recordPipeline(ctx context.Context, c *codehost.Checks) {
	if br.pipelineSet {
		return
	}
	var id string
	if p, _ := br.r.pipeline(ctx, c.HeadSHA); p != nil {
		id = strconv.FormatInt(p.ID, 10)
	} else if id = newestPipelineIn(c.Checks); id == "" {
		return
	}
	if err := br.b.o.Builder.o.Store.SetPipeline(ctx, br.built.Batch.ID, id); err != nil {
		br.r.log.WarnContext(ctx, "landing: record the batch's pipeline", "batch", br.built.Batch.ID, "err", err)
		return
	}
	br.pipelineSet = true
}

// newestPipelineIn is the highest pipeline number the checks' target URLs name.
func newestPipelineIn(checks []codehost.Check) string {
	var best int64
	for _, k := range checks {
		if m := pipelineInTarget.FindStringSubmatch(k.TargetURL); m != nil {
			if n, err := strconv.ParseInt(m[1], 10, 64); err == nil {
				best = max(best, n)
			}
		}
	}
	if best == 0 {
		return ""
	}
	return strconv.FormatInt(best, 10)
}

// evidenceOf is the batch's run as the gate takes it. A member that already
// merged carries its MergeSHA, which the caller sets.
func evidenceOf(built *Built, checks *codehost.Checks) *change.BatchEvidence {
	ev := &change.BatchEvidence{ID: built.Batch.ID, Branch: built.Batch.Branch, BaseSHA: built.Batch.BaseSHA, Checks: checks}
	for _, m := range built.Members {
		ev.Members = append(ev.Members, change.BatchMember{TicketID: m.Row.TicketID, HeadSHA: m.Row.HeadSHA, PrefixSHA: m.PrefixSHA})
	}
	return ev
}

func headsOf(ms []BuiltMember) []string {
	out := make([]string, len(ms))
	for i := range ms {
		out[i] = ms[i].Row.HeadSHA
	}
	return out
}

func prefixesOf(ms []BuiltMember) []string {
	out := make([]string, len(ms))
	for i := range ms {
		out[i] = ms[i].PrefixSHA
	}
	return out
}

// mergeAll merges the members from index from on, in order; the member at from
// is already leased and the lock held. A member that fails is handed back and
// the members behind it are requeued, since the batch's run no longer covers
// them.
func (br *batchRun) mergeAll(ctx context.Context, from int) {
	r, built := br.r, br.built
	for i := from; i < len(built.Members); i++ {
		if i > from && !br.take(ctx, i) {
			return
		}
		m := &built.Members[i]
		if fail := r.stillOn(br.held); fail != nil {
			br.handBackFrom(ctx, i, fail)
			return
		}
		rec := br.rec(i)
		gate, err := r.o.Tickets.ChangeGateFor(br.held, built.Server)
		if err != nil {
			br.failMember(ctx, i, r.classify(br.held, err))
			return
		}
		rec.ApproverUserID, rec.ApprovedAt = gate.Review.ApprovedByUserID, gate.Review.ApprovedAt
		if err := r.o.Queue.SetStep(br.held, m.Row.TicketID, Merging); err != nil {
			br.failMember(ctx, i, r.classify(br.held, err))
			return
		}
		merged, err := r.o.Changes.Merge(br.held, br.tenant, built.Server, &change.MergeInput{ExpectedSHA: m.Row.HeadSHA, Batch: br.ev})
		if err != nil {
			br.mergeFailed(ctx, i, err)
			return
		}
		br.ev.Members[i].MergeSHA = merged.MergeSHA
		rec.ChangeURL = merged.URL
		if !merged.Closed {
			if _, err := r.o.Tickets.Done(context.WithoutCancel(ctx), built.Server, &ticket.DoneParams{PRURL: merged.URL}); err != nil {
				// On the base, so not handed back: the next sweep closes it.
				r.log.ErrorContext(ctx, "landing: merged but could not close the ticket", "ticket", m.Row.TicketID, "err", err)
				br.voidFrom(ctx, i+1, "a member merged but could not be closed")
				return
			}
		}
		rec.Outcome = Merged
		r.finish(ctx, m.Row, rec)
		br.leased = -1
		if merged.BatchTree != nil && !merged.BatchTree.Matches {
			br.treeMismatch(ctx, i, merged.BatchTree.Why)
			return
		}
	}
	br.ended(batchGreen)
	if err := br.b.o.Builder.Done(ctx, built, ResultMerged, ""); err != nil {
		r.log.ErrorContext(ctx, "landing: finish the batch; its branch is left to be swept", "batch", built.Batch.ID, "err", err)
	}
}

// take leases and locks member i, whose predecessor's merge ended the lease
// and the lock. It is false when the batch cannot go on and has been ended.
func (br *batchRun) take(ctx context.Context, i int) bool {
	r, built := br.r, br.built
	br.stop()
	br.leased, br.held, br.stop = -1, ctx, func() {}
	if ctx.Err() != nil {
		return false
	}
	row := built.Members[i].Row
	claim, err := r.o.Tickets.ServerClaimLanding(ctx, built.Server, row.TicketID, row.AttemptNumber, r.cfg.LeaseTTL)
	switch {
	case err != nil && !isNotClaimable(err):
		r.log.ErrorContext(ctx, "landing: claim a batch member", "ticket", row.TicketID, "err", err)
		br.voidFrom(ctx, i, "a member could not be claimed")
		return false
	case err != nil, claim.Ticket == nil:
		br.voidFrom(ctx, i, "a member could not be claimed")
		return false
	}
	br.leased = i
	br.held, br.stop = r.heartbeat(ctx, built.Server, row.TicketID)
	if fail := r.awaitLock(br.held, built.Server); fail != nil {
		br.failMember(ctx, i, fail)
		return false
	}
	return true
}

// mergeFailed ends the batch on member i's refused or failed merge.
func (br *batchRun) mergeFailed(ctx context.Context, i int, err error) {
	r := br.r
	var refusal *change.BatchRefusal
	switch {
	case errors.Is(err, change.ErrAlreadyMerged), errors.Is(err, codehost.ErrAlreadyMerged):
		// Merged by someone else: its merge commit is unknown, so the batch's
		// evidence cannot go on. Closed as a restart closes it.
		row := br.built.Members[i].Row
		state, fail := r.reconcile(br.held, br.tenant, row)
		if fail == nil {
			state.Merged = true
			fail = r.closeMerged(br.held, br.built.Server, row, state)
		}
		if fail != nil && fail.outcome == AlreadyMerged {
			rec := br.rec(i)
			rec.Outcome, rec.Detail = fail.outcome, fail.detail
			r.finish(ctx, row, rec)
			br.leased = -1
			br.voidFrom(ctx, i+1, "a member was found merged outside the batch")
			return
		}
		br.failMember(ctx, i, fail)
	case errors.As(err, &refusal) && refusal.MainMoved():
		r.log.InfoContext(ctx, "landing: main moved while the batch was tested", "batch", br.built.Batch.ID, "ticket", br.built.Members[i].Row.TicketID, "err", err)
		br.ended(batchVoidedResult)
		br.failMember(ctx, i, batchVoidedFor("main moved while the batch was tested"))
	case errors.Is(err, change.ErrBatchEvidence):
		r.log.WarnContext(ctx, "landing: the gate refused the batch's evidence", "batch", br.built.Batch.ID, "ticket", br.built.Members[i].Row.TicketID, "err", err)
		br.ended(batchEvidenceRefused)
		br.failMember(ctx, i, batchVoided())
	default:
		br.failMember(ctx, i, r.classify(br.held, err))
	}
}

// failMember hands member i back for fail, if the run is still the lease's, and
// requeues the members behind it.
func (br *batchRun) failMember(ctx context.Context, i int, fail *failure) {
	if ctx.Err() != nil || br.held.Err() != nil {
		br.abandoned(ctx)
		return
	}
	if fail.mergedNotClosed {
		// On the base: the next sweep closes it. Not a failure of the member.
		br.r.log.ErrorContext(ctx, "landing: a batch member merged but the bookkeeping failed", "ticket", br.built.Members[i].Row.TicketID, "detail", fail.detail)
		br.voidFrom(ctx, i+1, "a member merged but could not be closed")
		return
	}
	if br.ev != nil {
		br.ev.Members[i].Dropped = true
	}
	if fail.outcome == MergeRefused && fail.detail == switchedOff().detail {
		br.handBackFrom(ctx, i, fail)
		return
	}
	br.handBackOne(ctx, i, fail)
	br.voidFrom(ctx, i+1, "a member ahead of it was handed back")
}

// treeMismatch stops the batch after member i merged with a tree the batch did
// not test. The member is on the base; the rest are requeued to be tested
// again on the new tip.
func (br *batchRun) treeMismatch(ctx context.Context, i int, why string) {
	built := br.built
	ticketID := built.Members[i].Row.TicketID
	br.r.log.ErrorContext(ctx, "landing: ALERT a batch member merged with a tree the batch did not test; the batch is stopped",
		"batch", built.Batch.ID, "ticket", ticketID, "why", why)
	if br.b.o.Alert != nil {
		br.b.o.Alert(context.WithoutCancel(ctx), ticketID, built.Batch.ID, why)
	}
	br.voidFrom(ctx, i+1, "a member merged with a tree the batch did not test")
}

func (br *batchRun) abandoned(ctx context.Context) {
	if ctx.Err() == nil {
		br.r.log.WarnContext(ctx, "landing: lost the lease during a batch", "cause", context.Cause(br.held))
	}
	// The lease and the rows are the recovery.
}

// fault ends the batch on a failure that is the server's: nothing is wrong with
// the members.
func (br *batchRun) fault(ctx context.Context, what string, err error) {
	br.r.log.ErrorContext(ctx, "landing: batch failed", "what", what, "err", err)
	if ctx.Err() != nil {
		return
	}
	br.handBackFrom(ctx, 0, hostError(what))
}

// handBackFrom hands back every member from i on for fail: the leased one by
// the lease, the others by a comment on their attempts.
func (br *batchRun) handBackFrom(ctx context.Context, from int, fail *failure) {
	if ctx.Err() != nil || (br.held != nil && br.held.Err() != nil) {
		br.abandoned(ctx)
		return
	}
	br.ended(batchFailed)
	for i := len(br.built.Members) - 1; i >= from; i-- {
		br.handBackOne(ctx, i, fail)
	}
	why := fail.detail
	if why == "" {
		why = string(fail.outcome)
	}
	br.end(ctx, "handed back: "+why)
}

// handBackOne hands back member i. Its reservation is cleared first; the batch
// keeps it in its record of members.
func (br *batchRun) handBackOne(ctx context.Context, i int, fail *failure) {
	r, built := br.r, br.built
	row := built.Members[i].Row
	f := *fail
	f.guidance = br.explain(i, fail.guidance)
	f.detail = batchDetail(built.Batch.ID, fail.detail)
	rec := br.rec(i)
	if err := br.b.o.Builder.o.Store.Release(context.WithoutCancel(ctx), built.Batch.ID, row.TicketID); err != nil {
		r.log.ErrorContext(ctx, "landing: release a batch member", "ticket", row.TicketID, "err", err)
		return
	}
	if i == br.leased {
		r.handBack(ctx, built.Server, row, &f, rec)
		br.leased = -1
		return
	}
	if err := br.b.o.Builder.handBackMember(ctx, built, row, &f, rec); err != nil {
		r.log.ErrorContext(ctx, "landing: hand back a batch member", "ticket", row.TicketID, "err", err)
	}
}

// voidFrom requeues the members from i on, which stay queued with their place,
// and ends the batch. The leased one, if it is among them, is handed back: a
// lease cannot be let go of without it.
func (br *batchRun) voidFrom(ctx context.Context, from int, why string) {
	if ctx.Err() != nil {
		return
	}
	br.ended(batchVoidedResult)
	for i := from; i < len(br.built.Members); i++ {
		if i == br.leased {
			br.handBackOne(ctx, i, batchVoidedFor(why))
			continue
		}
		rec := br.rec(i)
		rec.Outcome, rec.Detail = BatchVoided, fmt.Sprintf("batch %d: %s", br.built.Batch.ID, why)
		br.r.write(context.WithoutCancel(ctx), rec)
	}
	br.end(ctx, why)
}

// end deletes the branch and releases every member still reserved.
func (br *batchRun) end(ctx context.Context, why string) {
	if err := br.b.o.Builder.Void(ctx, br.built, why); err != nil {
		br.r.log.ErrorContext(ctx, "landing: end the batch; its branch is left to be swept", "batch", br.built.Batch.ID, "err", err)
	}
}

// explain says in the guidance what batch the member was tested in.
func (br *batchRun) explain(i int, g string) string {
	names := make([]string, 0, len(br.built.Members)-1)
	for j, m := range br.built.Members {
		if j != i {
			names = append(names, fmt.Sprintf("#%d", m.Row.TicketID))
		}
	}
	return fmt.Sprintf("%s\nThe server was testing this change in batch %d together with %s.", g, br.built.Batch.ID, strings.Join(names, ", "))
}

// rec is member i's outcome record, placed in its batch.
func (br *batchRun) rec(i int) *Record {
	row := br.built.Members[i].Row
	id := br.built.Batch.ID
	return &Record{TicketID: row.TicketID, AttemptNumber: row.AttemptNumber, ChangeURL: row.ChangeURL, HeadSHA: row.HeadSHA,
		EnqueuedAt: &row.EnqueuedAt, BatchID: &id, BatchSize: len(br.built.Members), BatchPosition: i + 1}
}

func batchVoided() *failure {
	return batchVoidedFor("the batch's run no longer covers this change")
}

func batchVoidedFor(why string) *failure {
	return &failure{outcome: BatchVoided, detail: why, keepsPlace: true,
		guidance: "The server tested your change in a batch and could not land it from that run (" + why +
			"). Nothing is known to be wrong with your change: call ticket_finish again."}
}

// batchDetail places a detail in its batch. A collision's detail stays first:
// it is what the collision bound counts.
func batchDetail(id int64, detail string) string {
	if strings.HasPrefix(detail, guidance.DetailPrefix) {
		return fmt.Sprintf("%s (batch %d)", detail, id)
	}
	return fmt.Sprintf("batch %d: %s", id, detail)
}
