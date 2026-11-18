package steward_test

import (
	"context"
	"io"
	"log/slog"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward"
)

// reviewFacts decides nothing: it returns what the test set.
type reviewFacts struct {
	facts
	lines     steward.LineCount
	limit     int
	reviews   []steward.PersonaReview
	reason    string
	protected []string
	// readErr is what the review readers named by failing return.
	readErr error
	failing string
	asked   []string
}

func (f *reviewFacts) fail(reader string) error {
	if f.failing == reader {
		return f.readErr
	}
	return nil
}

func (f *reviewFacts) ChangedLines(context.Context, int64) (*steward.LineCount, error) {
	return &f.lines, f.fail("lines")
}
func (f *reviewFacts) ReviewMaxLines(context.Context) (int, error) { return f.limit, f.fail("limit") }
func (f *reviewFacts) SlopReviews(context.Context, int64, string) ([]steward.PersonaReview, string, error) {
	return f.reviews, "comment 3", f.fail("slopreview")
}
func (f *reviewFacts) ReviewRequestReason(context.Context, int64) (string, string, error) {
	return f.reason, "comment 4", f.fail("reason")
}
func (f *reviewFacts) ProtectedAmong(_ context.Context, paths []string) ([]string, error) {
	f.asked = paths
	var hits []string
	for _, p := range paths {
		if slices.Contains(f.protected, p) {
			hits = append(hits, p)
		}
	}
	return hits, f.fail("protected")
}

func reviewEngine(f *reviewFacts) *steward.Engine {
	return steward.NewEngine(slog.NewTextHandler(io.Discard, nil),
		steward.CIGreenOnHead(f), steward.HeadUnchanged(f), steward.TicketRevUnchanged(f), steward.DiffWithinScope(f, f),
		steward.NoHumanOnlyMarker(f), steward.DiffSizeWithinLimit(f, f), steward.SlopReviewComplete(f),
		steward.NoReviewRequestReason(f), steward.NoProtectedPath(f, f, f))
}

func goodReview() *reviewFacts {
	f := &reviewFacts{facts: *goodFacts(), lines: steward.LineCount{Counted: 120, Generated: 3000}, limit: 400}
	for _, p := range steward.SlopReviewPersonas {
		f.reviews = append(f.reviews, steward.PersonaReview{Persona: p, Approved: true})
	}
	return f
}

func approveReview() *steward.Proposal {
	return &steward.Proposal{Category: "review_approve", Kind: steward.KindReviewDecision, Decision: steward.ReviewDecisionApprove,
		Reason: "docs only", Subject: steward.Subject{TicketID: 1, Revision: 4, HeadSHA: "abc123"}}
}

func failedChecks(a *steward.Assessment) []string {
	var out []string
	for _, e := range a.Evidence {
		if !e.OK {
			out = append(out, e.Check)
		}
	}
	return out
}

func TestReviewApproveIsGatedAtVetoOrAutoOnlyWhenEveryCheckPasses(t *testing.T) {
	a := reviewEngine(goodReview()).Assess(context.Background(), approveReview(), steward.LevelFull, nil)
	assert.Equal(t, steward.VerdictWouldAuto, a.Verdict, "%+v", a.Evidence)
	assert.Len(t, a.Evidence, 9)
}

func TestReviewApproveIsHumanBelowLevelFour(t *testing.T) {
	a := reviewEngine(goodReview()).Assess(context.Background(), approveReview(), steward.LevelRoutine, nil)
	assert.Equal(t, steward.TierHuman, a.Tier)
	assert.Equal(t, steward.VerdictNeedsHuman, a.Verdict)
}

