package daemon

import (
	"context"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-clock"
	"github.com/lowaa/slop-factory/slop-bot/internal/config"
	"github.com/lowaa/slop-factory/slop-bot/internal/control"
	"github.com/lowaa/slop-factory/slop-bot/internal/ident"
	"github.com/lowaa/slop-factory/slop-bot/internal/state"
	"github.com/lowaa/slop-factory/slop-bot/internal/transcript"
)

// The sweep deletes retained transcripts and events past their retention,
// each by its own setting.
func TestTheRetentionSweepPrunesTranscriptsAndEvents(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ctx := context.Background()
	clk := clock.NewFake(time.Date(2026, 11, 28, 9, 0, 0, 0, time.UTC))
	cfg, err := config.Parse([]byte("retention: {transcripts: 48h, events: 24h}\n"), dir, dir)
	require.NoError(t, err)
	st, err := state.Open(ctx, &state.Options{Path: filepath.Join(dir, "state.db"), Logger: slog.New(slog.DiscardHandler), Clock: clk})
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	archive, err := transcript.NewArchive(&transcript.ArchiveOptions{Dir: filepath.Join(dir, "t"), Index: st.Transcripts, Retain: cfg.Retention.Transcripts.D(), Clock: clk})
	require.NoError(t, err)
	d := &Daemon{opts: &Options{Config: cfg, Clock: clk}, state: st, archive: archive}

	_, err = st.Events.Append(ctx, &state.NewEvent{Type: "harness.text", Agent: "t1-aaaaaa"})
	require.NoError(t, err)
	require.NoError(t, archive.Keep(ctx, "t1-aaaaaa", "1", []transcript.Entry{{Kind: transcript.User, Text: "hi"}}))
	clk.Advance(25 * time.Hour)
	_, err = st.Events.Append(ctx, &state.NewEvent{Type: "harness.text", Agent: "t1-aaaaaa"})
	require.NoError(t, err)

	require.NoError(t, d.sweep(ctx))
	evs, err := st.Events.ReadAfter(ctx, 0, 0)
	require.NoError(t, err)
	assert.Len(t, evs, 1, "the day-old event is gone")
	_, err = archive.Load(ctx, "t1-aaaaaa")
	require.NoError(t, err, "kept for two days")

	clk.Advance(24 * time.Hour)
	require.NoError(t, d.sweep(ctx))
	_, err = archive.Load(ctx, "t1-aaaaaa")
	require.ErrorIs(t, err, transcript.ErrNotFound)
}

// The sweep also prunes state history, delivered messages and ended agents,
// each by its own setting, and keeps a person's take-over however old.
func TestTheRetentionSweepPrunesStateChangesMessagesAndAgents(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ctx := context.Background()
	clk := clock.NewFake(time.Date(2026, 11, 28, 9, 0, 0, 0, time.UTC))
	cfg, err := config.Parse([]byte("retention: {state_changes: 1h, messages: 2h, agents: 3h}\n"), dir, dir)
	require.NoError(t, err)
	st, err := state.Open(ctx, &state.Options{Path: filepath.Join(dir, "state.db"), Logger: slog.New(slog.DiscardHandler), Clock: clk})
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	archive, err := transcript.NewArchive(&transcript.ArchiveOptions{Dir: filepath.Join(dir, "t"), Index: st.Transcripts, Retain: cfg.Retention.Transcripts.D(), Clock: clk})
	require.NoError(t, err)
	d := &Daemon{opts: &Options{Config: cfg, Clock: clk}, state: st, archive: archive}

	newAgent := func(id ident.AgentID, ticket ident.TicketID) {
		_, err := st.Agents.Create(ctx, &state.NewAgent{ID: id, Ticket: ticket, Harness: "claude-code", Driver: "stream-json",
			Pool: state.PoolNormal, State: state.StateStarting, TokenHash: control.HashToken(string(id))})
		require.NoError(t, err)
	}
	end := func(id ident.AgentID, to state.AgentState, givenUp bool) {
		_, err := st.Agents.SetState(ctx, &state.Transition{Agent: id, To: to, GivenUp: givenUp})
		require.NoError(t, err)
	}
	newAgent("t1-done", "1")
	newAgent("t2-taken", "2")
	newAgent("t3-gaveup", "3")
	newAgent("t4-live", "4")
	end("t1-done", state.StateDone, false)
	end("t2-taken", state.StateReleased, false)
	end("t3-gaveup", state.StateReleased, true)
	msg, err := st.Messages.Enqueue(ctx, &state.NewMessage{From: state.UserParty, To: "t4-live", Kind: state.KindTell, Body: "hi"})
	require.NoError(t, err)
	require.NoError(t, st.Messages.MarkDelivered(ctx, msg.ID, state.DeliveryTurn))

	clk.Advance(90 * time.Minute)
	require.NoError(t, d.sweep(ctx))
	hist, err := st.Agents.History(ctx, "t4-live")
	require.NoError(t, err)
	assert.Empty(t, hist, "state changes past an hour are gone")
	log, err := st.Messages.Log(ctx, &state.LogQuery{})
	require.NoError(t, err)
	assert.Len(t, log, 1, "the message is kept for two hours")

	clk.Advance(time.Hour)
	require.NoError(t, d.sweep(ctx))
	log, err = st.Messages.Log(ctx, &state.LogQuery{})
	require.NoError(t, err)
	assert.Empty(t, log)
	_, err = st.Agents.Get(ctx, "t1-done")
	require.NoError(t, err, "ended agents are kept for three hours")

	clk.Advance(time.Hour)
	require.NoError(t, d.sweep(ctx))
	for _, id := range []ident.AgentID{"t1-done", "t3-gaveup"} {
		_, err = st.Agents.Get(ctx, id)
		require.ErrorIs(t, err, state.ErrNotFound, id)
	}
	taken, err := st.Agents.ReleasedForTicket(ctx, "2")
	require.NoError(t, err)
	assert.True(t, taken, "a person's take-over still keeps new agents off ticket 2")
	_, err = st.Agents.Get(ctx, "t4-live")
	require.NoError(t, err)
}
