package pi

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/transcript"
)

// testdata/session.jsonl is a real `pi -p` session (openai/gpt-6.2-sol on a
// ChatGPT login), with the system prompt removed, the session id fixed and
// the workspace scrubbed to /ws.
func TestParseARecordedSession(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile("testdata/session.jsonl")
	require.NoError(t, err)
	got, err := (&TranscriptReader{}).Parse(data)
	require.NoError(t, err)
	at := transcript.ParseTime
	call1 := "call_8ee9aba230dc43839d81170b6056dd71|fc_061cf5331d8a6195016b0a7e5f6ae487d09e0e8996a3653fc4"
	call2 := "call_c4f32b9d57814e989bf93036a8585212|fc_061cf5331d8a6195016b0a7e61ca0487d099e4689d5309729f"
	want := []transcript.Entry{
		{At: at("2026-11-28T07:10:19.314Z"), Kind: transcript.User, Text: "Use your bash tool to run: cat notes.txt. Then use bash to run: cat missing.txt. Then reply with exactly: DONE"},
		{At: at("2026-11-28T07:10:23.776Z"), Kind: transcript.ToolCall, Tool: "read", CallID: call1, Text: `{"path":"notes.txt","offset":null,"limit":null}`},
		{At: at("2026-11-28T07:10:23.776Z"), Kind: transcript.UsageKind, Usage: &transcript.Usage{Input: 1318, Output: 26}},
		{At: at("2026-11-28T07:10:23.781Z"), Kind: transcript.ToolResult, Tool: "read", CallID: call1, Text: "hello transcript\n"},
		{At: at("2026-11-28T07:10:26.193Z"), Kind: transcript.ToolCall, Tool: "read", CallID: call2, Text: `{"path":"missing.txt","offset":null,"limit":null}`},
		{At: at("2026-11-28T07:10:26.193Z"), Kind: transcript.UsageKind, Usage: &transcript.Usage{Input: 1357, Output: 26}},
		{At: at("2026-11-28T07:10:26.195Z"), Kind: transcript.ToolResult, Tool: "read", CallID: call2,
			Text: "ENOENT: no such file or directory, access '/ws/missing.txt'", IsError: true},
		{At: at("2026-11-28T07:10:27.771Z"), Kind: transcript.Assistant, Text: "DONE"},
		{At: at("2026-11-28T07:10:27.771Z"), Kind: transcript.UsageKind, Usage: &transcript.Usage{Input: 401, Output: 5, CacheRead: 1024}},
	}
	assert.Equal(t, want, got)
}

func TestLocateFindsTheSessionByID(t *testing.T) {
	t.Parallel()
	agentDir := t.TempDir()
	dir := filepath.Join(agentDir, "sessions", "--home-ada-ws--")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	file := filepath.Join(dir, "2026-11-28T07-10-19-267Z_a8128ffa-7db7-4271-98dd-626db00a7791.jsonl")
	require.NoError(t, os.WriteFile(file, nil, 0o600))

	fromEnv := (&Harness{Environ: func() []string { return []string{"HOME=/nowhere", "PI_CODING_AGENT_DIR=" + agentDir} }}).Transcripts()
	got, err := fromEnv.Locate("a8128ffa-7db7-4271-98dd-626db00a7791")
	require.NoError(t, err)
	assert.Equal(t, file, got)

	fromField := (&Harness{AgentDir: agentDir, Environ: func() []string { return []string{"HOME=/nowhere"} }}).Transcripts()
	got, err = fromField.Locate("a8128ffa-7db7-4271-98dd-626db00a7791")
	require.NoError(t, err)
	assert.Equal(t, file, got)

	_, err = fromField.Locate("a8128ffa")
	require.ErrorIs(t, err, transcript.ErrNotFound, "an exact id, not a prefix")
}
