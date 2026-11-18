package hookserver_test

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/humatest"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/agent"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/hookserver"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/session"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

func TestUserPromptSubmit_ResetsOnlyALiveRunsBlocks(t *testing.T) {
	t.Run("live", func(t *testing.T) {
		h := newHarness()
		require.NoError(t, h.handler.UserPromptSubmit(h.ctx, &hookserver.UserPromptSubmitInput{SessionID: "s1"}))
		assert.Equal(t, 1, h.sessions.resets)
	})
	t.Run("superseded", func(t *testing.T) {
		h := newHarness()
		h.sessions.superseded = true
		require.NoError(t, h.handler.UserPromptSubmit(h.ctx, &hookserver.UserPromptSubmitInput{SessionID: "s1"}))
		assert.Zero(t, h.sessions.resets)
	})
	t.Run("never seen", func(t *testing.T) {
		h := newHarness()
		h.sessions.unknownSession = true
		require.NoError(t, h.handler.UserPromptSubmit(h.ctx, &hookserver.UserPromptSubmitInput{SessionID: "s1"}))
		assert.Zero(t, h.sessions.resets)
		assert.Zero(t, h.sessions.startCalls, "a prompt binds nothing")
	})
	t.Run("conductor", func(t *testing.T) {
		h := newHarness()
		h.asConductor()
		require.NoError(t, h.handler.UserPromptSubmit(h.ctx, &hookserver.UserPromptSubmitInput{SessionID: "s1"}))
		assert.Zero(t, h.sessions.touched)
		assert.Zero(t, h.sessions.resets)
	})
}

// Bookkeeping must never stand between a human and their agent.
func TestUserPromptSubmit_BookkeepingFailuresAreOnlyLogged(t *testing.T) {
	t.Run("touch fails", func(t *testing.T) {
		h := newHarness()
		h.sessions.touchErr = errors.New("db down")
		require.NoError(t, h.handler.UserPromptSubmit(h.ctx, &hookserver.UserPromptSubmitInput{SessionID: "s1"}))
		assert.Zero(t, h.sessions.resets, "no reset on a run that could not be confirmed live")
	})
	t.Run("reset fails", func(t *testing.T) {
		h := newHarness()
		h.sessions.resetErr = errors.New("db down")
		require.NoError(t, h.handler.UserPromptSubmit(h.ctx, &hookserver.UserPromptSubmitInput{SessionID: "s1"}))
		assert.Equal(t, 1, h.sessions.resets)
	})
}

func TestUserPromptSubmitRoute_AnswersEmptyAndIgnoresUnknownFields(t *testing.T) {
	h := newHarness()
	_, api := humatest.New(t)
	api.UseMiddleware(func(ctx huma.Context, next func(huma.Context)) {
		next(huma.WithContext(ctx, authn.NewContext(ctx.Context(), identity{id: h.agentID})))
	})
	hookserver.RegisterRoutes(api, h.handler)

	resp := api.Post("/hooks/user-prompt-submit", map[string]any{
		"session_id": "s1", "cwd": "/w", "prompt": "check slophaus", "hook_event_name": "UserPromptSubmit",
	})

	require.Equal(t, http.StatusOK, resp.Code, resp.Body.String())
	assert.Empty(t, resp.Body.String(), "for this event stdout becomes context: the answer must say nothing")
	assert.Equal(t, 1, h.sessions.resets)
}

// nudgeHarness is the Stop and UserPromptSubmit hooks over the real session
// store: the loop breaker's count and the stuck notice live in the database,
// and a fake would only repeat what the test told it.
type nudgeHarness struct {
	*harness
	sessions  *session.Service
	agents    *agent.Service
	tenant    uuid.UUID
	tenantCtx context.Context
	ctx       context.Context
}

func newNudgeHarness(t *testing.T) *nudgeHarness {
	t.Helper()
	database := dbtest.WithDB(t)
	tenant := dbtest.NewTenant(t, database)
	tctx := db.WithTenant(context.Background(), tenant)
	agents := agent.NewService(agent.NewPostgresStore(database), ticket.NewService(ticket.NewPostgresStore(database), slog.DiscardHandler), nil, slog.DiscardHandler)
	created, err := agents.Create(tctx, "builder-1")
	require.NoError(t, err)

	sessions := session.NewService(session.NewPostgresStore(database), slog.DiscardHandler)
	h := newHarness()
	h.agentID = created.ID
	// Holding a ticket is what makes each stop a block.
	h.tickets.lease = &ticket.Lease{TicketID: 42}
	h.handler = hookserver.NewHandler(hookserver.Deps{
		Tickets: h.tickets, Sessions: sessions,
	}, slog.DiscardHandler)
	ctx := authn.NewContext(tctx, tenantAgent{identity: identity{id: created.ID}, tenant: tenant})
	_, err = sessions.Start(ctx, created.ID, "s1", "/w")
	require.NoError(t, err)
	return &nudgeHarness{harness: h, sessions: sessions, agents: agents, tenant: tenant, tenantCtx: tctx, ctx: ctx}
}

