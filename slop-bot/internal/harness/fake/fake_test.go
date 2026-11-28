package fake_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/harness"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/conformance"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/fake"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/fake/faketest"
	"github.com/lowaa/slop-factory/slop-bot/internal/transcript"
)

func TestMain(m *testing.M) { faketest.Main(m) }

func TestConformance(t *testing.T) {
	t.Parallel()
	conformance.Run(t, faketest.RPCSubject)
}

// The same suite as fake/rpc: the proof that the core can't tell the two
// kits apart.
func TestConformanceTUI(t *testing.T) {
	t.Parallel()
	conformance.Run(t, faketest.TUISubject)
}

// A graceful Stop right after an idle Send must wait for the turn that Send
// started, even though the harness hasn't reported it yet.
func TestStopRightAfterSendWaitsForThatTurn(t *testing.T) {
	t.Parallel()
	for name, subject := range map[string]func(*testing.T) *conformance.Subject{
		"rpc": faketest.RPCSubject,
		"tui": faketest.TUISubject,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			s := subject(t)
			ctx, cancel := context.WithTimeout(context.Background(), faketest.Timeout)
			defer cancel()
			conv, err := s.Harness.Start(ctx, s.Workspace(t), nil)
			require.NoError(t, err)
			defer func() { _ = conv.Stop(context.Background(), true) }()
			p := s.Puppet.(*faketest.Puppet)
			_, _, err = p.Started(ctx)
			require.NoError(t, err)

			require.NoError(t, conv.Send(ctx, &harness.Message{Text: "work"}))
			stopped := make(chan error, 1)
			go func() { stopped <- conv.Stop(ctx, false) }()

			// The fake hasn't started the turn yet; Stop must still be waiting.
			sess := p.Session()
			_, err = sess.Await(ctx, fake.RepUser)
			require.NoError(t, err)
			select {
			case err := <-stopped:
				require.Failf(t, "stop didn't wait", "returned %v before the turn ran", err)
			default:
			}
			for _, c := range []fake.Command{{Cmd: fake.CmdTurnStart}, {Cmd: fake.CmdTurnEnd}} {
				_, err := sess.Do(ctx, c)
				require.NoError(t, err)
			}
			require.NoError(t, <-stopped)
		})
	}
}

// The fake keeps a transcript of what it did, in the normalised format,
// and reports limits as a harness does: what the daemon's transcript and
// limit paths are tested with.
func TestTheFakeKeepsATranscriptAndReportsLimits(t *testing.T) {
	t.Parallel()
	s := faketest.RPCSubject(t)
	h := s.Harness.(*fake.Harness)
	h.TranscriptDir = t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), faketest.Timeout)
	defer cancel()
	conv, err := h.Start(ctx, s.Workspace(t), nil)
	require.NoError(t, err)
	defer func() { _ = conv.Stop(context.Background(), true) }()
	p := s.Puppet.(*faketest.Puppet)
	_, _, err = p.Started(ctx)
	require.NoError(t, err)
	sess := p.Session()

	require.NoError(t, conv.Send(ctx, &harness.Message{Text: "list it"}))
	_, err = sess.Expect(ctx, fake.RepUser)
	require.NoError(t, err)
	resets := time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)
	for _, c := range []fake.Command{
		{Cmd: fake.CmdTurnStart}, {Cmd: fake.CmdToolStart, Tool: "shell", Text: "ls"}, {Cmd: fake.CmdToolEnd, Tool: "shell", Text: "a.txt"},
		{Cmd: fake.CmdText, Text: "one file"}, {Cmd: fake.CmdLimit, Window: "five_hour", Used: 97.5, ResetsAt: resets.Unix()}, {Cmd: fake.CmdTurnEnd},
	} {
		_, err := sess.Do(ctx, c)
		require.NoError(t, err)
	}
	var limit *harness.Limit
	for ev := range conv.Events() {
		if ev.Kind == harness.LimitReport {
			limit = ev.Limit
		}
		if ev.Kind == harness.TurnEnded {
			break
		}
	}
	assert.Equal(t, &harness.Limit{Window: "five_hour", UsedPercent: 97.5, ResetsAt: resets}, limit)

	r := h.Transcripts()
	path, err := r.Locate(conv.Ref().ID)
	require.NoError(t, err)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	entries, err := r.Parse(data)
	require.NoError(t, err)
	for i := range entries {
		assert.False(t, entries[i].At.IsZero())
		entries[i].At = time.Time{}
	}
	assert.Equal(t, []transcript.Entry{
		{Kind: transcript.User, Text: "list it"},
		{Kind: transcript.ToolCall, Tool: "shell", CallID: "call-1", Text: "ls"},
		{Kind: transcript.ToolResult, Tool: "shell", CallID: "call-1", Text: "a.txt"},
		{Kind: transcript.Assistant, Text: "one file"},
	}, entries)
}
