package reviewreport_test

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-review/report"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/reviewreport"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

func TestInsertRun_FilesARunUnderTheAttemptItsAgentHolds(t *testing.T) {
	f := newFixture(t)
	builder := f.agent(t, f.ctx, "builder")
	id := f.ticket(t, f.ctx)
	f.hold(t, f.ctx, builder, id, 1)

	got := f.insert(t, f.ctx, builder, validRun(ticket.BranchName(id, 1)))
	assert.True(t, got.Created)
	assert.Equal(t, &reviewreport.Attribution{TicketID: id, Attempt: 1}, got.Attribution)
	runs, err := f.store.AttemptRuns(f.ctx, attempt(id, 1), 10)
	require.NoError(t, err)
	assert.Len(t, runs, 1)
}

// The trust boundary: an attributed run is evidence the attempt was
// reviewed, so only the agent holding the attempt can file one against it.
// Every other way of naming an attempt is stored, but unattributed.
func TestInsertRun_NoAgentCanFileAReviewAgainstAnAttemptItDoesNotHold(t *testing.T) {
	f := newFixture(t)
	builder, intruder := f.agent(t, f.ctx, "builder"), f.agent(t, f.ctx, "intruder")
	id := f.ticket(t, f.ctx)
	second := f.addAttempt(t, f.ctx, id)
	f.hold(t, f.ctx, builder, id, 1)
	released := f.hold(t, f.ctx, intruder, id, second)
	f.release(t, f.ctx, released)

	for name, c := range map[string]struct {
		agent  uuid.UUID
		branch string
	}{
		"another agent holds the attempt":           {intruder, ticket.BranchName(id, 1)},
		"its own lease on the attempt has ended":    {intruder, ticket.BranchName(id, second)},
		"it holds another attempt of the ticket":    {builder, ticket.BranchName(id, second)},
		"the branch names a ticket that is not one": {builder, ticket.BranchName(id+1000, 1)},
		"the branch only looks like an attempt's":   {builder, ticket.BranchName(id, 1) + "-fix"},
		"the branch is no attempt's":                {builder, "main"},
	} {
		t.Run(name, func(t *testing.T) {
			run := validRun(c.branch)
			got := f.insert(t, f.ctx, c.agent, run)
			assert.True(t, got.Created)
			assert.Nil(t, got.Attribution)
			var ticketID *int64
			require.NoError(t, f.database.QueryRow(f.ctx, `SELECT ticket_id FROM review_runs WHERE id = $1`, run.ID).Scan(&ticketID))
			assert.Nil(t, ticketID, "stored unattributed")
		})
	}
	runs, err := f.store.AttemptRuns(f.ctx, attempt(id, 1), 10)
	require.NoError(t, err)
	assert.Empty(t, runs, "the builder's attempt shows none of the intruder's runs")
}

func TestInsertRun_ARepostChangesNothing(t *testing.T) {
	f := newFixture(t)
	builder := f.agent(t, f.ctx, "builder")
	id := f.ticket(t, f.ctx)
	lease := f.hold(t, f.ctx, builder, id, 1)
	run := validRun(ticket.BranchName(id, 1), "standard")
	f.insert(t, f.ctx, builder, run)
	// Retried from the spool after the lease ended, and changed meanwhile:
	// the stored run is the one answered, as it was filed.
	f.release(t, f.ctx, lease)
	again := *run
	again.Verdict = "approve"
	again.Findings = nil

	got := f.insert(t, f.ctx, builder, &again)
	assert.False(t, got.Created)
	assert.Equal(t, &reviewreport.Attribution{TicketID: id, Attempt: 1}, got.Attribution)
	runs, err := f.store.AttemptRuns(f.ctx, attempt(id, 1), 10)
	require.NoError(t, err)
	require.Len(t, runs, 1)
	assert.Equal(t, "request_changes", runs[0].Verdict)
	assert.Len(t, runs[0].Findings, 1)
}

