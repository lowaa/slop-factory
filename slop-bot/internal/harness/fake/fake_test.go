package fake_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/harness"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/conformance"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/fake"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/fake/faketest"
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
