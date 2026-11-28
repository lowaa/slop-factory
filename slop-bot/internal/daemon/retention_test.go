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
