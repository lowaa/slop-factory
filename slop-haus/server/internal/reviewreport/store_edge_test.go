package reviewreport_test

import (
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/reviewreport"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// A blocking finding counts whichever persona it names: none, one the run
// was not asked for (its blast-radius and seams passes name their own), or
// the same one twice.
func TestRunsForAttemptHead_AFindingBlocksWhateverPersonaItNames(t *testing.T) {
	f := newFixture(t)
	builder := f.agent(t, f.ctx, "builder")
	id := f.ticket(t, f.ctx)
	f.hold(t, f.ctx, builder, id, 1)
	run := validRun(ticket.BranchName(id, 1), "standard", "standard", "blast-radius", "seams")
	run.Findings[0].Personas = nil
	run.Findings[1].Personas = []string{"standard", "standard"}
	f.insert(t, f.ctx, builder, run)

	got, err := f.store.RunsForAttemptHead(f.ctx, id, 1, headSHA, reviewreport.PostedByAnyone)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, 4, got[0].Blocking)
}

func TestRunsForAttemptHead_AnotherTenantsRunsAreNotRead(t *testing.T) {
	f := newFixture(t)
	builder := f.agent(t, f.ctx, "builder")
	id := f.ticket(t, f.ctx)
	f.hold(t, f.ctx, builder, id, 1)
	f.insert(t, f.ctx, builder, validRun(ticket.BranchName(id, 1)))

	// The other tenant's ticket with the same number, reviewed at the same head.
	stranger := f.agent(t, f.otherCtx, "stranger")
	otherID := f.ticket(t, f.otherCtx)
	require.Equal(t, id, otherID, "tickets are numbered per tenant")
	f.hold(t, f.otherCtx, stranger, otherID, 1)
	f.insert(t, f.otherCtx, stranger, validRun(ticket.BranchName(otherID, 1)))

	got, err := f.store.RunsForAttemptHead(f.ctx, id, 1, headSHA, reviewreport.PostedByAnyone)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, builder, got[0].Poster.ID)
}

func TestLabelFinding_TextPostgresCannotHoldIsInvalid(t *testing.T) {
	f := newFixture(t)
	builder := f.agent(t, f.ctx, "builder")
	run := validRun("main", "standard")
	f.insert(t, f.ctx, builder, run)
	err := f.store.LabelFinding(f.ctx, builder, run.Findings[0].ID, &reviewreport.Label{Verdict: "confirmed", Reason: "a\x00b"})
	assert.ErrorIs(t, err, reviewreport.ErrInvalid)
}

// The same run posted twice at once (a retry racing the original) is stored
// once: one post creates it, the other finds it.
func TestInsertRun_ConcurrentPostsOfOneRunStoreItOnce(t *testing.T) {
	f := newFixture(t)
	builder := f.agent(t, f.ctx, "builder")
	run := validRun("main", "standard", "security")
	const posts = 4
	start := make(chan struct{})
	results := make(chan *reviewreport.Inserted, posts)
	var wg sync.WaitGroup
	for range posts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			got, err := f.store.InsertRun(f.ctx, byAgent(builder), run)
			assert.NoError(t, err)
			results <- got
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	created := 0
	for got := range results {
		if got != nil && got.Created {
			created++
		}
	}
	assert.Equal(t, 1, created)
	var findings int
	require.NoError(t, f.database.QueryRow(f.ctx, `SELECT count(*) FROM review_findings WHERE run_id = $1`, run.ID).Scan(&findings))
	assert.Equal(t, 2, findings)
}

// The table itself refuses a stored head_author, whatever the Go side does.
func TestReviewRuns_TheTableRefusesAHeadAuthor(t *testing.T) {
	f := newFixture(t)
	builder := f.agent(t, f.ctx, "builder")
	_, err := f.database.Exec(f.ctx, `
		INSERT INTO review_runs (id, posted_by_kind, posted_by_id, kind, status, branch, head_sha, personas, verdict,
			incomplete, provider, model, started_at, finished_at, report)
		VALUES ($1, 'agent', $2, 'review', 'completed', 'main', 'abc1234', '{}', 'approve', false, 'p', 'm', now(), now(),
			'{"head_author": "Ada <ada@example.com>"}')`, uuid.New(), builder)
	assert.ErrorContains(t, err, "review_runs_no_head_author")
}
