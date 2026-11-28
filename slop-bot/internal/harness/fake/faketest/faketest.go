// Package faketest wires the fake harness into tests: the conformance
// puppet, and per-test rigs for fake/rpc and fake/tui.
package faketest

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-clock"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/conformance"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/fake"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/sessionhost"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/sessionhost/tmuxtest"
	"github.com/lowaa/slop-factory/slop-bot/internal/ident"
)

// reexec makes the test binary the fake harness: re-run with it set, it is
// fakeharness. No separate build step, and nothing to go stale.
const reexec = "SLOP_BOT_FAKE_REEXEC"

// Timeout bounds each step: a safety net, never a synchronisation.
const Timeout = 20 * time.Second

// Main is a test package's TestMain body.
func Main(m *testing.M) {
	if os.Getenv(reexec) == "1" {
		os.Exit(fake.Main(os.Args[1:], os.Stdin, os.Stdout, os.Getenv))
	}
	os.Exit(m.Run())
}

// Pointer is the tests' pointer: unlike the daemon's, it carries the texts,
// so a test sees which messages it stands for.
func Pointer(msgs []*harness.Message) string {
	texts := make([]string, len(msgs))
	for i, m := range msgs {
		texts[i] = m.Text
	}
	return fmt.Sprintf("%d new: %s", len(msgs), strings.Join(texts, " | "))
}

// Workspace is a fresh workspace whose harness is this test binary.
func Workspace(t *testing.T, agent string) *harness.Workspace {
	t.Helper()
	return &harness.Workspace{Agent: ident.AgentID(agent), Ticket: "7", Dir: t.TempDir(), Env: map[string]string{reexec: "1", "GORACE": "atexit_sleep_ms=0"}}
}

func self(t *testing.T) string {
	t.Helper()
	bin, err := os.Executable()
	require.NoError(t, err)
	return bin
}

func controller(t *testing.T) *fake.Controller {
	t.Helper()
	ctl, err := fake.NewController()
	require.NoError(t, err)
	t.Cleanup(func() { _ = ctl.Close() })
	return ctl
}

// RPCSubject is fake/rpc under the conformance suite.
func RPCSubject(t *testing.T) *conformance.Subject {
	ctl := controller(t)
	h := &fake.Harness{Binary: self(t), Control: ctl.Socket(), Logger: slog.New(slog.DiscardHandler), Clock: clock.Real{}}
	return &conformance.Subject{
		Harness:   h,
		Workspace: func(t *testing.T) *harness.Workspace { return Workspace(t, "a1") },
		Puppet:    &Puppet{ctl: ctl},
		Timeout:   Timeout,
	}
}

// TUI is fake/tui on a tmux server of its own.
type TUI struct {
	Ctl     *fake.Controller
	Host    *sessionhost.Tmux
	Hub     *fake.HookHub
	Harness *fake.TUIHarness
	Puppet  *Puppet
}

// TUIOptions shape a TUI rig; the zero value is the wall clock and the
// kit's default timings.
type TUIOptions struct {
	Clock          clock.Clock
	PollInterval   time.Duration
	ConfirmTimeout time.Duration
}

// NewTUI is a fake/tui rig, torn down when the test ends.
func NewTUI(t *testing.T, opts *TUIOptions) *TUI {
	t.Helper()
	ctl := controller(t)
	host := tmuxtest.New(t)
	hub, err := fake.NewHookHub(self(t))
	require.NoError(t, err)
	t.Cleanup(func() { _ = hub.Close() })
	h := &fake.TUIHarness{
		Binary: self(t), Control: ctl.Socket(), Host: host, Hub: hub,
		Pointer: Pointer, Logger: slog.New(slog.DiscardHandler), Clock: clock.Or(opts.Clock),
		PollInterval: opts.PollInterval, ConfirmTimeout: opts.ConfirmTimeout,
	}
	return &TUI{Ctl: ctl, Host: host, Hub: hub, Harness: h, Puppet: &Puppet{ctl: ctl, tui: true}}
}

// TUISubject is fake/tui under the conformance suite.
func TUISubject(t *testing.T) *conformance.Subject {
	rig := NewTUI(t, &TUIOptions{})
	return &conformance.Subject{
		Harness:   rig.Harness,
		Workspace: func(t *testing.T) *harness.Workspace { return Workspace(t, "a1") },
		Puppet:    rig.Puppet,
		Timeout:   Timeout,
	}
}

// Puppet drives a fake through its controller (conformance.Puppet). One
// subject runs one conversation at a time (resume is sequential), so the
// current session is the latest started.
type Puppet struct {
	ctl *fake.Controller
	// tui: what the fake received arrives among pastes, keys and hooks,
	// and an idle message arrives as its pointer.
	tui bool

	mu  sync.Mutex
	cur *fake.Session
	// held is a user report a script goroutine consumed, kept for Received.
	held *fake.Report
}

