package attemptsignals_test

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/attemptsignals"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/attemptsignals/usage"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

type fakeSource struct {
	in    *attemptsignals.Inputs
	err   error
	calls int
}

func (f *fakeSource) Inputs(context.Context, ticket.AttemptRef) (*attemptsignals.Inputs, error) {
	f.calls++
	return f.in, f.err
}

type fixture struct {
	t      *testing.T
	db     *db.DB
	ctx    context.Context
	source *fakeSource
	clock  time.Time
	svc    *attemptsignals.Service
	ref    ticket.AttemptRef
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	database := dbtest.WithDB(t)
	f := &fixture{t: t, db: database, source: &fakeSource{in: &attemptsignals.Inputs{}}, clock: now,
		ctx: db.WithTenant(context.Background(), dbtest.NewTenant(t, database))}
	var id int64
	require.NoError(t, database.QueryRow(f.ctx, `INSERT INTO tickets (title, status) VALUES ('t', 'open') RETURNING id`).Scan(&id))
	f.ref = ticket.AttemptRef{TicketID: id, Number: 1}
	f.svc = attemptsignals.NewService(attemptsignals.NewStore(database), usage.NewStore(database), f.source,
		slog.NewTextHandler(testWriter{t}, nil), &attemptsignals.Options{StaleAfter: time.Minute, Now: func() time.Time { return f.clock }})
	return f
}

type testWriter struct{ t *testing.T }

func (w testWriter) Write(p []byte) (int, error) { w.t.Log(string(p)); return len(p), nil }

func (f *fixture) get() *ticket.AttemptSignalsBody {
	f.t.Helper()
	got, err := f.svc.AttemptSignals(f.ctx, []ticket.AttemptRef{f.ref})
	require.NoError(f.t, err)
	return got[f.ref]
}

func TestAnAttemptWithNothingMeasuredIsStoredAsUnknownNotZero(t *testing.T) {
	f := newFixture(t)

	b := f.get()

	require.NotNil(t, b)
	assert.False(t, b.Size.Known)
	assert.False(t, b.Cheating.TestsDeleted.Known)
	assert.Equal(t, "unknown", b.Checks.Passed)
	assert.Nil(t, b.Time.Runs, "an attempt that never ran has no run count")

	var testsDeleted, linesAdded, runs *int64
	require.NoError(t, f.db.QueryRow(f.ctx, `SELECT tests_deleted, lines_added, runs FROM attempt_signals WHERE ticket_id = $1`, f.ref.TicketID).
		Scan(&testsDeleted, &linesAdded, &runs))
	assert.Nil(t, testsDeleted)
	assert.Nil(t, linesAdded)
	assert.Nil(t, runs)
}

func TestSignalsAreComputedEndToEndFromDiffAndUsage(t *testing.T) {
	f := newFixture(t)
	f.source.in = &attemptsignals.Inputs{HeadSHA: "h1", Diff: fixtureDiff, Scopes: []string{"pkg/"}}
	_, err := f.db.Exec(f.ctx, `INSERT INTO agents (id, name) VALUES ('11111111-1111-1111-1111-111111111111', 'a')`)
	require.NoError(t, err)
	_, err = f.db.Exec(f.ctx, `INSERT INTO leases (ticket_id, attempt_number, agent_id, acquired_at, expires_at, released_at, release_reason)
		VALUES ($1, 1, '11111111-1111-1111-1111-111111111111', now() - interval '2 hours', now() - interval '1 hour', now() - interval '1 hour', 'x')`, f.ref.TicketID)
	require.NoError(t, err)

	b := f.get()

	assert.Equal(t, []string{"pkg/gone_test.go"}, b.Cheating.TestsDeleted.Files)
	assert.Equal(t, []string{"elsewhere/x.go"}, b.Cheating.OutOfScope.Files)
	assert.Equal(t, 3, b.Size.LinesAdded)
	require.NotNil(t, b.Time.Runs)
	assert.EqualValues(t, 1, *b.Time.Runs)
	require.NotNil(t, b.Autonomy.Discussions)
	assert.EqualValues(t, 0, *b.Autonomy.Discussions, "an attempt that ran and never asked has zero discussions")

	var deleted, skips, asserts, outOfScope, added, removed, files, runs int
	var checks string
	var reruns *int
	require.NoError(t, f.db.QueryRow(f.ctx, `SELECT tests_deleted, skips_added, assertions_removed, out_of_scope_files,
		lines_added, lines_removed, files_touched, runs, checks_passed, reruns FROM attempt_signals WHERE ticket_id = $1`, f.ref.TicketID).
		Scan(&deleted, &skips, &asserts, &outOfScope, &added, &removed, &files, &runs, &checks, &reruns))
	assert.Equal(t, []int{1, 0, 0, 1, 3, 4, 3, 1}, []int{deleted, skips, asserts, outOfScope, added, removed, files, runs})
	assert.Equal(t, "unknown", checks)
	assert.Nil(t, reruns)
}

