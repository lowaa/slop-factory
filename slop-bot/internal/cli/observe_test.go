package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/api"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/fake"
	"github.com/lowaa/slop-factory/slop-bot/internal/transcript"
)

// streamed is a long-running CLI command (`watch`, `transcript --follow`)
// whose output the test reads line by line as it comes.
type streamed struct {
	t     *testing.T
	lines *bufio.Scanner
	done  chan int
	err   *bytes.Buffer
	// deadline bounds each wait for a line.
	deadline context.Context
}

func (e *e2e) stream(args ...string) *streamed {
	e.t.Helper()
	pr, pw := io.Pipe()
	deps := *e.h.deps
	errBuf := &bytes.Buffer{}
	deps.Stdout, deps.Stderr = pw, errBuf
	s := &streamed{t: e.t, lines: bufio.NewScanner(pr), done: make(chan int, 1), err: errBuf, deadline: e.ctx()}
	ctx, cancel := context.WithCancel(context.Background())
	e.t.Cleanup(cancel)
	go func() {
		code := Execute(ctx, &deps, args)
		_ = pw.Close()
		s.done <- code
	}()
	return s
}

// next is the command's next line; the test's deadline bounds the wait.
func (s *streamed) next() string {
	s.t.Helper()
	got := make(chan bool, 1)
	go func() { got <- s.lines.Scan() }()
	select {
	case ok := <-got:
		require.True(s.t, ok, "the command ended: %s", s.err)
		return s.lines.Text()
	case <-s.deadline.Done():
		require.FailNow(s.t, "no line from the command")
		return ""
	}
}

// until reads lines until one contains want, and returns it.
func (s *streamed) until(want string) string {
	s.t.Helper()
	for {
		if line := s.next(); strings.Contains(line, want) {
			return line
		}
	}
}

// exits waits for the command to end by itself, and returns its status.
func (s *streamed) exits() int {
	s.t.Helper()
	for s.lines.Scan() {
	}
	return <-s.done
}

func TestTranscriptAndWatchFollowAnAgentToItsEnd(t *testing.T) {
	t.Parallel()
	e := newE2E(t)
	tk := e.h.ticket(t, "ticket", "create", "--title", "Run the tests", "--assign")
	s, err := e.ctl.Next(e.ctx())
	require.NoError(t, err)
	_, err = s.Expect(e.ctx(), fake.RepUser)
	require.NoError(t, err)
	agent := e.await("agent.spawned", nil).Agent

	watch := e.stream("watch", tk.ID)
	assert.Contains(t, watch.next(), "watching "+agent+" (ticket "+tk.ID+")")
	follow := e.stream("transcript", tk.ID, "--follow")
	assert.Contains(t, follow.next(), `user: [slop-bot] You've been assigned ticket `+tk.ID)

	e.do(s, fake.CmdTurnStart)
	_, err = s.Do(e.ctx(), fake.Command{Cmd: fake.CmdToolStart, Tool: "shell", Text: "go test ./..."})
	require.NoError(t, err)
	assert.Contains(t, follow.until("tool shell"), "tool shell: go test ./...")
	_, err = s.Do(e.ctx(), fake.Command{Cmd: fake.CmdToolEnd, Tool: "shell", Text: strings.Repeat("ok\n", 1500)})
	require.NoError(t, err)
	result := follow.until("result")
	assert.Contains(t, result, "result shell: ok")
	assert.Contains(t, follow.until("more bytes"), "(--full shows them)", "long output is cut")
	_, err = s.Do(e.ctx(), fake.Command{Cmd: fake.CmdText, Text: "all green"})
	require.NoError(t, err)
	assert.Contains(t, follow.until("assistant"), "assistant: all green")

	assert.Contains(t, watch.until("turn started"), "turn started")
	assert.Contains(t, watch.until("tool"), "tool shell")
	assert.Contains(t, watch.until("tool done"), "tool done shell")
	assert.Contains(t, watch.until("text"), "text: all green")

	// Agents may not read transcripts, theirs or others'.
	for _, verb := range []string{"transcript", "watch"} {
		argv := []string{"/usr/bin/env", fakeReexec + "=", cliReexec + "=1", e.self, verb, tk.ID}
		r, err := s.Do(e.ctx(), fake.Command{Cmd: fake.CmdRun, Argv: argv})
		require.NoError(t, err)
		assert.Equal(t, 1, r.Code, verb)
		assert.Contains(t, r.Stderr, "only the user", verb)
	}

	e.agent(s, "done", "--summary", "Tests pass.")
	e.do(s, fake.CmdTurnEnd)
	assert.Contains(t, watch.until("→ done"), "state refining → done (the ticket is done)")
	assert.Equal(t, 0, watch.exits(), "watch ends with the agent")
	assert.Equal(t, 0, follow.exits(), "so does --follow")
	e.await("agent.finished", nil)

	// Once the harness has pruned its copy, the retained one is read.
	matches, err := filepath.Glob(filepath.Join(e.transcripts, "*.jsonl"))
	require.NoError(t, err)
	require.Len(t, matches, 1)
	require.NoError(t, os.Remove(matches[0]))
	out := e.h.ok(t, "transcript", agent, "--format", "jsonl", "--full")
	var kinds []transcript.Kind
	for line := range strings.Lines(out) {
		var en transcript.Entry
		require.NoError(t, json.Unmarshal([]byte(line), &en))
		kinds = append(kinds, en.Kind)
		if en.Kind == transcript.ToolResult {
			assert.Zero(t, en.Truncated, "--full")
		}
	}
	assert.Equal(t, []transcript.Kind{transcript.User, transcript.ToolCall, transcript.ToolResult, transcript.Assistant}, kinds)
	assert.Equal(t, "[", e.h.ok(t, "transcript", tk.ID, "--tail", "1")[:1])
	assert.Contains(t, e.h.ok(t, "transcript", tk.ID, "--tail", "1"), "assistant: all green")
	assert.Contains(t, e.h.ok(t, "watch", agent), "it has ended")

	assert.Equal(t, 1, e.h.run(t, "transcript", "nobody"))
	assert.Contains(t, e.h.err.String(), "no agent")
}

func TestStatusShowsThePlanLimits(t *testing.T) {
	t.Parallel()
	e := newE2E(t)
	e.h.ticket(t, "ticket", "create", "--title", "t", "--assign")
	s, err := e.ctl.Next(e.ctx())
	require.NoError(t, err)
	_, err = s.Expect(e.ctx(), fake.RepUser)
	require.NoError(t, err)
	resets := time.Now().Add(time.Hour).Truncate(time.Second)
	e.do(s, fake.CmdTurnStart)
	_, err = s.Do(e.ctx(), fake.Command{Cmd: fake.CmdLimit, Window: "five_hour", Used: 97, ResetsAt: resets.Unix()})
	require.NoError(t, err)
	e.await("harness.limit", nil)

	out := e.h.ok(t, "status")
	assert.Contains(t, out, "plan claude-code five_hour: 97% used (new agents wait at 95%), resets "+resets.Local().Format("Jan 2 15:04")+": holding new agents")
	var st api.Status
	require.NoError(t, json.Unmarshal([]byte(e.h.ok(t, "status", "--json")), &st))
	require.Len(t, st.Limits, 1)
	assert.True(t, st.Limits[0].Held)
	assert.True(t, resets.Equal(st.Limits[0].ResetsAt))
}
