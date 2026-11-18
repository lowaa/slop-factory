package change

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// CheckLine is one check on the change's head.
type CheckLine struct {
	Name  string
	State string
	// TargetURL is the CI's own address for the check, when the host gave one.
	TargetURL string
}

// Report is the checks on a change's head, as of the read.
type Report struct {
	URL string
	// HeadSHA is the commit the checks are for: the head the change has on
	// the host now.
	HeadSHA string
	// State is the overall state: success, failure, pending or none. None
	// -- no checks reported for this head -- is not a pass.
	State  string
	Passed bool
	// Base is the branch the change targets on the host now.
	Base   string
	Checks []CheckLine
	// ForExpectedHead is set when the caller named the commit it pushed:
	// whether the checks are for that commit.
	ForExpectedHead *bool
}

// Checks reads the checks on the head of the held ticket's change, on the
// host, now.
func (s *Service) Checks(ctx context.Context, tenant, agentID uuid.UUID, expectedSHA string) (*Report, error) {
	if err := tenantOf(ctx, tenant); err != nil {
		return nil, err
	}
	gate, err := s.tickets.ChangeGateFor(ctx, agentID)
	if err != nil {
		return nil, err
	}
	rec, err := s.store.Current(ctx, gate.Attempt.TicketID, gate.Attempt.Number)
	if err != nil {
		return nil, err
	}
	checks, err := s.readChecks(ctx, tenant, agentID, rec, "checks")
	if err != nil {
		return nil, err
	}
	report := reportOf(s.hosts.ChangeURL(rec.Change), checks)
	if expectedSHA != "" {
		match := strings.EqualFold(expectedSHA, checks.HeadSHA)
		report.ForExpectedHead = &match
	}
	return report, nil
}

func reportOf(url string, checks *codehost.Checks) *Report {
	r := &Report{URL: url, HeadSHA: checks.HeadSHA, Base: checks.Base, State: checks.State.String(), Passed: checks.Passed()}
	for _, c := range checks.Checks {
		r.Checks = append(r.Checks, CheckLine{Name: c.Name, State: c.State.String(), TargetURL: c.TargetURL})
	}
	return r
}

// readChecks reads the change's checks from its host and audits the read.
func (s *Service) readChecks(ctx context.Context, tenant, agentID uuid.UUID, rec *Record, op string) (*codehost.Checks, error) {
	changes, ok := s.hosts.Changes(rec.Change.Host)
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNoHost, rec.Change.Host)
	}
	url := s.hosts.ChangeURL(rec.Change)
	checks, err := changes.Checks(ctx, tenant, rec.Change)
	if err != nil {
		s.record(ctx, audit.ChangeOperationFailed, s.attrs(tenant, agentID, rec.TicketID, op, "change", url, "err", err.Error())...)
		return nil, err
	}
	s.record(ctx, audit.ChangeChecksRead, s.attrs(tenant, agentID, rec.TicketID, op, "change", url, "head_sha", checks.HeadSHA, "state", checks.State.String())...)
	s.linkDriftOutcome(ctx, tenant, changes, rec, checks)
	return checks, nil
}

// MergeInput is a merge of the held ticket's change.
type MergeInput struct {
	// ExpectedSHA is optionally the commit the caller pushed and read the
	// checks of. When given, a head that is anything else refuses the merge.
	ExpectedSHA string
	// Batch, when set, is a batch CI run offered as the checks of this
	// change in place of its own and of "its head contains the base's tip"
	// (checkBatch). Nil is the change's own checks.
	Batch *BatchEvidence
	// CIPassed are checks the host still shows pending on ExpectedSHA that
	// the CI system itself has reported finished and successful (its status
	// never reached the host). The gate counts those, and only those, as
	// passed; it never asks the CI itself. A check is matched by name and
	// target URL, so a rerun that moved the check to another URL is not
	// covered. One that is not pending on the host, or a head other than
	// ExpectedSHA, ignores them.
	CIPassed []CheckLine
}