func TestInsertRun_ARunOrFindingIDIsOneAgentsOnly(t *testing.T) {
	f := newFixture(t)
	builder, other := f.agent(t, f.ctx, "builder"), f.agent(t, f.ctx, "other")
	run := validRun("main", "standard")
	f.insert(t, f.ctx, builder, run)

	_, err := f.store.InsertRun(f.ctx, byAgent(other), run)
	assert.ErrorIs(t, err, reviewreport.ErrRunTaken)

	stealing := validRun("main", "standard")
	stealing.Findings[0].ID = run.Findings[0].ID
	_, err = f.store.InsertRun(f.ctx, byAgent(other), stealing)
	assert.ErrorIs(t, err, reviewreport.ErrFindingTaken)
	var n int
	require.NoError(t, f.database.QueryRow(f.ctx, `SELECT count(*) FROM review_runs WHERE id = $1`, stealing.ID).Scan(&n))
	assert.Zero(t, n, "the run is not stored without its findings")

	// Ids are the tenant's own: another tenant may reuse them.
	stranger := f.agent(t, f.otherCtx, "stranger")
	assert.True(t, f.insert(t, f.otherCtx, stranger, run).Created)
}

func TestInsertRun_NeverStoresTheHeadAuthor(t *testing.T) {
	f := newFixture(t)
	builder := f.agent(t, f.ctx, "builder")
	run := validRun("main")
	f.insert(t, f.ctx, builder, run)

	var stored string
	require.NoError(t, f.database.QueryRow(f.ctx, `SELECT report::text FROM review_runs WHERE id = $1`, run.ID).Scan(&stored))
	assert.NotContains(t, stored, "Ada")
	assert.NotContains(t, stored, "head_author")
	assert.Contains(t, stored, run.Summary)
	assert.Equal(t, "Ada Lovelace <ada@example.com>", run.HeadAuthor, "the caller's run is left as it was")
}

func TestInsertRun_TextPostgresCannotHoldIsInvalid(t *testing.T) {
	f := newFixture(t)
	builder := f.agent(t, f.ctx, "builder")
	run := validRun("main", "standard")
	run.Findings[0].Detail = "a\x00b"
	_, err := f.store.InsertRun(f.ctx, byAgent(builder), run)
	assert.ErrorIs(t, err, reviewreport.ErrInvalid)
}

func TestLabelFinding_OnlyTheRunsAgentCanLabelItsFindings(t *testing.T) {
	f := newFixture(t)
	builder, other := f.agent(t, f.ctx, "builder"), f.agent(t, f.ctx, "other")
	id := f.ticket(t, f.ctx)
	f.hold(t, f.ctx, builder, id, 1)
	run := validRun(ticket.BranchName(id, 1), "standard")
	f.insert(t, f.ctx, builder, run)
	finding := run.Findings[0].ID
	stranger := f.agent(t, f.otherCtx, "stranger")

	assert.ErrorIs(t, f.store.LabelFinding(f.ctx, other, finding, &reviewreport.Label{Verdict: "false_positive"}),
		reviewreport.ErrNoFinding, "another agent of the tenant")
	assert.ErrorIs(t, f.store.LabelFinding(f.otherCtx, stranger, finding, &reviewreport.Label{Verdict: "false_positive"}),
		reviewreport.ErrNoFinding, "another tenant's agent")
	assert.ErrorIs(t, f.store.LabelFinding(f.ctx, builder, uuid.New(), &reviewreport.Label{Verdict: "false_positive"}),
		reviewreport.ErrNoFinding, "no such finding")
	runs, err := f.store.AttemptRuns(f.ctx, attempt(id, 1), 10)
	require.NoError(t, err)
	assert.Nil(t, runs[0].Findings[0].Label, "nothing the refused labels said was kept")

	require.NoError(t, f.store.LabelFinding(f.ctx, builder, finding, &reviewreport.Label{Verdict: "false_positive", Reason: "no"}))
	require.NoError(t, f.store.LabelFinding(f.ctx, builder, finding, &reviewreport.Label{Verdict: "confirmed", Reason: "yes", FixedIn: "abc1234"}))
	runs, err = f.store.AttemptRuns(f.ctx, attempt(id, 1), 10)
	require.NoError(t, err)
	label := runs[0].Findings[0].Label
	require.NotNil(t, label)
	assert.Equal(t, reviewreport.Label{Verdict: "confirmed", Reason: "yes", FixedIn: "abc1234"}, label.Label, "the newest label is current")
}

