package ticket_test

import (
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost/codehosttest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// withAffinity points the fixture's service at a window other than the default.
func (f *fixture) withAffinity(d time.Duration) {
	f.svc = ticket.NewService(newStore(f.database), slog.DiscardHandler,
		ticket.WithCodeHosts(codehosttest.Registry(codehosttest.NewLinkHost(prHost))),
		ticket.WithResumeAffinity(d))
}

// Each human move that gives the asker first claim must use the configured
// window, not the default.
func TestResumeAffinityWindow_EveryHumanMoveUsesTheConfiguredWindow(t *testing.T) {
	t.Parallel()
	moves := map[string]func(t *testing.T, f *fixture) *ticket.Ticket{
		"discussion answer": func(t *testing.T, f *fixture) *ticket.Ticket {
			created := f.ticket(t, "Loose ticket", "server/")
			a := f.agent(t, "asker")
			f.claim(t, a)
			f.discuss(t, a)
			f.humanReply(t, created.ID)
			got, err := f.svc.Get(f.ctx, created.ID)
			require.NoError(t, err)
			return got
		},
		"review approval": func(t *testing.T, f *fixture) *ticket.Ticket {
			gated := f.gated(t, "Gated", "server/")
			a := f.agent(t, "author")
			f.claim(t, a)
			f.review(t, a)
			return f.approve(t, gated.ID)
		},
		"review changes requested": func(t *testing.T, f *fixture) *ticket.Ticket {
			gated := f.gated(t, "Gated", "server/")
			a := f.agent(t, "author")
			f.claim(t, a)
			f.review(t, a)
			return f.requestChanges(t, gated.ID)
		},
	}
	for name, move := range moves {
		t.Run(name+" with a long window", func(t *testing.T) {
			f := newFixture(t)
			f.withAffinity(90 * time.Minute)
			got := move(t, f)
			require.NotNil(t, got.AffinityUntil)
			// Well past the 30 minute default, so the default cannot satisfy it.
			assert.Greater(t, time.Until(*got.AffinityUntil), 60*time.Minute)
			assert.Less(t, time.Until(*got.AffinityUntil), 2*time.Hour)
		})
		t.Run(name+" with no window", func(t *testing.T) {
			f := newFixture(t)
			f.withAffinity(0)
			got := move(t, f)
			claimed := f.claim(t, f.agent(t, "other"))
			require.NotNil(t, claimed, "another agent may take it at once")
			assert.Equal(t, got.ID, claimed.ID)
		})
	}
}
