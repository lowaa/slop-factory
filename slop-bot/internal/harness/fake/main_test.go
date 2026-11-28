package fake_test

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/harness/fake"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/fake/faketest"
)

// inproc runs fake.Main in this process, so its protocol handling is
// exercised directly (and measured), not only through a re-exec'd binary.
type inproc struct {
	stdin  io.WriteCloser
	lines  chan []byte
	exited chan int
}

func startMain(t *testing.T, args []string, env map[string]string) *inproc {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	p := &inproc{stdin: inW, lines: make(chan []byte), exited: make(chan int, 1)}
	go func() {
		defer close(p.lines)
		sc := bufio.NewScanner(outR)
		for sc.Scan() {
			p.lines <- append([]byte(nil), sc.Bytes()...)
		}
	}()
	go func() {
		code := fake.Main(args, inR, outW, func(k string) string { return env[k] })
		_ = outW.Close()
		p.exited <- code
	}()
	t.Cleanup(func() { _ = inW.Close(); _ = outR.Close() })
	return p
}

func (p *inproc) send(t *testing.T, v any) {
	t.Helper()
	require.NoError(t, json.NewEncoder(p.stdin).Encode(v))
}

func (p *inproc) next(t *testing.T) map[string]any {
	t.Helper()
	// The timeout is a safety net so a broken fake fails instead of hanging.
	select {
	case line, ok := <-p.lines:
		require.True(t, ok, "stdout ended")
		m := map[string]any{}
		require.NoError(t, json.Unmarshal(line, &m))
		return m
	case <-time.After(faketest.Timeout):
		require.FailNow(t, "no output from the fake")
		return nil
	}
}

func TestMainAutonomousEchoesEachMessage(t *testing.T) {
	t.Parallel()
	p := startMain(t, nil, map[string]string{fake.EnvID: "id-1"})

	p.send(t, map[string]string{"op": "hello"})
	assert.Equal(t, map[string]any{"ev": "hello", "id": "id-1"}, p.next(t))

	p.send(t, map[string]string{"op": "user", "text": "hi"})
	assert.Equal(t, "turn_start", p.next(t)["ev"])
	assert.Equal(t, map[string]any{"ev": "text", "text": "echo: hi"}, p.next(t))
	assert.Equal(t, map[string]any{"ev": "turn_end", "outcome": "completed"}, p.next(t))

	require.NoError(t, p.stdin.Close())
	assert.Equal(t, 0, <-p.exited)
}

func TestMainResumeKeepsTheGivenID(t *testing.T) {
	t.Parallel()
	p := startMain(t, []string{"--resume", "old-id"}, nil)
	p.send(t, map[string]string{"op": "hello"})
	assert.Equal(t, "old-id", p.next(t)["id"])
}

func TestMainDefaultID(t *testing.T) {
	t.Parallel()
	p := startMain(t, nil, nil)
	p.send(t, map[string]string{"op": "hello"})
	assert.True(t, strings.HasPrefix(p.next(t)["id"].(string), "fake-"), "a fresh fake id")
}

func TestMainUnreachableControllerIsAnError(t *testing.T) {
	t.Parallel()
	p := startMain(t, nil, map[string]string{fake.EnvControl: filepath.Join(t.TempDir(), "none.sock")})
	assert.Equal(t, "error", p.next(t)["ev"])
	assert.Equal(t, 70, <-p.exited)
}

func TestHookRelayFailures(t *testing.T) {
	t.Parallel()
	assert.Equal(t, 2, fake.Main([]string{"--hook-relay", "only-one"}, strings.NewReader(""), io.Discard, nil))
	// Nothing listens on the socket.
	sock := filepath.Join(t.TempDir(), "none.sock")
	assert.Equal(t, 1, fake.Main([]string{"--hook-relay", sock, "s", "step"}, strings.NewReader(""), io.Discard, nil))
}