// Merged is a merge that happened.
type Merged struct {
	URL      string
	MergeSHA string
	// Closed is whether the ticket was closed as done, and with it the
	// agent's lease and integration lock ended. False when that failed after
	// the merge: the merge stands, and ticket_finish or the host's webhook
	// finishes the job.
	Closed bool
	// BatchTree is set for a merge on batch evidence: whether the host's
	// merge has the tree the batch tested. A mismatch stands merged; the
	// caller stops the batch there.
	BatchTree *BatchTree
}

// Merge merges the held ticket's change with a merge commit, the server
// doing it with the tenant's credential, once every part of the gate holds:
//
//   - the agent holds the integration lock;
//   - the ticket's approval, where it requires one, was given;
//   - the change is one slop-haus opened for this ticket, is not a draft and
//     is in the repository the ticket's scopes are in;
//   - the checks, read now, have passed on the change's head -- a head with
//     no checks has not passed;
//   - the head contains the base's tip, or lags it only by deploy-only
//     files (deployOnly).
//
// The merge is then asked of the host for exactly the head those checks
// were read for, so a push in between is refused by the host and not merged
// on the strength of checks that never ran on it. The lock and approval are
// read once more just before the merge is asked for. What stays outside the
// gate is what no read can close, as the merge is a call to another system:
// a person taking the lock or the approval away, or a check on that same
// commit failing, within the one call; the second needs the host's own
// branch protection.
func (s *Service) Merge(ctx context.Context, tenant, agentID uuid.UUID, in *MergeInput) (*Merged, error) {
	if err := tenantOf(ctx, tenant); err != nil {
		return nil, err
	}
	gate, err := s.tickets.ChangeGateFor(ctx, agentID)
	if err != nil {
		return nil, err
	}
	t := gate.Ticket
	rec, err := s.store.Current(ctx, gate.Attempt.TicketID, gate.Attempt.Number)
	if err != nil {
		return nil, s.refused(ctx, tenant, agentID, t.ID, "no_change", err)
	}
	tgt, err := s.checkGate(ctx, gate, rec)
	if err != nil {
		return nil, s.refused(ctx, tenant, agentID, t.ID, refusalReason(err), err)
	}
	url := s.hosts.ChangeURL(rec.Change)

	checks, err := s.readChecks(ctx, tenant, agentID, rec, "merge_checks")
	if err != nil {
		return nil, err
	}
	checks = withCIPassed(checks, in.ExpectedSHA, in.CIPassed)
	exactHead, err := s.checkHost(checks, in.ExpectedSHA, t, rec, tgt.base, in.Batch != nil)
	if err != nil {
		return nil, s.refused(ctx, tenant, agentID, t.ID, refusalReason(err), err, "head_sha", checks.HeadSHA, "reviewed_head_sha", rec.ReviewedHead)
	}
	changes, _ := s.hosts.Changes(rec.Change.Host)
	baseSHA := ""
	// Last of the reads of the host, so that what it says about the base's
	// tip is as recent as it can be before the merge.
	cmp, err := changes.CompareBase(ctx, tenant, rec.Change, checks.HeadSHA)
	if err != nil {
		s.record(ctx, audit.ChangeOperationFailed, s.attrs(tenant, agentID, t.ID, "merge_compare", "change", url, "head_sha", checks.HeadSHA, "err", err.Error())...)
		return nil, err
	}
	// The base is read again with the comparison, later than with the
	// checks: a retarget in between is seen. One after this is not, and no
	// host call can close it (the merge cannot pin the base); the host's
	// branch protection on the other branches does.
	if cmp.Base != tgt.base {
		err := fmt.Errorf("%w: it targets %q on the host, not %q", ErrWrongBase, cmp.Base, tgt.base)
		return nil, s.refused(ctx, tenant, agentID, t.ID, "wrong_base", err, "stage", "compare")
	}
	var drift *deployOnlyDrift
	// indep is what the independent-apps rule said about a head deploy-only
	// did not take. It is acted on only with the repo's enforce flag on;
	// until then it is a record beside the refusal, which stands.
	var indep *DriftDecision
	var batch *batchAcceptance
	if in.Batch != nil {
		batch, err = s.checkBatch(ctx, tenant, agentID, changes, rec, tgt, t, in.Batch, checks.HeadSHA, exactHead)
		if err != nil {
			var br *BatchRefusal
			more := []any{"head_sha", checks.HeadSHA, "batch_id", in.Batch.ID}
			if errors.As(err, &br) {
				more = append(more, "batch_reason", br.Reason)
			}
			return nil, s.refused(ctx, tenant, agentID, t.ID, refusalReason(err), err, more...)
		}
		drift, indep = batch.drift, batch.indep
	} else if !cmp.ContainsTip {
		var detail string
		drift, detail, err = s.deployOnly(ctx, tenant, agentID, changes, rec, checks.HeadSHA, tgt, exactHead)
		if err != nil && errors.Is(err, ErrBehindBase) {
			indep = s.independentApps(ctx, tenant, agentID, changes, rec, tgt, ticketPaths(t), checks.HeadSHA, exactHead)
			if tgt.skipRetestAcrossApps && indep.accepts() {
				err = nil
			}
		}
		if err != nil {
			more := []any{"head_sha", checks.HeadSHA, "deploy_only", detail}
			if indep != nil {
				more = append(more, indep.audit()...)
			}
			return nil, s.refused(ctx, tenant, agentID, t.ID, refusalReason(err), err, more...)
		}
	}
	viaIndependentApps := indep != nil && tgt.skipRetestAcrossApps && indep.accepts()
	if batch != nil {
		viaIndependentApps = indep != nil
	}
	approval := approvalNone
	if t.RequiresApproval {
		approval = approvalExactHead
	}
	if !exactHead {
		// A head the human was not shown is covered only by the same diff,
		// read now and only once the head is known to contain the base's tip:
		// then the tree that lands is the tip's plus that diff, which is
		// what was reviewed.
		diff, err := s.sameDiff(ctx, tenant, agentID, changes, rec, checks.HeadSHA, tgt.base, batch != nil)
		if err != nil {
			return nil, s.refused(ctx, tenant, agentID, t.ID, refusalReason(err), err, "head_sha", checks.HeadSHA,
				"reviewed_head_sha", rec.ReviewedHead, "reviewed_base_sha", rec.ReviewedBase, "stage", "diff")
		}
		approval = approvalSameDiff
		baseSHA = diff.BaseSHA
	}
	// The gate is read again as late as it can be: the lock and the approval
	// can be taken away by a person while the checks are read, and that is a
	// longer wait than the one between here and the host's answer.
	last, err := s.tickets.ChangeGateFor(ctx, agentID)
	if err == nil {
		var lastTgt *target
		lastTgt, err = s.checkGate(ctx, last, rec)
		// The rules a lagging head was let through by must still be the
		// repo's: an owner narrowing them meanwhile is a gate taken away.
		if err == nil && lastTgt.base != tgt.base {
			err = fmt.Errorf("%w (the repo's base branch changed while the merge was being checked)", behindBase(tgt.base))
		}
		if err == nil && drift != nil && !slices.Equal(lastTgt.deployOnly, tgt.deployOnly) {
			err = fmt.Errorf("%w (the repo's deploy-only files changed while the merge was being checked)", behindBase(tgt.base))
		}
		if err == nil && viaIndependentApps && !sameIndependentApps(lastTgt, tgt) || !slices.Equal(ticketPaths(last.Ticket), ticketPaths(gate.Ticket)) {
			err = fmt.Errorf("%w (the repo's independent apps changed while the merge was being checked)", behindBase(tgt.base))
		}
	}
	if err != nil {
		return nil, s.refused(ctx, tenant, agentID, t.ID, refusalReason(err), err, "stage", "before_merge")
	}
	t = last.Ticket
	m := &mergeAttempt{
		ticket: last.Ticket, rec: rec, approvedBy: last.Review.ApprovedByUserID, url: url, headSHA: checks.HeadSHA,
		approval: approval, baseSHA: baseSHA, drift: drift, indep: indep, viaIndependentApps: viaIndependentApps,
	}
	if batch != nil {
		m.batch = batch.batchMerge
	}
	merged, err := s.mergeOnHost(ctx, tenant, agentID, m)
	if err != nil || batch == nil {
		return merged, err
	}
	if batch.tip != "" {
		merged.BatchTree = s.verifyDriftedMerge(ctx, tenant, rec, batch, merged.MergeSHA)
	} else {
		merged.BatchTree = s.verifyBatchTree(ctx, tenant, tgt, batch.batchMerge, merged.MergeSHA)
	}
	if !merged.BatchTree.Matches {
		s.logger.Error("change: batch merge has a tree the batch did not test", "ticket", t.ID, "batch", in.Batch.ID, "why", merged.BatchTree.Why)
		s.record(ctx, audit.ChangeOperationFailed, s.attrs(tenant, agentID, t.ID, "batch_tree", "change", url, "batch_id", in.Batch.ID, "merge_sha", merged.MergeSHA, "err", merged.BatchTree.Why)...)
	}
	return merged, nil
}

