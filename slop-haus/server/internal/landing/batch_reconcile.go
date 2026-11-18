package landing

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/change"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
)

// sweepWindow is how far back the branches of ended batches are looked for.
const sweepWindow = 24 * time.Hour

// Reconcile settles the batches a stopped runner left, from the code host and
// never from memory, then deletes the branches of recently ended batches that
// were left on the host. It runs before the tenant's queue is worked, so none
// of the live batches is this process's. One held by another replica's live
// lease cannot be claimed and is not taken over; past BatchStuckAfter it
// alerts.
func (b *BatchRunner) Reconcile(ctx context.Context, tenant uuid.UUID) {
	live, err := b.o.Builder.o.Store.Live(ctx)
	if err != nil {
		b.r.log.ErrorContext(ctx, "landing: read the live batches", "err", err)
		return
	}
	for i := range live {
		if ctx.Err() != nil {
			return
		}
		b.reconcileOne(ctx, tenant, &live[i])
	}
	b.sweep(ctx, tenant)
}

// sweep deletes the branches of recently ended batches, once for each tenant
// this process serves: it is for a delete that failed or was lost to a crash,
// and the batches ended since are this process's own.
func (b *BatchRunner) sweep(ctx context.Context, tenant uuid.UUID) {
	r := b.r
	b.mu.Lock()
	seen := b.swept[tenant]
	b.mu.Unlock()
	if seen {
		return
	}
	done, err := b.o.Builder.o.Store.FinishedSince(ctx, r.clock.Now().Add(-sweepWindow))
	if err != nil {
		r.log.ErrorContext(ctx, "landing: list ended batches", "err", err)
		return
	}
	b.mu.Lock()
	b.swept[tenant] = true
	b.mu.Unlock()
	for i := range done {
		if err := b.o.Builder.o.Host.DeleteBranch(ctx, &done[i].Sample, done[i].Branch); err != nil {
			r.log.WarnContext(ctx, "landing: sweep a batch branch", "batch", done[i].ID, "err", err)
		}
	}
}

