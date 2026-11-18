package landing

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/landing/guidance"
)

// BatchOptions are what a BatchBuilder is built from.
type BatchOptions struct {
	Store *BatchStore
	Host  BatchHost
	// Members hands back the members that hold no lease.
	Members MemberHandBack
}

// BatchBuilder reserves the members of a batch and builds its branch: main's
// tip with each member's head merged in, in order. It does not wait for CI or
// merge anything; nothing calls it until the runner is wired to batch.
//
// The server agent holds one lease, so only the first member is claimed; the
// others are held by their reservation (BatchStore.Reserve). A member that
// drops out of the batch is cleared of it first, and handed back by the lease
// when it is the claimed one and by a comment on its attempt when it is not.
type BatchBuilder struct {
	r *Runner
	o BatchOptions
}

func NewBatchBuilder(r *Runner, o *BatchOptions) (*BatchBuilder, error) {
	if r == nil || o.Store == nil || o.Host == nil || o.Members == nil {
		return nil, errors.New("landing: a batch builder needs a Runner, Store, Host and Members")
	}
	return &BatchBuilder{r: r, o: *o}, nil
}

// BuiltMember is a member of a built batch and the batch branch's commit
// after the member was merged into it: the base plus the members up to and
// including this one. A gate's batch evidence names it (change.BatchMember).
type BuiltMember struct {
	Row       *LandingRow
	PrefixSHA string
}

// Built is a batch whose branch is built.
type Built struct {
	Batch   *Batch
	Members []BuiltMember
	Server  uuid.UUID
	// Held ends when the server's lease is lost.
	Held context.Context

	stop func()
	// holder is the member the server's lease is on.
	holder *LandingRow
	// repoRow is any row of the batch: they share a repository.
	repoRow *LandingRow
	// change is the draft change that makes CI run on the branch; nil until opened.
	change *codehost.ChangeRef
}

// Close stops the lease's heartbeat. It does not release the lease: that goes
// with the closing of the holder's ticket, as for a single landing.
func (b *Built) Close() { b.stop() }