// controlled is a fake under a controller, driven command by command.
func controlled(t *testing.T) (*inproc, *fake.Session) {
	t.Helper()
	ctl, err := fake.NewController()
	require.NoError(t, err)
	t.Cleanup(func() { _ = ctl.Close() })
	p := startMain(t, nil, map[string]string{fake.EnvControl: ctl.Socket(), fake.EnvID: "c1"})
	ctx, cancel := context.WithTimeout(context.Background(), faketest.Timeout)
	t.Cleanup(cancel)
	s, err := ctl.Next(ctx)
	require.NoError(t, err)
	assert.Equal(t, "c1", s.ID)
	return p, s
}

func TestControlledCommandsWriteTheirEvents(t *testing.T) {
	t.Parallel()
	p, s := controlled(t)
	ctx, cancel := context.WithTimeout(context.Background(), faketest.Timeout)
	defer cancel()

	for _, tc := range []struct {
		cmd  fake.Command
		want map[string]any
	}{
		{fake.Command{Cmd: fake.CmdTurnStart}, map[string]any{"ev": "turn_start"}},
		{fake.Command{Cmd: fake.CmdToolStart, Tool: "shell"}, map[string]any{"ev": "tool_start", "tool": "shell"}},
		{fake.Command{Cmd: fake.CmdToolEnd, Tool: "shell"}, map[string]any{"ev": "tool_end", "tool": "shell"}},
		{fake.Command{Cmd: fake.CmdText, Text: "t"}, map[string]any{"ev": "text", "text": "t"}},
		{fake.Command{Cmd: fake.CmdUsage}, map[string]any{"ev": "usage", "input": 100.0, "output": 10.0}},
		{fake.Command{Cmd: fake.CmdTurnEnd, Outcome: "failed"}, map[string]any{"ev": "turn_end", "outcome": "failed"}},
		{fake.Command{Cmd: fake.CmdTurnStart}, map[string]any{"ev": "turn_start"}},
		{fake.Command{Cmd: fake.CmdTurnEnd}, map[string]any{"ev": "turn_end", "outcome": "completed"}},
	} {
		// Pipes are synchronous: read the event while Do waits for its ack.
		done := make(chan map[string]any, 1)
		go func() { done <- p.next(t) }()
		r, err := s.Do(ctx, tc.cmd)
		require.NoError(t, err, tc.cmd.Cmd)
		assert.Equal(t, tc.cmd.Cmd, r.Ack)
		assert.Equal(t, tc.want, <-done, tc.cmd.Cmd)
	}

	r, err := s.Do(ctx, fake.Command{Cmd: "bogus"})
	require.ErrorContains(t, err, `unknown command "bogus"`)
	assert.Equal(t, "bogus", r.Ack)

	_, err = s.Do(ctx, fake.Command{Cmd: fake.CmdExit, Code: 7})
	require.NoError(t, err)
	assert.Equal(t, 7, <-p.exited)
}

func TestControlledRunReportsTheCommandsResult(t *testing.T) {
	t.Parallel()
	_, s := controlled(t)
	ctx, cancel := context.WithTimeout(context.Background(), faketest.Timeout)
	defer cancel()

	r, err := s.Do(ctx, fake.Command{Cmd: fake.CmdRun, Argv: []string{"sh", "-c", "echo out; echo err >&2; exit 3"}})
	require.NoError(t, err)
	assert.Equal(t, fake.RepRan, r.Rep)
	assert.Equal(t, 3, r.Code)
	assert.Equal(t, "out\n", r.Stdout)
	assert.Equal(t, "err\n", r.Stderr)

	r, err = s.Do(ctx, fake.Command{Cmd: fake.CmdRun, Argv: []string{"/nonexistent/binary"}})
	require.ErrorContains(t, err, "no such file")
	assert.NotEmpty(t, r.Err)

	_, err = s.Do(ctx, fake.Command{Cmd: fake.CmdRun})
	require.ErrorContains(t, err, "no argv")
}