// reconcileOne settles one live batch. The members that left the queue merged
// (or were cancelled); of the others, those the host shows merged are closed,
// and the first that is not carries on if the batch's evidence still holds
// (the base is the batch's base plus the members merged, and its run is still
// green), and otherwise every member from it is requeued with its place. A
// batch still building or being tested has no run to carry on: it is built
// again.
func (b *BatchRunner) reconcileOne(ctx context.Context, tenant uuid.UUID, batch *Batch) {
	r := b.r
	if batch.UpdatedAt.After(r.clock.Now().Add(-r.cfg.LeaseTTL)) {
		// Another replica may be between two of its steps, with no lease held for
		// a moment: a batch that dead has been still for longer than a lease lives.
		return
	}
	store := b.o.Builder.o.Store
	rows, err := store.MemberRows(ctx, batch.ID)
	if err != nil {
		r.log.ErrorContext(ctx, "landing: read a live batch's members", "batch", batch.ID, "err", err)
		return
	}
	if len(rows) == 0 && batch.State == BatchBuilding {
		// Members are reserved only once the branch is built, so none reserved
		// is a build that stopped part way, not a batch whose members merged.
		b.voidUnbuilt(ctx, batch)
		return
	}
	byID := map[int64]*LandingRow{}
	for i := range rows {
		byID[rows[i].TicketID] = &rows[i]
	}
	// The rows hold the enqueued heads; the batch tested the heads its members'
	// updates made, and the gate's evidence has to name those.
	if len(batch.Heads) == len(batch.Members) {
		for pos, id := range batch.Members {
			if row := byID[id]; row != nil && batch.Heads[pos] != "" {
				row.HeadSHA = batch.Heads[pos]
			}
		}
	}
	built := &Built{Batch: batch, change: batch.Change, stop: func() {}}
	left := make([]bool, len(batch.Members))
	first := -1
	for pos, id := range batch.Members {
		m := BuiltMember{Row: byID[id]}
		if pos < len(batch.Prefixes) {
			m.PrefixSHA = batch.Prefixes[pos]
		}
		if m.Row == nil {
			m.Row, left[pos] = &LandingRow{TicketID: id}, true
		} else if first < 0 {
			first = pos
		}
		built.Members = append(built.Members, m)
	}
	if first < 0 {
		b.endMerged(ctx, built)
		return
	}
	built.holder, built.repoRow = built.Members[first].Row, built.Members[first].Row
	server, err := r.o.Tickets.EnsureServerAgent(ctx)
	if err != nil {
		r.log.ErrorContext(ctx, "landing: no server agent", "err", err)
		return
	}
	built.Server = server
	br := &batchRun{b: b, r: r, tenant: tenant, built: built, leased: -1, held: ctx, stop: func() {}}
	defer br.observe()
	defer func() { br.stop() }()
	if taken, ended := br.claim(ctx, first); !taken {
		if !ended {
			b.notTaken(ctx, built, first)
		}
		return
	}
	k, ok := br.closeMergedMembers(ctx, first, left)
	if !ok {
		return
	}
	if k == len(built.Members) {
		br.stop()
		b.endMerged(ctx, built)
		return
	}
	switch batch.State {
	case BatchMerging:
	case BatchTesting:
		br.resumeTesting(ctx, k)
		return
	default:
		// Building has recorded neither the commits it merged nor started a CI
		// run worth waiting for, and its branch may be half built: it is
		// cheaper to build it again than to prove the branch whole.
		br.voidFrom(ctx, k, "the runner stopped while the batch was being built")
		return
	}
	ev, why := b.evidence(br, k)
	if ev == nil {
		br.voidFrom(ctx, k, why)
		return
	}
	br.ev = ev
	r.log.InfoContext(ctx, "landing: carrying on a batch a stopped runner left", "batch", batch.ID, "from", built.Members[k].Row.TicketID)
	br.mergeAll(ctx, k)
}

// resumeTesting carries on waiting for CI on a testing batch's head when
// nothing the run tested has changed: no member has merged, each is still at
// the head it was tested at, main is the tip the batch was built on and the
// branch is at the last commit built. Otherwise it voids the batch, naming
// the check that failed. The CI run survives a restart, so none of its wait
// is lost.
func (br *batchRun) resumeTesting(ctx context.Context, k int) {
	built, batch := br.built, br.built.Batch
	if k != 0 {
		br.voidFrom(ctx, k, "a member merged while the batch was being tested")
		return
	}
	if len(batch.Prefixes) != len(batch.Members) || len(batch.Heads) != len(batch.Members) {
		br.voidFrom(ctx, 0, "the batch's tested commits were not recorded")
		return
	}
	for pos, m := range built.Members {
		state, fail := br.r.reconcile(br.held, br.tenant, m.Row)
		if fail != nil {
			br.voidFrom(ctx, 0, "the code host could not be read")
			return
		}
		if state.Merged || !strings.EqualFold(state.HeadSHA, batch.Heads[pos]) {
			br.voidFrom(ctx, 0, fmt.Sprintf("member #%d is no longer at the head the batch tested", m.Row.TicketID))
			return
		}
	}
	checks, err := br.b.o.Checks.BranchChecks(br.held, built.holder, batch.Branch)
	if err != nil {
		br.voidFrom(ctx, 0, "the batch's checks could not be read")
		return
	}
	if !strings.EqualFold(checks.HeadSHA, batch.Prefixes[len(batch.Prefixes)-1]) {
		br.voidFrom(ctx, 0, "the batch branch is not at the last commit built")
		return
	}
	br.r.log.InfoContext(ctx, "landing: carrying on testing a batch a stopped runner left", "batch", batch.ID)
	br.awaitAndMerge(ctx)
}

