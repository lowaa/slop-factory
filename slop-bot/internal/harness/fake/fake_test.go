package fake_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-clock"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/conformance"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/fake"
)

// The test binary doubles as the fake harness: re-run with this variable
// set, it is fakeharness. No separate build step, and nothing to go stale.
const reexec = "SLOP_BOT_FAKE_REEXEC"

func TestMain(m *testing.M) {
	if os.Getenv(reexec) == "1" {
		os.Exit(fake.Main(os.Args[1:], os.Stdin, os.Stdout, os.Getenv))
	}
	os.Exit(m.Run())
}

// puppet drives a fake through its controller. One subject runs one
// conversation at a time (resume is sequential), so the current session is
// the latest started.
type puppet struct {
	ctl *fake.Controller
	mu  sync.Mutex
	cur *fake.Session
}

func (p *puppet) session() *fake.Session {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cur
}

func (p *puppet) Started(ctx context.Context) (string, bool, error) {
	s, err := p.ctl.Next(ctx)
	if err != nil {
		return "", false, err
	}
	p.mu.Lock()
	p.cur = s
	p.mu.Unlock()
	return s.ID, s.Resumed, nil
}

// Reply: the fake doesn't act on messages by itself under a controller, so
// the reply is scripted once the message has arrived (Received consumes it).
func (p *puppet) Reply(ctx context.Context, text string) error {
	go func() {
		s := p.session()
		r, err := s.Expect(ctx, fake.RepUser)
		if err != nil {
			return
		}
		pendingUser.Store(s, r)
		for _, c := range []fake.Command{{Cmd: fake.CmdTurnStart}, {Cmd: fake.CmdText, Text: text}, {Cmd: fake.CmdTurnEnd}} {
			if _, err := s.Do(ctx, c); err != nil {
				return
			}
		}
	}()
	return nil
}

func (p *puppet) LongTool(ctx context.Context) (func(context.Context) error, error) {
	s := p.session()
	started := make(chan error, 1)
	go func() {
		r, err := s.Expect(ctx, fake.RepUser)
		if err == nil {
			pendingUser.Store(s, r)
			_, err = s.Do(ctx, fake.Command{Cmd: fake.CmdTurnStart})
		}
		if err == nil {
			_, err = s.Do(ctx, fake.Command{Cmd: fake.CmdToolStart, Tool: "shell"})
		}
		started <- err
	}()
	return func(ctx context.Context) error {
		if _, err := s.Do(ctx, fake.Command{Cmd: fake.CmdToolEnd, Tool: "shell"}); err != nil {
			return err
		}
		_, err := s.Do(ctx, fake.Command{Cmd: fake.CmdTurnEnd})
		return err
	}, nil
}

// Accepted waits for the fake's report of the message, which it makes while
// still in the turn, and keeps it for Received.
func (p *puppet) Accepted(ctx context.Context, text string) error {
	s := p.session()
	r, err := s.Expect(ctx, fake.RepUser)
	if err != nil {
		return err
	}
	if r.Text != text {
		return fmt.Errorf("accepted %q, want %q", r.Text, text)
	}
	pendingUser.Store(s, r)
	return nil
}

// pendingUser holds a user report a script goroutine consumed, so Received
// can check it.
var pendingUser sync.Map

func (p *puppet) Received(ctx context.Context, text string, midTurn bool) error {
	s := p.session()
	if v, ok := pendingUser.LoadAndDelete(s); ok && v.(*fake.Report).Text == text {
		return check(v.(*fake.Report), text, midTurn)
	}
	r, err := s.Expect(ctx, fake.RepUser)
	if err != nil {
		return err
	}
	return check(r, text, midTurn)
}

func check(r *fake.Report, text string, midTurn bool) error {
	if r.Text != text || r.MidTurn != midTurn {
		return fmt.Errorf("received %q (mid-turn %v), want %q (mid-turn %v)", r.Text, r.MidTurn, text, midTurn)
	}
	return nil
}

// Remembers: the fake keeps no history, so being launched to resume the
// conversation is all of its memory.
func (p *puppet) Remembers(context.Context, string) error {
	if !p.session().Resumed {
		return errors.New("the session was not resumed")
	}
	return nil
}

func (p *puppet) Interrupted(ctx context.Context) error {
	_, err := p.session().Expect(ctx, fake.RepInterrupt)
	return err
}

func (p *puppet) Crash(ctx context.Context) error {
	_, err := p.session().Do(ctx, fake.Command{Cmd: fake.CmdExit, Code: 3})
	return err
}

func subject(t *testing.T) *conformance.Subject {
	ctl, err := fake.NewController()
	require.NoError(t, err)
	t.Cleanup(func() { _ = ctl.Close() })
	self, err := os.Executable()
	require.NoError(t, err)
	h := &fake.Harness{Binary: self, Control: ctl.Socket(), Logger: slog.New(slog.DiscardHandler), Clock: clock.Real{}}
	return &conformance.Subject{
		Harness: h,
		Workspace: func(t *testing.T) *harness.Workspace {
			return &harness.Workspace{Agent: "a1", Ticket: "7", Dir: t.TempDir(), Env: map[string]string{reexec: "1", "GORACE": "atexit_sleep_ms=0"}}
		},
		Puppet:  &puppet{ctl: ctl},
		Timeout: 20 * time.Second,
	}
}

func TestConformance(t *testing.T) {
	t.Parallel()
	conformance.Run(t, subject)
}

// A graceful Stop right after an idle Send must wait for the turn that Send
// started, even though the harness hasn't reported it yet.
func TestStopRightAfterSendWaitsForThatTurn(t *testing.T) {
	t.Parallel()
	s := subject(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	conv, err := s.Harness.Start(ctx, s.Workspace(t), nil)
	require.NoError(t, err)
	defer func() { _ = conv.Stop(context.Background(), true) }()
	p := s.Puppet.(*puppet)
	_, _, err = p.Started(ctx)
	require.NoError(t, err)

	require.NoError(t, conv.Send(ctx, &harness.Message{Text: "work"}))
	stopped := make(chan error, 1)
	go func() { stopped <- conv.Stop(ctx, false) }()

	// The fake hasn't started the turn yet; Stop must still be waiting.
	sess := p.session()
	_, err = sess.Expect(ctx, fake.RepUser)
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
}
