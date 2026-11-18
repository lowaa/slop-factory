package ticket_test

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// ownersOf vouches for exactly the agents it holds: they act for f.human, and
// every other agent is a worker. It decides nothing else.
type ownersOf struct {
	user   uuid.UUID
	agents map[uuid.UUID]bool
}

func (f *fixture) owners(agents ...uuid.UUID) ownersOf {
	o := ownersOf{user: f.human, agents: map[uuid.UUID]bool{}}
	for _, a := range agents {
		o.agents[a] = true
	}
	return o
}

func (o ownersOf) ResolveOwner(_ context.Context, agentID uuid.UUID) (*ticket.OwnerBound, error) {
	if !o.agents[agentID] {
		return nil, nil
	}
	return &ticket.OwnerBound{AgentID: agentID, AgentName: "console", UserID: o.user, UserName: "Pat"}, nil
}

// twoLive is a ticket with two live attempts, none leased.
func (f *fixture) twoLive(t *testing.T) int64 {
	t.Helper()
	made := f.ticket(t, "Two attempts", "a/")
	f.addAttempt(t, made.ID, false)
	return made.ID
}

func (f *fixture) commentAttempt(t *testing.T, ticketID int64, body string) *int {
	t.Helper()
	var n *int
	require.NoError(t, f.database.QueryRow(f.ctx,
		`SELECT attempt_number FROM ticket_comments WHERE ticket_id = $1 AND body LIKE $2 || '%'`, ticketID, body).Scan(&n))
	return n
}

func (f *fixture) commentCount(t *testing.T, ticketID int64, body string) int {
	t.Helper()
	var n int
	require.NoError(t, f.database.QueryRow(f.ctx,
		`SELECT count(*) FROM ticket_comments WHERE ticket_id = $1 AND body LIKE $2 || '%'`, ticketID, body).Scan(&n))
	return n
}

func intp(n int) *int { return &n }

func TestResolveAttempt_DefaultsNamesAndRefusals(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	single := f.ticket(t, "Single", "a/")
	two := f.twoLive(t)

	t.Run("one live attempt: none named leaves the caller's default", func(t *testing.T) {
		ref, err := f.svc.ResolveAttempt(f.ctx, single.ID, nil)
		require.NoError(t, err)
		assert.Nil(t, ref)
	})
	t.Run("naming it works", func(t *testing.T) {
		ref, err := f.svc.ResolveAttempt(f.ctx, single.ID, intp(1))
		require.NoError(t, err)
		assert.Equal(t, &ticket.AttemptRef{TicketID: single.ID, Number: 1}, ref)
	})
	t.Run("several live and none named is refused with the list", func(t *testing.T) {
		_, err := f.svc.ResolveAttempt(f.ctx, two, nil)
		require.ErrorIs(t, err, ticket.ErrAttemptAmbiguous)
		assert.Contains(t, err.Error(), fmt.Sprintf("#%d · attempt 1, #%d · attempt 2", two, two))
	})
	t.Run("naming one of several works", func(t *testing.T) {
		ref, err := f.svc.ResolveAttempt(f.ctx, two, intp(2))
		require.NoError(t, err)
		assert.Equal(t, 2, ref.Number)
	})
	t.Run("an attempt the ticket does not have", func(t *testing.T) {
		_, err := f.svc.ResolveAttempt(f.ctx, two, intp(3))
		require.ErrorIs(t, err, ticket.ErrNoSuchAttempt)
	})
	t.Run("an attempt that is not live", func(t *testing.T) {
		f.setAttemptStatus(t, two, 2, "closed")
		_, err := f.svc.ResolveAttempt(f.ctx, two, intp(2))
		require.ErrorIs(t, err, ticket.ErrAttemptNotLive)
		ref, err := f.svc.ResolveAttempt(f.ctx, two, nil)
		require.NoError(t, err, "a closed attempt is not among the live ones, so one is left")
		assert.Nil(t, ref)
	})
}

func TestBranchName_EveryAttemptGetsATitleFreeNameOfItsOwn(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "ticket-42-a1", ticket.BranchName(42, 1))
	assert.Equal(t, "ticket-42-a2", ticket.BranchName(42, 2))
	assert.NotContains(t, ticket.BranchName(7, 12), "/")
}

