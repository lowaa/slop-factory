package ticket_test

import (
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

func TestWithSettledHook_ToldWhichTicketsAStatusMoveSettled(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	tk := f.ticket(t, "Settles", "a/")
	var told []int64
	store := newStore(f.database, ticket.WithSettledHook(func(_ context.Context, ids []int64) {
		told = append(told, ids...)
	}))
	svc := ticket.NewService(store, slog.DiscardHandler)

	_, err := svc.MoveMany(f.ctx, []int64{tk.ID}, ticket.StatusCancelled)

	require.NoError(t, err)
	assert.Equal(t, []int64{tk.ID}, told)
}