// mergeAttempt is what Merge established before it asks the host to merge.
type mergeAttempt struct {
	ticket             *ticket.Ticket
	rec                *Record
	approvedBy         *uuid.UUID
	url                string
	headSHA            string
	approval           string
	baseSHA            string
	drift              *deployOnlyDrift
	indep              *DriftDecision
	viaIndependentApps bool
	// batch is the batch evidence the merge was accepted on, if any.
	batch *batchMerge
}

// mergeOnHost claims the merge and asks the host for it, then hands over to
// closeAfterMerge. A failed host call releases the claim and is not recorded
// as a merge.
func (s *Service) mergeOnHost(ctx context.Context, tenant, agentID uuid.UUID, m *mergeAttempt) (*Merged, error) {
	t, rec := m.ticket, m.rec
	changes, _ := s.hosts.Changes(rec.Change.Host)
	people := s.who(ctx, t, m.approvedBy, agentID)
	// Claimed for the merge: nothing replaces the change, nor merges it a
	// second time, while the host is being asked.
	claim, err := s.store.BeginMerge(ctx, rec.ID)
	if err != nil {
		return nil, err
	}
	if claim == nil {
		return nil, s.refused(ctx, tenant, agentID, t.ID, "merge_in_flight", ErrMergeInFlight)
	}
	merged, err := changes.Merge(ctx, tenant, &codehost.MergeRequest{
		Change: rec.Change, HeadSHA: m.headSHA,
		Subject: fmt.Sprintf("Merge %s: %s", rec.Head, singleLine(t.Title)),
		Body:    "Merged by slop-haus for agent " + people.Agent + "\n\n" + footer(t.ID, s.ticketURL(tenant, t.ID), people),
	})
	if err != nil {
		if endErr := s.store.EndMerge(ctx, claim); endErr != nil {
			s.logger.Error("change: release merge claim", "ticket", t.ID, "err", endErr)
		}
		s.record(ctx, audit.ChangeOperationFailed, s.attrs(tenant, agentID, t.ID, "merge", "change", m.url, "head_sha", m.headSHA, "err", err.Error())...)
		if errors.Is(err, codehost.ErrHeadMoved) {
			return nil, ErrHeadMoved
		}
		return nil, err
	}
	return s.closeAfterMerge(ctx, tenant, agentID, m, merged.SHA), nil
}

