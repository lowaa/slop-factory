package change_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/change"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

const (
	baseOld = "1111111111111111111111111111111111111111"
	baseNew = "2222222222222222222222222222222222222222"
)

const (
	hunkOne = "@@ -10,3 +10,4 @@ func one()\n a\n+b\n c\n d\n"
	hunkTwo = "@@ -40,2 +41,3 @@\n e\n-f\n+g\n+h\n"
)

// diffOf is the change's diff against a base tip that the head contains.
func diffOf(base string, files ...codehost.FileDiff) *codehost.Diff {
	return &codehost.Diff{Base: "main", BaseSHA: base, MergeBaseSHA: base, Files: files}
}

func modified(path, patch string) codehost.FileDiff {
	return codehost.FileDiff{Path: path, Status: codehost.DiffModified, Patch: patch}
}

// reviewedAtA is the change handed over at headA with diff, approved, and
// then main taken in: the host now reports headB, green.
func (f *fixture) reviewedAtA(reviewed *codehost.Diff) *holder {
	f.t.Helper()
	h := f.hold(true)
	f.open(h, false)
	f.host.DiffOf = map[string]*codehost.Diff{headA: reviewed}
	f.handOverAndApprove(h)
	f.setChecks(headB, codehost.Check{Name: "ci/build", State: codehost.CheckSuccess})
	return h
}

func (f *fixture) headBDiff(d *codehost.Diff) {
	f.host.DiffOf[headB] = d
}

func (f *fixture) requireRefused(h *holder) error {
	f.t.Helper()
	_, err := f.merge(h, "")
	require.ErrorIs(f.t, err, change.ErrHeadNotReviewed)
	assert.Empty(f.t, f.host.Merges())
	lines := f.log.audited(f.t, audit.ChangeMergeRefused)
	require.NotEmpty(f.t, lines)
	assert.Equal(f.t, "head_not_reviewed", lines[len(lines)-1]["reason"])
	return err
}

func TestReview_RecordsTheDiffFingerprintAndBase(t *testing.T) {
	f := newFixture(t)
	h := f.reviewedAtA(diffOf(baseOld, modified("a.go", hunkOne)))

	rec, err := f.store.Current(f.ctx, h.ticket.ID, 1)
	require.NoError(t, err)
	want, err := codehost.LineNumberChangeDetectionStrategy{}.Fingerprint(diffOf(baseOld, modified("a.go", hunkOne)))
	require.NoError(t, err)
	assert.Equal(t, want, rec.ReviewedDiff)
	assert.Equal(t, baseOld, rec.ReviewedBase)
	assert.Equal(t, headA, rec.ReviewedHead)
	line := f.log.audited(t, audit.ChangeReviewRequested)[0]
	assert.Equal(t, baseOld, line["base_sha"])
	assert.Equal(t, want, line["diff_fingerprint"])
}

// The case the rule exists for: main moved elsewhere (other files, or the
// change's files below its hunks), the agent merged it in and pushed, and the
// diff against main is the reviewed one. Only the function-context text
// after the second @@ differs.
func TestMerge_ACleanMainMergeWithTheSameDiffKeepsTheApproval(t *testing.T) {
	f := newFixture(t)
	h := f.reviewedAtA(diffOf(baseOld, modified("a.go", hunkOne), modified("b.go", hunkTwo)))
	renamed := "@@ -10,3 +10,4 @@ func renamedByMain()\n a\n+b\n c\n d\n"
	f.headBDiff(diffOf(baseNew, modified("b.go", hunkTwo), modified("a.go", renamed)))

	merged, err := f.merge(h, "")

	require.NoError(t, err)
	assert.Equal(t, "mergecommit", merged.MergeSHA)
	require.Len(t, f.host.Merges(), 1)
	assert.Equal(t, headB, f.host.Merges()[0].Merge.HeadSHA)
	line := f.log.audited(t, audit.ChangeMerged)[0]
	assert.Equal(t, "same_diff", line["approval_match"])
	assert.Equal(t, headB, line["head_sha"])
	assert.Equal(t, headA, line["reviewed_head_sha"])
	assert.Equal(t, baseNew, line["base_sha"])
	assert.Equal(t, baseOld, line["reviewed_base_sha"])
}

