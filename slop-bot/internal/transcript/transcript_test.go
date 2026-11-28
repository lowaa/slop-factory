package transcript_test

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/transcript"
)

var t0 = time.Date(2026, 11, 28, 9, 0, 0, 0, time.UTC)

func TestLinesLeavesAHalfWrittenLastLine(t *testing.T) {
	t.Parallel()
	assert.Equal(t, [][]byte{[]byte("a"), []byte("b")}, transcript.Lines([]byte("a\n\nb\nhalf")))
	assert.Nil(t, transcript.Lines([]byte("half")))
}

func TestEncodeDecodeRoundTrips(t *testing.T) {
	t.Parallel()
	in := []transcript.Entry{
		{At: t0, Kind: transcript.User, Text: "hi"},
		{Kind: transcript.ToolResult, Tool: "shell", CallID: "c1", Text: "out", IsError: true},
		{At: t0, Kind: transcript.UsageKind, Usage: &transcript.Usage{Input: 1, Output: 2, CacheRead: 3}},
	}
	var buf bytes.Buffer
	require.NoError(t, transcript.Encode(&buf, in))
	out, err := transcript.Decode(buf.Bytes())
	require.NoError(t, err)
	assert.Equal(t, in, out)
}

func TestTruncateCutsToolTextAtARuneBoundary(t *testing.T) {
	t.Parallel()
	in := []transcript.Entry{
		{Kind: transcript.Assistant, Text: strings.Repeat("x", 10)},
		{Kind: transcript.ToolResult, Text: "abé" + strings.Repeat("y", 7)}, // é is 2 bytes, at 2..3
		{Kind: transcript.ToolCall, Text: "short"},
	}
	out := transcript.Truncate(in, 3)
	assert.Equal(t, strings.Repeat("x", 10), out[0].Text, "messages stay whole")
	assert.Equal(t, "ab", out[1].Text, "not half a rune")
	assert.Equal(t, 9, out[1].Truncated)
	assert.Equal(t, "sho", out[2].Text)
	assert.Equal(t, "abé"+strings.Repeat("y", 7), in[1].Text, "the input is untouched")
}

func TestSinceAndTail(t *testing.T) {
	t.Parallel()
	es := []transcript.Entry{
		{At: t0, Text: "1"}, {At: t0.Add(time.Minute), Text: "2"}, {Text: "unstamped"}, {At: t0.Add(2 * time.Minute), Text: "3"},
	}
	texts := func(es []transcript.Entry) []string {
		var out []string
		for _, e := range es {
			out = append(out, e.Text)
		}
		return out
	}
	assert.Equal(t, []string{"2", "unstamped", "3"}, texts(transcript.Since(es, t0.Add(time.Minute))))
	assert.Equal(t, []string{"1", "2", "unstamped", "3"}, texts(transcript.Since(es, time.Time{})))
	assert.Equal(t, []string{"unstamped", "3"}, texts(transcript.Tail(es, 2)))
	assert.Len(t, transcript.Tail(es, 0), 4)
	assert.Len(t, transcript.Tail(es, 9), 4)
}

func TestRender(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	for _, e := range []transcript.Entry{
		{Kind: transcript.User, Text: "two\nlines"},
		{Kind: transcript.ToolCall, Tool: "Bash", Text: "ls"},
		{Kind: transcript.ToolResult, Tool: "Bash", Text: "nope", IsError: true, Truncated: 5},
		{Kind: transcript.UsageKind, Usage: &transcript.Usage{Input: 1, Output: 2}},
	} {
		require.NoError(t, transcript.Render(&buf, &e))
	}
	assert.Equal(t, `[--:--:--] user: two
    lines
[--:--:--] tool Bash: ls
[--:--:--] result Bash (error): nope
    … 5 more bytes (--full shows them)
[--:--:--] usage: input 1, output 2, cache read 0, cache write 0
`, buf.String())
}
