package landing_test

import (
	"github.com/lowaa/slop-factory/slop-haus/server/internal/landing"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"context"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
)

func landingRow(ticket int64, head string) *landing.LandingRow {
	return &landing.LandingRow{TicketID: ticket, AttemptNumber: 1, ChangeURL: "https://example.invalid/pr/1", HeadSHA: head}
}

func positions(t *testing.T, f *storeFixture, s *landing.LandingStore) []int64 {
	t.Helper()
	rows, err := s.Queue(f.ctx)
	require.NoError(t, err)
	var ids []int64
	for _, r := range rows {
		ids = append(ids, r.TicketID)
	}
	return ids
}

func TestLandingQueueIsFIFOAndEnqueueIsIdempotent(t *testing.T) {
	f := newStoreFixture(t)
	s := landing.NewLandingStore(f.database)
	a, b := f.ticket(t), f.ticket(t)
	require.NoError(t, s.Enqueue(f.ctx, landingRow(a, "h1")))
	require.NoError(t, s.Enqueue(f.ctx, landingRow(b, "h1")))
	require.NoError(t, s.Enqueue(f.ctx, landingRow(a, "h2"))) // repeated finish call

	assert.Equal(t, []int64{a, b}, positions(t, f, s))
	got, err := s.Get(f.ctx, a)
	require.NoError(t, err)
	assert.Equal(t, 1, got.Position)
	assert.Equal(t, "h1", got.HeadSHA, "a repeat while landing changes nothing")
	got, _ = s.Get(f.ctx, b)
	assert.Equal(t, 2, got.Position)
}

func TestLandingStepsAndHandBack(t *testing.T) {
	f := newStoreFixture(t)
	s := landing.NewLandingStore(f.database)
	a := f.ticket(t)
	require.NoError(t, s.Enqueue(f.ctx, landingRow(a, "h1")))
	for _, st := range []landing.Step{landing.MergingMain, landing.WaitingChecks, landing.Merging} {
		require.NoError(t, s.SetStep(f.ctx, a, st))
		got, err := s.Get(f.ctx, a)
		require.NoError(t, err)
		assert.Equal(t, st, got.Step)
	}
	assert.ErrorIs(t, s.SetStep(f.ctx, a, "bogus"), landing.ErrInvalid)

	require.NoError(t, s.HandBack(f.ctx, a, landing.ChecksRed, false))
	got, err := s.Get(f.ctx, a)
	require.NoError(t, err)
	assert.Equal(t, landing.BackWithAgent, got.State)
	assert.Equal(t, landing.ChecksRed, got.BackReason)
	assert.Empty(t, got.Step)
	assert.Zero(t, got.Position)

	assert.ErrorIs(t, s.SetStep(f.ctx, a, landing.Merging), landing.ErrNotLanding)
	assert.ErrorIs(t, s.HandBack(f.ctx, a, landing.ChecksRed, false), landing.ErrNotLanding)
	assert.ErrorIs(t, s.HandBack(f.ctx, a, landing.Merged, false), landing.ErrInvalid)
	assert.ErrorIs(t, s.SetStep(f.ctx, f.ticket(t), landing.Merging), landing.ErrNoLanding)
}

func TestHandedBackTicketRejoinsAtBackUnlessServerFault(t *testing.T) {
	f := newStoreFixture(t)
	s := landing.NewLandingStore(f.database)
	a, b, c := f.ticket(t), f.ticket(t), f.ticket(t)
	for _, id := range []int64{a, b, c} {
		require.NoError(t, s.Enqueue(f.ctx, landingRow(id, "h1")))
	}
	require.NoError(t, s.HandBack(f.ctx, a, landing.ChecksRed, false))
	require.NoError(t, s.HandBack(f.ctx, b, landing.CodeHostError, true))
	assert.Equal(t, []int64{c, a, b}, positions(t, f, s), "landing first, then those handed back")

	cRow, err := s.Get(f.ctx, c)
	require.NoError(t, err)
	assert.Equal(t, 1, cRow.Position, "handed-back rows leave no hole in the positions")

	f.attempt(t, a, 2)
	f.attempt(t, b, 2)
	for _, id := range []int64{a, b} {
		next := landingRow(id, "h2")
		next.AttemptNumber, next.ChangeURL = 2, "https://example.invalid/pr/2"
		require.NoError(t, s.Enqueue(f.ctx, next))
	}
	// b kept its place ahead of c; a went to the back.
	assert.Equal(t, []int64{b, c, a}, positions(t, f, s))
	rows, err := s.Queue(f.ctx)
	require.NoError(t, err)
	for i, r := range rows {
		assert.Equal(t, i+1, r.Position)
		if r.TicketID != c {
			assert.Equal(t, 2, r.AttemptNumber)
			assert.Equal(t, "https://example.invalid/pr/2", r.ChangeURL)
			assert.Equal(t, "h2", r.HeadSHA)
		}
	}
	got, err := s.Get(f.ctx, b)
	require.NoError(t, err)
	assert.Equal(t, landing.Landing, got.State)
	assert.Empty(t, got.BackReason)
	assert.False(t, got.KeepsPlace)
}