func TestMerge_TheReviewedHeadMergesAsAnExactMatchWithoutReadingTheDiff(t *testing.T) {
	f := newFixture(t)
	h := f.hold(true)
	f.open(h, false)
	f.host.DiffOf = map[string]*codehost.Diff{headA: diffOf(baseOld, modified("a.go", hunkOne))}
	f.handOverAndApprove(h)
	before := len(f.host.Diffed())

	_, err := f.merge(h, "")

	require.NoError(t, err)
	assert.Equal(t, before, len(f.host.Diffed()))
	assert.Equal(t, "exact_head", f.log.audited(t, audit.ChangeMerged)[0]["approval_match"])
}

func TestMerge_ADifferentDiffIsRefused(t *testing.T) {
	reviewed := func() *codehost.Diff { return diffOf(baseOld, modified("a.go", hunkOne), modified("b.go", hunkTwo)) }
	tests := map[string]*codehost.Diff{
		"a hunk gained": diffOf(baseNew, modified("a.go", hunkOne), modified("b.go", hunkTwo),
			modified("c.go", "@@ -1 +1 @@\n-x\n+y\n")),
		"a hunk lost":               diffOf(baseNew, modified("a.go", hunkOne)),
		"a hunk of one file gained": diffOf(baseNew, modified("a.go", hunkOne+hunkTwo), modified("b.go", hunkTwo)),
		"a line changed within a hunk": diffOf(baseNew, modified("a.go", "@@ -10,3 +10,4 @@\n a\n+EVIL\n c\n d\n"),
			modified("b.go", hunkTwo)),
		"a context line changed (main touched the neighbourhood)": diffOf(baseNew,
			modified("a.go", "@@ -10,3 +10,4 @@\n a\n+b\n CHANGED-BY-MAIN\n d\n"), modified("b.go", hunkTwo)),
		"main shifted the lines above a hunk": diffOf(baseNew,
			modified("a.go", "@@ -15,3 +15,4 @@ func one()\n a\n+b\n c\n d\n"), modified("b.go", hunkTwo)),
		"the hunk text moved to another place in the file": diffOf(baseNew,
			modified("a.go", "@@ -210,3 +210,4 @@ func one()\n a\n+b\n c\n d\n"), modified("b.go", hunkTwo)),
		"only the new side moved": diffOf(baseNew,
			modified("a.go", "@@ -10,3 +11,4 @@\n a\n+b\n c\n d\n"), modified("b.go", hunkTwo)),
		"a file's status changed": diffOf(baseNew, modified("a.go", hunkOne),
			codehost.FileDiff{Path: "b.go", Status: codehost.DiffAdded, Patch: hunkTwo}),
		"a file moved to another path": diffOf(baseNew, modified("a.go", hunkOne), modified("sub/b.go", hunkTwo)),
	}
	for name, got := range tests {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			h := f.reviewedAtA(reviewed())
			f.headBDiff(got)

			err := f.requireRefused(h)

			assert.Contains(t, err.Error(), "differ")
		})
	}
}

func TestMerge_ADiffThatCannotBeComparedIsRefused(t *testing.T) {
	good := func() *codehost.Diff { return diffOf(baseNew, modified("a.go", hunkOne)) }
	tests := map[string]func(f *fixture){
		"truncated": func(f *fixture) {
			f.host.DiffErr = codehost.ErrDiffUnsupported
		},
		"the host failed": func(f *fixture) { f.host.DiffErr = codehost.ErrUnreachable },
		"the head is not known to the host": func(f *fixture) {
			delete(f.host.DiffOf, headB)
		},
		"unparseable patch": func(f *fixture) {
			f.headBDiff(diffOf(baseNew, modified("a.go", "not a patch\n")))
		},
		"hunk shorter than its header": func(f *fixture) {
			f.headBDiff(diffOf(baseNew, modified("a.go", "@@ -10,3 +10,4 @@\n a\n+b\n")))
		},
		"binary file with no patch": func(f *fixture) {
			f.headBDiff(diffOf(baseNew, modified("a.go", "")))
		},
		"renamed file": func(f *fixture) {
			f.headBDiff(diffOf(baseNew, codehost.FileDiff{Path: "a.go", Status: codehost.DiffRenamed, PreviousPath: "z.go", Patch: hunkOne}))
		},
		"no files": func(f *fixture) { f.headBDiff(diffOf(baseNew)) },
	}
	for name, break_ := range tests {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			h := f.reviewedAtA(diffOf(baseOld, modified("a.go", hunkOne)))
			f.headBDiff(good())
			break_(f)

			f.requireRefused(h)
		})
	}
}

