package landing

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/change"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/landing/guidance"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// landing is one row's landing, with what it has done so far.
type landing struct {
	r      *Runner
	tenant uuid.UUID
	server uuid.UUID
	row    *LandingRow
	rec    *Record
	// updated is whether the base was merged into the head by this landing.
	updated bool
	// preMergeHead is the head the change had before the base was merged in.
	preMergeHead string
	// ahead are the members of the batch this landing is split from that
	// landed before it.
	ahead []int64
	// read, when set, reads the checks the landing waits on in place of the
	// held ticket's change's: a batch waits on its branch's.
	read func(ctx context.Context) (*change.Report, *failure)
	// remerged is whether the base was merged into the head a second time because
	// the first merge's commit got no pipeline.
	remerged bool
	// mergedHead is the head the last merge of the base made.
	mergedHead string
	// ci is the landing's CI wait, which keeps the retry state across waits.
	ci *ciWait
}

// waiter is the landing's CI wait, made on first use.
func (l *landing) waiter() *ciWait {
	if l.ci == nil {
		l.ci = &ciWait{r: l.r, read: l.readChecks, decorateRed: l.decorateRed}
		l.ci.onCIPassed = func(names []string) {
			if l.rec != nil {
				l.rec.Detail = "status not delivered to the host; the CI says success: " + strings.Join(names, ", ")
			}
		}
	}
	return l.ci
}

// awaitChecks waits for checks that have passed on the head: wantHead, when
// set, is the head the wait is for; otherwise notHead is the head merge-main
// replaced. seen is a report already read, if any.
func (l *landing) awaitChecks(ctx context.Context, wantHead, notHead string, seen *change.Report) (*change.Report, *failure) {
	return l.waiter().await(ctx, wantHead, notHead, seen)
}

// ciPassed are the checks the host showed pending that the CI said had
// passed, for the head awaitChecks last returned: the gate is told so.
func (l *landing) ciPassed() []change.CheckLine {
	return l.waiter().passed
}

// decorateRed adds to a final red what the landing knows of it: the guidance
// for a change the base was merged into, and a batch interaction.
func (l *landing) decorateRed(ctx context.Context, fail *failure, failing []string) *failure {
	if l.updated && l.r.o.Guidance != nil {
		fail.withCollision(l.r.o.Guidance.Red(ctx, l.row.TicketID, l.row.AttemptNumber, l.preMergeHead, failing, l.redFacts(ctx, failing)))
	}
	if len(l.ahead) > 0 && fail.collision != nil && fail.collision.Cause == guidance.CauseInteraction && fail.collision.Counts {
		return batchInteraction(fail, l.ahead)
	}
	return fail
}

// drive lands the change under the lock. The gate is change.Service's own: the
// merge is tried first, so a head that is green and current (or behind only in
// ways the gate accepts) merges without a CI run spent on merge-main, and the
// base is merged in only when the gate says the head is behind it.
func (l *landing) drive(ctx context.Context) *failure {
	r := l.r
	report, fail := l.checks(ctx)
	if fail != nil {
		return fail
	}
	for {
		l.rec.ChangeURL, l.rec.HeadSHA = report.URL, report.HeadSHA
		if report.State != "success" {
			if report, fail = l.awaitChecks(ctx, "", "", report); fail != nil {
				return fail
			}
			l.rec.HeadSHA = report.HeadSHA
		}
		if fail := r.stillOn(ctx); fail != nil {
			return fail
		}
		if err := r.o.Queue.SetStep(ctx, l.row.TicketID, Merging); err != nil {
			return r.classify(ctx, err)
		}
		merged, err := r.o.Changes.Merge(ctx, l.tenant, l.server, &change.MergeInput{ExpectedSHA: report.HeadSHA, CIPassed: l.ciPassed()})
		switch {
		case err == nil:
			return l.afterMerge(ctx, merged)
		case errors.Is(err, change.ErrAlreadyMerged), errors.Is(err, codehost.ErrAlreadyMerged):
			state, fail := r.reconcile(ctx, l.tenant, l.row)
			if fail != nil {
				return fail
			}
			state.Merged = true
			return r.closeMerged(ctx, l.server, l.row, state)
		case errors.Is(err, change.ErrBehindBase) && !l.updated:
			if report, fail = l.mergeMain(ctx, report.HeadSHA); fail != nil {
				return fail
			}
			continue
		}
		return r.classify(ctx, err)
	}
}

