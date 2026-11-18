package ticket_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

func (f *fixture) writeTicket(t *testing.T, id int64, w *ticket.TicketWrite) error {
	t.Helper()
	return f.svc.InTransition(f.ctx, func(tx ticket.TransitionTx) error { return tx.WriteTicket(f.ctx, id, w) })
}

func (f *fixture) attemptPR(t *testing.T, id int64, number int) *string {
	t.Helper()
	var pr *string
	require.NoError(t, f.database.QueryRow(f.ctx,
		`SELECT pr_url FROM attempts WHERE ticket_id = $1 AND number = $2`, id, number).Scan(&pr))
	return pr
}

// Ticket-level writes follow the promotion, as the reads do: once attempt 2
// is the promoted one a write lands on it, and attempt 1 is left alone.
func TestTicketWrites_GoToThePromotedAttempt(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	created := f.ticket(t, "Two attempts", "a/")
	_, err := f.database.Exec(f.ctx, `INSERT INTO attempts (ticket_id, number, status) VALUES ($1, 2, 'queued')`, created.ID)
	require.NoError(t, err)
	f.setAttempt(t, created.ID, "promoted_at = NULL")
	_, err = f.database.Exec(f.ctx, `UPDATE attempts SET promoted_at = now() WHERE ticket_id = $1 AND number = 2`, created.ID)
	require.NoError(t, err)

	pr := "https://github.com/o/r/pull/80"
	require.NoError(t, f.writeTicket(t, created.ID, &ticket.TicketWrite{PRURL: &pr}))

	require.NotNil(t, f.attemptPR(t, created.ID, 2))
	assert.Equal(t, pr, *f.attemptPR(t, created.ID, 2))
	assert.Nil(t, f.attemptPR(t, created.ID, 1), "the demoted attempt is not written")
}