// Build claims the first claimable candidate, reserves it and the candidates
// behind it as a batch and builds the batch branch. Candidates that cannot be
// claimed or reserved are left out and stay queued. It returns nil when no
// batch is left to run. A host or store failure voids the batch and hands the
// holder back for it, keeping its place.
func (b *BatchBuilder) Build(ctx context.Context, cands []Candidate) (*Built, error) {
	r := b.r
	server, err := r.o.Tickets.EnsureServerAgent(ctx)
	if err != nil {
		return nil, fmt.Errorf("landing: no server agent: %w", err)
	}
	start, err := b.claimFrom(ctx, server, rowsOf(cands))
	if err != nil || start < 0 {
		return nil, err
	}
	cands = cands[start:]
	held, stop := r.heartbeat(ctx, server, cands[0].Row.TicketID)
	built := &Built{Server: server, Held: held, stop: stop, holder: cands[0].Row, repoRow: cands[0].Row}
	byID := map[int64]Candidate{}
	ids := make([]int64, len(cands))
	for i, c := range cands {
		byID[c.Row.TicketID], ids[i] = c, c.Row.TicketID
	}

	tip, err := b.o.Host.BaseTip(held, built.holder)
	if err != nil {
		return nil, b.abort(ctx, built, "read the base's tip", err)
	}
	// The members stay queued while the branch is built: updateMember and the
	// merges can still fail, and a member must not read as batch(N) until
	// nothing can. Reserve at the end takes them all or none.
	batch, err := b.o.Store.Create(held, tip, ids)
	if err != nil {
		// The lease is left to run out, as a landing abandoned anywhere is.
		stop()
		return nil, err
	}
	built.Batch = batch
	if err := b.o.Host.CreateBranch(held, built.holder, batch.Branch, tip); err != nil {
		return nil, b.abort(ctx, built, "create the batch branch", err)
	}

	remaining := batch.Members
	for len(remaining) > 0 {
		c := byID[remaining[0]]
		remaining = remaining[1:]
		var merge *codehost.BranchMerge
		err := b.updateMember(held, c.Row)
		if errors.Is(err, codehost.ErrMergeConflict) {
			// The member conflicts with the base itself, whatever is ahead of it.
			if err = b.drop(ctx, built, c, mergeMainConflict(err), &remaining, byID); err != nil {
				return nil, err
			}
			continue
		}
		if err == nil {
			err = r.hostCall(held, func() (err error) {
				merge, err = b.o.Host.MergeHead(held, c.Row, batch.Branch, c.Row.HeadSHA)
				return err
			})
		}
		switch {
		case err == nil:
			built.Members = append(built.Members, BuiltMember{Row: c.Row, PrefixSHA: merge.SHA})
			continue
		case errors.Is(err, codehost.ErrUpdatePending):
			err = b.drop(ctx, built, c, updatePending(), &remaining, byID)
		case errors.Is(err, codehost.ErrMergeConflict):
			fail := mergeMainConflict(err)
			b.explainConflict(held, fail, c, built.Members)
			err = b.drop(ctx, built, c, fail, &remaining, byID)
		case known(err) != nil:
			err = b.drop(ctx, built, c, known(err), &remaining, byID)
		default:
			return nil, b.abort(ctx, built, "merge a member into the batch branch", err)
		}
		if err != nil {
			return nil, err
		}
	}
	if len(built.Members) == 0 {
		return nil, b.Void(ctx, built, "no member could be built into the batch")
	}
	// Opened once the members are merged: the change's creation is what starts
	// CI, and it must test the final head.
	var ref codehost.ChangeRef
	if err := r.hostCall(held, func() (err error) {
		ref, err = b.o.Host.OpenBatchChange(held, built.holder, batch.Branch)
		return err
	}); err != nil {
		return nil, b.abort(ctx, built, "open the batch's change", err)
	}
	built.change = &ref
	if err := b.o.Store.SetChange(built.Held, batch.ID, ref); err != nil {
		return nil, b.abort(ctx, built, "record the batch's change", err)
	}
	if built.Batch, err = b.o.Store.Reserve(built.Held, batch.ID); err != nil {
		if errors.Is(err, ErrNotReservable) {
			// A member left the queue while the branch was built: nothing was
			// reserved, so the others are still queued and are built again.
			// The lease is left to run out, as for a refused reservation.
			built.Batch = batch
			built.stop()
			return nil, errors.Join(err, b.Void(ctx, built, "a member left the queue while the batch was built"))
		}
		built.Batch = batch
		return nil, b.abort(ctx, built, "reserve the batch's members", err)
	}
	return built, nil
}

// updateMember brings the member's branch up to date with the base, as the
// serial landing does before it waits for checks, and takes the new head as
// the member's. The gate wants a member's own head to contain the batch's
// base, and a member queued before the base last moved does not.
func (b *BatchBuilder) updateMember(ctx context.Context, row *LandingRow) error {
	var upd *codehost.BranchUpdate
	err := b.r.hostCall(ctx, func() (err error) {
		upd, err = b.o.Host.UpdateMember(ctx, row, row.HeadSHA)
		return err
	})
	if err != nil {
		return err
	}
	if !upd.UpToDate {
		row.HeadSHA = upd.SHA
	}
	return nil
}