// closeAfterMerge records a merge the host has made: audit, the stored
// merge, a comment and closing the ticket. None of it can fail the merge.
func (s *Service) closeAfterMerge(ctx context.Context, tenant, agentID uuid.UUID, m *mergeAttempt, mergeSHA string) *Merged {
	t, rec, url := m.ticket, m.rec, m.url
	approver := ""
	if m.approvedBy != nil {
		approver = m.approvedBy.String()
	}
	mergedAttrs := []any{"head_sha", m.headSHA,
		"merge_sha", mergeSHA, "approved_by_user_id", approver, "requires_approval", t.RequiresApproval,
		"approval_match", m.approval, "reviewed_head_sha", rec.ReviewedHead, "base_sha", m.baseSHA, "reviewed_base_sha", rec.ReviewedBase,
		"behind_deploy_only", m.drift != nil}
	if m.drift != nil {
		mergedAttrs = append(mergedAttrs, "deploy_only_files", m.drift.files, "deploy_only_tip_sha", m.drift.tipSHA)
	}
	if m.batch != nil {
		mergedAttrs = append(mergedAttrs, "batch_id", m.batch.evidence.ID, "batch_position", m.batch.position+1)
	}
	if m.viaIndependentApps {
		mergedAttrs = append(mergedAttrs, "behind_independent_apps", true)
		mergedAttrs = append(mergedAttrs, m.indep.audit()...)
	}
	s.record(ctx, audit.ChangeMerged, s.attrs(tenant, agentID, t.ID, "merge", append([]any{"change", url}, mergedAttrs...)...)...)

	// The merge has happened: failing to write it down is logged, not
	// returned, since an agent told the merge failed would try it again.
	if recorded, err := s.store.MarkMerged(ctx, rec.ID, agentID, mergeSHA); err != nil || !recorded {
		s.logger.Error("change: record merge", "ticket", t.ID, "change", url, "recorded", recorded, "err", err)
	}
	if _, err := s.tickets.AddComment(ctx, t.ID, ticket.AgentAuthor(agentID), fmt.Sprintf("Merged %s (merge commit %s) on head %s%s.", url, mergeSHA, m.headSHA, approvalNote(m.approval, rec.ReviewedHead))); err != nil {
		s.logger.Warn("change: comment on merge", "ticket", t.ID, "err", err)
	}
	// Never reported as a failed merge: the change is on the base branch
	// whatever happens here.
	_, err := s.tickets.Done(ctx, agentID, &ticket.DoneParams{PRURL: url})
	if err != nil {
		s.logger.Warn("change: close ticket after merge", "ticket", t.ID, "change", url, "err", err)
		s.record(ctx, audit.ChangeOperationFailed, s.attrs(tenant, agentID, t.ID, "close_after_merge", "change", url, "err", err.Error())...)
	}
	return &Merged{URL: url, MergeSHA: mergeSHA, Closed: err == nil}
}