func TestMerge_AMainChangeThatOverlapsTheHunksIsRefused(t *testing.T) {
	f := newFixture(t)
	h := f.reviewedAtA(diffOf(baseOld, modified("a.go", "@@ -10,3 +10,3 @@\n a\n-b\n+B\n c\n")))
	// Main rewrote the line the change replaced, so the hunk now removes
	// something else.
	f.headBDiff(diffOf(baseNew, modified("a.go", "@@ -10,3 +10,3 @@\n a\n-b2\n+B\n c\n")))

	f.requireRefused(h)
}

// A head that does not contain the base's tip has a diff that is not the
// tip plus the change, whatever it says.
func TestMerge_ADiffReadAgainstAMovedBaseIsNotTrusted(t *testing.T) {
	f := newFixture(t)
	h := f.reviewedAtA(diffOf(baseOld, modified("a.go", hunkOne)))
	moved := diffOf(baseNew, modified("a.go", hunkOne))
	moved.MergeBaseSHA = baseOld
	f.headBDiff(moved)

	_, err := f.merge(h, "")

	require.ErrorIs(t, err, change.ErrBehindBase)
	assert.Empty(t, f.host.Merges())
}

func TestMerge_ADiffReadAgainstAMovedBaseNamesTheConfiguredBranch(t *testing.T) {
	f := newFixture(t)
	f.baseBranch = "release"
	f.svc = f.serviceWith(f.host)
	reviewed := diffOf(baseOld, modified("a.go", hunkOne))
	reviewed.Base = "release"
	h := f.reviewedAtA(reviewed)
	f.setBase("release")
	f.host.CompareOf[f.host.Opened].Base = "release"
	moved := diffOf(baseNew, modified("a.go", hunkOne))
	moved.Base = "release"
	moved.MergeBaseSHA = baseOld
	f.headBDiff(moved)

	_, err := f.merge(h, "")

	require.ErrorIs(t, err, change.ErrBehindBase)
	assert.Contains(t, err.Error(), "merge origin/release into your branch")
}

func TestMerge_ADiffOfAnotherBaseBranchIsRefused(t *testing.T) {
	f := newFixture(t)
	h := f.reviewedAtA(diffOf(baseOld, modified("a.go", hunkOne)))
	other := diffOf(baseNew, modified("a.go", hunkOne))
	other.Base = "release"
	f.headBDiff(other)

	_, err := f.merge(h, "")

	require.ErrorIs(t, err, change.ErrWrongBase)
	assert.Empty(t, f.host.Merges())
}

func TestMerge_BehindTheBaseIsStillRefusedBeforeTheDiffIsRead(t *testing.T) {
	f := newFixture(t)
	h := f.reviewedAtA(diffOf(baseOld, modified("a.go", hunkOne)))
	f.headBDiff(diffOf(baseNew, modified("a.go", hunkOne)))
	f.host.CompareOf[f.host.Opened].ContainsTip = false

	diffsBefore := len(f.host.Diffed())

	_, err := f.merge(h, "")

	require.ErrorIs(t, err, change.ErrBehindBase)
	assert.Empty(t, f.host.Merges())
	assert.Len(t, f.host.Compared(), 1)
	assert.Equal(t, diffsBefore, len(f.host.Diffed()), "the diff is not read of a head the comparison already refused")
}

// A hand-over whose diff could not be fingerprinted is still a hand-over;
// the approval then covers the commit only, as before.
func TestMerge_NoRecordedDiffMeansTheApprovalIsOfTheCommitOnly(t *testing.T) {
	f := newFixture(t)
	h := f.hold(true)
	f.open(h, false)
	f.host.DiffErr = codehost.ErrDiffUnsupported
	f.handOverAndApprove(h)
	rec, err := f.store.Current(f.ctx, h.ticket.ID, 1)
	require.NoError(t, err)
	assert.Equal(t, headA, rec.ReviewedHead)
	assert.Empty(t, rec.ReviewedDiff)
	f.host.DiffErr = nil
	f.host.DiffOf = map[string]*codehost.Diff{headB: diffOf(baseNew, modified("a.go", hunkOne))}
	f.setChecks(headB, codehost.Check{Name: "ci/build", State: codehost.CheckSuccess})

	f.requireRefused(h)
}