func TestRemoveAndTenantIsolation(t *testing.T) {
	f := newStoreFixture(t)
	s := landing.NewLandingStore(f.database)
	a := f.ticket(t)
	require.NoError(t, s.Enqueue(f.ctx, landingRow(a, "h1")))

	other := db.WithTenant(context.Background(), dbtest.NewTenant(t, f.database))
	rows, err := s.Queue(other)
	require.NoError(t, err)
	assert.Empty(t, rows)

	require.NoError(t, s.Remove(f.ctx, a, landing.PhaseMerged))
	require.NoError(t, s.Remove(f.ctx, a, landing.PhaseMerged))
	_, err = s.Get(f.ctx, a)
	assert.ErrorIs(t, err, landing.ErrNoLanding)
}

func TestLandingTableRefusesInconsistentRows(t *testing.T) {
	f := newStoreFixture(t)
	a := f.ticket(t)
	for name, sql := range map[string]string{
		"back without a reason":     `INSERT INTO landings (ticket_id, attempt_number, change_url, head_sha, queue_state) VALUES ($1, 1, 'u', 'h', 'back_with_agent')`,
		"reason while landing":      `INSERT INTO landings (ticket_id, attempt_number, change_url, head_sha, back_reason) VALUES ($1, 1, 'u', 'h', 'checks_red')`,
		"keeps place while landing": `INSERT INTO landings (ticket_id, attempt_number, change_url, head_sha, keeps_place) VALUES ($1, 1, 'u', 'h', true)`,
		"unknown state":             `INSERT INTO landings (ticket_id, attempt_number, change_url, head_sha, queue_state) VALUES ($1, 1, 'u', 'h', 'bogus')`,
	} {
		_, err := f.database.Exec(f.ctx, sql, a)
		assert.Error(t, err, name)
	}
}

func TestNewOutcomeCodesAreAcceptedByTheTable(t *testing.T) {
	f := newStoreFixture(t)
	a := f.ticket(t)
	for _, o := range []landing.Outcome{landing.MergeMainConflict, landing.AlreadyMerged, landing.BatchVoided, landing.BatchInteraction} {
		_, err := f.database.Exec(f.ctx, `INSERT INTO server_merge_outcomes (ticket_id, attempt_number, change_url, head_sha, outcome) VALUES ($1, 1, 'u', 'h', $2)`, a, string(o))
		assert.NoError(t, err, o)
	}
}

func TestBatchColumnsAndConstraints(t *testing.T) {
	f := newStoreFixture(t)
	a := f.ticket(t)
	var batch int64
	require.NoError(t, f.database.QueryRow(f.ctx, `INSERT INTO batches (base_sha, branch, member_ticket_ids, state) VALUES ('b', 'batch-1', ARRAY[$1::bigint], 'building') RETURNING id`, a).Scan(&batch))

	_, err := f.database.Exec(f.ctx, `INSERT INTO server_merge_outcomes (ticket_id, attempt_number, change_url, head_sha, outcome, batch_id, batch_size, batch_position) VALUES ($1, 1, 'u', 'h', 'merged', $2, 2, 1)`, a, batch)
	assert.NoError(t, err)

	for name, sql := range map[string]string{
		"no members":            `INSERT INTO batches (base_sha, branch, member_ticket_ids, state) VALUES ('b', 'x', '{}', 'building')`,
		"unknown batch state":   `INSERT INTO batches (base_sha, branch, member_ticket_ids, state) VALUES ('b', 'x', '{1}', 'bogus')`,
		"zero batch size":       `INSERT INTO server_merge_outcomes (ticket_id, attempt_number, change_url, head_sha, outcome, batch_size) VALUES ($1, 1, 'u', 'h', 'merged', 0)`,
		"landing zero position": `INSERT INTO landings (ticket_id, attempt_number, change_url, head_sha, batch_position) VALUES ($1, 1, 'u', 'h', 0)`,
	} {
		_, err := f.database.Exec(f.ctx, sql, a)
		assert.Error(t, err, name)
	}

	_, err = f.database.Exec(f.ctx, `INSERT INTO landings (ticket_id, attempt_number, change_url, head_sha, queue_state, batch_id, batch_position) VALUES ($1, 1, 'u', 'h', 'batch_testing', $2, 1)`, a, batch)
	require.NoError(t, err)
	_, err = f.database.Exec(f.ctx, `DELETE FROM batches WHERE id = $1`, batch)
	require.NoError(t, err)
	var n int
	require.NoError(t, f.database.QueryRow(f.ctx, `SELECT count(*) FROM landings WHERE ticket_id = $1 AND batch_id IS NULL`, a).Scan(&n))
	assert.Equal(t, 1, n, "a deleted batch leaves the landing unbatched")
}