func TestParseBranchName_ReadsBackWhatBranchNameWrites(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		id      int64
		attempt int
	}{{1, 1}, {42, 2}, {9223372036854775807, 2147483647}} {
		id, attempt, ok := ticket.ParseBranchName(ticket.BranchName(c.id, c.attempt))
		require.True(t, ok, "%d/%d", c.id, c.attempt)
		assert.Equal(t, c.id, id)
		assert.Equal(t, c.attempt, attempt)
	}
}

// Only the exact form is an attempt's branch: anything a person could name a
// branch that merely looks like one must not be read as that attempt.
func TestParseBranchName_RefusesAnythingButTheExactForm(t *testing.T) {
	t.Parallel()
	for _, branch := range []string{
		"", "main", "ticket-42", "ticket-42-a", "ticket--a1", "ticket-42-a1x", "xticket-42-a1",
		"ticket-42-a1/fix", "feature/ticket-42-a1", "Ticket-42-a1", "ticket-042-a1", "ticket-42-a01",
		"ticket-0-a1", "ticket-42-a0", "ticket-+42-a1", "ticket--42-a1", "ticket-42-a-1", "ticket-4 2-a1",
		"ticket-42-a2147483648", "ticket-9223372036854775808-a1", "ticket-42-a1-a2", "ticket-42-b1",
	} {
		_, _, ok := ticket.ParseBranchName(branch)
		assert.False(t, ok, "%q", branch)
	}
}

func TestClaim_NamesTheAttemptItGranted(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a1, a2 := f.agent(t, "first"), f.agent(t, "second")
	made := f.ticket(t, "Two attempts", "a/")
	f.addAttempt(t, made.ID, false)

	first := f.claimOnly(t, a1, made.ID)
	second := f.claimOnly(t, a2, made.ID)
	require.NotNil(t, first.Attempt)
	require.NotNil(t, second.Attempt)
	assert.Equal(t, 1, first.Attempt.Number)
	assert.Equal(t, 2, second.Attempt.Number)
}

func TestAgentComment_AConsoleAddressesAnAttempt(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	console := f.agent(t, "console")
	f.with(ticket.WithOwnerResolver(f.owners(console)))
	single := f.ticket(t, "Single", "a/")
	two := f.twoLive(t)

	t.Run("with one live attempt it defaults to the ticket's own rule", func(t *testing.T) {
		_, err := f.svc.AgentCommentOn(f.ctx, console, single.ID, nil, "unnamed on one")
		require.NoError(t, err)
		assert.Nil(t, f.commentAttempt(t, single.ID, "unnamed on one"), "work has not started: the ticket's own")
	})
	t.Run("naming the attempt addresses it", func(t *testing.T) {
		_, err := f.svc.AgentCommentOn(f.ctx, console, single.ID, intp(1), "named on one")
		require.NoError(t, err)
		require.NotNil(t, f.commentAttempt(t, single.ID, "named on one"))
		assert.Equal(t, 1, *f.commentAttempt(t, single.ID, "named on one"))
	})
	t.Run("with several live and none named it is the ticket's, for every attempt", func(t *testing.T) {
		_, err := f.svc.AgentCommentOn(f.ctx, console, two, nil, "unnamed on two")
		require.NoError(t, err)
		assert.Nil(t, f.commentAttempt(t, two, "unnamed on two"))
	})
	t.Run("naming one of several addresses that one", func(t *testing.T) {
		_, err := f.svc.AgentCommentOn(f.ctx, console, two, intp(2), "named on two")
		require.NoError(t, err)
		require.NotNil(t, f.commentAttempt(t, two, "named on two"))
		assert.Equal(t, 2, *f.commentAttempt(t, two, "named on two"))
	})
	t.Run("a missing attempt is refused", func(t *testing.T) {
		_, err := f.svc.AgentCommentOn(f.ctx, console, two, intp(7), "missing")
		require.ErrorIs(t, err, ticket.ErrNoSuchAttempt)
		assert.Zero(t, f.commentCount(t, two, "missing"))
	})
}