// mergeMain merges the base into the head and returns the report of the new
// head's checks, once they have passed. If the CI has no pipeline at all for
// the commit that made (its pull_request event went missing, as on 31 Oct), the
// base is merged once more, and the new commit gets a new event. The change is
// not at fault, so it is not handed back for it; if the base has not moved, or
// the second commit gets no pipeline either, it is.
func (l *landing) mergeMain(ctx context.Context, head string) (*change.Report, *failure) {
	report, fail := l.mergeMainOnce(ctx, head)
	if fail == nil || !fail.noPipeline || l.remerged || l.mergedHead == "" {
		return report, fail
	}
	l.remerged = true
	missed := l.mergedHead
	l.r.log.WarnContext(ctx, "landing: the CI has no pipeline for the merge of the base; merging the base again", "ticket", l.row.TicketID, "head", missed)
	rec := *l.rec
	rec.Outcome, rec.HeadSHA = CIEventMissingRequeued, missed
	rec.Detail = "the CI had no pipeline for " + missed + " after " + l.r.cfg.NoPipelineWait.String()
	// Not told to the observer: it counts landings, and this one goes on.
	if err := l.r.o.Outcomes.Record(context.WithoutCancel(ctx), &rec); err != nil {
		l.r.log.ErrorContext(ctx, "landing: could not record the outcome", "ticket", rec.TicketID, "outcome", rec.Outcome, "err", err)
	}
	report, again := l.mergeMainOnce(ctx, missed)
	if again != nil && again.outcome == MainMoved {
		// Nothing new to merge: the commit stays without a pipeline.
		return nil, fail
	}
	return report, again
}

func (l *landing) mergeMainOnce(ctx context.Context, head string) (*change.Report, *failure) {
	r := l.r
	if fail := r.stillOn(ctx); fail != nil {
		return nil, fail
	}
	if err := r.o.Queue.SetStep(ctx, l.row.TicketID, MergingMain); err != nil {
		return nil, r.classify(ctx, err)
	}
	var upd *codehost.BranchUpdate
	err := r.hostCall(ctx, func() (err error) {
		upd, err = r.o.Host.UpdateBranch(ctx, l.tenant, l.row.TicketID, l.row.AttemptNumber, head)
		return err
	})
	switch {
	case errors.Is(err, codehost.ErrUpdatePending):
		// The update may still land; the head is read until it does, for a
		// short bound and not the CI one: the host may never apply it.
		var fail *failure
		if upd, fail = l.awaitUpdate(ctx, head); fail != nil {
			return nil, fail
		}
		if upd.UpToDate {
			return nil, mainMoved(change.ErrBehindBase)
		}
	case errors.Is(err, codehost.ErrMergeConflict):
		fail := r.classify(ctx, err)
		if r.o.Guidance != nil {
			fail.withCollision(r.o.Guidance.Conflict(ctx, l.row.TicketID, l.row.AttemptNumber, head))
		}
		return nil, fail
	case err != nil:
		return nil, r.classify(ctx, err)
	case upd.UpToDate:
		// The host says the head has the base, yet the gate said behind: the
		// base moved between the two reads. Not retried in a loop.
		return nil, mainMoved(change.ErrBehindBase)
	}
	l.updated = true
	if l.preMergeHead == "" {
		l.preMergeHead = head
	}
	if err := r.o.Queue.SetStep(ctx, l.row.TicketID, WaitingChecks); err != nil {
		return nil, r.classify(ctx, err)
	}
	newHead := upd.SHA
	l.mergedHead = newHead
	report, fail := l.awaitChecks(ctx, newHead, head, nil)
	if fail != nil {
		return nil, fail
	}
	l.rec.HeadSHA = report.HeadSHA
	return report, nil
}