// claimFrom claims the first of rows that can be, and is its index; -1 when
// none can.
func (b *BatchBuilder) claimFrom(ctx context.Context, server uuid.UUID, rows []*LandingRow) (int, error) {
	r := b.r
	for i, row := range rows {
		claim, err := r.o.Tickets.ServerClaimLanding(ctx, server, row.TicketID, row.AttemptNumber, r.cfg.LeaseTTL)
		switch {
		case isNotClaimable(err):
			r.log.InfoContext(ctx, "landing: batch member not claimable", "ticket", row.TicketID, "reason", err)
		case err != nil:
			return -1, fmt.Errorf("landing: claim for a batch: %w", err)
		case claim.Ticket == nil:
			r.log.InfoContext(ctx, "landing: another ticket holds paths a batch member needs", "ticket", row.TicketID)
		default:
			return i, nil
		}
	}
	return -1, nil
}

// explainConflict names, next to the existing collision guidance, the members
// merged ahead of this one: the conflict can be with any of them and not with
// the base, which the author cannot tell from the text alone.
func (b *BatchBuilder) explainConflict(ctx context.Context, fail *failure, c Candidate, ahead []BuiltMember) {
	var col *guidance.Collision
	if b.r.o.Guidance != nil {
		col = b.r.o.Guidance.Conflict(ctx, c.Row.TicketID, c.Row.AttemptNumber, c.Row.HeadSHA)
	}
	if len(ahead) == 0 {
		if col != nil {
			fail.withCollision(col)
		}
		return
	}
	if col == nil {
		col = &guidance.Collision{Cause: guidance.CauseMergeConflict, Counts: true}
	}
	names := make([]string, len(ahead))
	for i := range ahead {
		names[i] = fmt.Sprintf("#%d", ahead[i].Row.TicketID)
		col.Predecessors = append(col.Predecessors, ahead[i].Row.TicketID)
	}
	base := col.Guidance
	if base == "" {
		base = fail.guidance
	}
	col.Guidance = base + "\nThe server was testing it in a batch, merged after " + strings.Join(names, ", ") +
		", and the conflict may be with one of them rather than with the base: if so, wait for them to land, then merge the base branch again."
	fail.withCollision(col)
}

// drop takes a member out of the batch and hands it back. The holder is handed
// back by the lease and the lease moves to the next member that can take it.
func (b *BatchBuilder) drop(ctx context.Context, built *Built, c Candidate, fail *failure, remaining *[]int64, byID map[int64]Candidate) error {
	r := b.r
	dropped, err := b.o.Store.Drop(ctx, built.Batch.ID, c.Row.TicketID)
	if err != nil {
		return b.abort(ctx, built, "drop a member", err)
	}
	built.Batch = dropped
	rec := &Record{TicketID: c.Row.TicketID, AttemptNumber: c.Row.AttemptNumber, ChangeURL: c.Row.ChangeURL, HeadSHA: c.Row.HeadSHA, EnqueuedAt: &c.Row.EnqueuedAt}
	if c.Row.TicketID != built.holder.TicketID {
		return b.handBackMember(ctx, built, c.Row, fail, rec)
	}
	if !r.handBack(ctx, built.Server, c.Row, fail, rec) {
		return b.abort(ctx, built, "hand back the first member", errors.New("hand back failed"))
	}
	// The lease ended with the hand back, and its heartbeat with it.
	built.stop()
	rows := make([]*LandingRow, len(*remaining))
	for i, id := range *remaining {
		rows[i] = byID[id].Row
	}
	next, err := b.claimFrom(ctx, built.Server, rows)
	if err != nil {
		return err
	}
	// Members passed over cannot take the lease: they are no longer part of the batch.
	for _, row := range rows[:max(next, 0)] {
		if built.Batch, err = b.o.Store.Drop(ctx, built.Batch.ID, row.TicketID); err != nil {
			return b.abort(ctx, built, "drop a member that could not be claimed", err)
		}
	}
	if next < 0 {
		*remaining = nil
		built.Held, built.stop = ctx, func() {}
		return nil
	}
	*remaining = (*remaining)[next:]
	built.holder = rows[next]
	built.Held, built.stop = r.heartbeat(ctx, built.Server, built.holder.TicketID)
	return nil
}