func TestAgentComment_AWorkerNeverNamesAnAttempt(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	worker := f.agent(t, "worker")
	f.with(ticket.WithOwnerResolver(f.owners()))
	id, a1, _ := f.running(t)

	for _, attempt := range []int{1, 2} {
		_, err := f.svc.AgentCommentOn(f.ctx, a1, id, intp(attempt), fmt.Sprintf("worker names %d", attempt))
		require.ErrorIs(t, err, ticket.ErrWorkerNamedAttempt, "even its own attempt: it never passes one")
		assert.Zero(t, f.commentCount(t, id, fmt.Sprintf("worker names %d", attempt)))
	}
	_, err := f.svc.AgentCommentOn(f.ctx, worker, id, intp(2), "stranger names 2")
	require.ErrorIs(t, err, ticket.ErrWorkerNamedAttempt)

	// Naming nothing, a worker's comment lands on the attempt its lease is on.
	_, err = f.svc.AgentCommentOn(f.ctx, a1, id, nil, "worker unnamed")
	require.NoError(t, err)
	require.NotNil(t, f.commentAttempt(t, id, "worker unnamed"))
	assert.Equal(t, 1, *f.commentAttempt(t, id, "worker unnamed"))
}

func TestAgentComment_AConsoleThatWorksAnAttemptCannotReachItsSibling(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	id, a1, _ := f.running(t)
	f.with(ticket.WithOwnerResolver(f.owners(a1)))

	_, err := f.svc.AgentCommentOn(f.ctx, a1, id, intp(2), "reach past the lease")
	require.ErrorIs(t, err, ticket.ErrWorkerNamedAttempt)
	assert.Zero(t, f.commentCount(t, id, "reach past the lease"))

	_, err = f.svc.AgentCommentOn(f.ctx, a1, id, intp(1), "its own")
	require.NoError(t, err)
}

func (f *fixture) postComment(t *testing.T, mux http.Handler, id int64, attempt string) (code int, body string) {
	t.Helper()
	return f.postCommentBody(t, mux, id, attempt, "A board word.")
}

func (f *fixture) postCommentBody(t *testing.T, mux http.Handler, id int64, attempt, text string) (code int, body string) {
	t.Helper()
	rec := send(t, mux, http.MethodPost, fmt.Sprintf("/api/tickets/%d/comments%s", id, attempt), fmt.Sprintf(`{"body": %q}`, text))
	return rec.Code, rec.Body.String()
}

func TestBoardComment_TakesTheAttempt(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	mux := f.boardMux()
	single := f.ticket(t, "Single", "a/")
	two := f.twoLive(t)

	t.Run("one live attempt: no argument needed, and the comment goes where it always did", func(t *testing.T) {
		code, body := f.postComment(t, mux, single.ID, "")
		require.Equal(t, http.StatusCreated, code, body)
		assert.Nil(t, f.commentAttempt(t, single.ID, "A board word."))
	})
	t.Run("several live and none named: the ticket's own, for every attempt", func(t *testing.T) {
		code, body := f.postComment(t, mux, two, "")
		require.Equal(t, http.StatusCreated, code, body)
		assert.Nil(t, f.commentAttempt(t, two, "A board word."))
		assert.Equal(t, 1, f.commentCount(t, two, "A board word."))
	})
	t.Run("naming one lands the comment on it", func(t *testing.T) {
		code, body := f.postCommentBody(t, mux, two, "?attempt=2", "A word for two.")
		require.Equal(t, http.StatusCreated, code, body)
		require.NotNil(t, f.commentAttempt(t, two, "A word for two."))
		assert.Equal(t, 2, *f.commentAttempt(t, two, "A word for two."))
	})
	t.Run("an attempt that does not exist: 404", func(t *testing.T) {
		code, body := f.postComment(t, mux, two, "?attempt=5")
		assert.Equal(t, http.StatusNotFound, code, body)
	})
	t.Run("an attempt that is not live: 409", func(t *testing.T) {
		f.setAttemptStatus(t, two, 1, "closed")
		code, body := f.postComment(t, mux, two, "?attempt=1")
		assert.Equal(t, http.StatusConflict, code, body)
	})
}

