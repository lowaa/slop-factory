package state_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/ident"
	"github.com/lowaa/slop-factory/slop-bot/internal/state"
)

func TestCursorsArePerSystem(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	_, ok, err := f.Cursors.Get(ctx, "local")
	require.NoError(t, err)
	assert.False(t, ok)

	require.NoError(t, f.Cursors.Set(ctx, "local", "17"))
	require.NoError(t, f.Cursors.Set(ctx, "slop-tickets", "abc"))
	require.NoError(t, f.Cursors.Set(ctx, "local", "18"))

	pos, ok, err := f.Cursors.Get(ctx, "local")
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, "18", pos)
	pos, _, err = f.Cursors.Get(ctx, "slop-tickets")
	require.NoError(t, err)
	assert.Equal(t, "abc", pos)
}

func TestAutonomySetting(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	_, ok, err := f.Settings.Autonomy(ctx)
	require.NoError(t, err)
	assert.False(t, ok, "unset: the config's default applies")

	for _, want := range []bool{true, false, true} {
		require.NoError(t, f.Settings.SetAutonomy(ctx, want))
		got, ok, err := f.Settings.Autonomy(ctx)
		require.NoError(t, err)
		assert.True(t, ok)
		assert.Equal(t, want, got)
	}
}

func TestStateSurvivesAReopen(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := t.TempDir() + "/state.db"
	opts := &state.Options{Path: path, Logger: discardLogger(), Clock: newFakeClock()}

	s, err := state.Open(ctx, opts)
	require.NoError(t, err)
	require.NoError(t, s.Settings.SetAutonomy(ctx, true))
	require.NoError(t, s.Queue.PushFront(ctx, state.PoolNormal, "7"))
	require.NoError(t, s.Close())

	s, err = state.Open(ctx, opts)
	require.NoError(t, err)
	defer func() { _ = s.Close() }()
	on, ok, err := s.Settings.Autonomy(ctx)
	require.NoError(t, err)
	assert.True(t, ok && on)
	list, err := s.Queue.List(ctx, state.PoolNormal)
	require.NoError(t, err)
	assert.Equal(t, []state.QueuedTicket{{Ticket: "7", Pool: state.PoolNormal, Front: true, EnqueuedAt: t0}}, list)
}

func TestOpenNeedsAClock(t *testing.T) {
	t.Parallel()
	_, err := state.Open(context.Background(), &state.Options{Path: t.TempDir() + "/x.db", Logger: discardLogger()})
	assert.Error(t, err)
}

func TestTranscripts(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	add := func(path string, agent string, retain time.Duration) {
		t.Helper()
		require.NoError(t, f.Transcripts.Add(ctx, &state.NewTranscript{
			Path: path, Agent: ident.AgentID(agent), Ticket: "1", RetainUntil: t0.Add(retain),
		}))
	}
	add("/t/a1-1.jsonl", "a1", time.Hour)
	add("/t/a1-2.jsonl", "a1", 2*time.Hour)
	add("/t/a2.jsonl", "a2", 3*time.Hour)

	mine, err := f.Transcripts.ForAgent(ctx, "a1")
	require.NoError(t, err)
	require.Len(t, mine, 2)
	assert.Equal(t, state.Transcript{Path: "/t/a1-1.jsonl", Agent: "a1", Ticket: "1", RetainUntil: t0.Add(time.Hour), CreatedAt: t0}, mine[0])

	expired, err := f.Transcripts.Expired(ctx, t0.Add(2*time.Hour))
	require.NoError(t, err)
	assert.Equal(t, []string{"/t/a1-1.jsonl", "/t/a1-2.jsonl"}, []string{expired[0].Path, expired[1].Path}, "at the deadline counts")
	assert.Len(t, expired, 2)

	// Re-adding a path extends it.
	add("/t/a1-1.jsonl", "a1", 10*time.Hour)
	expired, err = f.Transcripts.Expired(ctx, t0.Add(2*time.Hour))
	require.NoError(t, err)
	require.Len(t, expired, 1)
	assert.Equal(t, "/t/a1-2.jsonl", expired[0].Path)

	require.NoError(t, f.Transcripts.Remove(ctx, "/t/a1-2.jsonl"))
	assert.ErrorIs(t, f.Transcripts.Remove(ctx, "/t/a1-2.jsonl"), state.ErrNotFound)
}
