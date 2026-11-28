package agents_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/agents"
	"github.com/lowaa/slop-factory/slop-bot/internal/config"
	"github.com/lowaa/slop-factory/slop-bot/internal/control"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/fake"
	"github.com/lowaa/slop-factory/slop-bot/internal/ident"
	"github.com/lowaa/slop-factory/slop-bot/internal/state"
	"github.com/lowaa/slop-factory/slop-bot/internal/tickets"
	"github.com/lowaa/slop-factory/slop-bot/internal/transcript"
)

// reportLimit has a fresh ticket's agent report used% of its plan, resetting
// at resets, then ask the user, so it waits without holding a slot (or
// stalling when the clock moves).
func (f *fixture) reportLimit(used float64, resets time.Time) *state.Agent {
	f.t.Helper()
	return f.reportLimits(fake.Command{Cmd: fake.CmdLimit, Used: used, ResetsAt: resets.Unix()})
}

// reportLimits is reportLimit for any limit commands.
func (f *fixture) reportLimits(limits ...fake.Command) *state.Agent {
	f.t.Helper()
	tk := f.file("reporter", nil)
	s := f.session()
	f.user(s)
	a := f.agentFor(tk.ID)
	f.turn(s, func() {
		f.do(s, limits...)
		_, err := f.mgr.Ask(f.ctx(), ident.AgentPrincipal(a.ID, tk.ID), "?", nil, "")
		require.NoError(f.t, err)
	})
	f.await(agents.EventState, toState(state.StateWaitingUser))
	return a
}

// blockUntil waits for n timers on the fake clock, within the test's
// deadline.
func (f *fixture) blockUntil(n int) {
	f.t.Helper()
	done := make(chan struct{})
	go func() {
		f.clock.BlockUntil(n)
		close(done)
	}()
	select {
	case <-done:
	case <-f.ctx().Done():
		require.FailNow(f.t, "timers never set", "want %d, have %d", n, f.clock.Waiters())
	}
}

func heldFor(id ident.TicketID) func(*state.Event) bool {
	return func(e *state.Event) bool { return e.Ticket == id }
}

func TestAHarnessAtItsLimitHoldsNewAgentsUntilItsWindowResets(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "")
	resets := t0.Add(time.Hour)
	f.reportLimit(97, resets)
	assert.Equal(t, []agents.HarnessLimit{{
		Harness: config.ClaudeCode, UsedPercent: 97, ResetsAt: resets, ReportedAt: t0, Threshold: config.DefaultLimitThreshold, Held: true,
	}}, f.mgr.Limits())

	// The reporter is quiet, so its timers are settled.
	timers := f.clock.Waiters()
	held := f.file("waits", nil)
	f.await(agents.EventHeld, heldFor(held.ID))
	f.await(agents.EventUserTold, nil)
	_, err := f.state.Agents.LiveForTicket(context.Background(), held.ID)
	require.ErrorIs(t, err, state.ErrNotFound, "held, not started")

	// The window resets on the clock, and the scheduler looks again by
	// itself: it set a timer for the reset.
	f.blockUntil(timers + 1)
	f.clock.Advance(time.Hour)
	f.await(agents.EventSpawned, heldFor(held.ID))
	assert.False(t, f.mgr.Limits()[0].Held)
}

// Each window is its own limit: the 5-hour one resetting doesn't free a
// plan whose weekly one is still full, and a report of one window doesn't
// replace another's.
func TestEveryFullWindowHoldsUntilTheLastResets(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "")
	week := t0.Add(72 * time.Hour)
	f.reportLimits(
		fake.Command{Cmd: fake.CmdLimit, Window: "five_hour", Used: 99, ResetsAt: t0.Add(time.Hour).Unix()},
		fake.Command{Cmd: fake.CmdLimit, Window: "seven_day", Used: 96, ResetsAt: week.Unix()},
		fake.Command{Cmd: fake.CmdLimit, Window: "five_hour", Used: 98, ResetsAt: t0.Add(time.Hour).Unix()},
	)
	held := f.file("waits", nil)
	ev := f.await(agents.EventHeld, heldFor(held.ID))
	var p struct {
		Until time.Time `json:"until"`
	}
	require.NoError(t, json.Unmarshal(ev.Payload, &p))
	assert.Equal(t, week, p.Until)
	var windows []string
	for _, l := range f.mgr.Limits() {
		windows = append(windows, l.Window)
		assert.True(t, l.Held, l.Window)
	}
	assert.Equal(t, []string{"five_hour", "seven_day"}, windows)
}

// A full window with no reset time can't hold for ever: no agent is left
// on the harness to report it lower.
func TestAHoldWithNoResetTimeEnds(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "")
	f.reportLimits(fake.Command{Cmd: fake.CmdLimit, Used: 100})
	timers := f.clock.Waiters()
	held := f.file("waits", nil)
	f.await(agents.EventHeld, heldFor(held.ID))
	f.blockUntil(timers + 1)
	f.clock.Advance(agents.NoResetHold)
	f.await(agents.EventSpawned, heldFor(held.ID))
}

func TestALowerReportReleasesTheHold(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "harnesses: {claude-code: {limit_threshold: 80}}\n")
	tk := f.file("reporter", nil)
	s := f.session()
	f.user(s)
	a := f.agentFor(tk.ID)
	f.do(s, fake.Command{Cmd: fake.CmdTurnStart}, fake.Command{Cmd: fake.CmdLimit, Used: 85})
	f.await(agents.EventHarness+"limit", nil)
	held := f.file("waits", nil)
	f.await(agents.EventHeld, heldFor(held.ID))

	// 85% is past this harness's 80, with no reset time: held until the
	// harness says otherwise.
	f.do(s, fake.Command{Cmd: fake.CmdLimit, Used: 20})
	f.await(agents.EventSpawned, heldFor(held.ID))
	_ = a
}