// claim leases and locks member i. It is not taken when the member cannot be
// claimed, or when the lock could not be had and the batch has been ended.
func (br *batchRun) claim(ctx context.Context, i int) (taken, ended bool) {
	r, built := br.r, br.built
	row := built.Members[i].Row
	claim, err := r.o.Tickets.ServerClaimLanding(ctx, built.Server, row.TicketID, row.AttemptNumber, r.cfg.LeaseTTL)
	if err != nil || claim.Ticket == nil {
		r.log.InfoContext(ctx, "landing: a live batch's member cannot be claimed", "batch", built.Batch.ID, "ticket", row.TicketID, "err", err)
		return false, false
	}
	br.leased = i
	br.held, br.stop = r.heartbeat(ctx, built.Server, row.TicketID)
	if fail := r.awaitLock(br.held, built.Server); fail != nil {
		br.handBackFrom(ctx, i, fail)
		return false, true
	}
	return true, false
}

// notTaken is a live batch whose first member could not be taken: it is
// someone else's, or stuck. It is never taken over; it is alerted once it has
// sat too long.
func (b *BatchRunner) notTaken(ctx context.Context, built *Built, first int) {
	if built.Batch.UpdatedAt.After(b.r.clock.Now().Add(-b.r.cfg.BatchStuckAfter)) {
		return
	}
	why := fmt.Sprintf("batch %d has been %s since %s and its first member cannot be claimed", built.Batch.ID, built.Batch.State, built.Batch.UpdatedAt.Format(time.RFC3339))
	b.r.log.ErrorContext(ctx, "landing: ALERT a batch is stuck; it is not taken over", "batch", built.Batch.ID, "why", why)
	if b.o.Alert != nil {
		b.o.Alert(context.WithoutCancel(ctx), built.Members[first].Row.TicketID, built.Batch.ID, why)
	}
}

// closeMergedMembers closes the members from first that the host shows merged,
// in order, and is the index of the first that is not (the member leased), or
// the number of members. It is false when the batch has been ended instead.
func (br *batchRun) closeMergedMembers(ctx context.Context, first int, left []bool) (int, bool) {
	r, built := br.r, br.built
	for k := first; k < len(built.Members); k++ {
		if left[k] {
			continue
		}
		if k > first && !br.take(ctx, k) {
			return 0, false
		}
		row := built.Members[k].Row
		state, fail := r.reconcile(br.held, br.tenant, row)
		if fail != nil {
			br.voidFrom(ctx, k, "the code host could not be read")
			return 0, false
		}
		if !state.Merged {
			return k, true
		}
		if fail := r.closeMerged(br.held, built.Server, row, state); fail.outcome != AlreadyMerged {
			r.log.ErrorContext(ctx, "landing: a batch member merged but the bookkeeping failed", "ticket", row.TicketID, "detail", fail.detail)
			br.voidFrom(ctx, k+1, "a member merged but could not be closed")
			return 0, false
		}
		rec := br.rec(k)
		rec.Outcome, rec.Detail = AlreadyMerged, "found merged on the host after a restart"
		r.finish(ctx, row, rec)
		br.leased = -1
		left[k] = true
	}
	return len(built.Members), true
}

// evidence is the batch's run as evidence for carrying on from member k, or
// why it is not: the commits it tested are known, every member ahead has a
// merge commit and the base's tip is the last of them (the batch base when
// none), and the batch branch's checks are still green for its head.
func (b *BatchRunner) evidence(br *batchRun, k int) (*change.BatchEvidence, string) {
	built, batch := br.built, br.built.Batch
	ctx := br.held
	if len(batch.Prefixes) != len(batch.Members) {
		return nil, "the batch's commits were not recorded"
	}
	merges := make([]string, k)
	for j := range k {
		sha, err := b.o.Recovery.MergeSHA(ctx, built.Members[j].Row.TicketID)
		if err != nil || sha == "" {
			return nil, "a member merged without its merge commit being recorded"
		}
		merges[j] = sha
	}
	// The base's tip is the gate's to judge, as it may have moved by drift
	// a serial merge allows.
	checks, err := b.o.Checks.BranchChecks(ctx, built.holder, batch.Branch)
	if err != nil {
		return nil, "the batch's checks could not be read"
	}
	if !strings.EqualFold(checks.HeadSHA, batch.Prefixes[len(batch.Prefixes)-1]) {
		return nil, "the batch's checks are not green for its head"
	}
	var ciPassed []change.CheckLine
	if !checks.Passed() {
		// The wait that started this merge may have been cut short by the CI
		// vouching for a check the host never saw finish; the host still
		// shows it pending, so ask the CI again.
		if ciPassed = br.ciVouched(ctx, checks); ciPassed == nil {
			return nil, "the batch's checks are not green for its head"
		}
	}
	ev := evidenceOf(built, checks)
	ev.CIPassed = ciPassed
	for j := range k {
		ev.Members[j].MergeSHA = merges[j]
	}
	return ev, ""
}

