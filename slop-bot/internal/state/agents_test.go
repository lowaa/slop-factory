package state_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/control"
	"github.com/lowaa/slop-factory/slop-bot/internal/ident"
	"github.com/lowaa/slop-factory/slop-bot/internal/state"
)

func TestCreateAndGetRoundTripEveryField(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	n := &state.NewAgent{
		ID: "a1", Ticket: "42", Harness: "agy", Driver: "tui", Model: "m", Effort: "high",
		TUISession: "slop-a1", Pool: state.PoolUrgent, State: state.StateRefining, Reason: "spawned",
		TokenHash: control.HashToken("secret"),
		Worktrees: []state.Worktree{
			{Repo: "go-db", Path: "/w/t42-go-db", Branch: "t42"},
			{Repo: "slop-factory", Path: "/w/t42-slop-factory", Branch: "t42", Primary: true},
		},
	}
	created, err := f.Agents.Create(ctx, n)
	require.NoError(t, err)

	got, err := f.Agents.Get(ctx, "a1")
	require.NoError(t, err)
	want := &state.Agent{
		ID: "a1", Ticket: "42", Harness: "agy", Driver: "tui", Model: "m", Effort: "high",
		Mode: state.ModeProgrammatic, TUISession: "slop-a1", Pool: state.PoolUrgent,
		State: state.StateRefining, StateSince: t0, CreatedAt: t0,
		// The primary first: it is the harness's cwd.
		Worktrees: []state.Worktree{n.Worktrees[1], n.Worktrees[0]},
	}
	assert.Equal(t, want, got)
	assert.Equal(t, want, created)

	hist, err := f.Agents.History(ctx, "a1")
	require.NoError(t, err)
	require.Len(t, hist, 1)
	assert.Equal(t, state.StateChange{ID: hist[0].ID, Agent: "a1", To: state.StateRefining, Reason: "spawned", At: t0}, hist[0])
}

func TestTokenIsStoredOnlyAsItsHash(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	n := newAgent("a1", "1")
	n.TokenHash = control.HashToken("plaintext-token")
	_, err := f.Agents.Create(ctx, n)
	require.NoError(t, err)

	var stored string
	require.NoError(t, f.db.NewRaw("SELECT token_hash FROM agents").Scan(ctx, &stored))
	assert.Equal(t, control.HashToken("plaintext-token"), stored)
	assert.NotContains(t, stored, "plaintext-token")

	// The store is what socket auth resolves tokens through.
	var resolver control.TokenResolver = f.Agents
	got, err := resolver.AgentForTokenHash(ctx, control.HashToken("plaintext-token"))
	require.NoError(t, err)
	assert.Equal(t, ident.AgentPrincipal("a1", "1"), got)
	_, err = resolver.AgentForTokenHash(ctx, control.HashToken("other"))
	assert.ErrorIs(t, err, state.ErrNotFound)

	// Anything but a hex sha256 is refused, so a plaintext token can't be
	// stored by mistake.
	n = newAgent("a2", "2")
	n.TokenHash = "plaintext-token"
	_, err = f.Agents.Create(ctx, n)
	assert.ErrorContains(t, err, "CHECK")
}

func TestCreateRefusesConflicts(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	f.createAgent(t, "a1", "1")

	cases := map[string]*state.NewAgent{
		"second live agent for a ticket": newAgent("a2", "1"),
		"taken id":                       newAgent("a1", "2"),
		"reused token hash": func() *state.NewAgent {
			n := newAgent("a3", "3")
			n.TokenHash = control.HashToken("token-a1")
			return n
		}(),
		"two primary worktrees": func() *state.NewAgent {
			n := newAgent("a4", "4")
			n.Worktrees = []state.Worktree{{Repo: "x", Path: "/x", Primary: true}, {Repo: "y", Path: "/y", Primary: true}}
			return n
		}(),
		"the same repo twice": func() *state.NewAgent {
			n := newAgent("a5", "5")
			n.Worktrees = []state.Worktree{{Repo: "x", Path: "/x"}, {Repo: "x", Path: "/x2"}}
			return n
		}(),
	}
	// The group returns once its parallel subtests have.
	t.Run("refused", func(t *testing.T) {
		for name, n := range cases {
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				_, err := f.Agents.Create(ctx, n)
				assert.ErrorIs(t, err, state.ErrConflict)
			})
		}
	})
	// None of the refused creates left anything behind: each is one
	// transaction, worktrees and history included.
	assert.EqualValues(t, 1, f.count(t, "agents"))
	assert.EqualValues(t, 1, f.count(t, "agent_state_changes"))
	assert.EqualValues(t, 0, f.count(t, "agent_worktrees"))
}

