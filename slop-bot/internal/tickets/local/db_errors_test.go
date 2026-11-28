package local_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/lowaa/slop-factory/go-clock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/ident"
	"github.com/lowaa/slop-factory/slop-bot/internal/tickets"
)

// A database call that fails is reported with the step it was part of. Each
// case breaks the one table that step reads or writes, after a ticket exists.
func TestDatabaseFailuresAreWrappedWithTheirStep(t *testing.T) {
	t.Parallel()
	user := ident.UserPrincipal()
	get := func(ctx context.Context, s tickets.TicketingSystem) error { _, err := s.Get(ctx, "1"); return err }
	tests := []struct {
		name    string
		breakDB string
		call    func(ctx context.Context, s tickets.TicketingSystem) error
		wantMsg string
	}{
		{"reading the ticket", `ALTER TABLE tickets RENAME TO tickets_gone`, get, "local: reading ticket 1"},
		{"reading open blockers", `DROP TABLE blockers`, get, "local: reading open blockers"},
		{"reading comments", `DROP TABLE comments`, get, "local: reading comments of 1"},
		{
			"counting agent-filed tickets", `ALTER TABLE tickets RENAME TO tickets_gone`,
			func(ctx context.Context, s tickets.TicketingSystem) error {
				_, err := s.CountOutstandingAgentCreated(ctx)
				return err
			},
			"local: counting agent-filed tickets",
		},
		{
			"recording the update event", `DROP TABLE events`,
			func(ctx context.Context, s tickets.TicketingSystem) error {
				_, err := s.Update(ctx, user, "1", &tickets.TicketChange{Title: ptr("renamed")})
				return err
			},
			"local: recording " + string(tickets.EventUpdated) + " event",
		},
		{
			"checking an agent's ticket", `ALTER TABLE tickets RENAME TO tickets_gone`,
			func(ctx context.Context, s tickets.TicketingSystem) error {
				_, err := s.Update(ctx, ident.AgentPrincipal("a1", "1"), "1", &tickets.TicketChange{Title: ptr("renamed")})
				return err
			},
			"local: reading agent's ticket",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b := &backend{path: filepath.Join(t.TempDir(), "x.db"), clock: clock.NewFake(t0)}
			s := b.open(t)
			ctx := context.Background()
			_, err := s.Create(ctx, user, &tickets.NewTicket{Title: "x"})
			require.NoError(t, err)
			_, err = s.DB().ExecContext(ctx, tc.breakDB)
			require.NoError(t, err)

			err = tc.call(ctx, s)
			require.Error(t, err)
			assert.ErrorContains(t, err, tc.wantMsg)
			assert.NotErrorIs(t, err, tickets.ErrNotFound)
		})
	}
}

func TestUpdateReportsAFailedTicketWrite(t *testing.T) {
	t.Parallel()
	b := &backend{path: filepath.Join(t.TempDir(), "x.db"), clock: clock.NewFake(t0)}
	s := b.open(t)
	ctx := context.Background()
	user := ident.UserPrincipal()
	_, err := s.Create(ctx, user, &tickets.NewTicket{Title: "x"})
	require.NoError(t, err)
	_, err = s.DB().ExecContext(ctx, `CREATE TRIGGER no_ticket_updates BEFORE UPDATE ON tickets
		BEGIN SELECT RAISE(ABORT, 'ticket updates refused'); END`)
	require.NoError(t, err)

	_, err = s.Update(ctx, user, "1", &tickets.TicketChange{Title: ptr("renamed")})
	require.Error(t, err)
	assert.ErrorContains(t, err, "local: updating ticket 1")
	assert.ErrorContains(t, err, "ticket updates refused")
}