// handBackMember hands back a member that holds no lease: its reservation is
// already cleared, so the row is handed back to its author, who is told on the
// attempt.
func (b *BatchBuilder) handBackMember(ctx context.Context, built *Built, row *LandingRow, fail *failure, rec *Record) error {
	r := b.r
	ctx = context.WithoutCancel(ctx)
	rec.Outcome, rec.Detail = fail.outcome, fail.detail
	if err := b.o.Members.Comment(ctx, built.Server, row, fail.guidance); err != nil {
		return b.abort(ctx, built, "tell a dropped member's author", err)
	}
	if err := r.o.Queue.HandBack(ctx, row.TicketID, fail.outcome, fail.keepsPlace); err != nil {
		return b.abort(ctx, built, "mark a dropped member handed back", err)
	}
	if err := b.o.Members.Requeue(ctx, built.Server, row); err != nil {
		return b.abort(ctx, built, "return a dropped member to its author's queue", err)
	}
	r.write(ctx, rec)
	return nil
}

// abort ends the batch on a failure that is the server's and not a member's:
// every member goes back to the queue with its place, and the holder is handed
// back for a code host error. The error returned says what failed.
func (b *BatchBuilder) abort(ctx context.Context, built *Built, what string, cause error) error {
	r := b.r
	if ctx.Err() != nil {
		// Abandoned: the lease and the rows are the recovery.
		built.stop()
		return fmt.Errorf("landing: %s: %w", what, cause)
	}
	r.log.ErrorContext(ctx, "landing: batch failed", "what", what, "err", cause)
	if built.Batch != nil {
		_ = b.Void(ctx, built, fmt.Sprintf("%s: %v", what, cause))
	}
	rec := &Record{TicketID: built.holder.TicketID, AttemptNumber: built.holder.AttemptNumber, ChangeURL: built.holder.ChangeURL,
		HeadSHA: built.holder.HeadSHA, EnqueuedAt: &built.holder.EnqueuedAt}
	r.handBack(ctx, built.Server, built.holder, hostError("code host or store error"), rec)
	built.stop()
	return fmt.Errorf("landing: %s: %w", what, cause)
}

// Done ends a batch whose members have all merged and been removed from the
// queue: its branch is deleted and its state is done. A member still queued is
// released by Void, not here.
func (b *BatchBuilder) Done(ctx context.Context, built *Built, result BatchResult, reason string) error {
	return b.finish(ctx, built, false, result, reason)
}

// Void ends a batch whose evidence is no longer good: its branch is deleted,
// and every member still in it is released to the queue with its place.
func (b *BatchBuilder) Void(ctx context.Context, built *Built, why string) error {
	return b.finish(ctx, built, true, ResultVoided, why)
}

func (b *BatchBuilder) finish(ctx context.Context, built *Built, void bool, result BatchResult, reason string) error {
	ctx = context.WithoutCancel(ctx)
	var closeErr error
	if built.change != nil {
		closeErr = b.o.Host.CloseBatchChange(ctx, built.repoRow, *built.change)
	}
	delErr := errors.Join(closeErr, b.o.Host.DeleteBranch(ctx, built.repoRow, built.Batch.Branch))
	if void {
		// The members are released whether or not the branch could be deleted:
		// a stuck reservation costs more than a stray branch, which is swept.
		if built.Batch.State == BatchDone {
			// The last member's drop ended it.
			return delErr
		}
		return errors.Join(delErr, b.o.Store.Void(ctx, built.Batch.ID, reason))
	}
	if delErr != nil {
		// Left live, so that it is found again and its branch deleted.
		return fmt.Errorf("landing: delete the batch branch: %w", delErr)
	}
	return b.o.Store.Finish(ctx, built.Batch.ID, result, reason)
}

func rowsOf(cands []Candidate) []*LandingRow {
	rows := make([]*LandingRow, len(cands))
	for i := range cands {
		rows[i] = cands[i].Row
	}
	return rows
}