func TestControlledInterruptEndsTheTurnUnlessIgnored(t *testing.T) {
	t.Parallel()
	p, s := controlled(t)
	ctx, cancel := context.WithTimeout(context.Background(), faketest.Timeout)
	defer cancel()
	do := func(c fake.Command) map[string]any {
		t.Helper()
		done := make(chan map[string]any, 1)
		if c.Cmd == fake.CmdTurnStart || c.Cmd == fake.CmdTurnEnd {
			go func() { done <- p.next(t) }()
		} else {
			done <- nil
		}
		_, err := s.Do(ctx, c)
		require.NoError(t, err)
		return <-done
	}

	assert.Equal(t, "turn_start", do(fake.Command{Cmd: fake.CmdTurnStart})["ev"])
	p.send(t, map[string]string{"op": "interrupt"})
	assert.Equal(t, map[string]any{"ev": "turn_end", "outcome": "interrupted"}, p.next(t))
	_, err := s.Expect(ctx, fake.RepInterrupt)
	require.NoError(t, err)

	// An ignored interrupt is reported but doesn't end the turn.
	do(fake.Command{Cmd: fake.CmdIgnoreInterrupts, On: true})
	assert.Equal(t, "turn_start", do(fake.Command{Cmd: fake.CmdTurnStart})["ev"])
	p.send(t, map[string]string{"op": "interrupt"})
	_, err = s.Expect(ctx, fake.RepInterrupt)
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"ev": "turn_end", "outcome": "completed"}, do(fake.Command{Cmd: fake.CmdTurnEnd}), "the interrupt must not have ended the turn")
}

func TestControlledStdinCloseIsReportedAndExitsUnlessIgnored(t *testing.T) {
	t.Parallel()
	p, s := controlled(t)
	ctx, cancel := context.WithTimeout(context.Background(), faketest.Timeout)
	defer cancel()

	_, err := s.Do(ctx, fake.Command{Cmd: fake.CmdIgnoreEOF, On: true})
	require.NoError(t, err)
	require.NoError(t, p.stdin.Close())
	_, err = s.Expect(ctx, fake.RepStdinClosed)
	require.NoError(t, err)

	// Still alive: it answers commands after EOF, and exits when told.
	_, err = s.Do(ctx, fake.Command{Cmd: fake.CmdExit})
	require.NoError(t, err)
	assert.Equal(t, 0, <-p.exited)
}

func TestControlledFakeExitsWhenTheControllerGoesAway(t *testing.T) {
	t.Parallel()
	ctl, err := fake.NewController()
	require.NoError(t, err)
	p := startMain(t, nil, map[string]string{fake.EnvControl: ctl.Socket()})
	ctx, cancel := context.WithTimeout(context.Background(), faketest.Timeout)
	defer cancel()
	_, err = ctl.Next(ctx)
	require.NoError(t, err)
	require.NoError(t, ctl.Close())
	assert.Equal(t, 0, <-p.exited)
}

func TestSessionErrors(t *testing.T) {
	t.Parallel()
	p, s := controlled(t)
	ctx, cancel := context.WithTimeout(context.Background(), faketest.Timeout)
	defer cancel()

	// Expect names the unexpected report it got instead.
	go p.send(t, map[string]string{"op": "user", "text": "x"})
	r, err := s.Expect(ctx, fake.RepInterrupt)
	require.ErrorContains(t, err, `got report "user"`)
	assert.Equal(t, "x", r.Text)

	// A cancelled context ends a wait.
	cctx, ccancel := context.WithCancel(ctx)
	ccancel()
	_, err = s.Expect(cctx, fake.RepUser)
	require.ErrorIs(t, err, context.Canceled)
	_, err = s.Await(cctx, fake.RepUser)
	require.ErrorIs(t, err, context.Canceled)

	// Once the fake is gone, waits say the session ended.
	_, err = s.Do(ctx, fake.Command{Cmd: fake.CmdExit})
	require.NoError(t, err)
	<-p.exited
	_, err = s.Expect(ctx, fake.RepUser)
	require.ErrorContains(t, err, "session ended")
	_, err = s.Await(ctx, fake.RepUser)
	require.ErrorContains(t, err, "session ended")
	_, err = s.Do(ctx, fake.Command{Cmd: fake.CmdText})
	require.Error(t, err)
}

func TestControllerNextHonoursContextAndCloseIsIdempotent(t *testing.T) {
	t.Parallel()
	ctl, err := fake.NewController()
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = ctl.Next(ctx)
	require.ErrorIs(t, err, context.Canceled)
	require.NoError(t, ctl.Close())
	require.NoError(t, ctl.Close())
}