func TestReviewApproveFailsOnEachCheck(t *testing.T) {
	for _, c := range []struct {
		name   string
		mutate func(*reviewFacts)
		failed string
	}{
		{"ci", func(f *reviewFacts) { f.ciPassed = false }, steward.CheckCIGreenOnHead},
		{"head moved", func(f *reviewFacts) { f.head = "def456" }, steward.CheckHeadUnchanged},
		{"ticket edited", func(f *reviewFacts) { f.revision = 5 }, steward.CheckTicketRevUnchanged},
		{"outside scope", func(f *reviewFacts) { f.changed = []string{"other/x.go"} }, steward.CheckDiffWithinScope},
		{"too big", func(f *reviewFacts) { f.lines.Counted = 401 }, steward.CheckDiffSizeWithinLimit},
		{"no review recorded", func(f *reviewFacts) { f.reviews = nil }, steward.CheckSlopReviewComplete},
		{"only another persona reviewed", func(f *reviewFacts) { f.reviews = []steward.PersonaReview{{Persona: "security", Approved: true}} }, steward.CheckSlopReviewComplete},
		{"blocking finding", func(f *reviewFacts) { f.reviews[0].Blocking = 1 }, steward.CheckSlopReviewComplete},
		{"review incomplete", func(f *reviewFacts) { f.reviews[0].Incomplete = true }, steward.CheckSlopReviewComplete},
		{"persona not approved", func(f *reviewFacts) { f.reviews[0].Approved = false }, steward.CheckSlopReviewComplete},
		{"agent asked for a human", func(f *reviewFacts) { f.reason = "touches migrations" }, steward.CheckNoReviewRequestReason},
		{"protected path in the diff", func(f *reviewFacts) { f.protected = []string{"server/a.go"} }, steward.CheckNoProtectedPath},
		{"protected path in the ticket's scope", func(f *reviewFacts) { f.protected = []string{"README.md"} }, steward.CheckNoProtectedPath},
		{"too much generated", func(f *reviewFacts) { f.lines.Generated = 4001 }, steward.CheckDiffSizeWithinLimit},
		{"human only", func(f *reviewFacts) { f.humanOnly = true }, steward.CheckNoHumanOnlyMarker},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := goodReview()
			c.mutate(f)
			a := reviewEngine(f).Assess(context.Background(), approveReview(), steward.LevelFull, nil)
			assert.Equal(t, steward.VerdictNeedsHuman, a.Verdict)
			assert.Equal(t, []string{c.failed}, failedChecks(a))
		})
	}
}

func TestReviewApproveSizeEvidenceRecordsCountAndLimit(t *testing.T) {
	a := reviewEngine(goodReview()).Assess(context.Background(), approveReview(), steward.LevelFull, nil)
	for _, e := range a.Evidence {
		if e.Check == steward.CheckDiffSizeWithinLimit {
			assert.Equal(t, "120 changed lines counted, limit 400 (3000 in generated files not counted)", e.Detail)
			return
		}
	}
	t.Fatal("no size evidence")
}

func TestReviewSizeLimitIsInclusive(t *testing.T) {
	f := goodReview()
	f.lines.Counted = 400
	a := reviewEngine(f).Assess(context.Background(), approveReview(), steward.LevelFull, nil)
	assert.Equal(t, steward.VerdictWouldAuto, a.Verdict)
}

func TestReviewApproveRefusesAnythingButApprove(t *testing.T) {
	for _, decision := range []string{"request_changes", ""} {
		p := approveReview()
		p.Decision = decision
		a := reviewEngine(goodReview()).Assess(context.Background(), p, steward.LevelFull, nil)
		assert.Equal(t, steward.VerdictNeedsHuman, a.Verdict, decision)
		assert.Equal(t, "decision", a.Evidence[0].Check)
	}
}

func TestReviewApproveAnUnreadableFactFailsItsOwnCheck(t *testing.T) {
	for reader, check := range map[string]string{
		"lines": steward.CheckDiffSizeWithinLimit, "limit": steward.CheckDiffSizeWithinLimit,
		"slopreview": steward.CheckSlopReviewComplete, "reason": steward.CheckNoReviewRequestReason,
		"protected": steward.CheckNoProtectedPath,
	} {
		t.Run(reader, func(t *testing.T) {
			f := goodReview()
			f.readErr, f.failing = assert.AnError, reader
			a := reviewEngine(f).Assess(context.Background(), approveReview(), steward.LevelFull, nil)
			assert.Equal(t, steward.VerdictNeedsHuman, a.Verdict)
			assert.Equal(t, []string{check}, failedChecks(a))
		})
	}
}

func TestNoProtectedPathAsksAboutScopeDiffAndProposedPaths(t *testing.T) {
	f := goodReview()
	p := approveReview()
	p.Subject.ProposedPaths = []string{"proposed/x.go"}
	reviewEngine(f).Assess(context.Background(), p, steward.LevelFull, nil)
	assert.ElementsMatch(t, []string{"proposed/x.go", "server/", "README.md", "server/a.go"}, f.asked)
}