// awaitUpdate waits for a branch update the host left pending to move the head
// off the one it was asked from. The wait is UpdateWait; then the update is
// asked for once more and waited for the same time; a host that still has not
// applied it is a handback, so a lost update costs minutes and not the CI bound.
// It returns the new head in upd.SHA.
func (l *landing) awaitUpdate(ctx context.Context, head string) (*codehost.BranchUpdate, *failure) {
	r := l.r
	for ask := 1; ; ask++ {
		deadline := r.clock.Now().Add(r.cfg.UpdateWait)
		for {
			report, fail := l.checks(ctx)
			if fail != nil {
				return nil, fail
			}
			if !strings.EqualFold(report.HeadSHA, head) {
				r.log.InfoContext(ctx, "landing: the pending branch update landed", "ticket", l.row.TicketID, "head", report.HeadSHA)
				return &codehost.BranchUpdate{SHA: report.HeadSHA}, nil
			}
			if !r.clock.Now().Before(deadline) {
				break
			}
			if !r.sleep(ctx, r.cfg.PollInterval) {
				return nil, r.cancelled()
			}
			if fail := r.stillOn(ctx); fail != nil {
				return nil, fail
			}
		}
		if ask >= 2 {
			r.log.WarnContext(ctx, "landing: the code host did not apply the branch update again; handing back", "ticket", l.row.TicketID, "head", head, "waited", r.cfg.UpdateWait)
			return nil, hostError("the code host didn't apply the branch update")
		}
		r.log.WarnContext(ctx, "landing: the branch update is still pending; asking again", "ticket", l.row.TicketID, "head", head, "waited", r.cfg.UpdateWait)
		var upd *codehost.BranchUpdate
		err := r.hostCall(ctx, func() (err error) {
			upd, err = r.o.Host.UpdateBranch(ctx, l.tenant, l.row.TicketID, l.row.AttemptNumber, head)
			return err
		})
		switch {
		case errors.Is(err, codehost.ErrUpdatePending), errors.Is(err, codehost.ErrHeadMoved):
			// A head moved since the first ask is most likely that update landing
			// just now: the next wait reads it.
		case errors.Is(err, codehost.ErrMergeConflict):
			return nil, mergeMainConflict(err)
		case err != nil:
			return nil, r.classify(ctx, err)
		case upd.UpToDate:
			return nil, mainMoved(change.ErrBehindBase)
		default:
			return upd, nil
		}
	}
}

func (l *landing) readChecks(ctx context.Context) (*change.Report, *failure) {
	if l.read != nil {
		return l.read(ctx)
	}
	return l.checks(ctx)
}

func (l *landing) checks(ctx context.Context) (*change.Report, *failure) {
	r := l.r
	var report *change.Report
	err := r.hostCall(ctx, func() (err error) {
		report, err = r.o.Changes.Checks(ctx, l.tenant, l.server, "")
		return err
	})
	if err != nil {
		return nil, r.classify(ctx, err)
	}
	return report, nil
}

// afterMerge closes a ticket the merge left open. The change is on the base
// whatever happens here.
func (l *landing) afterMerge(ctx context.Context, merged *change.Merged) *failure {
	if merged.Closed {
		return nil
	}
	if _, err := l.r.o.Tickets.Done(context.WithoutCancel(ctx), l.server, &ticket.DoneParams{PRURL: merged.URL}); err != nil {
		l.r.log.ErrorContext(ctx, "landing: merged but could not close the ticket", "ticket", l.row.TicketID, "change", merged.URL, "err", err)
		return mergedNotClosed("the ticket could not be closed")
	}
	return nil
}

// redFacts asks CI whether the failing checks pass on the base's tip and on the
// change's head from before the base was merged in. An answer CI could not give
// is left unknown, never read as a pass.
func (l *landing) redFacts(ctx context.Context, failing []string) guidance.Facts {
	var f guidance.Facts
	if ok, err := l.r.o.Pipelines.PassedOnBase(ctx, failing); err == nil {
		f.GreenOnBase = &ok
	}
	if ok, err := l.r.o.Pipelines.PassedOnCommit(ctx, l.preMergeHead, failing); err == nil {
		f.GreenOnHead = &ok
	}
	return f
}
