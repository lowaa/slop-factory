package stewardwire_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/manifest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/reviewreport"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/stewardwire"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// fakeHeadRuns returns the runs it is given and records what it was asked.
type fakeHeadRuns struct {
	runs []reviewreport.HeadRun

	ticketID int64
	attempt  int
	head     string
	postedBy reviewreport.PostedBy
}

func (f *fakeHeadRuns) RunsForAttemptHead(_ context.Context, ticketID int64, attempt int, head string, postedBy reviewreport.PostedBy) ([]reviewreport.HeadRun, error) {
	f.ticketID, f.attempt, f.head, f.postedBy = ticketID, attempt, head, postedBy
	return f.runs, nil
}

func headRun(verdict string, incomplete bool, blocking int, personas ...string) reviewreport.HeadRun {
	return reviewreport.HeadRun{RunID: uuid.New(), Personas: personas, Verdict: verdict, Incomplete: incomplete, Blocking: blocking}
}

// Each persona's review is the newest run covering it, mapped field by field;
// runs come newest first. Its blocking findings are the whole run's.
func TestReviewRunReader_EachPersonaIsItsNewestRun(t *testing.T) {
	newest := headRun("approve_with_comments", false, 2, "standard", "security")
	runs := &fakeHeadRuns{runs: []reviewreport.HeadRun{
		newest,
		headRun("request_changes", false, 5, "standard"),
		headRun("approve", true, 0, "concurrency"),
		headRun("request_changes", false, 0, "test-efficacy"),
	}}
	r := stewardwire.NewReviewRunReader(runs, fakeAttempts{held()})
	got, ref, err := r.SlopReviews(context.Background(), 5, headA)
	require.NoError(t, err)
	assert.Equal(t, []steward.PersonaReview{
		{Persona: "standard", Approved: true, Blocking: 2},
		{Persona: "security", Approved: true, Blocking: 2},
		{Persona: "concurrency", Approved: true, Incomplete: true},
		{Persona: "test-efficacy"},
	}, got)
	assert.Equal(t, "attempt 1 review run "+newest.RunID.String(), ref)
	assert.Equal(t, int64(5), runs.ticketID)
	assert.Equal(t, headA, runs.head)
	assert.Equal(t, reviewreport.PostedBySupervisorOnly, runs.postedBy, "an agent's own report is only its word")
}

// The attempt is the one the change checks read: the held one, among
// siblings; with none identifiable the check cannot pass.
func TestReviewRunReader_ReadsTheLiveAttempt(t *testing.T) {
	runs := &fakeHeadRuns{}
	attempts := []ticket.Attempt{{Number: 1, Status: "open"}, {Number: 2, Holder: &ticket.AttemptHolder{}}, {Number: 3, Status: "open"}}
	_, _, err := stewardwire.NewReviewRunReader(runs, fakeAttempts{attempts}).SlopReviews(context.Background(), 5, headA)
	require.NoError(t, err)
	assert.Equal(t, 2, runs.attempt)

	twoHeld := []ticket.Attempt{{Number: 1, Holder: &ticket.AttemptHolder{}}, {Number: 2, Holder: &ticket.AttemptHolder{}}}
	_, _, err = stewardwire.NewReviewRunReader(runs, fakeAttempts{twoHeld}).SlopReviews(context.Background(), 5, headA)
	assert.Error(t, err)
}

// What the check concludes from the runs, end to end: the supervisor's pass
// approving, complete and with no blocker or major finding passes; anything
// short of that fails, and so do runs of other personas only.
func TestReviewRunReader_FeedsSlopReviewComplete(t *testing.T) {
	pass := func(verdict string, incomplete bool, blocking int) []reviewreport.HeadRun {
		return []reviewreport.HeadRun{headRun(verdict, incomplete, blocking, steward.SlopReviewPersonas...)}
	}
	for name, c := range map[string]struct {
		runs []reviewreport.HeadRun
		ok   bool
	}{
		"approved":                {pass("approve", false, 0), true},
		"approved with comments":  {pass("approve_with_comments", false, 0), true},
		"changes requested":       {pass("request_changes", false, 0), false},
		"incomplete":              {pass("approve", true, 0), false},
		"a blocking finding":      {pass("approve", false, 1), false},
		"only other personas ran": {[]reviewreport.HeadRun{headRun("approve", false, 0, "security", "concurrency", "test-efficacy")}, false},
		"no runs":                 {nil, false},
	} {
		t.Run(name, func(t *testing.T) {
			check := steward.SlopReviewComplete(stewardwire.NewReviewRunReader(&fakeHeadRuns{runs: c.runs}, fakeAttempts{held()}))
			got, err := check.Run(context.Background(), &steward.Subject{TicketID: 5, HeadSHA: headA})
			require.NoError(t, err)
			assert.Equal(t, c.ok, got.OK, got.Detail)
		})
	}
}