func TestOnlyABlankUnstartedTicketFallsBackToAnotherHarness(t *testing.T) {
	t.Parallel()
	f := newFixtureFor(t, "harnesses: {claude-code: {}, codex: {}}\n", []string{config.ClaudeCode, config.Codex})
	f.reportLimit(99, t0.Add(time.Hour))
	ctx := context.Background()

	blank := f.file("blank", nil)
	ev := f.await(agents.EventSpawned, heldFor(blank.ID))
	var p map[string]string
	require.NoError(t, json.Unmarshal(ev.Payload, &p))
	assert.Equal(t, config.Codex, p["harness"])
	assert.Equal(t, config.Codex, f.agentFor(blank.ID).Harness)
	f.session()

	named := f.file("named", func(nt *tickets.NewTicket) { nt.Agent.Harness = config.ClaudeCode })
	f.await(agents.EventHeld, heldFor(named.ID))
	withModel := f.file("model", func(nt *tickets.NewTicket) { nt.Agent.Model = "claude-opus-5-5" })
	f.await(agents.EventHeld, heldFor(withModel.ID))
	withEffort := f.file("effort", func(nt *tickets.NewTicket) { nt.Agent.Effort = "max" })
	f.await(agents.EventHeld, heldFor(withEffort.ID))

	// A ticket that has had an agent keeps to its harness: the
	// conversation is there.
	started := f.file("started", func(nt *tickets.NewTicket) { nt.Assignee = "" })
	_, err := f.state.Agents.Create(ctx, &state.NewAgent{ID: "old", Ticket: started.ID, Harness: config.ClaudeCode, Driver: "rpc",
		Pool: state.PoolNormal, State: state.StateStarting, TokenHash: control.HashToken("old")})
	require.NoError(t, err)
	_, err = f.state.Agents.SetState(ctx, &state.Transition{Agent: "old", To: state.StateReleased})
	require.NoError(t, err)
	_, err = f.tickets.Assign(ctx, ident.UserPrincipal(), started.ID, "ada")
	require.NoError(t, err)
	f.mgr.Kick()
	f.await(agents.EventHeld, heldFor(started.ID))
}

func TestARestartedDaemonStillHoldsAHarnessAtItsLimit(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "")
	require.NoError(t, f.mgr.Stop(context.Background()))
	require.NoError(t, f.state.Settings.SetHarnessLimit(context.Background(),
		&state.HarnessLimit{Harness: config.ClaudeCode, UsedPercent: 100, ResetsAt: t0.Add(time.Hour), ReportedAt: t0}))
	f.mgr = f.newManager(&fakeHarness{h: &fake.Harness{}})
	require.NoError(t, f.mgr.Start(context.Background()))
	t.Cleanup(func() { _ = f.mgr.Stop(context.Background()) })
	held := f.file("waits", nil)
	f.await(agents.EventHeld, heldFor(held.ID))
}

func TestAnEndedAgentsTranscriptIsRetained(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "")
	tk := f.file("t", nil)
	s := f.session()
	f.user(s)
	a := f.agentFor(tk.ID)
	f.turn(s, func() {
		f.do(s, fake.Command{Cmd: fake.CmdToolStart, Tool: "shell", Text: "make"}, fake.Command{Cmd: fake.CmdToolEnd, Tool: "shell", Text: "ok"})
		require.NoError(t, f.mgr.Done(f.ctx(), ident.AgentPrincipal(a.ID, tk.ID), "built"))
	})
	f.await(agents.EventRetained, func(e *state.Event) bool { return e.Agent == a.ID })
	f.await(agents.EventFinished, nil)

	kept, err := f.archive.Load(context.Background(), a.ID)
	require.NoError(t, err)
	var kinds []transcript.Kind
	for _, e := range kept {
		kinds = append(kinds, e.Kind)
	}
	assert.Equal(t, []transcript.Kind{transcript.User, transcript.ToolCall, transcript.ToolResult}, kinds)
	idx, err := f.state.Transcripts.ForAgent(context.Background(), a.ID)
	require.NoError(t, err)
	require.Len(t, idx, 1)
	assert.Equal(t, tk.ID, idx[0].Ticket)
	assert.Equal(t, t0.Add(f.cfg.Retention.Transcripts.D()), idx[0].RetainUntil)
	_, err = os.Stat(filepath.Join(f.paths.TranscriptsDir(), string(a.ID)+".jsonl"))
	require.NoError(t, err)
}

// A conversation whose transcript can't be found is told, not recorded as
// kept.
func TestAMissingTranscriptIsToldNotRetained(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "")
	tk := f.file("t", nil)
	s := f.session()
	f.user(s)
	a := f.agentFor(tk.ID)
	require.NoError(t, os.Remove(filepath.Join(f.transcripts, s.ID+".jsonl")))
	f.turn(s, func() {
		require.NoError(t, f.mgr.Done(f.ctx(), ident.AgentPrincipal(a.ID, tk.ID), "built"))
	})
	ev := f.await(agents.EventUserTold, func(e *state.Event) bool { return e.Agent == a.ID })
	assert.Contains(t, string(ev.Payload), "transcript couldn't be kept")
	f.await(agents.EventFinished, nil)
	idx, err := f.state.Transcripts.ForAgent(context.Background(), a.ID)
	require.NoError(t, err)
	assert.Empty(t, idx)
}