func (n *nudgeHarness) stop(t *testing.T, sessionID string) hookserver.StopDecision {
	t.Helper()
	d, err := n.handler.Stop(n.ctx, &hookserver.StopInput{SessionID: sessionID})
	require.NoError(t, err)
	return d
}

func (n *nudgeHarness) nudge(t *testing.T, sessionID string) {
	t.Helper()
	require.NoError(t, n.handler.UserPromptSubmit(n.ctx, &hookserver.UserPromptSubmitInput{SessionID: sessionID}))
}

// trip stops with no progress until the loop breaker lets the run stop.
func (n *nudgeHarness) trip(t *testing.T) {
	t.Helper()
	for range session.MaxStopBlocks - 1 {
		require.Equal(t, "block", n.stop(t, "s1").Decision)
	}
	require.Empty(t, n.stop(t, "s1").Decision, "the breaker lets the run stop")
}

func (n *nudgeHarness) current(t *testing.T) *session.Session {
	t.Helper()
	s, err := n.sessions.Current(n.ctx, n.agentID)
	require.NoError(t, err)
	return s
}

// Seen on a real fleet: a human typed "check slophaus" into agents the
// breaker had stopped, and each one's next Stop tripped at once because
// the count was still at the limit -- no block, so the agent was never told
// what to do.
func TestNudge_AfterTheBreakerTripsTheAgentIsBlockedAgain(t *testing.T) {
	n := newNudgeHarness(t)
	n.tickets.ticket = &ticket.Ticket{ID: 42, Status: ticket.StatusInProgress}
	n.trip(t)
	require.NotNil(t, n.current(t).Stuck)

	n.nudge(t, "s1")
	d := n.stop(t, "s1")

	assert.Equal(t, "block", d.Decision, "a nudged agent gets its blocks back")
	assert.Contains(t, d.Reason, "#42")
}

func TestNudge_TheBreakerTripsAgainAfterMoreBlocksWithNoProgress(t *testing.T) {
	n := newNudgeHarness(t)
	n.trip(t)

	n.nudge(t, "s1")
	n.trip(t)
}

func TestNudge_TheStuckBannerSurvivesItAndClearsOnProgress(t *testing.T) {
	n := newNudgeHarness(t)
	n.trip(t)
	stuck := n.current(t).Stuck
	require.NotNil(t, stuck)

	n.nudge(t, "s1")
	require.Equal(t, "block", n.stop(t, "s1").Decision)
	after := n.current(t).Stuck
	require.NotNil(t, after, "a nudge that does not help still shows as stuck")
	assert.Equal(t, stuck.At, after.At)

	require.NoError(t, n.sessions.RecordProgress(n.ctx, n.agentID))
	assert.Nil(t, n.current(t).Stuck)
}

func TestNudge_ASupersededRunsPromptLeavesTheLiveRunsCountAlone(t *testing.T) {
	n := newNudgeHarness(t)
	_, err := n.sessions.Start(n.ctx, n.agentID, "s2", "/w")
	require.NoError(t, err)
	for range session.MaxStopBlocks - 1 {
		require.Equal(t, "block", n.stop(t, "s2").Decision)
	}

	n.nudge(t, "s1")

	assert.Equal(t, session.MaxStopBlocks-1, n.current(t).StopBlockCount)
}

// The route authenticates as the agent, and the reset is scoped to that
// agent's own run: a session id another agent happens to hold is not
// reachable by naming it.
func TestNudge_APromptCannotResetAnotherAgentsRun(t *testing.T) {
	n := newNudgeHarness(t)
	other, err := n.agents.Create(n.tenantCtx, "builder-2")
	require.NoError(t, err)
	otherCtx := authn.NewContext(n.tenantCtx, tenantAgent{identity: identity{id: other.ID}, tenant: n.tenant})
	_, err = n.sessions.Start(otherCtx, other.ID, "s1", "/w2")
	require.NoError(t, err)
	for range session.MaxStopBlocks {
		_, _, err = n.sessions.RecordBlock(otherCtx, other.ID, "s1")
		require.NoError(t, err)
	}

	n.nudge(t, "s1")

	theirs, err := n.sessions.Current(otherCtx, other.ID)
	require.NoError(t, err)
	assert.Equal(t, session.MaxStopBlocks, theirs.StopBlockCount)
}
