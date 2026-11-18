package ticketusage_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticketusage"
)

// attempted is worker for one attempt of the ticket (which must exist).
func (f *fixture) attempted(t *testing.T, ticketID int64, attempt int, s spec) {
	t.Helper()
	f.worker(t, ticketID, s)
	_, err := f.database.Exec(f.ctx,
		`UPDATE llm_usage SET attempt_number = $2 WHERE ticket_id = $1 AND transcript_key = $3`, ticketID, attempt, s.key)
	require.NoError(t, err)
}

func (f *fixture) addAttempt(t *testing.T, ticketID int64) {
	t.Helper()
	_, err := f.database.Exec(f.ctx, `INSERT INTO attempts (ticket_id, number, status) VALUES ($1, 2, 'queued')`, ticketID)
	require.NoError(t, err)
}

func TestAttemptCostsSumsEachAttemptAlone(t *testing.T) {
	f := newFixture(t, dbtest.WithDB(t))
	one, other := f.ticket(t), f.ticket(t)
	f.addAttempt(t, one)
	f.addAttempt(t, other)
	f.attempted(t, one, 1, spec{key: "a", requests: 2, in: 100, est: micros(10)})
	f.attempted(t, one, 1, spec{key: "b", requests: 1, in: 7, est: micros(5)})
	f.attempted(t, one, 1, spec{key: "m", mode: "metered", requests: 1, in: 1, reported: micros(300)})
	f.attempted(t, one, 2, spec{key: "c", requests: 4, in: 1000, est: micros(70)})
	f.attempted(t, other, 1, spec{key: "d", requests: 9, in: 9, est: micros(9)})
	f.attempted(t, other, 2, spec{key: "u", requests: 3, in: 30})

	got, err := ticketusage.NewPostgresStore(f.database).AttemptCosts(f.ctx, []ticket.AttemptRef{
		{TicketID: one, Number: 1}, {TicketID: one, Number: 2}, {TicketID: other, Number: 2},
	})
	require.NoError(t, err)

	require.Len(t, got, 3, "the attempt asked for and not the sibling's")
	first := got[ticket.AttemptRef{TicketID: one, Number: 1}]
	require.Len(t, first, 2)
	assert.Equal(t, "metered", first[0].BillingMode)
	assert.EqualValues(t, 300, *first[0].CostMicros)
	assert.Equal(t, "subscription", first[1].BillingMode)
	assert.EqualValues(t, 107, first[1].InputTokens)
	assert.EqualValues(t, 15, *first[1].CostMicros, "attempt 1's two rows, not attempt 2's")
	assert.EqualValues(t, 3, first[1].Requests)

	second := got[ticket.AttemptRef{TicketID: one, Number: 2}]
	require.Len(t, second, 1)
	assert.EqualValues(t, 70, *second[0].CostMicros)

	unpriced := got[ticket.AttemptRef{TicketID: other, Number: 2}]
	require.Len(t, unpriced, 1)
	assert.Nil(t, unpriced[0].CostMicros, "no priced row is not a zero")
	assert.EqualValues(t, 1, unpriced[0].UnpricedRecords)
}

func TestAttemptCostsIsEmptyForAnAttemptThatSpentNothing(t *testing.T) {
	f := newFixture(t, dbtest.WithDB(t))
	tk := f.ticket(t)
	got, err := ticketusage.NewPostgresStore(f.database).AttemptCosts(f.ctx, []ticket.AttemptRef{{TicketID: tk, Number: 1}})
	require.NoError(t, err)
	assert.Empty(t, got)
	got, err = ticketusage.NewPostgresStore(f.database).AttemptCosts(f.ctx, nil)
	require.NoError(t, err)
	assert.Empty(t, got)
}

// Another tenant's usage is invisible whatever the refs name: row-level
// security scopes the read, not the caller's list.
func TestAttemptCostsNeverReadsAnotherTenant(t *testing.T) {
	database := dbtest.WithDB(t)
	mine := newFixture(t, database)
	theirs := newFixture(t, database)
	tk := theirs.ticket(t)
	theirs.attempted(t, tk, 1, spec{key: "x", requests: 1, in: 1, est: micros(1)})

	// Ids are per tenant, so the refs may name the other tenant's ticket's own id.
	got, err := ticketusage.NewPostgresStore(database).AttemptCosts(mine.ctx, []ticket.AttemptRef{{TicketID: tk, Number: 1}})
	require.NoError(t, err)
	assert.Empty(t, got)
}