// A diff against another branch is not what a merge into main brings, so
// no fingerprint is kept for it.
func TestReview_ADiffAgainstAnotherBranchIsNotFingerprinted(t *testing.T) {
	f := newFixture(t)
	other := diffOf(baseOld, modified("a.go", hunkOne))
	other.Base = "release"
	h := f.reviewedAtA(other)

	rec, err := f.store.Current(f.ctx, h.ticket.ID, 1)
	require.NoError(t, err)
	assert.Equal(t, headA, rec.ReviewedHead)
	assert.Empty(t, rec.ReviewedDiff)
}

func TestReview_ANewHandOverReplacesTheRecordedDiff(t *testing.T) {
	f := newFixture(t)
	h := f.reviewedAtA(diffOf(baseOld, modified("a.go", hunkOne)))
	require.NoError(t, f.tickets.ReleaseIntegrationLock(f.ctx, h.agent))
	f.headBDiff(diffOf(baseNew, modified("a.go", hunkOne+hunkTwo)))
	f.handOverAndApprove(h)
	f.host.DiffOf[headA] = diffOf(baseOld, modified("a.go", hunkOne))

	rec, err := f.store.Current(f.ctx, h.ticket.ID, 1)
	require.NoError(t, err)
	want, err := codehost.LineNumberChangeDetectionStrategy{}.Fingerprint(diffOf(baseNew, modified("a.go", hunkOne+hunkTwo)))
	require.NoError(t, err)
	assert.Equal(t, want, rec.ReviewedDiff)
	assert.Equal(t, headB, rec.ReviewedHead)
}

// A hand-over of another PR clears the whole record, the diff included: an
// approval of that PR never lets this change's head through as "the same".
func TestReview_AnotherPRClearsTheRecordedDiffToo(t *testing.T) {
	f := newFixture(t)
	h := f.reviewedAtA(diffOf(baseOld, modified("a.go", hunkOne)))
	f.headBDiff(diffOf(baseNew, modified("a.go", hunkOne)))
	require.NoError(t, f.tickets.ReleaseIntegrationLock(f.ctx, h.agent))
	_, err := f.svc.Review(f.ctx, f.tenant, h.agent, &ticket.ReviewParams{Reason: "reviewed by a person", PRURL: "https://git.example/acme/app/pull/99", Handoff: "another"})
	require.NoError(t, err)
	_, err = f.tickets.Approve(f.ctx, h.ticket.ID, f.approver)
	require.NoError(t, err)
	_, err = f.tickets.Claim(f.ctx, h.agent, &h.ticket.ID)
	require.NoError(t, err)
	f.lock(h)

	rec, err := f.store.Current(f.ctx, h.ticket.ID, 1)
	require.NoError(t, err)
	assert.Empty(t, rec.ReviewedDiff)
	f.requireRefused(h)
}

// A fingerprint is only comparable with ones from the strategy that made it.
func TestMerge_AFingerprintFromAnotherStrategyIsRefused(t *testing.T) {
	f := newFixture(t)
	h := f.reviewedAtA(diffOf(baseOld, modified("a.go", hunkOne)))
	f.headBDiff(diffOf(baseNew, modified("a.go", hunkOne)))
	f.detect = otherStrategy{}
	f.svc = f.serviceWith(f.host)
	before := len(f.host.Diffed())

	err := f.requireRefused(h)

	assert.Contains(t, err.Error(), "another method")
	assert.Len(t, f.host.Diffed(), before, "the diff is not read when the strategy differs")
}

// otherStrategy is a strategy whose fingerprints are always equal, so a
// service that compared across strategies would accept anything.
type otherStrategy struct{}

func (otherStrategy) Prefix() string { return "other-v9:" }
func (otherStrategy) Fingerprint(*codehost.Diff) (string, error) {
	return "other-v9:same", nil
}