// Session is the current fake, for a test to script directly.
func (p *Puppet) Session() *fake.Session {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cur
}

func (p *Puppet) await(ctx context.Context, s *fake.Session, rep string) (*fake.Report, error) {
	if p.tui {
		return s.Await(ctx, rep)
	}
	return s.Expect(ctx, rep)
}

func (p *Puppet) hold(r *fake.Report) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.held = r
}

// Started implements conformance.Puppet.
func (p *Puppet) Started(ctx context.Context) (string, bool, error) {
	s, err := p.ctl.Next(ctx)
	if err != nil {
		return "", false, err
	}
	p.mu.Lock()
	p.cur, p.held = s, nil
	p.mu.Unlock()
	return s.ID, s.Resumed, nil
}

// Reply implements conformance.Puppet: the fake doesn't act on messages by
// itself under a controller, so the reply is scripted once the message has
// arrived (Received checks it).
func (p *Puppet) Reply(ctx context.Context, text string) error {
	s := p.Session()
	go func() {
		r, err := p.await(ctx, s, fake.RepUser)
		if err != nil {
			return
		}
		p.hold(r)
		for _, c := range []fake.Command{{Cmd: fake.CmdTurnStart}, {Cmd: fake.CmdText, Text: text}, {Cmd: fake.CmdTurnEnd}} {
			if _, err := s.Do(ctx, c); err != nil {
				return
			}
		}
	}()
	return nil
}

// LongTool implements conformance.Puppet.
func (p *Puppet) LongTool(ctx context.Context) (func(context.Context) error, error) {
	s := p.Session()
	go func() {
		r, err := p.await(ctx, s, fake.RepUser)
		if err != nil {
			return
		}
		p.hold(r)
		if _, err := s.Do(ctx, fake.Command{Cmd: fake.CmdTurnStart}); err == nil {
			_, _ = s.Do(ctx, fake.Command{Cmd: fake.CmdToolStart, Tool: "shell"})
		}
	}()
	return func(ctx context.Context) error {
		if _, err := s.Do(ctx, fake.Command{Cmd: fake.CmdToolEnd, Tool: "shell"}); err != nil {
			return err
		}
		_, err := s.Do(ctx, fake.Command{Cmd: fake.CmdTurnEnd})
		return err
	}, nil
}

// Accepted implements conformance.Puppet: it waits for the fake's report of
// the mid-turn message, made while still in the turn, and keeps it for
// Received. On fake/tui that report comes from a hook, so it runs one.
func (p *Puppet) Accepted(ctx context.Context, text string) error {
	s := p.Session()
	if p.tui {
		if _, err := s.Do(ctx, fake.Command{Cmd: fake.CmdStep}); err != nil {
			return err
		}
	}
	r, err := p.await(ctx, s, fake.RepUser)
	if err != nil {
		return err
	}
	if r.Text != text {
		return fmt.Errorf("accepted %q, want %q", r.Text, text)
	}
	p.hold(r)
	return nil
}

// Remembers implements conformance.Puppet: the fake keeps no history, so
// being launched to resume the conversation is all of its memory.
func (p *Puppet) Remembers(context.Context, string) error {
	if !p.Session().Resumed {
		return errors.New("the session was not resumed")
	}
	return nil
}

// Received implements conformance.Puppet. On fake/tui a mid-turn message
// waits for a hook that can inject it, so the puppet runs one, as a real
// harness does before its next model call.
func (p *Puppet) Received(ctx context.Context, text string, midTurn bool) error {
	s := p.Session()
	want := &fake.Report{Text: text, MidTurn: midTurn}
	if p.tui {
		want.Via = fake.ViaHook
		if !midTurn {
			want.Via, want.Text = fake.ViaKeys, Pointer([]*harness.Message{{Text: text}})
		}
	}
	p.mu.Lock()
	r := p.held
	p.held = nil
	p.mu.Unlock()
	if r == nil || r.Text != want.Text {
		if p.tui && midTurn {
			if _, err := s.Do(ctx, fake.Command{Cmd: fake.CmdStep}); err != nil {
				return err
			}
		}
		var err error
		if r, err = p.await(ctx, s, fake.RepUser); err != nil {
			return err
		}
	}
	if r.Text != want.Text || r.MidTurn != want.MidTurn || r.Via != want.Via {
		return fmt.Errorf("received %q (mid-turn %v, via %q), want %q (mid-turn %v, via %q)", r.Text, r.MidTurn, r.Via, want.Text, want.MidTurn, want.Via)
	}
	return nil
}

// Interrupted implements conformance.Puppet.
func (p *Puppet) Interrupted(ctx context.Context) error {
	_, err := p.await(ctx, p.Session(), fake.RepInterrupt)
	return err
}

// Crash implements conformance.Puppet.
func (p *Puppet) Crash(ctx context.Context) error {
	_, err := p.Session().Do(ctx, fake.Command{Cmd: fake.CmdExit, Code: 3})
	return err
}
