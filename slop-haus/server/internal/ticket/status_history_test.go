package ticket_test

import (
	"os"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// The approximation queries in status_history.md are what someone runs to
// answer for the time before the status history: they must run, against the
// schema as it is, and say something about a finished ticket.
func TestStatusHistoryDoc_ApproximationQueriesRun(t *testing.T) {
	t.Parallel()
	doc, err := os.ReadFile("status_history.md")
	require.NoError(t, err)
	blocks := regexp.MustCompile("(?s)```sql\n(.*?)```").FindAllSubmatch(doc, -1)
	require.Len(t, blocks, 4)

	f := newFixture(t)
	a := f.agent(t, "builder-1")
	tk := f.ticket(t, "Finished", "done/")
	_, err = f.svc.Claim(f.ctx, a, &tk.ID)
	require.NoError(t, err)
	f.discuss(t, a)
	f.humanReply(t, tk.ID)
	f.claim(t, a)
	_, err = f.svc.Done(f.ctx, a, &ticket.DoneParams{Summary: "ok"})
	require.NoError(t, err)

	// created five hours ago, claimed at four, done now.
	_, err = f.database.Exec(f.ctx, `
		UPDATE tickets SET created_at = now() - interval '5 hours', closed_at = now() WHERE id = $1;`, tk.ID)
	require.NoError(t, err)
	_, err = f.database.Exec(f.ctx, `
		UPDATE leases SET acquired_at = now() - interval '4 hours' WHERE ticket_id = $1 AND id = (SELECT min(id) FROM leases WHERE ticket_id = $1)`, tk.ID)
	require.NoError(t, err)

	var leadTimeHours []float64
	require.NoError(t, f.database.QueryRow(f.ctx, string(blocks[0][1])).Scan(new(int64), &leadTimeHours))
	assert.InDelta(t, 5.0, leadTimeHours[0], 0.01, "the lead time of the one finished ticket")
	var waitingHours, workingHours []float64
	require.NoError(t, f.database.QueryRow(f.ctx, string(blocks[1][1])).Scan(&waitingHours, &workingHours))
	assert.InDelta(t, 1.0, waitingHours[0], 0.01, "an hour from filing to the first claim")
	assert.InDelta(t, 4.0, workingHours[0], 0.01, "four hours from the first claim to done")

	for _, block := range blocks {
		rows, err := f.database.Query(f.ctx, string(block[1]))
		require.NoError(t, err, "%s", block[1])
		require.True(t, rows.Next(), "%s", block[1])
		values, err := rows.Values()
		require.NoError(t, err)
		rows.Close()
		require.NoError(t, rows.Err())
		assert.NotEmpty(t, values)
	}
}