func TestConcurrentFirstEnqueuesAllSucceed(t *testing.T) {
	f := newStoreFixture(t)
	s := landing.NewLandingStore(f.database)
	a := f.ticket(t)
	const n = 8
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		go func() { errs <- s.Enqueue(f.ctx, landingRow(a, "h1")) }()
	}
	for i := 0; i < n; i++ {
		assert.NoError(t, <-errs)
	}
	assert.Equal(t, []int64{a}, positions(t, f, s))
}

func (f *storeFixture) attempt(t *testing.T, ticket int64, number int) {
	t.Helper()
	_, err := f.database.Exec(f.ctx, `INSERT INTO attempts (ticket_id, number, status) VALUES ($1, $2, 'queued')`, ticket, number)
	require.NoError(t, err)
}

// atPhase puts a ticket's row in a serial or queued phase through the legal moves.
func (f *storeFixture) atPhase(t *testing.T, s *landing.LandingStore, p landing.Phase) int64 {
	t.Helper()
	id := f.ticket(t)
	require.NoError(t, s.Enqueue(f.ctx, landingRow(id, "h1")))
	switch p {
	case landing.PhaseQueued:
	case landing.PhaseSerialMergingMain:
		require.NoError(t, s.SetStep(f.ctx, id, landing.MergingMain))
	case landing.PhaseSerialWaitingChecks:
		require.NoError(t, s.SetStep(f.ctx, id, landing.MergingMain))
		require.NoError(t, s.SetStep(f.ctx, id, landing.WaitingChecks))
	case landing.PhaseSerialMerging:
		require.NoError(t, s.SetStep(f.ctx, id, landing.Merging))
	case landing.PhaseBackWithAgent:
		require.NoError(t, s.HandBack(f.ctx, id, landing.ChecksRed, false))
	default:
		t.Fatalf("no setup for %s", p)
	}
	return id
}

var storedPhases = []landing.Phase{
	landing.PhaseQueued, landing.PhaseSerialMergingMain, landing.PhaseSerialWaitingChecks,
	landing.PhaseSerialMerging, landing.PhaseBackWithAgent,
}

func phaseOfRow(t *testing.T, f *storeFixture, id int64) string {
	t.Helper()
	var p string
	require.NoError(t, f.database.QueryRow(f.ctx, `SELECT queue_state FROM landings WHERE ticket_id = $1`, id).Scan(&p))
	return p
}

func TestSetStepFollowsTheTransitionTableFromEveryPhase(t *testing.T) {
	f := newStoreFixture(t)
	s := landing.NewLandingStore(f.database)
	targets := map[landing.Step]landing.Phase{
		landing.MergingMain: landing.PhaseSerialMergingMain, landing.WaitingChecks: landing.PhaseSerialWaitingChecks, landing.Merging: landing.PhaseSerialMerging,
	}
	for _, from := range storedPhases {
		for step, to := range targets {
			id := f.atPhase(t, s, from)
			err := s.SetStep(f.ctx, id, step)
			name := string(from) + " -> " + string(step)
			switch {
			case from == landing.PhaseBackWithAgent:
				assert.ErrorIs(t, err, landing.ErrNotLanding, name)
				assert.Equal(t, string(from), phaseOfRow(t, f, id), name)
			case landing.Allowed(from, to):
				assert.NoError(t, err, name)
				assert.Equal(t, string(to), phaseOfRow(t, f, id), name)
			default:
				assert.ErrorIs(t, err, landing.ErrIllegalTransition, name)
				assert.Equal(t, string(from), phaseOfRow(t, f, id), "an illegal move changes nothing: "+name)
			}
		}
	}
}

func TestHandBackIsLegalFromEveryPhaseButBackWithAgent(t *testing.T) {
	f := newStoreFixture(t)
	s := landing.NewLandingStore(f.database)
	for _, from := range storedPhases {
		id := f.atPhase(t, s, from)
		err := s.HandBack(f.ctx, id, landing.ChecksRed, false)
		if from == landing.PhaseBackWithAgent {
			assert.ErrorIs(t, err, landing.ErrNotLanding, string(from))
			continue
		}
		assert.NoError(t, err, string(from))
		assert.Equal(t, "back_with_agent", phaseOfRow(t, f, id), string(from))
	}
}

