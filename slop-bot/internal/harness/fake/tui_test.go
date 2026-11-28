package fake_test

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/harness/fake"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/fake/faketest"
)

// inprocTUI is the fake's TUI mode run in this process over pipes (stdin is
// not a terminal, so there is no raw mode), so its key handling is measured
// and a test can read its screen draws.
type inprocTUI struct {
	stdin   io.WriteCloser
	screens chan string
	exited  chan int
	s       *fake.Session
	ctx     context.Context
}

func startTUI(t *testing.T) *inprocTUI {
	t.Helper()
	ctl, err := fake.NewController()
	require.NoError(t, err)
	t.Cleanup(func() { _ = ctl.Close() })
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	p := &inprocTUI{stdin: inW, screens: make(chan string, 4096), exited: make(chan int, 1)}
	// A hook that does nothing: the fake still reports each hook it runs.
	hook, err := json.Marshal([]string{"true"})
	require.NoError(t, err)
	env := map[string]string{fake.EnvControl: ctl.Socket(), fake.EnvID: "t1", fake.EnvHookCmd: string(hook)}
	go func() {
		buf := make([]byte, 64<<10)
		for {
			n, err := outR.Read(buf)
			if n > 0 {
				p.screens <- string(buf[:n])
			}
			if err != nil {
				return
			}
		}
	}()
	go func() {
		code := fake.Main([]string{"--tui"}, inR, outW, func(k string) string { return env[k] })
		_ = outW.Close()
		p.exited <- code
	}()
	t.Cleanup(func() { _ = inW.Close(); _ = outR.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), faketest.Timeout)
	t.Cleanup(cancel)
	p.ctx = ctx
	p.s, err = ctl.Next(ctx)
	require.NoError(t, err)
	require.True(t, p.s.TUI)
	return p
}

func (p *inprocTUI) press(t *testing.T, keys string) {
	t.Helper()
	_, err := p.stdin.Write([]byte(keys))
	require.NoError(t, err)
}

func (p *inprocTUI) do(t *testing.T, c fake.Command) {
	t.Helper()
	_, err := p.s.Do(p.ctx, c)
	require.NoError(t, err)
}

// screenWith waits for a draw containing want.
func (p *inprocTUI) screenWith(t *testing.T, want string) string {
	t.Helper()
	for {
		select {
		case s := <-p.screens:
			if strings.Contains(s, want) {
				return s
			}
		case <-p.ctx.Done():
			require.FailNow(t, "no screen with "+want)
		}
	}
}

// hooksThrough is the events of the hooks the fake ran, up to and including
// last.
func (p *inprocTUI) hooksThrough(t *testing.T, last string) []string {
	t.Helper()
	var events []string
	for {
		r, err := p.s.Await(p.ctx, fake.RepHook)
		require.NoError(t, err)
		events = append(events, r.Event)
		if r.Event == last {
			return events
		}
	}
}

func TestTUILoneEscapeCancelsTheTurn(t *testing.T) {
	t.Parallel()
	p := startTUI(t)
	p.do(t, fake.Command{Cmd: fake.CmdTurnStart})
	p.screenWith(t, "busy")

	// A lone ESC is the Escape key once its wait for a sequence runs out.
	p.press(t, "\x1b")
	_, err := p.s.Await(p.ctx, fake.RepInterrupt)
	require.NoError(t, err)
	p.screenWith(t, "interrupted")
	p.do(t, fake.Command{Cmd: fake.CmdText, Text: "after"})
	assert.Contains(t, p.hooksThrough(t, fake.HookText), fake.HookTurnEnd, "the cancel ended the turn by hook")
}

func TestTUIQuietCancelRunsNoTurnEndHook(t *testing.T) {
	t.Parallel()
	p := startTUI(t)
	p.do(t, fake.Command{Cmd: fake.CmdQuietCancel, On: true})
	p.do(t, fake.Command{Cmd: fake.CmdTurnStart})
	p.press(t, "\x1b")
	_, err := p.s.Await(p.ctx, fake.RepInterrupt)
	require.NoError(t, err)
	p.screenWith(t, "interrupted")
	p.do(t, fake.Command{Cmd: fake.CmdText, Text: "after"})
	assert.NotContains(t, p.hooksThrough(t, fake.HookText), fake.HookTurnEnd)
}

func TestTUIIgnoredInterruptKeepsTheTurn(t *testing.T) {
	t.Parallel()
	p := startTUI(t)
	p.do(t, fake.Command{Cmd: fake.CmdIgnoreInterrupts, On: true})
	p.do(t, fake.Command{Cmd: fake.CmdTurnStart})
	p.press(t, "\x1b")
	_, err := p.s.Await(p.ctx, fake.RepInterrupt)
	require.NoError(t, err)

	p.do(t, fake.Command{Cmd: fake.CmdText, Text: "still"})
	s := p.screenWith(t, "still")
	assert.Contains(t, s, "busy")
	assert.NotContains(t, s, "interrupted")
}

func TestTUICancelOutsideATurnClearsTheInput(t *testing.T) {
	t.Parallel()
	p := startTUI(t)
	p.press(t, "abc")
	p.screenWith(t, "> abc")
	p.press(t, "\x1b")
	_, err := p.s.Await(p.ctx, fake.RepInterrupt)
	require.NoError(t, err)
	s := p.screenWith(t, "idle")
	assert.NotContains(t, s, "abc")
}

func TestTUIDroppedSubmitsAreReportedAsDropped(t *testing.T) {
	t.Parallel()
	p := startTUI(t)
	p.do(t, fake.Command{Cmd: fake.CmdDropSubmits, Count: 1})
	p.press(t, "hi\r")
	r, err := p.s.Await(p.ctx, fake.RepKey)
	require.NoError(t, err)
	assert.True(t, r.Dropped)

	p.press(t, "\r")
	r, err = p.s.Await(p.ctx, fake.RepKey)
	require.NoError(t, err)
	assert.False(t, r.Dropped)
	r, err = p.s.Await(p.ctx, fake.RepUser)
	require.NoError(t, err)
	assert.Equal(t, "hi", r.Text, "the dropped Enter left the input for the next")
}

func TestTUIExitsWhenTheTerminalGoesAway(t *testing.T) {
	t.Parallel()
	p := startTUI(t)
	require.NoError(t, p.stdin.Close())
	assert.Equal(t, 0, <-p.exited)
}