func TestCreateRefusesTheUserNameAndAnEndingState(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	_, err := f.Agents.Create(ctx, newAgent("user", "1"))
	assert.ErrorContains(t, err, "CHECK")

	n := newAgent("a1", "1")
	n.State = state.StateDone
	_, err = f.Agents.Create(ctx, n)
	assert.Error(t, err)
	assert.EqualValues(t, 0, f.count(t, "agents"))
}

func TestATicketGetsANewAgentOnceItsLastEnded(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	f.createAgent(t, "a1", "1")
	f.setState(t, "a1", state.StateDone)

	_, err := f.Agents.Create(ctx, newAgent("a2", "1"))
	require.NoError(t, err)
	live, err := f.Agents.LiveForTicket(ctx, "1")
	require.NoError(t, err)
	assert.Equal(t, ident.AgentID("a2"), live.ID)
}

func TestSetStateRecordsHistoryAndSince(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	f.createAgent(t, "a1", "1")

	f.clock.Advance(time.Minute)
	changed, err := f.Agents.SetState(ctx, &state.Transition{Agent: "a1", To: state.StateWorking, Reason: "first event"})
	require.NoError(t, err)
	assert.True(t, changed)
	f.clock.Advance(time.Minute)
	f.setState(t, "a1", state.StateIdle)

	// Already idle: nothing written, so StateSince still says since when.
	f.clock.Advance(time.Minute)
	changed, err = f.Agents.SetState(ctx, &state.Transition{Agent: "a1", To: state.StateIdle})
	require.NoError(t, err)
	assert.False(t, changed)

	got, err := f.Agents.Get(ctx, "a1")
	require.NoError(t, err)
	assert.Equal(t, state.StateIdle, got.State)
	assert.Equal(t, t0.Add(2*time.Minute), got.StateSince)

	hist, err := f.Agents.History(ctx, "a1")
	require.NoError(t, err)
	var steps []string
	for _, h := range hist {
		steps = append(steps, fmt.Sprintf("%s->%s@%s %s", h.From, h.To, h.At.Sub(t0), h.Reason))
	}
	assert.Equal(t, []string{"->starting@0s ", "starting->working@1m0s first event", "working->idle@2m0s "}, steps)
}

func TestSetStateFromMustMatch(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	f.createAgent(t, "a1", "1")

	_, err := f.Agents.SetState(ctx, &state.Transition{Agent: "a1", From: state.StateWorking, To: state.StateIdle})
	assert.ErrorIs(t, err, state.ErrConflict)
	_, err = f.Agents.SetState(ctx, &state.Transition{Agent: "nope", To: state.StateIdle})
	assert.ErrorIs(t, err, state.ErrNotFound)

	got, err := f.Agents.Get(ctx, "a1")
	require.NoError(t, err)
	assert.Equal(t, state.StateStarting, got.State)
	assert.EqualValues(t, 1, f.count(t, "agent_state_changes"))
}

func TestConcurrentConditionalSetStateHasOneWinner(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	f.createAgent(t, "a1", "1")
	f.setState(t, "a1", state.StateWorking)

	targets := []state.AgentState{state.StateIdle, state.StateWaitingUser, state.StateStalled, state.StateExited, state.StateCrashed, state.StateDiscussion}
	errs := make([]error, len(targets))
	var wg sync.WaitGroup
	for i, to := range targets {
		wg.Go(func() {
			_, errs[i] = f.Agents.SetState(ctx, &state.Transition{Agent: "a1", From: state.StateWorking, To: to})
		})
	}
	wg.Wait()

	var winner state.AgentState
	for i, err := range errs {
		if err == nil {
			assert.Empty(t, winner, "two transitions out of working both won")
			winner = targets[i]
			continue
		}
		assert.ErrorIs(t, err, state.ErrConflict)
	}
	got, err := f.Agents.Get(ctx, "a1")
	require.NoError(t, err)
	assert.Equal(t, winner, got.State)
	hist, err := f.Agents.History(ctx, "a1")
	require.NoError(t, err)
	assert.Len(t, hist, 3, "created, working, and the one winner")
}

