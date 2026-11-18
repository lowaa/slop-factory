package stewardwire_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/change"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost/codehosttest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/pendingaction"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/stewardwire"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

const (
	headA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	headB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

var ref = codehost.ChangeRef{Host: "git.example", Owner: "acme", Repo: "app", Number: 7}

// The fakes below return what the test gives them and decide nothing.
type fakeChanges struct{ rec *change.Record }

func (f fakeChanges) Current(_ context.Context, ticketID int64, attempt int) (*change.Record, error) {
	if f.rec == nil || f.rec.TicketID != ticketID || f.rec.Attempt != attempt {
		return nil, change.ErrNoChange
	}
	return f.rec, nil
}

type fakeAttempts struct{ attempts []ticket.Attempt }

func (f fakeAttempts) Attempts(context.Context, int64) ([]ticket.Attempt, error) {
	return f.attempts, nil
}

func changeFixture(t *testing.T, host *codehosttest.Fake, attempts []ticket.Attempt) (*stewardwire.ChangeReader, context.Context) {
	t.Helper()
	rec := &change.Record{TicketID: 5, Attempt: 1, Change: ref}
	reader := stewardwire.NewChangeReader(codehosttest.Registry(host), fakeChanges{rec}, fakeAttempts{attempts})
	return reader, db.WithTenant(context.Background(), uuid.New())
}

func held() []ticket.Attempt {
	return []ticket.Attempt{{Number: 1, Holder: &ticket.AttemptHolder{}}}
}

func hostWith(head string, state codehost.CheckState, files ...string) *codehosttest.Fake {
	host := codehosttest.NewLinkHost("git.example")
	host.ChecksOf = map[codehost.ChangeRef]*codehost.Checks{
		ref: codehost.NewChecks(head, "main", []codehost.Check{{Name: "ci", State: state}}),
	}
	diff := &codehost.Diff{Base: "main", BaseSHA: "x", MergeBaseSHA: "x"}
	for _, f := range files {
		diff.Files = append(diff.Files, codehost.FileDiff{Path: f})
	}
	host.DiffOf = map[string]*codehost.Diff{head: diff}
	return host
}

func TestChangeReader_ChecksPassedOnlyForTheCurrentHead(t *testing.T) {
	reader, ctx := changeFixture(t, hostWith(headA, codehost.CheckSuccess), held())

	ok, _, err := reader.ChecksPassed(ctx, 5, headA)
	require.NoError(t, err)
	assert.True(t, ok)

	ok, _, err = reader.ChecksPassed(ctx, 5, headB)
	require.NoError(t, err)
	assert.False(t, ok, "checks that are for another commit are not a pass")
}

func TestChangeReader_FailingChecksAreNotAPass(t *testing.T) {
	reader, ctx := changeFixture(t, hostWith(headA, codehost.CheckFailure), held())
	ok, _, err := reader.ChecksPassed(ctx, 5, headA)
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestChangeReader_HeadAndPaths(t *testing.T) {
	reader, ctx := changeFixture(t, hostWith(headA, codehost.CheckSuccess, "server/a.go", "README.md"), held())

	head, err := reader.CurrentHead(ctx, 5)
	require.NoError(t, err)
	assert.Equal(t, headA, head)

	paths, err := reader.ChangedPaths(ctx, 5)
	require.NoError(t, err)
	assert.Equal(t, []string{"server/a.go", "README.md"}, paths)
}

func TestChangeReader_HostFailuresAreErrors(t *testing.T) {
	host := hostWith(headA, codehost.CheckSuccess, "a.go")
	host.DiffErr = codehost.ErrDiffUnsupported
	reader, ctx := changeFixture(t, host, held())
	_, err := reader.ChangedPaths(ctx, 5)
	assert.ErrorIs(t, err, codehost.ErrDiffUnsupported)

	host.ChecksErr = errors.New("host down")
	_, _, err = reader.ChecksPassed(ctx, 5, headA)
	assert.Error(t, err)
}

func TestChangeReader_AmbiguousAttemptsFailClosed(t *testing.T) {
	two := []ticket.Attempt{{Number: 1, Holder: &ticket.AttemptHolder{}}, {Number: 2, Holder: &ticket.AttemptHolder{}}}
	reader, ctx := changeFixture(t, hostWith(headA, codehost.CheckSuccess), two)
	_, err := reader.CurrentHead(ctx, 5)
	assert.Error(t, err)

	reader, ctx = changeFixture(t, hostWith(headA, codehost.CheckSuccess), nil)
	_, err = reader.CurrentHead(ctx, 5)
	assert.Error(t, err)
}

func TestChangeReader_FallsBackToThePromotedAttempt(t *testing.T) {
	now := time.Now()
	reader, ctx := changeFixture(t, hostWith(headA, codehost.CheckSuccess), []ticket.Attempt{{Number: 1, PromotedAt: &now}})
	head, err := reader.CurrentHead(ctx, 5)
	require.NoError(t, err)
	assert.Equal(t, headA, head)
}

type fakeComments struct {
	status   ticket.Status
	attempts []ticket.Attempt
	comments []ticket.Comment
}

func (f fakeComments) Attempts(context.Context, int64) ([]ticket.Attempt, error) {
	return f.attempts, nil
}
func (f fakeComments) Get(context.Context, int64) (*ticket.Ticket, error) {
	return &ticket.Ticket{Status: f.status}, nil
}
func (f fakeComments) ListComments(context.Context, int64) ([]ticket.Comment, error) {
	return f.comments, nil
}

var (
	workerID = uuid.New()
)

func agentSays(id uuid.UUID, body string) ticket.Comment {
	return ticket.Comment{ID: 11, AuthorAgentID: &id, Body: body}
}

func humanSays(body string) ticket.Comment {
	u := uuid.New()
	return ticket.Comment{ID: 12, AuthorUserID: &u, Body: body}
}

func commentReader(status ticket.Status, comments ...ticket.Comment) *stewardwire.CommentReader {
	return stewardwire.NewCommentReader(fakeComments{status: status, comments: comments, attempts: []ticket.Attempt{{Number: 1, Status: "in_progress"}}})
}

func TestCommentReader_HumanOnly(t *testing.T) {
	tests := []struct {
		name     string
		status   ticket.Status
		comments []ticket.Comment
		want     bool
	}{
		{"plain question", ticket.StatusDiscussion, []ticket.Comment{agentSays(workerID, "Shall I carry on?")}, false},
		{"marked question", ticket.StatusDiscussion, []ticket.Comment{agentSays(workerID, "Pick a design.\n  [human-only]  \n")}, true},
		{"marker only in an older question", ticket.StatusDiscussion, []ticket.Comment{agentSays(workerID, "[human-only]"), agentSays(workerID, "Shall I?")}, false},
		{"marker inside a sentence is not the marker", ticket.StatusDiscussion, []ticket.Comment{agentSays(workerID, "do not use [human-only] here")}, false},
		{"human text after the question is ignored", ticket.StatusDiscussion, []ticket.Comment{agentSays(workerID, "Shall I?"), humanSays("[human-only]")}, false},
		{"marked review reason", ticket.StatusReview, []ticket.Comment{agentSays(workerID, "Review requested though not required: x\n[human-only]")}, true},
		{"plain review", ticket.StatusReview, []ticket.Comment{agentSays(workerID, "handed over")}, false},
		{"not in discussion fails closed", ticket.StatusInProgress, []ticket.Comment{agentSays(workerID, "Shall I?")}, true},
		{"no agent comment fails closed", ticket.StatusDiscussion, []ticket.Comment{humanSays("hi")}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _, err := commentReader(tt.status, tt.comments...).HumanOnly(context.Background(), 5)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestCommentReader_SiblingAttemptsFailClosed(t *testing.T) {
	siblings := []ticket.Attempt{{Number: 1, Status: "in_progress"}, {Number: 2, Status: "in_progress"}}
	r := stewardwire.NewCommentReader(fakeComments{status: ticket.StatusDiscussion, attempts: siblings,
		comments: []ticket.Comment{agentSays(workerID, "Shall I?")}})
	humanOnly, _, err := r.HumanOnly(context.Background(), 5)
	require.NoError(t, err)
	assert.True(t, humanOnly)

	siblings[1].Status = "closed"
	humanOnly, _, err = r.HumanOnly(context.Background(), 5)
	require.NoError(t, err)
	assert.False(t, humanOnly)
}

type fakeProposals struct {
	found  []pendingaction.Action
	filter *pendingaction.ListFilter
}

func (f *fakeProposals) List(_ context.Context, filter *pendingaction.ListFilter) ([]pendingaction.Action, error) {
	f.filter = filter
	return f.found, nil
}

func TestPlanReader_NeedsAnAnswerAPersonApproved(t *testing.T) {
	fake := &fakeProposals{}
	r := stewardwire.NewPlanReader(fake)
	ok, _, err := r.ApprovedPlan(context.Background(), 5)
	require.NoError(t, err)
	assert.False(t, ok)
	assert.Equal(t, int64(5), *fake.filter.TicketID)
	assert.Equal(t, pendingaction.KindDiscussionAnswer, *fake.filter.Kind)
	assert.Equal(t, []pendingaction.State{pendingaction.StateApproved}, fake.filter.States)

	person := uuid.New()
	undecided := pendingaction.Action{ID: uuid.New(), State: pendingaction.StateApproved}
	auto := pendingaction.Action{ID: uuid.New(), State: pendingaction.StateApproved, DecidedByUserID: &person, Outcome: pendingaction.OutcomeAutoApproved}
	clicked := pendingaction.Action{ID: uuid.New(), State: pendingaction.StateApproved, DecidedByUserID: &person}

	fake.found = []pendingaction.Action{undecided, auto}
	ok, _, err = r.ApprovedPlan(context.Background(), 5)
	require.NoError(t, err)
	assert.False(t, ok, "an auto-approved answer is not a person's approval")

	fake.found = []pendingaction.Action{auto, clicked}
	ok, ref, err := r.ApprovedPlan(context.Background(), 5)
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, "proposal "+clicked.ID.String(), ref)
}

func TestChangeReader_HeldAttemptWinsAndTerminalPromotedIsSkipped(t *testing.T) {
	now := time.Now()
	reader, ctx := changeFixture(t, hostWith(headA, codehost.CheckSuccess), []ticket.Attempt{
		{Number: 1, Holder: &ticket.AttemptHolder{}}, {Number: 2, PromotedAt: &now}})
	_, err := reader.CurrentHead(ctx, 5)
	require.NoError(t, err)

	reader, ctx = changeFixture(t, hostWith(headA, codehost.CheckSuccess), []ticket.Attempt{{Number: 1, PromotedAt: &now, Status: "done"}})
	_, err = reader.CurrentHead(ctx, 5)
	assert.Error(t, err)
}