// checkGate is the part of the gate that needs no call to the host.
// It returns the repository the change is in.
func (s *Service) checkGate(ctx context.Context, gate *ticket.ChangeGate, rec *Record) (*target, error) {
	t := gate.Ticket
	switch {
	case t.NeedsDiscussion:
		return nil, fmt.Errorf("%w (ticket #%d)", ticket.ErrNeedsDiscussion, t.ID)
	case !gate.Promoted:
		return nil, ticket.ErrAttemptAwaitsChoice
	case !gate.HoldsLock:
		return nil, ErrLockNotHeld
	case t.RequiresApproval && gate.Review.ApprovedAt == nil:
		return nil, ErrNotApproved
	case rec.MergedAt != nil:
		return nil, ErrAlreadyMerged
	case rec.Draft:
		return nil, ErrDraft
	}
	tgt, err := s.targetFor(ctx, t)
	if err != nil {
		return nil, err
	}
	// The lock is the one of the branch the change merges into: another
	// branch's lock does not serialise this merge with the ones into it.
	if gate.HeldLock != tgt.lock {
		return nil, ErrLockNotHeld
	}
	if rec.Base != tgt.base {
		return nil, fmt.Errorf("%w: it targets %s, not %s", ErrWrongBase, rec.Base, tgt.base)
	}
	if tgt.host != rec.Change.Host || tgt.repo != rec.Change.InRepo() {
		return nil, ErrWrongRepo
	}
	return tgt, nil
}

