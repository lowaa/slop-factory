package codex

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/transcript"
)

func ms(v int64) time.Time { return time.UnixMilli(v).UTC() }

// testdata/rollout.jsonl is a real rollout (codex-cli 0.172.0, `codex exec`,
// a ChatGPT login): session_meta cut to its ids, the event_msg records, and
// the assistant message and tool-script response_items, which Parse must
// not count twice. Developer and environment-context messages are dropped,
// the workspace is /ws, and rate limits are removed from token_count.
func TestParseARecordedRollout(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile("testdata/rollout.jsonl")
	require.NoError(t, err)
	got, err := (&TranscriptReader{}).Parse(data)
	require.NoError(t, err)
	want := []transcript.Entry{
		{At: ms(1795849304979), Kind: transcript.User, Text: "Run the shell command: cat notes.txt. Then run: cat missing.txt. Then reply with exactly: DONE"},
		{At: ms(1795849306844), Kind: transcript.Assistant, Text: "I’ll run the two commands in order."},
		{At: ms(1795849307274), Kind: transcript.ToolCall, Tool: "shell", CallID: "exec-0146a66c-f1fd-47a9-87a3-a3c571012080", Text: "cat notes.txt"},
		{At: ms(1795849307274), Kind: transcript.ToolResult, Tool: "shell", CallID: "exec-0146a66c-f1fd-47a9-87a3-a3c571012080", Text: "hello transcript\n"},
		{At: ms(1795849307295), Kind: transcript.ToolCall, Tool: "shell", CallID: "exec-291f1e7f-1e25-4943-8ba4-cac977a6459a", Text: "cat missing.txt"},
		{At: ms(1795849307295), Kind: transcript.ToolResult, Tool: "shell", CallID: "exec-291f1e7f-1e25-4943-8ba4-cac977a6459a",
			Text: "cat: missing.txt: No such file or directory\n", IsError: true},
		{At: transcript.ParseTime("2026-11-28T07:01:47.302Z"), Kind: transcript.UsageKind, Usage: &transcript.Usage{Input: 17021 - 13952, Output: 49, CacheRead: 13952}},
		{At: ms(1795849308768), Kind: transcript.Assistant, Text: "DONE"},
		{At: transcript.ParseTime("2026-11-28T07:01:48.792Z"), Kind: transcript.UsageKind, Usage: &transcript.Usage{Input: 17173 - 16768, Output: 5, CacheRead: 16768}},
	}
	assert.Equal(t, want, got)
}

func TestLocateFindsTheRolloutByThreadID(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	dir := filepath.Join(home, "sessions", "2026", "11", "28")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	id := "01a220f5-b208-7cf3-954a-d36be6b8274f"
	file := filepath.Join(dir, "rollout-2026-11-28T15-01-42-"+id+".jsonl")
	require.NoError(t, os.WriteFile(file, nil, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "rollout-2026-11-28T15-01-42-01a220f5-0000-7cf3-954a-d36be6b8274f.jsonl"), nil, 0o600))
	r := (&Harness{Environ: func() []string { return []string{"HOME=/nowhere", "CODEX_HOME=" + home} }}).Transcripts()

	got, err := r.Locate(id)
	require.NoError(t, err)
	assert.Equal(t, file, got)
	_, err = r.Locate("01a220f5-ffff-7cf3-954a-d36be6b8274f")
	require.ErrorIs(t, err, transcript.ErrNotFound)
}
