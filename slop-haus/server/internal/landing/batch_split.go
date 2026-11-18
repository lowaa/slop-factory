package landing

import (
	"context"
	"fmt"
	"strings"
)

// splitRed lands the members of a batch whose run came back red, one at a time
// and in order, each through the serial path: merged as soon as its own run is
// green, handed back when it is red. With the size capped at 3 this is no
// dearer than halving and needs no branch of its own. The members that land
// are the innocents, who are told on their tickets and never handed back; the
// culprit is handed back with its own run's failure and the batch's id, and a
// member that is green alone but red behind the ones ahead of it is the batch's
// interaction. The batch branch's evidence is not used again.
func (br *batchRun) splitRed(ctx context.Context) {
	r, built := br.r, br.built
	r.log.WarnContext(ctx, "landing: batch is red; landing its members one at a time", "batch", built.Batch.ID)
	br.tellSplit(ctx)
	var ahead []int64
	for i := range built.Members {
		if i > 0 && !br.take(ctx, i) {
			return
		}
		m := &built.Members[i]
		if fail := r.stillOn(br.held); fail != nil {
			br.handBackFrom(ctx, i, fail)
			return
		}
		rec := br.rec(i)
		fail := r.runBehind(br.held, br.tenant, built.Server, m.Row, rec, ahead)
		switch {
		case fail == nil:
			rec.Outcome = Merged
			r.finish(ctx, m.Row, rec)
			br.leased = -1
			ahead = append(ahead, m.Row.TicketID)
		case fail.mergedNotClosed:
			// On the base: the next sweep closes it. Not a failure of the member.
			r.log.ErrorContext(ctx, "landing: a batch member merged but the bookkeeping failed", "ticket", m.Row.TicketID, "detail", fail.detail)
			br.voidFrom(ctx, i+1, "a member merged but could not be closed")
			return
		case fail.outcome == AlreadyMerged:
			rec.Outcome, rec.Detail = fail.outcome, fail.detail
			r.finish(ctx, m.Row, rec)
			br.leased = -1
			ahead = append(ahead, m.Row.TicketID)
		case ctx.Err() != nil || br.held.Err() != nil:
			br.abandoned(ctx)
			return
		case fail.outcome == MergeRefused && fail.detail == switchedOff().detail:
			br.handBackFrom(ctx, i, fail)
			return
		default:
			br.handBackOne(ctx, i, fail)
		}
	}
	if err := br.b.o.Builder.Done(ctx, built, ResultSplit, "red; members landed one at a time"); err != nil {
		r.log.ErrorContext(ctx, "landing: finish the batch; its branch is left to be swept", "batch", built.Batch.ID, "err", err)
	}
}

// tellSplit leaves a comment on every member's ticket: an innocent is not
// handed back, so this is all it hears of the red batch.
func (br *batchRun) tellSplit(ctx context.Context) {
	built := br.built
	ctx = context.WithoutCancel(ctx)
	ids := make([]string, len(built.Members))
	for i, m := range built.Members {
		ids[i] = fmt.Sprintf("#%d", m.Row.TicketID)
	}
	text := fmt.Sprintf("The server's batch %d (%s) failed its checks, so the batch was split: each change is now tested on its own, in the same order. "+
		"Nothing is needed from you unless this change is handed back.", built.Batch.ID, strings.Join(ids, ", "))
	for _, m := range built.Members {
		if err := br.b.o.Builder.o.Members.Comment(ctx, built.Server, m.Row, text); err != nil {
			br.r.log.ErrorContext(ctx, "landing: tell a batch member the batch was split", "ticket", m.Row.TicketID, "err", err)
		}
	}
}
