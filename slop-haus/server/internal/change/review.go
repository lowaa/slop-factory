package change

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// Review is ticket.Review for the held ticket, which for a change slop-haus
// opened also records the head commit the change has on the host now, as the
// commit the human is asked to review, with a fingerprint of its diff against
// the base. An approval then covers that commit, or a later head whose diff
// against the base is the same (main taken in): Merge refuses anything else.
//
// The head is read at the hand-over, not when the human approves, because
// the human reviews what they were pointed at. Read at approval, a push made
// while they were reading would be approved unseen. A push after the
// hand-over leaves the recorded head behind, and unless its diff is the same
// the merge is refused until the change is handed over again.
//
// The record is written in the transaction that hands the ticket over: the
// reviewed head is the one the approval that follows is of, or nothing
// changed. A hand-over of any other PR -- the agent-reported pr_url path --
// clears the record in that same transaction, so the approval of it never
// pairs with a commit recorded by an earlier hand-over. A change already
// merged is refused with ErrAlreadyMerged: handing it over for review would
// only park the ticket where nobody can approve anything.
func (s *Service) Review(ctx context.Context, tenant, agentID uuid.UUID, params *ticket.ReviewParams) (*ticket.Ticket, error) {
	if err := tenantOf(ctx, tenant); err != nil {
		return nil, err
	}
	gate, err := s.tickets.ChangeGateFor(ctx, agentID)
	if err != nil {
		return nil, err
	}
	rec, err := s.store.Current(ctx, gate.Attempt.TicketID, gate.Attempt.Number)
	if errors.Is(err, ErrNoChange) {
		return s.tickets.Review(ctx, agentID, params)
	}
	if err != nil {
		return nil, err
	}
	if rec.MergedAt != nil {
		return nil, ErrAlreadyMerged
	}
	if !s.isChangeURL(params.PRURL, rec) {
		// Whatever the human approves now is not of the change, so no earlier
		// hand-over's commit may stay recorded for this approval to pair with.
		handed := *params
		handed.Within = func(q db.Querier) error {
			return s.store.ClearReviewedHead(ctx, q, rec.ID)
		}
		return s.tickets.Review(ctx, agentID, &handed)
	}
	checks, err := s.readChecks(ctx, tenant, agentID, rec, "review")
	if err != nil {
		return nil, err
	}
	if checks.HeadSHA == "" {
		return nil, fmt.Errorf("%w: the host named no head commit", ErrInvalid)
	}
	// The head a push has moved since change_open is checked again, here and
	// at the exact commit the human is pointed at.
	if !gate.Promoted && rec.Base == ticket.UnpromotedBase(rec.TicketID) {
		changes, ok := s.hosts.Changes(rec.Change.Host)
		if !ok {
			return nil, fmt.Errorf("%w: %s", ErrNoHost, rec.Change.Host)
		}
		tgt, err := s.targetFor(ctx, gate.Ticket)
		if err != nil {
			return nil, err
		}
		tgt = tgt.forChange(rec)
		tgt.changes = changes
		if err := s.checkShadowBase(ctx, tenant, agentID, rec.TicketID, gate.Ticket.BaseCommit, tgt, rec.Head, checks.HeadSHA); err != nil {
			return nil, err
		}
	}
	reviewed := &Reviewed{HeadSHA: checks.HeadSHA}
	// A diff that cannot be compared is not a failed hand-over: the approval
	// then covers this commit only, as it did before diffs were compared.
	diffNote := ""
	if diff, err := s.reviewedDiff(ctx, tenant, agentID, rec, checks.HeadSHA); err != nil {
		diffNote = " Its changes against the base could not be fingerprinted (" + err.Error() + "), so a later push needs another review even when it only takes in main."
	} else {
		reviewed.DiffFingerprint, reviewed.BaseSHA = diff.fingerprint, diff.baseSHA
	}
	// Where the human looks: what their approval will cover.
	note := "Handed over for review at commit " + checks.HeadSHA + "."
	if diffNote == "" {
		note += " An approval covers this commit, and a later head whose changes against " + rec.Base + " are the same (for example after merging " + rec.Base +
			" in, when it touched none of them); any other push needs another review."
	} else {
		note += " An approval covers this commit only; a push after it needs another review." + diffNote
	}
	// Written in the hand-over's transaction and addressed to the reviewed
	// attempt: the hand-over ends the lease the author would otherwise be
	// addressed by, which would leave the note on the ticket's conversation,
	// readable by every sibling attempt.
	attempt := gate.Attempt
	handed := *params
	handed.Within = func(q db.Querier) error {
		if err := s.store.SetReviewed(ctx, q, rec.ID, reviewed); err != nil {
			return err
		}
		_, err := ticket.InsertAttemptComment(ctx, q, &attempt, ticket.AgentAuthor(agentID), note)
		return err
	}
	left, err := s.tickets.Review(ctx, agentID, &handed)
	if err != nil {
		return nil, err
	}
	s.record(ctx, audit.ChangeReviewRequested, s.attrs(tenant, agentID, rec.TicketID, "review", "change", s.hosts.ChangeURL(rec.Change), "head_sha", checks.HeadSHA, "base_sha", reviewed.BaseSHA, "diff_fingerprint", reviewed.DiffFingerprint)...)
	return left, nil
}

type reviewedDiff struct{ fingerprint, baseSHA string }

// reviewedDiff reads the change's diff at headSHA and fingerprints it.
func (s *Service) reviewedDiff(ctx context.Context, tenant, agentID uuid.UUID, rec *Record, headSHA string) (*reviewedDiff, error) {
	changes, ok := s.hosts.Changes(rec.Change.Host)
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNoHost, rec.Change.Host)
	}
	diff, err := changes.Diff(ctx, tenant, rec.Change, headSHA)
	if err != nil {
		s.record(ctx, audit.ChangeOperationFailed, s.attrs(tenant, agentID, rec.TicketID, "review_diff", "change", s.hosts.ChangeURL(rec.Change), "head_sha", headSHA, "err", err.Error())...)
		return nil, err
	}
	// A diff against another branch is not what a merge into the base brings.
	if diff.Base != rec.Base {
		return nil, fmt.Errorf("it targets %q on the host, not %q", diff.Base, rec.Base)
	}
	fp, err := s.detect.Fingerprint(diff)
	if err != nil {
		return nil, err
	}
	return &reviewedDiff{fingerprint: fp, baseSHA: diff.BaseSHA}, nil
}

// isChangeURL is whether raw is the link of the change rec records.
func (s *Service) isChangeURL(raw string, rec *Record) bool {
	ref, ok := s.hosts.ParseChangeURL(raw)
	return ok && ref == rec.Change
}
