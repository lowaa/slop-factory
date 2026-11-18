package change

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// ErrBatchEvidence is a batch CI run that is not evidence for the merge it
// was presented with. The *BatchRefusal wrapping it says which condition
// failed.
var ErrBatchEvidence = errors.New("change: the batch's CI run is not evidence for this merge")

// Why a batch run was refused as evidence, as the audit names it.
const (
	batchNotMember          = "not_member"
	batchHeadChanged        = "head_changed"
	batchNotContained       = "not_contained"
	batchBaseNotInHead      = "base_not_in_head"
	batchOutOfOrder         = "out_of_order"
	batchPredecessorDropped = "predecessor_dropped"
	batchDisturbed          = "main_disturbed"
	batchChecksNotGreen     = "checks_not_green"
	batchChecksOtherHead    = "checks_other_head"
	batchBranchMoved        = "batch_branch_moved"
	batchInvalid            = "invalid_evidence"
	batchNoTreeReader       = "no_tree_reader"
)

// BatchRefusal is ErrBatchEvidence with the condition that failed.
type BatchRefusal struct {
	Reason string
	Detail string
}

func (e *BatchRefusal) Error() string {
	return fmt.Sprintf("%v (%s): %s", ErrBatchEvidence, e.Reason, e.Detail)
}

// MainMoved is whether the refusal is main moving in a way the drift rules do
// not allow, which voids the batch rather than faulting its evidence.
func (e *BatchRefusal) MainMoved() bool { return e.Reason == batchDisturbed }

func (e *BatchRefusal) Unwrap() error { return ErrBatchEvidence }

func batchRefused(reason, format string, args ...any) error {
	return &BatchRefusal{Reason: reason, Detail: fmt.Sprintf(format, args...)}
}

// BatchMember is one change of a batch, in the order the batch tested them.
type BatchMember struct {
	TicketID int64
	// HeadSHA is the member's head the batch branch merged.
	HeadSHA string
	// PrefixSHA is the batch branch's commit after this member was merged
	// into it (base + the members up to and including this one).
	PrefixSHA string
	// MergeSHA is the merge commit this member got on the base branch; empty
	// until it has merged.
	MergeSHA string
	// Dropped is a member the runner handed back: the tested tree held it,
	// and main never will.
	Dropped bool
}

// BatchEvidence is a batch CI run offered as the checks for one of its
// members (merge_queue_batching.md, question 3). The runner builds it from
// its batch row; the gate trusts none of it that the host can say.
type BatchEvidence struct {
	ID int64
	// Branch is the batch branch and BaseSHA the base-branch commit it was
	// built on.
	Branch  string
	BaseSHA string
	Members []BatchMember
	// Checks are the checks read for the batch branch's head, which is the
	// last member's PrefixSHA.
	Checks *codehost.Checks
	// CIPassed are checks pending on the host that the CI said passed, as
	// MergeInput.CIPassed is for a change's own checks. They count only for
	// the head Checks was read for.
	CIPassed []CheckLine
}

func (e *BatchEvidence) position(ticketID int64) int {
	for i := range e.Members {
		if e.Members[i].TicketID == ticketID {
			return i
		}
	}
	return -1
}

// TreeReader reads the tree a commit has. It is optional on the Service, but
// a merge on batch evidence is refused without it: the tree check after each
// member merges is what makes the unpinnable window of the host's merge
// observable.
type TreeReader interface {
	TreeOf(ctx context.Context, tenant uuid.UUID, repo codehost.Repo, sha string) (string, error)
}

// BatchTree is the result of comparing the host's merge of a member with the
// batch branch's commit for it.
type BatchTree struct {
	Matches bool
	// Why is what did not match, or could not be read.
	Why string
}

// batchMerge is a member whose batch evidence the gate accepted.
type batchMerge struct {
	evidence *BatchEvidence
	position int
}

func (b *batchMerge) member() *BatchMember { return &b.evidence.Members[b.position] }

// batchAcceptance is what checkBatch established besides the member.
type batchAcceptance struct {
	*batchMerge
	drift *deployOnlyDrift
	indep *DriftDecision
	// tip and ours are set with drift or indep: the tip the gate accepted and
	// the files the member's change touches, for the check of the merge.
	tip  string
	ours map[string]bool
}