func TestAttemptRuns_NewestFirstWithFindingsInOrder(t *testing.T) {
	f := newFixture(t)
	builder := f.agent(t, f.ctx, "builder")
	id := f.ticket(t, f.ctx)
	f.hold(t, f.ctx, builder, id, 1)
	older := validRun(ticket.BranchName(id, 1), "standard", "security", "standard")
	newer := validRun(ticket.BranchName(id, 1))
	newer.StartedAt, newer.FinishedAt = older.FinishedAt, older.FinishedAt.Add(time.Minute)
	failed := validRun(ticket.BranchName(id, 1))
	failed.FinishedAt = older.StartedAt.Add(time.Second)
	failed.Status, failed.Failure, failed.Verdict = "failed", &report.Failure{Class: report.FailureUsageLimit, Message: "limit"}, ""
	for _, r := range []*report.Run{older, newer, failed} {
		f.insert(t, f.ctx, builder, r)
	}

	runs, err := f.store.AttemptRuns(f.ctx, attempt(id, 1), 10)
	require.NoError(t, err)
	require.Len(t, runs, 3)
	assert.Equal(t, []uuid.UUID{newer.ID, older.ID, failed.ID}, []uuid.UUID{runs[0].ID, runs[1].ID, runs[2].ID})
	assert.Equal(t, "builder", runs[0].PosterName)
	assert.Equal(t, reviewreport.Poster{Kind: reviewreport.PostedByAgent, ID: builder}, runs[0].Poster)
	assert.Equal(t, older.Summary, runs[1].Summary)
	require.Len(t, runs[1].Findings, 3)
	for i, finding := range runs[1].Findings {
		assert.Equal(t, older.Findings[i].ID, finding.ID)
		assert.Equal(t, older.Findings[i].Personas, finding.Personas)
	}
	assert.Equal(t, "failed", runs[2].Status)
	assert.Equal(t, "usage_limit", runs[2].FailureClass)
	assert.Equal(t, "limit", runs[2].FailureMessage)
	assert.Empty(t, runs[2].Findings)

	limited, err := f.store.AttemptRuns(f.ctx, attempt(id, 1), 1)
	require.NoError(t, err)
	assert.Len(t, limited, 1)
}

func TestAttemptRuns_AnAttemptTheTenantDoesNotHaveIsNotFound(t *testing.T) {
	f := newFixture(t)
	id := f.ticket(t, f.ctx)
	_, err := f.store.AttemptRuns(f.ctx, attempt(id, 2), 10)
	assert.ErrorIs(t, err, ticket.ErrNoSuchAttempt)
	_, err = f.store.AttemptRuns(f.otherCtx, attempt(id, 1), 10)
	assert.ErrorIs(t, err, ticket.ErrNoSuchAttempt, "another tenant's ticket")
}

func TestRunsForAttemptHead_MatchesTheHeadByFullOrAbbreviatedSha(t *testing.T) {
	f := newFixture(t)
	builder := f.agent(t, f.ctx, "builder")
	id := f.ticket(t, f.ctx)
	f.hold(t, f.ctx, builder, id, 1)
	full := validRun(ticket.BranchName(id, 1))
	short := validRun(ticket.BranchName(id, 1))
	short.HeadSHA = strings.ToUpper(headSHA[:7])
	short.FinishedAt = full.FinishedAt.Add(time.Minute)
	elsewhere := validRun(ticket.BranchName(id, 1))
	elsewhere.HeadSHA = "0000000" + headSHA[7:]
	tooShort := validRun(ticket.BranchName(id, 1))
	tooShort.HeadSHA = headSHA[:6]
	for _, r := range []*report.Run{full, short, elsewhere, tooShort} {
		f.insert(t, f.ctx, builder, r)
	}
	ids := func(runs []reviewreport.HeadRun) []uuid.UUID {
		var out []uuid.UUID
		for _, r := range runs {
			out = append(out, r.RunID)
		}
		return out
	}

	for name, query := range map[string]string{
		"the full sha":          headSHA,
		"upper case":            strings.ToUpper(headSHA),
		"a 7-character prefix":  headSHA[:7],
		"a 12-character prefix": headSHA[:12],
	} {
		got, err := f.store.RunsForAttemptHead(f.ctx, id, 1, query, reviewreport.PostedByAnyone)
		require.NoError(t, err, name)
		assert.Equal(t, []uuid.UUID{short.ID, full.ID}, ids(got), name)
	}
	got, err := f.store.RunsForAttemptHead(f.ctx, id, 1, headSHA[:6], reviewreport.PostedByAnyone)
	require.NoError(t, err)
	assert.Empty(t, got, "a prefix under 7 characters matches nothing")
	got, err = f.store.RunsForAttemptHead(f.ctx, id, 2, headSHA, reviewreport.PostedByAnyone)
	require.NoError(t, err)
	assert.Empty(t, got, "another attempt")
}