func TestSetStateRollsBackWhenHistoryCannotBeWritten(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	f.createAgent(t, "a1", "1")
	// The real schema, made to refuse the history row: the state update
	// before it must not survive alone.
	_, err := f.db.NewRaw(`CREATE TRIGGER no_history BEFORE INSERT ON agent_state_changes
		BEGIN SELECT RAISE(ABORT, 'history refused'); END`).Exec(ctx)
	require.NoError(t, err)

	f.clock.Advance(time.Minute)
	_, err = f.Agents.SetState(ctx, &state.Transition{Agent: "a1", To: state.StateDone})
	require.ErrorContains(t, err, "history refused")

	got, err := f.Agents.Get(ctx, "a1")
	require.NoError(t, err)
	assert.Equal(t, state.StateStarting, got.State)
	assert.Equal(t, t0, got.StateSince)
	assert.Nil(t, got.EndedAt)
}

func TestAnEndedAgentIsFrozenAndNoLongerLive(t *testing.T) {
	t.Parallel()
	for _, end := range []state.AgentState{state.StateDone, state.StateReleased} {
		t.Run(string(end), func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			ctx := context.Background()
			f.createAgent(t, "a1", "1")
			f.createAgent(t, "a2", "2")
			f.clock.Advance(time.Hour)
			f.setState(t, "a1", end)

			got, err := f.Agents.Get(ctx, "a1")
			require.NoError(t, err)
			assert.Equal(t, ptr(t0.Add(time.Hour)), got.EndedAt)

			_, err = f.Agents.SetState(ctx, &state.Transition{Agent: "a1", To: state.StateWorking})
			assert.ErrorIs(t, err, state.ErrEnded)
			assert.ErrorIs(t, f.Agents.SetProgressNote(ctx, "a1", "x"), state.ErrEnded)
			assert.ErrorIs(t, f.Agents.SetConversationID(ctx, "a1", "x"), state.ErrEnded)
			assert.ErrorIs(t, f.Agents.ResetNudges(ctx, "a1"), state.ErrEnded)
			_, err = f.Agents.AddNudge(ctx, "a1")
			assert.ErrorIs(t, err, state.ErrEnded)
			assert.ErrorIs(t, f.Agents.AddWorktree(ctx, "a1", &state.Worktree{Repo: "r", Path: "/r"}), state.ErrEnded)

			_, err = f.Agents.AgentForTokenHash(ctx, control.HashToken("token-a1"))
			assert.ErrorIs(t, err, state.ErrNotFound, "an ended agent's token authenticates no one")
			_, err = f.Agents.LiveForTicket(ctx, "1")
			assert.ErrorIs(t, err, state.ErrNotFound)
			live, err := f.Agents.ListLive(ctx)
			require.NoError(t, err)
			require.Len(t, live, 1)
			assert.Equal(t, ident.AgentID("a2"), live[0].ID)
		})
	}
}

func TestExitedAndCrashedAgentsStayLive(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	f.createAgent(t, "a1", "1")
	f.setState(t, "a1", state.StateCrashed)
	// The daemon resumes a crashed harness.
	f.setState(t, "a1", state.StateWorking)
	_, err := f.Agents.LiveForTicket(ctx, "1")
	assert.NoError(t, err)
}

func TestListLiveIsOldestFirstWithWorktrees(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	f.createAgent(t, "b", "2")
	f.clock.Advance(time.Second)
	f.createAgent(t, "a", "1")
	require.NoError(t, f.Agents.AddWorktree(ctx, "a", &state.Worktree{Repo: "r", Path: "/w/t1-r", Primary: true}))

	live, err := f.Agents.ListLive(ctx)
	require.NoError(t, err)
	require.Len(t, live, 2)
	assert.Equal(t, ident.AgentID("b"), live[0].ID)
	assert.Empty(t, live[0].Worktrees)
	assert.Equal(t, []state.Worktree{{Repo: "r", Path: "/w/t1-r", Primary: true}}, live[1].Worktrees)
}

func TestAgentFieldUpdates(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	f.createAgent(t, "a1", "1")

	require.NoError(t, f.Agents.SetConversationID(ctx, "a1", "conv-1"))
	require.NoError(t, f.Agents.SetProgressNote(ctx, "a1", "running the e2e suite"))
	for want := 1; want <= 3; want++ {
		n, err := f.Agents.AddNudge(ctx, "a1")
		require.NoError(t, err)
		assert.Equal(t, want, n)
	}
	got, err := f.Agents.Get(ctx, "a1")
	require.NoError(t, err)
	assert.Equal(t, "conv-1", got.ConversationID)
	assert.Equal(t, "running the e2e suite", got.ProgressNote)
	assert.Equal(t, 3, got.Nudges)

	require.NoError(t, f.Agents.ResetNudges(ctx, "a1"))
	got, err = f.Agents.Get(ctx, "a1")
	require.NoError(t, err)
	assert.Equal(t, 0, got.Nudges)

	assert.ErrorIs(t, f.Agents.SetProgressNote(ctx, "nope", "x"), state.ErrNotFound)
	_, err = f.Agents.AddNudge(ctx, "nope")
	assert.ErrorIs(t, err, state.ErrNotFound)
	_, err = f.Agents.Get(ctx, "nope")
	assert.ErrorIs(t, err, state.ErrNotFound)
}

