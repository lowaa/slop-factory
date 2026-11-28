package daemon

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-clock"
	"github.com/lowaa/slop-factory/slop-bot/internal/agents"
	"github.com/lowaa/slop-factory/slop-bot/internal/sqlitex"
	"github.com/lowaa/slop-factory/slop-bot/internal/state"
	"github.com/lowaa/slop-factory/slop-bot/internal/tickets/local"
	"github.com/lowaa/slop-factory/slop-bot/internal/transcript"
)

// A status read that can't list the live agents is an error, not a status
// with no agents in it. A cancelled context fails the statement itself.
func TestStatusReportsAFailureListingTheLiveAgents(t *testing.T) {
	t.Parallel()
	p := testPaths(t)
	d := newDaemon(t, p)
	run(t, d, p)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	rec := httptest.NewRecorder()
	d.agentRoutes.status(rec, httptest.NewRequestWithContext(ctx, http.MethodGet, "/status", nil))

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Contains(t, rec.Body.String(), "listing live agents")
}

// Likewise when the tickets can't be counted: with the tickets table gone
// from under the running daemon, the listing succeeds and the count fails.
func TestStatusReportsAFailureCountingOutstandingTickets(t *testing.T) {
	t.Parallel()
	p := testPaths(t)
	d := newDaemon(t, p)
	run(t, d, p)
	db, err := sqlitex.Open(context.Background(), &sqlitex.Options{Path: p.TicketsDB(), Migrations: local.Migrations(), Logger: slog.New(slog.DiscardHandler), Durable: true})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.ExecContext(context.Background(), `ALTER TABLE tickets RENAME TO tickets_gone`)
	require.NoError(t, err)

	rec := httptest.NewRecorder()
	d.agentRoutes.status(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/status", nil))

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Contains(t, rec.Body.String(), "counting agent-filed tickets")
}

// settleCounter counts the settle waits a follower asks the clock for.
type settleCounter struct {
	clock.Clock
	settles atomic.Int32
}

func (c *settleCounter) After(d time.Duration) <-chan time.Time {
	if d == settleAfter {
		c.settles.Add(1)
	}
	return c.Clock.After(d)
}

// A page of events that holds nothing of the followed agent's conversation
// does not start a settle wait or re-read the transcript; the follower goes
// on to the next page, here the agent's end. A full page of other agents'
// events is read on its own, so no timing decides it.
func TestAFollowerSkipsAPageOfEventsThatChangeNothing(t *testing.T) {
	t.Parallel()
	p := testPaths(t)
	d := newDaemon(t, p)
	run(t, d, p)
	ctx := context.Background()
	seq, err := d.state.Events.Latest(ctx)
	require.NoError(t, err)
	for range state.DefaultEventLimit {
		_, err := d.state.Events.Append(ctx, &state.NewEvent{Type: agents.EventHarness + ".text", Agent: "other-1"})
		require.NoError(t, err)
	}
	_, err = d.state.Events.Append(ctx, &state.NewEvent{Type: agents.EventFinished, Agent: "a1"})
	require.NoError(t, err)

	clk := &settleCounter{Clock: clock.Real{}}
	o := &observeRoutes{d: &Daemon{opts: &Options{Clock: clk}, state: d.state, transcripts: d.transcripts}}
	rec := httptest.NewRecorder()
	f := &follower{o: o, agent: &state.Agent{ID: "a1"}, seq: seq, q: &transcriptQuery{}, out: newStream(rec)}
	f.run(ctx)

	assert.Zero(t, clk.settles.Load(), "a page that changed nothing is not waited on")
	assert.Empty(t, rec.Body.String())
}

// lineWriter is a ResponseWriter that hands each write to the test, so it
// knows when the follower has streamed something.
type lineWriter struct {
	header http.Header
	lines  chan string
}

func (w *lineWriter) Header() http.Header { return w.header }
func (w *lineWriter) WriteHeader(int)     {}
func (w *lineWriter) Write(b []byte) (int, error) {
	w.lines <- string(b)
	return len(b), nil
}

func (w *lineWriter) next(t *testing.T) string {
	t.Helper()
	select {
	case l := <-w.lines:
		return l
	case <-time.After(time.Minute): // a safety net, never a synchronisation
		require.FailNow(t, "the follower streamed nothing")
		return ""
	}
}

// A transcript the harness appends to after its last event is still
// streamed: the follower reads it once more when the conversation has been
// quiet for settleAfter.
func TestAFollowerRereadsTheTranscriptOnceItSettles(t *testing.T) {
	t.Parallel()
	p := testPaths(t)
	d := newDaemon(t, p)
	run(t, d, p)
	ctx := context.Background()
	keep := func(texts ...string) {
		entries := make([]transcript.Entry, len(texts))
		for i, s := range texts {
			entries[i] = transcript.Entry{Kind: transcript.Assistant, Text: s}
		}
		require.NoError(t, d.archive.Keep(ctx, "a1", "7", entries))
	}
	keep("first")
	seq, err := d.state.Events.Latest(ctx)
	require.NoError(t, err)
	_, err = d.state.Events.Append(ctx, &state.NewEvent{Type: agents.EventHarness + ".text", Agent: "a1"})
	require.NoError(t, err)

	clk := clock.NewFake(time.Date(2026, 11, 28, 9, 0, 0, 0, time.UTC))
	o := &observeRoutes{d: &Daemon{opts: &Options{Clock: clk}, state: d.state, transcripts: d.transcripts}}
	w := &lineWriter{header: http.Header{}, lines: make(chan string)}
	f := &follower{o: o, agent: &state.Agent{ID: "a1"}, seq: seq, q: &transcriptQuery{}, out: newStream(w)}
	done := make(chan struct{})
	go func() { defer close(done); f.run(ctx) }()

	assert.Contains(t, w.next(t), "first", "the event's own re-read")
	keep("first", "late")
	clk.BlockUntil(1)
	clk.Advance(settleAfter)
	assert.Contains(t, w.next(t), "late", "the settled re-read")

	_, err = d.state.Events.Append(ctx, &state.NewEvent{Type: agents.EventFinished, Agent: "a1"})
	require.NoError(t, err)
	<-done
}