func TestRunsForAttemptHead_OnlyCompletedReviewsCount(t *testing.T) {
	f := newFixture(t)
	builder := f.agent(t, f.ctx, "builder")
	id := f.ticket(t, f.ctx)
	f.hold(t, f.ctx, builder, id, 1)
	review := validRun(ticket.BranchName(id, 1))
	failed := validRun(ticket.BranchName(id, 1))
	failed.Status, failed.Failure, failed.Verdict = "failed", &report.Failure{Class: report.FailureAuth, Message: "login"}, ""
	bench := validRun(ticket.BranchName(id, 1))
	bench.Kind, bench.Bench = report.KindBench, &report.Bench{Case: "c", Variant: "v", Run: 1}
	for _, r := range []*report.Run{review, failed, bench} {
		f.insert(t, f.ctx, builder, r)
	}
	got, err := f.store.RunsForAttemptHead(f.ctx, id, 1, headSHA, reviewreport.PostedByAnyone)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, review.ID, got[0].RunID)
	assert.Equal(t, []string{"security", "standard"}, got[0].Personas)
	assert.Equal(t, "request_changes", got[0].Verdict)
}

// Blocker and major findings block, minor and nit ones do not, and no label
// changes that: labels are the reviewed agent's own word.
func TestRunsForAttemptHead_CountsBlockingFindingsWhateverTheirLabels(t *testing.T) {
	f := newFixture(t)
	builder := f.agent(t, f.ctx, "builder")
	id := f.ticket(t, f.ctx)
	f.hold(t, f.ctx, builder, id, 1)
	run := validRun(ticket.BranchName(id, 1),
		"standard", "standard", "standard", "standard", "standard", "standard", "security", "security")
	severities := []string{"blocker", "major", "minor", "nit", "major", "major", "minor", "blocker"}
	for i, sev := range severities {
		run.Findings[i].Severity = sev
	}
	run.Findings[7].Personas = []string{"security", "standard"}
	f.insert(t, f.ctx, builder, run)
	label := func(i int, l reviewreport.Label) {
		require.NoError(t, f.store.LabelFinding(f.ctx, builder, run.Findings[i].ID, &l))
	}
	// Every label that once settled a finding; none of them does now.
	label(0, reviewreport.Label{Verdict: "false_positive"})
	label(1, reviewreport.Label{Verdict: "confirmed", FixedIn: "abc1234"})
	label(4, reviewreport.Label{Verdict: "wont_fix"})
	label(5, reviewreport.Label{Verdict: "fabricated"})
	label(7, reviewreport.Label{Verdict: "duplicate"})

	got, err := f.store.RunsForAttemptHead(f.ctx, id, 1, headSHA, reviewreport.PostedByAnyone)
	require.NoError(t, err)
	require.Len(t, got, 1)
	// Blocker 0 and 7, majors 1, 4 and 5: 7 names two personas and counts once.
	assert.Equal(t, 5, got[0].Blocking)

	clean := validRun(ticket.BranchName(id, 1))
	clean.FinishedAt = run.FinishedAt.Add(time.Minute)
	f.insert(t, f.ctx, builder, clean)
	got, err = f.store.RunsForAttemptHead(f.ctx, id, 1, headSHA, reviewreport.PostedByAnyone)
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Zero(t, got[0].Blocking)
}