func TestWorktrees(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	f.createAgent(t, "a1", "1")

	require.NoError(t, f.Agents.AddWorktree(ctx, "a1", &state.Worktree{Repo: "a", Path: "/a", Primary: true}))
	require.NoError(t, f.Agents.AddWorktree(ctx, "a1", &state.Worktree{Repo: "b", Path: "/b"}))
	assert.ErrorIs(t, f.Agents.AddWorktree(ctx, "a1", &state.Worktree{Repo: "c", Path: "/c", Primary: true}), state.ErrConflict)
	assert.ErrorIs(t, f.Agents.AddWorktree(ctx, "a1", &state.Worktree{Repo: "d", Path: "/b"}), state.ErrConflict)
	assert.ErrorIs(t, f.Agents.AddWorktree(ctx, "nope", &state.Worktree{Repo: "a", Path: "/a"}), state.ErrNotFound)

	// Cleanup runs after the ticket ends.
	f.setState(t, "a1", state.StateDone)
	require.NoError(t, f.Agents.RemoveWorktree(ctx, "a1", "a"))
	assert.ErrorIs(t, f.Agents.RemoveWorktree(ctx, "a1", "a"), state.ErrNotFound)

	got, err := f.Agents.Get(ctx, "a1")
	require.NoError(t, err)
	assert.Equal(t, []state.Worktree{{Repo: "b", Path: "/b"}}, got.Worktrees)
}

func TestAttachLease(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	f.createAgent(t, "a1", "1")
	lease := func(holder string, d time.Duration) *state.Lease {
		return &state.Lease{Holder: holder, ExpiresAt: f.clock.Now().Add(d)}
	}

	require.NoError(t, f.Agents.AcquireLease(ctx, "a1", lease("cli-1", time.Minute)))
	got, err := f.Agents.Get(ctx, "a1")
	require.NoError(t, err)
	assert.Equal(t, state.ModeAttached, got.Mode)
	assert.Equal(t, &state.Lease{Holder: "cli-1", ExpiresAt: t0.Add(time.Minute)}, got.Lease)

	// A second attach is refused, naming who holds it.
	err = f.Agents.AcquireLease(ctx, "a1", lease("cli-2", time.Minute))
	assert.ErrorIs(t, err, state.ErrConflict)
	var held *state.LeaseHeldError
	require.ErrorAs(t, err, &held)
	assert.Equal(t, "cli-1", held.Holder)

	// The holder's heartbeat extends it; nobody else's does.
	require.NoError(t, f.Agents.RenewLease(ctx, "a1", lease("cli-1", 2*time.Minute)))
	assert.ErrorIs(t, f.Agents.RenewLease(ctx, "a1", lease("cli-2", time.Hour)), state.ErrConflict)

	f.clock.Advance(time.Minute)
	expired, err := f.Agents.ExpiredLeases(ctx, f.clock.Now())
	require.NoError(t, err)
	assert.Empty(t, expired)
	f.clock.Advance(time.Minute)
	expired, err = f.Agents.ExpiredLeases(ctx, f.clock.Now())
	require.NoError(t, err)
	require.Len(t, expired, 1)
	assert.Equal(t, ident.AgentID("a1"), expired[0].ID)

	// Expired, so another attach takes it over.
	require.NoError(t, f.Agents.AcquireLease(ctx, "a1", lease("cli-2", time.Minute)))
	assert.ErrorIs(t, f.Agents.ReleaseLease(ctx, "a1", "cli-1"), state.ErrConflict)
	require.NoError(t, f.Agents.ReleaseLease(ctx, "a1", "cli-2"))
	got, err = f.Agents.Get(ctx, "a1")
	require.NoError(t, err)
	assert.Equal(t, state.ModeProgrammatic, got.Mode)
	assert.Nil(t, got.Lease)

	// Once handed back, a late heartbeat can't re-attach it.
	assert.ErrorIs(t, f.Agents.RenewLease(ctx, "a1", lease("cli-2", time.Minute)), state.ErrConflict)
}