func TestEnqueueRefinishesOnlyAHandedBackRow(t *testing.T) {
	f := newStoreFixture(t)
	s := landing.NewLandingStore(f.database)
	for _, from := range storedPhases {
		id := f.atPhase(t, s, from)
		next := landingRow(id, "h2")
		require.NoError(t, s.Enqueue(f.ctx, next), string(from))
		got, err := s.Get(f.ctx, id)
		require.NoError(t, err)
		if from == landing.PhaseBackWithAgent {
			assert.Equal(t, "h2", got.HeadSHA)
			assert.Equal(t, "queued", phaseOfRow(t, f, id))
		} else {
			assert.Equal(t, "h1", got.HeadSHA, "a row in %s is left alone", from)
			assert.Equal(t, string(from), phaseOfRow(t, f, id))
		}
	}
}

func TestRemoveFollowsTheTransitionTableFromEveryPhase(t *testing.T) {
	f := newStoreFixture(t)
	s := landing.NewLandingStore(f.database)
	for _, from := range storedPhases {
		for _, to := range []landing.Phase{landing.PhaseMerged, landing.PhaseDequeued} {
			id := f.atPhase(t, s, from)
			err := s.Remove(f.ctx, id, to)
			name := string(from) + " -> " + string(to)
			if landing.Allowed(from, to) {
				assert.NoError(t, err, name)
				_, err = s.Get(f.ctx, id)
				assert.ErrorIs(t, err, landing.ErrNoLanding, name)
			} else {
				assert.ErrorIs(t, err, landing.ErrIllegalTransition, name)
				assert.Equal(t, string(from), phaseOfRow(t, f, id), "the row stays: "+name)
			}
		}
	}
	assert.ErrorIs(t, s.Remove(f.ctx, f.atPhase(t, s, landing.PhaseQueued), landing.PhaseQueued), landing.ErrIllegalTransition, "not a terminal phase")
}

func TestAMoveFromAStalePhaseIsATypedConflictAndWritesNothing(t *testing.T) {
	f := newStoreFixture(t)
	s := landing.NewLandingStore(f.database)
	id := f.atPhase(t, s, landing.PhaseQueued)

	err := landing.MoveForTest(f.ctx, f.database, id, landing.PhaseSerialMerging, landing.PhaseSerialMergingMain, landing.CauseMainMoved)
	var conflict *landing.ConflictError
	require.ErrorAs(t, err, &conflict)
	assert.ErrorIs(t, err, landing.ErrConflict)
	assert.Equal(t, landing.PhaseQueued, conflict.Actual)
	assert.Equal(t, "queued", phaseOfRow(t, f, id))

	err = landing.MoveForTest(f.ctx, f.database, id+1000, landing.PhaseQueued, landing.PhaseSerialMergingMain, landing.CauseSerialClaimed)
	require.ErrorAs(t, err, &conflict)
	assert.Empty(t, conflict.Actual, "no row at all")

	assert.ErrorIs(t, landing.MoveForTest(f.ctx, f.database, id, landing.PhaseQueued, landing.PhaseSerialMergingMain, landing.CauseMerged), landing.ErrIllegalTransition, "wrong cause")
}

func TestTwoWritersFromTheSamePhaseOneWinsAndTheOtherIsToldSo(t *testing.T) {
	f := newStoreFixture(t)
	s := landing.NewLandingStore(f.database)
	id := f.atPhase(t, s, landing.PhaseSerialMergingMain)

	reasons := []landing.Outcome{landing.ChecksRed, landing.MainMoved}
	errs := make([]error, len(reasons))
	var wg sync.WaitGroup
	locked, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unlock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unlock()
	holderDone := make(chan error, 1)
	// A transaction holds the row, so both writers read the original phase and
	// then queue on their UPDATE: the overlap is forced, not hoped for.
	go func() {
		holderDone <- f.database.InTx(f.ctx, func(q db.Querier) error {
			if _, err := q.Exec(f.ctx, `SELECT 1 FROM landings WHERE ticket_id = $1 FOR UPDATE`, id); err != nil {
				return err
			}
			close(locked)
			<-release
			return nil
		})
	}()
	<-locked
	for i := range reasons {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = s.HandBack(f.ctx, id, reasons[i], false)
		}()
	}
	require.Eventually(t, func() bool {
		var waiting int
		require.NoError(t, f.database.QueryRow(f.ctx, `SELECT count(*) FROM pg_stat_activity WHERE wait_event_type = 'Lock'`).Scan(&waiting))
		return waiting == len(reasons)
	}, 30*time.Second, 10*time.Millisecond, "both writers are queued on the row")
	unlock()
	require.NoError(t, <-holderDone)
	wg.Wait()

	winner := -1
	for i, err := range errs {
		if err == nil {
			require.Equal(t, -1, winner, "only one writer wins")
			winner = i
			continue
		}
		assert.ErrorIs(t, err, landing.ErrNotLanding)
	}
	require.NotEqual(t, -1, winner, "one writer wins")
	got, err := s.Get(f.ctx, id)
	require.NoError(t, err)
	assert.Equal(t, reasons[winner], got.BackReason, "the loser did not overwrite the winner")
}