// checkBatch decides whether the batch's run counts as the checks of the
// ticket's change, in place of "its own head passed and contains the base's
// tip". All of these hold, or it is refused:
//
//   - member: the ticket is one of the batch's, and not a dropped one;
//   - contained: the head read now is the one the batch merged, and the batch
//     branch contains it;
//   - ordered: every member ahead of it has merged, none was dropped;
//   - undisturbed: the base's tip is the batch base plus the members already
//     merged, or lags that only by drift the per-change rules allow;
//   - green: the batch branch's head has not moved from the one checked, and
//     those checks passed.
//
// The head must also contain the batch base.
func (s *Service) checkBatch(ctx context.Context, tenant, agentID uuid.UUID, changes codehost.Changes, rec *Record, tgt *target, t *ticket.Ticket, ev *BatchEvidence, headSHA string, exactHead bool) (*batchAcceptance, error) {
	if ev.Branch == "" || ev.BaseSHA == "" || len(ev.Members) == 0 || ev.Checks == nil {
		return nil, batchRefused(batchInvalid, "batch %d names no branch, base, members or checks", ev.ID)
	}
	if s.trees == nil {
		return nil, batchRefused(batchNoTreeReader, "the merged tree cannot be verified against the batch's")
	}
	pos := ev.position(t.ID)
	if pos < 0 || ev.Members[pos].Dropped {
		return nil, batchRefused(batchNotMember, "ticket #%d is not a member of batch %d", t.ID, ev.ID)
	}
	m := &ev.Members[pos]
	if !strings.EqualFold(m.HeadSHA, headSHA) {
		return nil, batchRefused(batchHeadChanged, "the batch tested %s, the change's head is %s", m.HeadSHA, headSHA)
	}
	for i := 0; i < pos; i++ {
		if ev.Members[i].Dropped {
			return nil, batchRefused(batchPredecessorDropped, "ticket #%d ahead of this one in batch %d was dropped, and the tested tree held it", ev.Members[i].TicketID, ev.ID)
		}
	}
	for i := 0; i < pos; i++ {
		if ev.Members[i].MergeSHA == "" {
			return nil, batchRefused(batchOutOfOrder, "ticket #%d is ahead of this one in batch %d and has not merged", ev.Members[i].TicketID, ev.ID)
		}
	}

	// Green: the checks are for the head the batch branch has now.
	batchHead := ev.Members[len(ev.Members)-1].PrefixSHA
	if !strings.EqualFold(ev.Checks.HeadSHA, batchHead) {
		return nil, batchRefused(batchChecksOtherHead, "the checks are for %s, the batch's head is %s", ev.Checks.HeadSHA, batchHead)
	}
	if err := passed(withCIPassed(ev.Checks, batchHead, ev.CIPassed), ""); err != nil {
		return nil, batchRefused(batchChecksNotGreen, "%v", err)
	}
	now, err := changes.BranchHead(ctx, tenant, tgt.repo, ev.Branch)
	if err != nil {
		return nil, fmt.Errorf("change: read batch branch %s: %w", ev.Branch, err)
	}
	if !strings.EqualFold(now.SHA, batchHead) {
		return nil, batchRefused(batchBranchMoved, "branch %s is at %s now, the checks were for %s", ev.Branch, now.SHA, batchHead)
	}

	// Contained.
	in, err := changes.BranchContains(ctx, tenant, tgt.repo, ev.Branch, headSHA)
	if err != nil {
		return nil, fmt.Errorf("change: read whether batch branch %s contains %s: %w", ev.Branch, headSHA, err)
	}
	if !in {
		return nil, batchRefused(batchNotContained, "batch branch %s does not contain %s", ev.Branch, headSHA)
	}
	has, err := changes.BranchContains(ctx, tenant, tgt.repo, rec.Head, ev.BaseSHA)
	if err != nil {
		return nil, fmt.Errorf("change: read whether %s contains the batch base %s: %w", rec.Head, ev.BaseSHA, err)
	}
	if !has {
		return nil, batchRefused(batchBaseNotInHead, "the change's branch %s does not contain the batch base %s", rec.Head, ev.BaseSHA)
	}

	// Undisturbed.
	expected := ev.BaseSHA
	if pos > 0 {
		expected = ev.Members[pos-1].MergeSHA
	}
	tip, err := changes.BranchHead(ctx, tenant, tgt.repo, tgt.base)
	if err != nil {
		return nil, fmt.Errorf("change: read the tip of %s: %w", tgt.base, err)
	}
	acc := &batchAcceptance{batchMerge: &batchMerge{evidence: ev, position: pos}}
	if strings.EqualFold(tip.SHA, expected) {
		return acc, nil
	}
	if err := s.batchDrift(ctx, tenant, agentID, changes, rec, tgt, t, acc, headSHA, exactHead, expected, tip.SHA); err != nil {
		return nil, err
	}
	return acc, nil
}