func TestBoardDecisions_TakeTheAttempt(t *testing.T) {
	t.Parallel()
	// Attempt 1 is in review and attempt 2 is queued; attempt 1 is promoted.
	setup := func(t *testing.T) (*fixture, http.Handler, int64) {
		f := newFixture(t)
		a1 := f.agent(t, "first")
		made := f.ticket(t, "In review", "a/")
		f.addAttempt(t, made.ID, false)
		require.NotNil(t, f.claim(t, a1))
		f.review(t, a1)
		return f, f.boardMux(), made.ID
	}
	post := func(mux http.Handler, id int64, verb, query, body string) (int, string) {
		rec := send(t, mux, http.MethodPost, fmt.Sprintf("/api/tickets/%d/%s%s", id, verb, query), body)
		return rec.Code, rec.Body.String()
	}

	for _, verb := range []struct{ path, body string }{
		{"approve", ``},
		{"request_changes", `{"comment": "Split it."}`},
	} {
		t.Run(verb.path+": several live and none named is refused with the list", func(t *testing.T) {
			_, mux, id := setup(t)
			code, body := post(mux, id, verb.path, "", verb.body)
			require.Equal(t, http.StatusConflict, code, body)
			assert.Contains(t, body, "attempt 2")
		})
		t.Run(verb.path+": the promoted attempt, named, is decided", func(t *testing.T) {
			f, mux, id := setup(t)
			code, body := post(mux, id, verb.path, "?attempt=1", verb.body)
			require.Equal(t, http.StatusOK, code, body)
			assert.NotEqual(t, ticket.StatusReview, f.status(t, id))
		})
		t.Run(verb.path+": a sibling not in review, named, is refused and nothing changes", func(t *testing.T) {
			f, mux, id := setup(t)
			code, body := post(mux, id, verb.path, "?attempt=2", verb.body)
			require.Equal(t, http.StatusConflict, code, body)
			assert.Equal(t, ticket.StatusReview, f.status(t, id))
		})
		t.Run(verb.path+": an attempt that does not exist: 404", func(t *testing.T) {
			_, mux, id := setup(t)
			code, body := post(mux, id, verb.path, "?attempt=9", verb.body)
			assert.Equal(t, http.StatusNotFound, code, body)
		})
	}
}

func TestNarrowScope_IsRefusedWhileSeveralAttemptsAreLive(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	id, a1, _ := f.running(t)
	require.Equal(t, []string{"a/"}, f.paths(t, id))
	f.addPath(t, id, "b/")

	_, err := f.svc.NarrowScope(f.ctx, a1, []string{"b/"}, nil)
	require.ErrorIs(t, err, ticket.ErrNarrowWithSiblings)
	assert.ElementsMatch(t, []string{"a/", "b/"}, f.paths(t, id), "nothing was dropped")

	// With the sibling no longer live there is one attempt, as before.
	f.setAttemptStatus(t, id, 2, "closed")
	result, err := f.svc.NarrowScope(f.ctx, a1, []string{"b/"}, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"a/"}, result.Remaining)
}

func (f *fixture) addPath(t *testing.T, id int64, path string) {
	t.Helper()
	_, err := f.database.Exec(f.ctx, `INSERT INTO ticket_paths (ticket_id, path, source) VALUES ($1, $2, 'filed')`, id, path)
	require.NoError(t, err)
}

func TestAgentComment_AConsoleThatWorksAnAttemptNeedsNoNameToReachIt(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	id, a1, _ := f.running(t)
	f.with(ticket.WithOwnerResolver(f.owners(a1)))

	_, err := f.svc.AgentCommentOn(f.ctx, a1, id, nil, "unnamed by the holder")
	require.NoError(t, err, "its lease names the attempt, so several live ones are no ambiguity")
	require.NotNil(t, f.commentAttempt(t, id, "unnamed by the holder"))
	assert.Equal(t, 1, *f.commentAttempt(t, id, "unnamed by the holder"))
}

// The board resolves the attempt before the comment's transaction begins, so
// the transaction checks it is still live.
func TestPostComment_RefusesAnAttemptClosedSinceItWasResolved(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	id := f.twoLive(t)
	ref, err := f.svc.ResolveAttempt(f.ctx, id, intp(2))
	require.NoError(t, err)
	f.setAttemptStatus(t, id, 2, "closed")

	_, err = f.svc.PostComment(f.ctx, &ticket.HumanComment{TicketID: id, UserID: f.human, Body: "late", Attempt: ref})
	require.ErrorIs(t, err, ticket.ErrAttemptNotLive)
	assert.Zero(t, f.commentCount(t, id, "late"))
}