func TestARunningAttemptIsRecomputedOnlyWhenStale(t *testing.T) {
	f := newFixture(t)
	f.get()
	f.get()
	assert.Equal(t, 1, f.source.calls, "a fresh row is served as stored")

	f.clock = f.clock.Add(2 * time.Minute)
	f.source.in = &attemptsignals.Inputs{Diff: fixtureDiff}
	b := f.get()

	assert.Equal(t, 2, f.source.calls)
	assert.True(t, b.Size.Known)
}

func TestAFinalRowNeverMoves(t *testing.T) {
	f := newFixture(t)
	f.source.in = &attemptsignals.Inputs{Final: true, Diff: fixtureDiff}
	f.get()

	f.clock = f.clock.Add(time.Hour)
	f.source.in = &attemptsignals.Inputs{Final: true}
	got, err := f.svc.Refresh(f.ctx, f.ref)

	require.NoError(t, err)
	assert.True(t, got.Final)
	assert.True(t, got.Size.Known, "the closed attempt's figures were not overwritten by a later, emptier computation")
	calls := f.source.calls
	f.get()
	assert.Equal(t, calls, f.source.calls, "a final row is not recomputed on read")
}

func TestAFailingSourceKeepsWhatWasStoredAndNeverFailsTheRead(t *testing.T) {
	f := newFixture(t)
	f.source.in = &attemptsignals.Inputs{Diff: fixtureDiff}
	f.get()

	f.clock = f.clock.Add(time.Hour)
	f.source.err = errors.New("host down")
	b := f.get()

	require.NotNil(t, b)
	assert.True(t, b.Size.Known)
}

func TestAnAttemptNeverComputedHasNoSignals(t *testing.T) {
	f := newFixture(t)
	f.source.err = errors.New("host down")

	assert.Nil(t, f.get(), "null on the wire, which says nothing about the work")
}

func TestSignalsOfAnotherTenantAreNotRead(t *testing.T) {
	f := newFixture(t)
	f.get()
	other := db.WithTenant(context.Background(), dbtest.NewTenant(t, f.db))

	got, err := attemptsignals.NewStore(f.db).Get(other, []ticket.AttemptRef{f.ref})

	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestAFailingHostIsNotCalledAgainOnEveryRead(t *testing.T) {
	f := newFixture(t)
	f.source.err = errors.New("host down")

	f.get()
	f.get()
	f.get()
	assert.Equal(t, 1, f.source.calls, "polling must not multiply code host calls")

	f.clock = f.clock.Add(2 * time.Minute)
	f.get()
	assert.Equal(t, 2, f.source.calls)
}

func TestAnOlderRefreshDoesNotReplaceANewerOne(t *testing.T) {
	f := newFixture(t)
	f.source.in = &attemptsignals.Inputs{Diff: fixtureDiff}
	f.clock = now.Add(time.Hour)
	_, err := f.svc.Refresh(f.ctx, f.ref)
	require.NoError(t, err)

	f.clock = now // a refresh that started earlier finishes last
	f.source.in = &attemptsignals.Inputs{}
	got, err := f.svc.Refresh(f.ctx, f.ref)

	require.NoError(t, err)
	assert.True(t, got.Size.Known)
	assert.Equal(t, now.Add(time.Hour), got.ComputedAt)
}