// checkHost is the part of the gate that is about what the host said when
// the checks were read: the base the change targets there now, the checks on
// its head and, where the ticket requires approval, that this head is the
// one the human was asked to review.
//
// exactHead is whether the head is the reviewed one (or no review is needed).
// A head that is not it is returned without an error only when a diff
// fingerprint was recorded, for the caller to compare once the head is known
// to contain the base's tip.
//
// With batch set the change's own checks are not required to pass: the
// batch's run stands in for them (checkBatch).
func (s *Service) checkHost(checks *codehost.Checks, expectedSHA string, t *ticket.Ticket, rec *Record, base string, batch bool) (exactHead bool, err error) {
	// The host merges into whatever the change targets now, which a person
	// with write access can have changed since slop-haus opened it.
	if checks.Base != base {
		return false, fmt.Errorf("%w: it targets %q on the host, not %q", ErrWrongBase, checks.Base, base)
	}
	if batch {
		err = headNamed(checks, expectedSHA)
	} else {
		err = passed(checks, expectedSHA)
	}
	if err != nil {
		return false, err
	}
	if !t.RequiresApproval || strings.EqualFold(rec.ReviewedHead, checks.HeadSHA) {
		return true, nil
	}
	if rec.ReviewedHead == "" || rec.ReviewedDiff == "" {
		return false, fmt.Errorf("%w: it is %s, the reviewed one is %s, and no diff was recorded to compare it with", ErrHeadNotReviewed, checks.HeadSHA, orNone(rec.ReviewedHead))
	}
	// A fingerprint made by another strategy (or version) says nothing about
	// what this one would compute for the same diff.
	if !strings.HasPrefix(rec.ReviewedDiff, s.detect.Prefix()) {
		return false, fmt.Errorf("%w: it is %s, the reviewed one is %s, and the recorded diff was fingerprinted by another method", ErrHeadNotReviewed, checks.HeadSHA, rec.ReviewedHead)
	}
	return false, nil
}

// How an approval was matched to the head that merges, as the audit says.
const (
	approvalNone      = "not_required"
	approvalExactHead = "exact_head"
	approvalSameDiff  = "same_diff"
)

func approvalNote(approval, reviewedHead string) string {
	if approval == approvalSameDiff {
		return " (approved at " + reviewedHead + "; same changes against the base)"
	}
	return ""
}

// sameDiff reads the change's diff against the base now and requires it to
// be the one the human reviewed. It fails closed: a diff that cannot be read
// or compared, and one that differs, are both ErrHeadNotReviewed.
//
// A batch member need not contain the tip: the members ahead of it are on the
// base but not in its head, and the batch tested it with them. Its diff
// against the merge base is the same either way, and that is what is compared.
func (s *Service) sameDiff(ctx context.Context, tenant, agentID uuid.UUID, changes codehost.Changes, rec *Record, headSHA, base string, batch bool) (*codehost.Diff, error) {
	url := s.hosts.ChangeURL(rec.Change)
	diff, err := changes.Diff(ctx, tenant, rec.Change, headSHA)
	if err != nil {
		s.record(ctx, audit.ChangeOperationFailed, s.attrs(tenant, agentID, rec.TicketID, "merge_diff", "change", url, "head_sha", headSHA, "err", err.Error())...)
		return nil, fmt.Errorf("%w: its changes against the base could not be compared with the reviewed ones: %w", ErrHeadNotReviewed, err)
	}
	switch {
	case diff.Base != base:
		return nil, fmt.Errorf("%w: it targets %q on the host, not %q", ErrWrongBase, diff.Base, base)
	case !batch && !diff.ContainsTip():
		return nil, fmt.Errorf("%w (the base moved while its diff was read)", behindBase(base))
	}
	got, err := s.detect.Fingerprint(diff)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrHeadNotReviewed, err)
	}
	if subtle.ConstantTimeCompare([]byte(got), []byte(rec.ReviewedDiff)) != 1 {
		return nil, fmt.Errorf("%w: its changes against the base differ from the reviewed ones (head %s, reviewed %s)", ErrHeadNotReviewed, headSHA, rec.ReviewedHead)
	}
	return diff, nil
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

// headNamed is whether the host named the head the checks were read for, and
// it is the one the caller expected.
func headNamed(checks *codehost.Checks, expectedSHA string) error {
	if checks.HeadSHA == "" {
		return fmt.Errorf("%w: the host named no head commit", ErrChecksNotPassed)
	}
	if expectedSHA != "" && !strings.EqualFold(expectedSHA, checks.HeadSHA) {
		return fmt.Errorf("%w: the change's head is %s, not %s", ErrHeadMismatch, checks.HeadSHA, expectedSHA)
	}
	return nil
}

