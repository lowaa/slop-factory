package claudecode

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/transcript"
)

func at(s string) time.Time { return transcript.ParseTime(s) }

// testdata/transcript.jsonl is a real `claude -p` session's file (Claude
// Code with claude-haiku-5-5), cut to its user and assistant lines plus one
// attachment, with the workspace scrubbed to /ws and thinking signatures
// and request ids removed.
func TestParseARecordedSession(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile("testdata/transcript.jsonl")
	require.NoError(t, err)
	got, err := (&TranscriptReader{}).Parse(data)
	require.NoError(t, err)
	usage1 := &transcript.Usage{Input: 2, Output: 163, CacheRead: 11837, CacheWrite: 13355}
	want := []transcript.Entry{
		{At: at("2026-11-28T07:00:36.424Z"), Kind: transcript.User, Text: "Use your Bash tool to run: cat notes.txt. Then use Bash to run: cat missing.txt. Then reply with exactly: DONE"},
		{At: at("2026-11-28T07:00:37.923Z"), Kind: transcript.ToolCall, Tool: "Bash", CallID: "toolu_01NgcCPrAKc15DWJgU5QfiiG", Text: "cat notes.txt"},
		{At: at("2026-11-28T07:00:37.923Z"), Kind: transcript.UsageKind, Usage: usage1},
		{At: at("2026-11-28T07:00:38.017Z"), Kind: transcript.ToolResult, Tool: "Bash", CallID: "toolu_01NgcCPrAKc15DWJgU5QfiiG", Text: "hello transcript"},
		// The same API message's second block: its usage isn't counted again.
		{At: at("2026-11-28T07:00:38.119Z"), Kind: transcript.ToolCall, Tool: "Bash", CallID: "toolu_01DjA4L6ysBXZdpaTqQaTXBN", Text: "cat missing.txt"},
		{At: at("2026-11-28T07:00:38.151Z"), Kind: transcript.ToolResult, Tool: "Bash", CallID: "toolu_01DjA4L6ysBXZdpaTqQaTXBN",
			Text: "Exit code 1\ncat: missing.txt: No such file or directory", IsError: true},
		{At: at("2026-11-28T07:00:40.722Z"), Kind: transcript.UsageKind, Usage: &transcript.Usage{Input: 2, Output: 365, CacheRead: 25192, CacheWrite: 2444}},
		{At: at("2026-11-28T07:00:40.958Z"), Kind: transcript.Assistant,
			Text: "DONE\n\nNote: `missing.txt` does not exist in the working directory, so `cat missing.txt` failed with \"No such file or directory\". `notes.txt` printed `hello transcript`."},
	}
	assert.Equal(t, want, got)
}

// slop-bot's own sessions send prompts as content blocks (stream-json), and
// results can be block lists too.
func TestParseBlockContentAndSkipsMetaAndSidechains(t *testing.T) {
	t.Parallel()
	data := []byte(`{"type":"user","timestamp":"2026-11-28T07:00:00Z","message":{"role":"user","content":[{"type":"text","text":"from slop-bot"}]}}
{"type":"user","isMeta":true,"message":{"role":"user","content":"<local-command-caveat>"}}
{"type":"assistant","isSidechain":true,"message":{"id":"m0","content":[{"type":"text","text":"a sub-agent"}]}}
{"type":"assistant","message":{"id":"m1","content":[{"type":"tool_use","id":"t1","name":"Read","input":{"file_path":"/ws/a"}}]}}
{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":[{"type":"text","text":"line 1"},{"type":"text","text":"line 2"}]}]}}
{"type":"assistant","message":{"id":"m2","content":[{"type":"text","text":"still being writ`)
	got, err := (&TranscriptReader{}).Parse(data)
	require.NoError(t, err)
	assert.Equal(t, []transcript.Entry{
		{At: at("2026-11-28T07:00:00Z"), Kind: transcript.User, Text: "from slop-bot"},
		{Kind: transcript.ToolCall, Tool: "Read", CallID: "t1", Text: `{"file_path":"/ws/a"}`},
		{Kind: transcript.ToolResult, Tool: "Read", CallID: "t1", Text: "line 1\nline 2"},
	}, got, "the half-written last line waits for the next read")
}

func TestLocateFindsTheSessionInAnyProject(t *testing.T) {
	t.Parallel()
	cfg := t.TempDir()
	dir := filepath.Join(cfg, "projects", "-home-ada-ws--slop-bot-t1")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	file := filepath.Join(dir, "0b5e0c1e-0000-4000-8000-000000000001.jsonl")
	require.NoError(t, os.WriteFile(file, nil, 0o600))
	r := (&Harness{Environ: func() []string { return []string{"HOME=/nowhere", "CLAUDE_CONFIG_DIR=" + cfg} }}).Transcripts()

	got, err := r.Locate("0b5e0c1e-0000-4000-8000-000000000001")
	require.NoError(t, err)
	assert.Equal(t, file, got)

	_, err = r.Locate("0b5e0c1e-0000-4000-8000-000000000002")
	require.ErrorIs(t, err, transcript.ErrNotFound)
	_, err = r.Locate("../../etc/passwd")
	require.Error(t, err)
	require.NotErrorIs(t, err, transcript.ErrNotFound, "a path is refused, not looked for")
}

func TestLocateDefaultsToTheHomesClaudeDir(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	dir := filepath.Join(home, ".claude", "projects", "p")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "abc.jsonl"), nil, 0o600))
	r := (&Harness{Environ: func() []string { return []string{"HOME=" + home} }}).Transcripts()
	got, err := r.Locate("abc")
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(dir, "abc.jsonl"), got)
}
