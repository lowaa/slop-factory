package scaleperf_test

import (
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/schema/seeddata"
)

// What is held in the PR gate is what a list draws (list.maxRows): a ceiling
// that does not depend on the machine at all and shows the regression this
// guards against, a page that stopped being a page.
//
// The time budget is not: a list that reads every ticket's comments one query
// at a time costs seconds at this size and is caught by it, but a busy CI
// agent pushed a healthy list past it, which turned unrelated pull requests
// red. It is asserted only when SLOP_HAUS_PERF_BUDGETS is set, which test.sh
// does not do; run it by hand or from a scheduled job.
//
// At DefaultScale (5,000 tickets) on a developer machine the slowest list,
// the board with done tickets included, takes about 40ms and every other list
// under 25ms. A list is read three times and held to its fastest read, so one
// read stalled by the garbage collector does not fail it.
const (
	listBudget = time.Second
	reads      = 3
	// perfBudgetsEnv turns the time budget on.
	perfBudgetsEnv = "SLOP_HAUS_PERF_BUDGETS"
)

func TestListsStayPagedAtScale(t *testing.T) {
	_, timed := os.LookupEnv(perfBudgetsEnv)
	w := seeded(t, seeddata.DefaultScale)
	require.Equal(t, seeddata.DefaultScale.Tickets, w.sum.Tickets)

	for _, l := range lists {
		t.Run(l.name, func(t *testing.T) {
			fastest := time.Duration(1<<63 - 1)
			var rows int
			for range reads {
				start := time.Now()
				var err error
				rows, err = l.run(w)
				require.NoError(t, err)
				fastest = min(fastest, time.Since(start))
			}
			t.Logf("%d rows, fastest of %d reads %s (budget %s)", rows, reads, fastest, listBudget)
			assert.NotZero(t, rows, "an empty list meets any budget")
			if l.maxRows > 0 {
				assert.LessOrEqual(t, rows, l.maxRows, "the list drew more than a page")
			}
			if timed {
				assert.Less(t, fastest, listBudget)
			}
		})
	}
}