// batchDrift decides whether a base tip that is not the one the batch
// predicts lags it only by what a head behind the base may lag today: the
// deploy-only files, and with the repo's enforce flag the independent apps.
// The commits judged are those since the predicted tip, not the head's merge
// base, which for a later member includes the earlier members.
func (s *Service) batchDrift(ctx context.Context, tenant, agentID uuid.UUID, changes codehost.Changes, rec *Record, tgt *target, t *ticket.Ticket, acc *batchAcceptance, headSHA string, exactHead bool, expected, tip string) error {
	disturbed := func(format string, args ...any) error {
		return batchRefused(batchDisturbed, "%s moved from %s to %s: %s", tgt.base, expected, tip, fmt.Sprintf(format, args...))
	}
	if !exactHead {
		return disturbed("the approval covers this head only by its diff")
	}
	url := s.hosts.ChangeURL(rec.Change)
	failed := func(op string, err error) {
		s.record(ctx, audit.ChangeOperationFailed, s.attrs(tenant, agentID, rec.TicketID, op, "change", url, "head_sha", headSHA, "err", err.Error())...)
	}
	diff, err := changes.Diff(ctx, tenant, rec.Change, headSHA)
	if err != nil {
		failed("merge_batch_drift", err)
		return disturbed("the change's own files could not be read: %v", err)
	}
	if diff.Base != tgt.base {
		return fmt.Errorf("%w: it targets %q on the host, not %q", ErrWrongBase, diff.Base, tgt.base)
	}
	since, err := changes.CompareCommits(ctx, tenant, rec.Change, expected, tip)
	if err != nil {
		failed("merge_batch_drift", err)
		return disturbed("what changed since %s could not be read: %v", expected, err)
	}
	if since.BaseSHA != expected || since.MergeBaseSHA != expected {
		return disturbed("%s is not an ancestor of the tip", expected)
	}
	ours := make(map[string]bool, len(diff.Files))
	for _, f := range diff.Files {
		ours[f.Path] = true
		if f.PreviousPath != "" {
			ours[f.PreviousPath] = true
		}
	}
	var why string
	for i := range since.Files {
		if why = deployOnlyFile(&since.Files[i], tgt.deployOnly, ours); why != "" {
			break
		}
	}
	if why == "" {
		acc.drift, acc.tip, acc.ours = &deployOnlyDrift{files: len(since.Files), tipSHA: tip}, tip, ours
		return nil
	}
	if !tgt.skipRetestAcrossApps {
		return disturbed("%s", why)
	}
	d := decideDrift(&driftRule{RepoPath: tgt.repoPath, Apps: tgt.independentApps, Shared: tgt.shared, DeployOnly: tgt.deployOnly, TicketPaths: ticketPaths(t)}, since.Files, ours)
	d.TicketID, d.ChangeID, d.HeadSHA, d.TipSHA, d.Enforced = rec.TicketID, rec.ID, headSHA, tip, true
	if !d.accepts() {
		return disturbed("%s", d.Reason)
	}
	acc.indep, acc.tip, acc.ours = &d, tip, ours
	return nil
}

// verifyBatchTree compares the tree of the host's merge of the member with
// the batch branch's commit for it. The host cannot pin the base of a merge,
// so a commit landing between the gate's read and the merge is not
// preventable; this makes it visible.
func (s *Service) verifyBatchTree(ctx context.Context, tenant uuid.UUID, tgt *target, b *batchMerge, mergeSHA string) *BatchTree {
	prefix := b.member().PrefixSHA
	merged, err := s.trees.TreeOf(ctx, tenant, tgt.repo, mergeSHA)
	if err != nil {
		return &BatchTree{Why: fmt.Sprintf("the tree of merge commit %s could not be read: %v", mergeSHA, err)}
	}
	tested, err := s.trees.TreeOf(ctx, tenant, tgt.repo, prefix)
	if err != nil {
		return &BatchTree{Why: fmt.Sprintf("the tree of batch commit %s could not be read: %v", prefix, err)}
	}
	if merged == "" || merged != tested {
		return &BatchTree{Why: fmt.Sprintf("merge commit %s has tree %q, the batch tested %s with tree %q", mergeSHA, merged, prefix, tested)}
	}
	return &BatchTree{Matches: true}
}

// verifyDriftedMerge checks a merge made on a tip the gate accepted by drift:
// the trees cannot be compared with the batch's, but the merge commit must
// add nothing to that tip beyond the member's own files, which shows a commit
// landing between the gate's read and the merge.
func (s *Service) verifyDriftedMerge(ctx context.Context, tenant uuid.UUID, rec *Record, b *batchAcceptance, mergeSHA string) *BatchTree {
	host, ok := s.hosts.Changes(rec.Change.Host)
	if !ok {
		return &BatchTree{Why: fmt.Sprintf("the host of merge commit %s cannot be read", mergeSHA)}
	}
	added, err := host.CompareCommits(ctx, tenant, rec.Change, b.tip, mergeSHA)
	if err != nil {
		return &BatchTree{Why: fmt.Sprintf("what merge commit %s adds to %s could not be read: %v", mergeSHA, b.tip, err)}
	}
	for i := range added.Files {
		f := &added.Files[i]
		if !b.ours[f.Path] {
			return &BatchTree{Why: fmt.Sprintf("merge commit %s adds %s to %s, which is not a file of the change", mergeSHA, f.Path, b.tip)}
		}
	}
	return &BatchTree{Matches: true}
}