func TestAnAgentThatEndsWhileAttachedCanStillBeReleased(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	f.createAgent(t, "a1", "1")
	require.NoError(t, f.Agents.AcquireLease(ctx, "a1", &state.Lease{Holder: "cli", ExpiresAt: t0.Add(time.Minute)}))
	f.setState(t, "a1", state.StateDone)

	assert.ErrorIs(t, f.Agents.AcquireLease(ctx, "a1", &state.Lease{Holder: "cli-2", ExpiresAt: t0.Add(time.Hour)}), state.ErrEnded)
	require.NoError(t, f.Agents.ReleaseLease(ctx, "a1", "cli"))
	assert.ErrorIs(t, f.Agents.AcquireLease(ctx, "nope", &state.Lease{Holder: "cli", ExpiresAt: t0}), state.ErrNotFound)
}

func TestLeaseColumnsMoveWithTheMode(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	f.createAgent(t, "a1", "1")
	// The schema refuses a lease on a programmatic agent, and an attached
	// agent without one, whatever writes the row.
	_, err := f.db.NewRaw("UPDATE agents SET lease_holder = 'x' WHERE id = 'a1'").Exec(ctx)
	assert.ErrorContains(t, err, "CHECK")
	_, err = f.db.NewRaw("UPDATE agents SET mode = 'attached' WHERE id = 'a1'").Exec(ctx)
	assert.ErrorContains(t, err, "CHECK")
}

func TestTheExpirySweepLosesToAHeartbeatThatBeatIt(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	f.createAgent(t, "a1", "1")
	require.NoError(t, f.Agents.AcquireLease(ctx, "a1", &state.Lease{Holder: "cli", ExpiresAt: t0.Add(time.Minute)}))

	f.clock.Advance(time.Minute)
	sweptAt := f.clock.Now()
	expired, err := f.Agents.ExpiredLeases(ctx, sweptAt)
	require.NoError(t, err)
	require.Len(t, expired, 1)

	// Expired, so the heartbeat can't revive it: the CLI must re-acquire.
	assert.ErrorIs(t, f.Agents.RenewLease(ctx, "a1", &state.Lease{Holder: "cli", ExpiresAt: sweptAt.Add(time.Minute)}), state.ErrConflict)
	require.NoError(t, f.Agents.AcquireLease(ctx, "a1", &state.Lease{Holder: "cli", ExpiresAt: sweptAt.Add(time.Minute)}))

	// The sweep, acting on what it read before the re-acquire, loses.
	assert.ErrorIs(t, f.Agents.ReleaseExpiredLease(ctx, "a1", "cli", sweptAt), state.ErrConflict)
	got, err := f.Agents.Get(ctx, "a1")
	require.NoError(t, err)
	assert.Equal(t, state.ModeAttached, got.Mode)

	// Left to expire again, the sweep hands it back.
	f.clock.Advance(time.Minute)
	require.NoError(t, f.Agents.ReleaseExpiredLease(ctx, "a1", "cli", f.clock.Now()))
	got, err = f.Agents.Get(ctx, "a1")
	require.NoError(t, err)
	assert.Equal(t, state.ModeProgrammatic, got.Mode)
}

func TestRenewingTheLeaseOfAnEndedAgentIsErrEnded(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	f.createAgent(t, "a1", "1")
	require.NoError(t, f.Agents.AcquireLease(ctx, "a1", &state.Lease{Holder: "cli", ExpiresAt: t0.Add(time.Minute)}))
	f.setState(t, "a1", state.StateDone)

	assert.ErrorIs(t, f.Agents.RenewLease(ctx, "a1", &state.Lease{Holder: "cli", ExpiresAt: t0.Add(time.Hour)}), state.ErrEnded)
	assert.ErrorIs(t, f.Agents.RenewLease(ctx, "nope", &state.Lease{Holder: "cli", ExpiresAt: t0.Add(time.Hour)}), state.ErrNotFound)
}

func TestRotateTokenHashRetiresTheOldToken(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	n := newAgent("a1", "1")
	n.TokenHash = control.HashToken("old")
	_, err := f.Agents.Create(ctx, n)
	require.NoError(t, err)

	require.NoError(t, f.Agents.RotateTokenHash(ctx, "a1", control.HashToken("new")))
	_, err = f.Agents.AgentForTokenHash(ctx, control.HashToken("old"))
	assert.ErrorIs(t, err, state.ErrNotFound)
	got, err := f.Agents.AgentForTokenHash(ctx, control.HashToken("new"))
	require.NoError(t, err)
	assert.Equal(t, ident.AgentPrincipal("a1", "1"), got)
}
