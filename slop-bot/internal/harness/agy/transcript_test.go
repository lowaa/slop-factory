package agy

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/transcript"
)

func TestLocateFindsTheConversationsStepLog(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	logs := filepath.Join(home, ".gemini", "antigravity-cli", "brain", conversationID, ".system_generated", "logs")
	require.NoError(t, os.MkdirAll(logs, 0o700))
	// transcript.jsonl is agy's shorter log, beside the full one.
	for _, name := range []string{"transcript_full.jsonl", "transcript.jsonl"} {
		require.NoError(t, os.WriteFile(filepath.Join(logs, name), nil, 0o600))
	}
	r := (&Harness{Environ: func() []string { return []string{"HOME=" + home} }}).Transcripts()
	path, err := r.Locate(conversationID)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(logs, "transcript_full.jsonl"), path)

	_, err = r.Locate("00000000-0000-0000-0000-000000000000")
	assert.ErrorIs(t, err, transcript.ErrNotFound)
}

// The step log is a TUI session's on agy 1.5.18: a SessionStart injection,
// a backgrounded command with PreInvocation injections, a cancelled
// command, then a resume whose turn a Stop hook continued.
func TestParseTheStepLog(t *testing.T) {
	t.Parallel()
	entries, err := (&TranscriptReader{}).Parse(testdata(t, "transcript_full.jsonl"))
	require.NoError(t, err)
	var kinds []transcript.Kind
	var users, assistants, calls []string
	for _, e := range entries {
		kinds = append(kinds, e.Kind)
		switch e.Kind {
		case transcript.User:
			users = append(users, e.Text)
		case transcript.Assistant:
			assistants = append(assistants, e.Text)
		case transcript.ToolCall:
			calls = append(calls, e.Tool+": "+e.Text)
		}
	}
	assert.Equal(t, []string{
		"What are the briefing codeword and the fruit word? Reply with just both, or NONE for each you don't know.",
		"The fruit word is GUAVA.",
		"Run exactly the shell command 'sleep 12; echo done' with run_command and WaitMsBeforeAsync 60000, wait for it, then reply with just: SLEPT",
		"Also end your reply with the word STEER-SEEN.",
		"Also end your reply with the word STEER-SEEN.",
		"Run exactly the shell command 'sleep 9731' with run_command and wait for it.",
		"What word did I ask you to reply with after sleeping? Reply with just it.",
		"Now also reply with the word CEDAR.",
	}, users, "prompts without agy's wrapping, and the hooks' injections; not agy's own notices")
	assert.Equal(t, []string{"ZEBRA-42\nGUAVA", "Launched the command and waiting for it to finish.", "SLEPT\nSTEER-SEEN", "SLEPT", "CEDAR"}, assistants)
	assert.Equal(t, []string{"run_command: sleep 12; echo done", "run_command: sleep 9731"}, calls)

	var result *transcript.Entry
	for i := range entries {
		if entries[i].Kind == transcript.ToolResult {
			require.Nil(t, result, "one result: the cancelled call has none")
			result = &entries[i]
		}
	}
	require.NotNil(t, result)
	assert.Equal(t, "run_command", result.Tool, "paired with its call")
	assert.Contains(t, result.Text, "background task")

	first := entries[2]
	require.Equal(t, transcript.Assistant, first.Kind)
	assert.Equal(t, time.Date(2026, 11, 28, 8, 24, 15, 0, time.UTC), first.At)
	require.Equal(t, transcript.UsageKind, entries[3].Kind)
	assert.Equal(t, &transcript.Usage{Input: 10590, Output: 304}, entries[3].Usage)
	assert.Contains(t, kinds, transcript.UsageKind)
}

func TestParseKeepsAHalfWrittenLineForLater(t *testing.T) {
	t.Parallel()
	entries, err := (&TranscriptReader{}).Parse([]byte(`{"type":"PLANNER_RESPONSE","content":"done"}` + "\n" + `{"type":"PLANNER_RES`))
	require.NoError(t, err)
	assert.Equal(t, []transcript.Entry{{Kind: transcript.Assistant, Text: "done"}}, entries)
}

func TestToolArgs(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "go test ./...", toolArgs([]byte(`{"CommandLine":"go test ./...","WaitMsBeforeAsync":5000,"toolSummary":"run tests"}`)))
	assert.JSONEq(t, `{"AbsolutePath":"/w/a.go"}`, toolArgs([]byte(`{"AbsolutePath":"/w/a.go","toolSummary":"read a.go"}`)))
	assert.Equal(t, `"x"`, toolArgs([]byte(`"x"`)))
}