// passed is whether the checks, read for this head, let it merge.
// withCIPassed returns the checks with the named pending ones counted as
// passed, re-deriving the overall state by the host's own rule. The caller's
// claim is bound to the head it was read for.
func withCIPassed(checks *codehost.Checks, expectedSHA string, vouched []CheckLine) *codehost.Checks {
	if len(vouched) == 0 || expectedSHA == "" || !strings.EqualFold(expectedSHA, checks.HeadSHA) {
		return checks
	}
	lines := slices.Clone(checks.Checks)
	for i := range lines {
		ref := CheckLine{Name: lines[i].Name, TargetURL: lines[i].TargetURL}
		if lines[i].State == codehost.CheckPending && slices.ContainsFunc(vouched, func(v CheckLine) bool { return v.Name == ref.Name && v.TargetURL == ref.TargetURL }) {
			lines[i].State = codehost.CheckSuccess
		}
	}
	out := codehost.NewChecks(checks.HeadSHA, checks.Base, lines)
	out.Title = checks.Title
	return out
}

func passed(checks *codehost.Checks, expectedSHA string) error {
	if err := headNamed(checks, expectedSHA); err != nil {
		return err
	}
	if checks.Passed() {
		return nil
	}
	if checks.State == codehost.CheckNone {
		return fmt.Errorf("%w: no checks have reported on %s, and no checks is not a pass (CI may not have started: wait, then read them again)", ErrChecksNotPassed, checks.HeadSHA)
	}
	var names []string
	for _, c := range checks.Checks {
		if c.State == codehost.CheckFailure || c.State == codehost.CheckPending {
			names = append(names, c.Name+" "+c.State.String())
		}
	}
	return fmt.Errorf("%w: %s on %s (%s)", ErrChecksNotPassed, checks.State, checks.HeadSHA, strings.Join(names, ", "))
}

func (s *Service) refused(ctx context.Context, tenant, agentID uuid.UUID, ticketID int64, reason string, err error, more ...any) error {
	s.record(ctx, audit.ChangeMergeRefused, s.attrs(tenant, agentID, ticketID, "merge", append([]any{"reason", reason}, more...)...)...)
	return err
}

// refusalReason is the short, stable name an audit record gives a refusal.
func refusalReason(err error) string {
	switch {
	case errors.Is(err, ErrLockNotHeld):
		return "lock_not_held"
	case errors.Is(err, ErrNotApproved):
		return "not_approved"
	case errors.Is(err, ErrDraft):
		return "draft"
	case errors.Is(err, ErrAlreadyMerged):
		return "already_merged"
	case errors.Is(err, ErrWrongRepo):
		return "wrong_repo"
	case errors.Is(err, ErrWrongBase):
		return "wrong_base"
	case errors.Is(err, ErrNoRepo):
		return "no_repo"
	case errors.Is(err, ErrHeadNotReviewed):
		return "head_not_reviewed"
	case errors.Is(err, ErrBehindBase):
		return "behind_base"
	case errors.Is(err, ErrHeadMismatch):
		return "head_mismatch"
	case errors.Is(err, ErrChecksNotPassed):
		return "checks_not_passed"
	case errors.Is(err, ErrBatchEvidence):
		return "batch_evidence"
	case errors.Is(err, ticket.ErrNeedsDiscussion):
		return "needs_discussion"
	case errors.Is(err, ErrNoChange):
		return "no_change"
	}
	return "other"
}

// behindBase is ErrBehindBase with the remedy, which names the configured
// base branch: it is not always main.
func behindBase(base string) error {
	return fmt.Errorf("%w: merge origin/%s into your branch, push, and wait for the checks on that push", ErrBehindBase, base)
}

func ticketPaths(t *ticket.Ticket) []string {
	out := make([]string, len(t.Paths))
	for i, p := range t.Paths {
		out[i] = p.Path
	}
	return out
}

// sameIndependentApps is whether the rule's inputs, and whether it is acted
// on, are what they were when it was read.
func sameIndependentApps(a, b *target) bool {
	return a.repoPath == b.repoPath && a.skipRetestAcrossApps == b.skipRetestAcrossApps && slices.Equal(a.independentApps, b.independentApps) &&
		slices.Equal(a.shared, b.shared) && slices.Equal(a.deployOnly, b.deployOnly)
}
