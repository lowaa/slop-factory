package transcript_test

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-clock"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/fake"
	"github.com/lowaa/slop-factory/slop-bot/internal/state"
	"github.com/lowaa/slop-factory/slop-bot/internal/transcript"
)

type archiveFixture struct {
	clock   *clock.Fake
	state   *state.State
	archive *transcript.Archive
	dir     string
}

func newArchive(t *testing.T) *archiveFixture {
	t.Helper()
	dir := t.TempDir()
	clk := clock.NewFake(t0)
	st, err := state.Open(context.Background(), &state.Options{Path: filepath.Join(dir, "state.db"), Logger: slog.New(slog.DiscardHandler), Clock: clk})
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	a, err := transcript.NewArchive(&transcript.ArchiveOptions{
		Dir: filepath.Join(dir, "transcripts"), Index: st.Transcripts, Retain: 24 * time.Hour,
		Clock: clk,
	})
	require.NoError(t, err)
	return &archiveFixture{clock: clk, state: st, archive: a, dir: filepath.Join(dir, "transcripts")}
}

var entries = []transcript.Entry{{At: t0, Kind: transcript.User, Text: "hi"}, {At: t0, Kind: transcript.Assistant, Text: "hello"}}

func TestKeepThenLoad(t *testing.T) {
	t.Parallel()
	f := newArchive(t)
	ctx := context.Background()
	_, err := f.archive.Load(ctx, "t1-aaaaaa")
	require.ErrorIs(t, err, transcript.ErrNotFound)

	require.NoError(t, f.archive.Keep(ctx, "t1-aaaaaa", "1", entries))
	got, err := f.archive.Load(ctx, "t1-aaaaaa")
	require.NoError(t, err)
	assert.Equal(t, entries, got)
	kept, err := f.state.Transcripts.ForAgent(ctx, "t1-aaaaaa")
	require.NoError(t, err)
	require.Len(t, kept, 1)
	assert.Equal(t, t0.Add(24*time.Hour), kept[0].RetainUntil)
	assert.Equal(t, filepath.Join(f.dir, "t1-aaaaaa.jsonl"), kept[0].Path)
	fi, err := os.Stat(kept[0].Path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), fi.Mode().Perm(), "transcripts can hold secrets the agent saw")

	require.Error(t, f.archive.Keep(ctx, "../escape", "1", entries))
}

// Sweep deletes, file and index entry, what is past its retention.
func TestTheSweepDeletesWhatIsPastItsRetention(t *testing.T) {
	t.Parallel()
	f := newArchive(t)
	ctx := context.Background()
	require.NoError(t, f.archive.Keep(ctx, "t1-aaaaaa", "1", entries))
	f.clock.Advance(12 * time.Hour)
	require.NoError(t, f.archive.Keep(ctx, "t2-bbbbbb", "2", entries))

	require.NoError(t, f.archive.Sweep(ctx))
	_, err := f.archive.Load(ctx, "t1-aaaaaa")
	require.NoError(t, err, "12h into 24h: kept")

	f.clock.Advance(13 * time.Hour)
	require.NoError(t, f.archive.Sweep(ctx))
	_, err = f.archive.Load(ctx, "t1-aaaaaa")
	require.ErrorIs(t, err, transcript.ErrNotFound, "25h: gone")
	_, err = os.Stat(filepath.Join(f.dir, "t1-aaaaaa.jsonl"))
	require.ErrorIs(t, err, os.ErrNotExist)
	_, err = f.archive.Load(ctx, "t2-bbbbbb")
	require.NoError(t, err, "13h into 24h: kept")
}

type harnesses map[string]harness.AIHarness

func (h harnesses) Harness(name string) (harness.AIHarness, bool) {
	a, ok := h[name]
	return a, ok
}

// The harness's own copy is read while it exists; once it is gone, the
// retained copy.
func TestTheLibraryFallsBackToTheRetainedCopy(t *testing.T) {
	t.Parallel()
	f := newArchive(t)
	ctx := context.Background()
	fakeDir := t.TempDir()
	lib := transcript.NewLibrary(harnesses{"claude-code": &fake.Harness{TranscriptDir: fakeDir}}, f.archive)
	agent := &state.Agent{ID: "t1-aaaaaa", Ticket: "1", Harness: "claude-code", ConversationID: "fake-1"}

	_, err := lib.Read(ctx, agent)
	require.ErrorIs(t, err, transcript.ErrNotFound)
	require.ErrorIs(t, lib.Retain(ctx, agent), transcript.ErrNotFound, "nothing to retain says so")

	var buf bytes.Buffer
	require.NoError(t, transcript.Encode(&buf, entries))
	live := filepath.Join(fakeDir, "fake-1.jsonl")
	require.NoError(t, os.WriteFile(live, buf.Bytes(), 0o600))
	got, err := lib.Read(ctx, agent)
	require.NoError(t, err)
	assert.Equal(t, entries, got)

	require.NoError(t, lib.Retain(ctx, agent))
	// The harness prunes its copy, or carries on after the retained one.
	more := append(append([]transcript.Entry(nil), entries...), transcript.Entry{Kind: transcript.User, Text: "after"})
	buf.Reset()
	require.NoError(t, transcript.Encode(&buf, more))
	require.NoError(t, os.WriteFile(live, buf.Bytes(), 0o600))
	got, err = lib.Read(ctx, agent)
	require.NoError(t, err)
	assert.Equal(t, more, got, "the harness's copy while it exists")

	require.NoError(t, os.Remove(live))
	got, err = lib.Read(ctx, agent)
	require.NoError(t, err)
	assert.Equal(t, entries, got, "then the retained one")
}