// ciVouched is the pending checks of a batch's head when every one of them is
// one the CI says passed and nothing else is red, or nil. No stuck-for wait
// applies: the batch was already waited on before it began to merge.
func (br *batchRun) ciVouched(ctx context.Context, checks *codehost.Checks) []change.CheckLine {
	if checks.State != codehost.CheckPending {
		return nil
	}
	var passed []change.CheckLine
	for _, c := range checks.Checks {
		switch c.State {
		case codehost.CheckSuccess, codehost.CheckNeutral:
			continue
		case codehost.CheckPending:
		default:
			return nil
		}
		if c.TargetURL == "" {
			return nil
		}
		verdict, err := br.r.o.Pipelines.CheckVerdict(ctx, checks.HeadSHA, c.TargetURL)
		if err != nil || verdict != CIPassed {
			return nil
		}
		passed = append(passed, change.CheckLine{Name: c.Name, State: c.State.String(), TargetURL: c.TargetURL})
	}
	return passed
}

// endMerged ends a batch every member of which has merged.
func (b *BatchRunner) endMerged(ctx context.Context, built *Built) {
	store := b.o.Builder.o.Store
	if built.repoRow == nil {
		sample, err := store.Sample(ctx, built.Batch.ID)
		if err != nil || sample == nil {
			// No repository to name: the batch is marked done and its branch,
			// if any, is not found again.
			b.r.log.WarnContext(ctx, "landing: no member to name a finished batch's repository", "batch", built.Batch.ID, "err", err)
			_ = store.Finish(ctx, built.Batch.ID, ResultMerged, "every member merged")
			return
		}
		built.repoRow = sample
	}
	if err := b.o.Builder.Done(ctx, built, ResultMerged, "every member merged"); err != nil {
		b.r.log.ErrorContext(ctx, "landing: finish a batch; its branch is left to be swept", "batch", built.Batch.ID, "err", err)
	}
}

// voidUnbuilt ends a batch a stopped runner left while it was being built. Its
// members were never reserved and are still queued; only the branch, and the
// change if one was opened, are left to clean up.
func (b *BatchRunner) voidUnbuilt(ctx context.Context, batch *Batch) {
	store := b.o.Builder.o.Store
	const why = "the runner stopped while the batch was being built"
	repo, err := store.LandingOf(ctx, batch.Members)
	if err == nil && repo == nil {
		repo, err = store.Sample(ctx, batch.ID)
	}
	if err != nil || repo == nil {
		b.r.log.WarnContext(ctx, "landing: no member to name an unbuilt batch's repository", "batch", batch.ID, "err", err)
		if err := store.Void(ctx, batch.ID, why); err != nil {
			b.r.log.ErrorContext(ctx, "landing: void an unbuilt batch", "batch", batch.ID, "err", err)
		}
		return
	}
	built := &Built{Batch: batch, change: batch.Change, repoRow: repo, stop: func() {}}
	if err := b.o.Builder.Void(ctx, built, why); err != nil {
		b.r.log.ErrorContext(ctx, "landing: void an unbuilt batch; its branch is left to be swept", "batch", batch.ID, "err", err)
	}
}