// The reviewer's personas are one list on both sides.
func TestSlopReviewPersonasAreTheReportServers(t *testing.T) {
	assert.Equal(t, reviewreport.RequiredPersonas, steward.SlopReviewPersonas)
}

func TestCommentReader_ReviewRequestReason(t *testing.T) {
	lead := ticket.VoluntaryReviewLead
	got, _, err := commentReader(ticket.StatusReview, agentSays(workerID, "handed over")).ReviewRequestReason(context.Background(), 5)
	require.NoError(t, err)
	assert.Empty(t, got)

	got, _, err = commentReader(ticket.StatusReview, agentSays(workerID, lead+"touches migrations")).ReviewRequestReason(context.Background(), 5)
	require.NoError(t, err)
	assert.Equal(t, "touches migrations", got)

	got, _, err = commentReader(ticket.StatusReview, humanSays(lead+"x")).ReviewRequestReason(context.Background(), 5)
	require.NoError(t, err)
	assert.Empty(t, got, "only the agent's comment is its reason")
}

func TestDiffLines_CountsHandWrittenLinesAndSkipsGenerated(t *testing.T) {
	host := hostWith(headA, codehost.CheckSuccess)
	host.DiffOf[headA].Files = []codehost.FileDiff{
		{Path: "a.go", Patch: "@@ -1,2 +1,3 @@\n ctx\n-old\n+new\n+more\n"},
		{Path: "gen.go", Patch: "@@ -0,0 +1,3 @@\n+// Code generated by x. DO NOT EDIT.\n+package a\n+var x int\n"},
		{Path: "api/client.ts", Patch: "@@ -1 +1,2 @@\n-a\n+b\n+c\n"},
		{Path: "api/.gitattributes", Patch: "@@ -0,0 +1 @@\n+* linguist-generated=true\n"},
		{Path: "other/b.ts", Patch: "@@ -1 +1 @@\n-a\n+b\n"},
	}
	reader, ctx := changeFixture(t, host, held())
	got, err := stewardwire.NewDiffLines(reader).ChangedLines(ctx, 5)
	require.NoError(t, err)
	// a.go 3, other/b.ts 2 count; gen.go 3, api/client.ts 3 and api/.gitattributes 1 do not.
	assert.Equal(t, &steward.LineCount{Counted: 5, Generated: 7}, got)
}

type fakeManifest struct{ repos []manifest.Repo }

func (f fakeManifest) Get(context.Context) (*manifest.Manifest, error) {
	return &manifest.Manifest{Repos: f.repos}, nil
}

func TestProtectedPaths_NoManifestProtectsNothing(t *testing.T) {
	r := stewardwire.NewProtectedPaths(nilManifest{})
	hits, err := r.ProtectedAmong(context.Background(), []string{"server/a.go"})
	require.NoError(t, err)
	assert.Empty(t, hits)
}

type nilManifest struct{}

func (nilManifest) Get(context.Context) (*manifest.Manifest, error) { return nil, nil }

func TestProtectedPaths(t *testing.T) {
	r := stewardwire.NewProtectedPaths(fakeManifest{[]manifest.Repo{{Path: "app",
		ProtectedPaths: []manifest.ProtectedPathRule{{Path: "server/auth/", Reason: "auth"}}}}})
	hits, err := r.ProtectedAmong(context.Background(), []string{"app/server/auth/login.go", "server/auth/x.go", "app/web/a.ts", "web/b.ts"})
	require.NoError(t, err)
	assert.Equal(t, []string{"app/server/auth/login.go (server/auth/: auth)", "server/auth/x.go (server/auth/: auth)"}, hits)
}

func TestProtectedPaths_ProtectedGivesTheRulesReasonPerPath(t *testing.T) {
	r := stewardwire.NewProtectedPaths(fakeManifest{[]manifest.Repo{{Path: "app",
		ProtectedPaths: []manifest.ProtectedPathRule{{Path: "server/auth/", Reason: "auth"}, {Path: "infra/"}}}}})
	got, err := r.Protected(context.Background(), []string{"app/server/auth/login.go", "infra/x.yaml", "web/b.ts"})
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"app/server/auth/login.go": "auth", "infra/x.yaml": ""}, got)
}

func TestDiffLines_AFileWithoutAPatchCannotBeCounted(t *testing.T) {
	host := hostWith(headA, codehost.CheckSuccess)
	host.DiffOf[headA].Files = []codehost.FileDiff{{Path: "big.bin"}}
	reader, ctx := changeFixture(t, host, held())
	_, err := stewardwire.NewDiffLines(reader).ChangedLines(ctx, 5)
	assert.Error(t, err)
}
