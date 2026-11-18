package ticket_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

// Pending proposals decide whether a ticket has moved on without
// tickets.updated_at, because routine activity must not make them stale.
// This pins the fact that decision rests on: comments and keep-alives leave
// updated_at alone, while a real change moves it.
func TestUpdatedAtIgnoresCommentsAndHeartbeats(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	created := f.ticket(t, "Held ticket", "server/")
	f.claim(t, a)
	before, err := f.svc.Get(f.ctx, created.ID)
	require.NoError(t, err)

	_, err = f.svc.AddComment(f.ctx, created.ID, ticket.AgentAuthor(a), "still on it")
	require.NoError(t, err)
	_, err = f.svc.AddComment(f.ctx, created.ID, ticket.UserAuthor(f.human), "noted")
	require.NoError(t, err)
	require.NoError(t, f.svc.Heartbeat(f.ctx, a))

	after, err := f.svc.Get(f.ctx, created.ID)
	require.NoError(t, err)
	assert.True(t, before.UpdatedAt.Equal(after.UpdatedAt), "comments and heartbeats moved updated_at")

	high := model.PriorityHigh
	_, err = f.svc.Update(f.ctx, created.ID, &ticket.UpdateParams{Priority: &high})
	require.NoError(t, err)
	moved, err := f.svc.Get(f.ctx, created.ID)
	require.NoError(t, err)
	assert.True(t, moved.UpdatedAt.After(after.UpdatedAt), "a real edit must move it, or the check above proves nothing")
}
