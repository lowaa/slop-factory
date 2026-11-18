package reports_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/reports"
)

// now is the end of every test's period. Times are given relative to it so
// no figure depends on the clock.
var now = time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)

const (
	hour = time.Hour
	day  = 24 * time.Hour
)

type step struct {
	status string
	at     time.Duration // after the ticket's creation
}

type ticketSpec struct {
	priority string
	created  time.Duration // relative to now, negative
	steps    []step
	// noHistory leaves the ticket with no recorded change, as one last moved
	// before history was kept.
	noHistory bool
	status    string // for noHistory
}

// seedTicket inserts a ticket and replaces the creation row the trigger
// wrote with the given steps at the given times: the status a ticket ends
// in is its last step's.
func seedTicket(t *testing.T, ctx context.Context, database *db.DB, s ticketSpec) int64 {
	t.Helper()
	created := now.Add(s.created)
	status := s.status
	if !s.noHistory {
		status = s.steps[len(s.steps)-1].status
	}
	var id int64
	require.NoError(t, database.InTx(ctx, func(q db.Querier) error {
		if err := q.QueryRow(ctx, `INSERT INTO tickets (title, priority, status, created_at)
			VALUES ('t', $1, $2, $3) RETURNING id`, s.priority, status, created).Scan(&id); err != nil {
			return err
		}
		if _, err := q.Exec(ctx, `DELETE FROM ticket_status_changes WHERE ticket_id = $1`, id); err != nil {
			return err
		}
		if s.noHistory {
			return nil
		}
		for i, st := range s.steps {
			var from *string
			if i > 0 {
				from = &s.steps[i-1].status
			}
			if _, err := q.Exec(ctx, `INSERT INTO ticket_status_changes
				(ticket_id, from_status, to_status, priority, changed_at, actor_kind)
				VALUES ($1, $2, $3, $4, $5, 'system')`,
				id, from, st.status, s.priority, created.Add(st.at)); err != nil {
				return err
			}
		}
		return nil
	}))
	return id
}

type fixture struct {
	database *db.DB
	store    *reports.PostgresStore
	ctx      context.Context
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	database := dbtest.WithDB(t)
	return &fixture{
		database: database,
		store:    reports.NewPostgresStore(database, 0),
		ctx:      db.WithTenant(context.Background(), dbtest.NewTenant(t, database)),
	}
}

func (f *fixture) filter(days int, priority string) *reports.Filter {
	return &reports.Filter{From: now.AddDate(0, 0, -days), To: now, Priority: priority}
}

// knownHistory seeds the hand-built history the figures below are worked out
// from.
func (f *fixture) knownHistory(t *testing.T) {
	t.Helper()
	// Several visits to in_progress and discussion, then done.
	seedTicket(t, f.ctx, f.database, ticketSpec{priority: "normal", created: -10 * day, steps: []step{
		{"open", 0}, {"in_progress", 2 * hour}, {"discussion", 3 * hour},
		{"in_progress", 13 * hour}, {"review", 15 * hour}, {"done", 16 * hour},
	}})
	seedTicket(t, f.ctx, f.database, ticketSpec{priority: "high", created: -5 * day, steps: []step{
		{"open", 0}, {"in_progress", 1 * hour}, {"done", 5 * hour},
	}})
	// Reopened: done twice, the second time counted from creation again.
	seedTicket(t, f.ctx, f.database, ticketSpec{priority: "normal", created: -20 * day, steps: []step{
		{"open", 0}, {"in_progress", 1 * hour}, {"done", 25 * hour},
		{"open", 5 * day}, {"in_progress", 5*day + 1*hour}, {"done", 5*day + 4*hour},
	}})
	// Still in discussion at the end of the period: counted up to it.
	seedTicket(t, f.ctx, f.database, ticketSpec{priority: "normal", created: -3 * day, steps: []step{
		{"open", 0}, {"in_progress", 1 * hour}, {"discussion", 2 * hour},
	}})
	// Finished before the period: nothing of it counts.
	seedTicket(t, f.ctx, f.database, ticketSpec{priority: "normal", created: -40 * day, steps: []step{
		{"open", 0}, {"in_progress", 1 * hour}, {"done", 2 * hour},
	}})
	// Last moved before history was kept.
	seedTicket(t, f.ctx, f.database, ticketSpec{priority: "normal", created: -60 * day, noHistory: true, status: "open"})
}

func secs(d time.Duration) float64 { return d.Seconds() }

func percentilesOf(t *testing.T, p *reports.Percentiles, p50, p90 time.Duration) {
	t.Helper()
	require.NotNil(t, p)
	assert.InDelta(t, secs(p50), p.P50, 1, "p50")
	assert.InDelta(t, secs(p90), p.P90, 1, "p90")
}

func stateByName(s *reports.Summary, name string) *reports.StateFigure {
	for i := range s.States {
		if s.States[i].State == name {
			return &s.States[i]
		}
	}
	return nil
}

func TestSummaryFigures(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.knownHistory(t)

	got, err := f.store.Summary(f.ctx, f.filter(30, ""))
	require.NoError(t, err)

	// Lead times 16h, 5h, 25h and 124h: p50 is between the middle two,
	// p90 is 0.7 of the way from the third to the fourth.
	assert.EqualValues(t, 4, got.Lead.Done)
	percentilesOf(t, got.Lead.CreatedToDone, 20*hour+30*time.Minute, 94*hour+18*time.Minute)
	// First claimed to done: 14h, 4h, 24h, 123h.
	assert.EqualValues(t, 4, got.Lead.ClaimedDone)
	percentilesOf(t, got.Lead.ClaimedToDone, 19*hour, 93*hour+18*time.Minute)

	// Time in each state, every stay counted where it ended.
	open := stateByName(got, "open")
	require.NotNil(t, open)
	assert.EqualValues(t, 5, open.Stays)
	assert.Zero(t, open.Ongoing)
	percentilesOf(t, open.Duration, 1*hour, 1*hour+36*time.Minute)

	inProgress := stateByName(got, "in_progress")
	require.NotNil(t, inProgress)
	assert.EqualValues(t, 6, inProgress.Stays)
	percentilesOf(t, inProgress.Duration, 2*hour+30*time.Minute, 14*hour)

	// 10h done, plus a ticket still in discussion, counted to the period's
	// end: from 2h after its creation 3 days before now.
	discussion := stateByName(got, "discussion")
	require.NotNil(t, discussion)
	assert.EqualValues(t, 2, discussion.Stays)
	assert.EqualValues(t, 1, discussion.Ongoing)
	percentilesOf(t, discussion.Duration, 40*hour, 64*hour)
	assert.InDelta(t, secs(80*hour), discussion.TotalSeconds, 1)

	review := stateByName(got, "review")
	require.NotNil(t, review)
	percentilesOf(t, review.Duration, 1*hour, 1*hour)

	// Done and cancelled are never timed.
	assert.Nil(t, stateByName(got, "done"))

	// Weeks start on Monday, UTC.
	require.Len(t, got.LeadByWeek, 3)
	assert.Equal(t, time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC), got.LeadByWeek[0].WeekStart.UTC())
	assert.EqualValues(t, 2, got.LeadByWeek[0].Done)
	percentilesOf(t, got.LeadByWeek[0].CreatedToDone, 74*hour+30*time.Minute, 114*hour+6*time.Minute)
	assert.Equal(t, time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC), got.LeadByWeek[1].WeekStart.UTC())
	assert.Equal(t, time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC), got.LeadByWeek[2].WeekStart.UTC())

	require.Len(t, got.Throughput, 4)
	for i, want := range []time.Time{
		time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC), time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC), time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC),
	} {
		assert.Equal(t, want, got.Throughput[i].Day.UTC())
		assert.EqualValues(t, 1, got.Throughput[i].Done)
	}

	// Now: the discussion one has history, the open one has none.
	assert.Equal(t, []reports.WhereNow{
		{Status: "discussion", Count: 1},
		{Status: "open", Count: 1, WithoutHistory: 1},
	}, got.Now)

	require.NotNil(t, got.HistoryFrom)
	assert.Equal(t, now.Add(-40*day), got.HistoryFrom.UTC())
}

func TestSummaryPriorityFilter(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.knownHistory(t)

	got, err := f.store.Summary(f.ctx, f.filter(30, "high"))
	require.NoError(t, err)

	assert.EqualValues(t, 1, got.Lead.Done)
	percentilesOf(t, got.Lead.CreatedToDone, 5*hour, 5*hour)
	require.Len(t, got.States, 2)
	percentilesOf(t, stateByName(got, "open").Duration, 1*hour, 1*hour)
	percentilesOf(t, stateByName(got, "in_progress").Duration, 4*hour, 4*hour)
	assert.Empty(t, got.Now)
}

func TestSummaryShorterPeriodLeavesOutEarlierWork(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.knownHistory(t)

	got, err := f.store.Summary(f.ctx, f.filter(7, ""))
	require.NoError(t, err)

	// Only the high ticket finished in the last week.
	assert.EqualValues(t, 1, got.Lead.Done)
	assert.Len(t, got.LeadByWeek, 1)
	assert.Equal(t, 5*hour, time.Duration(got.Lead.CreatedToDone.P50)*time.Second)
}

func TestSummaryEmptyIsAbsentNotZero(t *testing.T) {
	t.Parallel()
	f := newFixture(t)

	got, err := f.store.Summary(f.ctx, f.filter(30, ""))
	require.NoError(t, err)

	assert.Nil(t, got.HistoryFrom)
	assert.Nil(t, got.Lead.CreatedToDone)
	assert.Nil(t, got.Lead.ClaimedToDone)
	assert.Empty(t, got.States)
	assert.Empty(t, got.Throughput)
	assert.Empty(t, got.Now)
}

func TestWaitingOrdersLongestFirstAndPages(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	older := seedTicket(t, f.ctx, f.database, ticketSpec{priority: "normal", created: -2 * day, steps: []step{{"open", 0}}})
	newer := seedTicket(t, f.ctx, f.database, ticketSpec{priority: "high", created: -1 * day, steps: []step{{"open", 0}}})
	seedTicket(t, f.ctx, f.database, ticketSpec{priority: "normal", created: -9 * day, noHistory: true, status: "open"})
	seedTicket(t, f.ctx, f.database, ticketSpec{priority: "normal", created: -1 * day, steps: []step{{"open", 0}, {"in_progress", hour}}})

	page, err := f.store.Waiting(f.ctx, "open", "", 1, 0)
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, older, page.Items[0].TicketID)
	assert.Equal(t, now.Add(-2*day), page.Items[0].Since.UTC())
	// The one with no recorded time is not in the list or the total.
	assert.EqualValues(t, 2, page.Total)

	page, err = f.store.Waiting(f.ctx, "open", "", 1, 1)
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, newer, page.Items[0].TicketID)

	page, err = f.store.Waiting(f.ctx, "open", "high", 10, 0)
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, newer, page.Items[0].TicketID)
}

// Another tenant's changes and tickets never reach a report: row level
// security hides them, and the owner connection proves they are there.
func TestTenantIsolation(t *testing.T) {
	t.Parallel()
	app, owner := dbtest.WithOwnerDB(t)
	tenantA := db.WithTenant(context.Background(), dbtest.NewTenant(t, app))
	tenantB := db.WithTenant(context.Background(), dbtest.NewTenant(t, app))
	store := reports.NewPostgresStore(app, 0)

	for _, ctx := range []context.Context{tenantA, tenantB} {
		seedTicket(t, ctx, app, ticketSpec{priority: "normal", created: -2 * day, steps: []step{
			{"open", 0}, {"in_progress", hour}, {"done", 3 * hour},
		}})
	}
	// Only B has this one, waiting and in flight.
	seedTicket(t, tenantB, app, ticketSpec{priority: "urgent", created: -1 * day, steps: []step{{"open", 0}}})

	var everyone int
	require.NoError(t, owner.Global().QueryRow(context.Background(), `SELECT count(*) FROM ticket_status_changes`).Scan(&everyone))
	require.Equal(t, 7, everyone)

	f := &reports.Filter{From: now.AddDate(0, 0, -30), To: now}
	got, err := store.Summary(tenantA, f)
	require.NoError(t, err)
	assert.EqualValues(t, 1, got.Lead.Done)
	assert.Empty(t, got.Now)
	require.Len(t, got.Throughput, 1)
	assert.EqualValues(t, 1, got.Throughput[0].Done)
	for _, s := range got.States {
		assert.EqualValues(t, 1, s.Stays, s.State)
	}

	waiting, err := store.Waiting(tenantA, "open", "", 10, 0)
	require.NoError(t, err)
	assert.Empty(t, waiting.Items)
	assert.Zero(t, waiting.Total)

	gotB, err := store.Summary(tenantB, f)
	require.NoError(t, err)
	assert.EqualValues(t, 1, gotB.Lead.Done)
	assert.Equal(t, []reports.WhereNow{{Status: "open", Count: 1}}, gotB.Now)
}

// Not parallel: a 1ms deadline is also a socket deadline, so a loaded
// machine can fail the connection setup before the statement ever runs.
// Sequential tests finish before any parallel one resumes.
func TestStatementTimeoutFailsClearly(t *testing.T) {
	database := dbtest.WithDB(t)
	ctx := db.WithTenant(context.Background(), dbtest.NewTenant(t, database))
	// 1ms cannot run a report over any history.
	store := reports.NewPostgresStore(database, time.Millisecond)
	for i := 0; i < 300; i++ {
		seedTicket(t, ctx, database, ticketSpec{priority: "normal", created: -2 * day, steps: []step{
			{"open", 0}, {"in_progress", hour}, {"done", 3 * hour},
		}})
	}

	_, err := store.Summary(ctx, &reports.Filter{From: now.AddDate(0, 0, -30), To: now})
	assert.ErrorIs(t, err, reports.ErrTooSlow)
}

// parkedHistory seeds a ticket parked for 30 days before it was done, a
// ticket parked now, and one that was never parked.
func (f *fixture) parkedHistory(t *testing.T) {
	t.Helper()
	// Claimed, parked 30 days, claimed again, done: 35 days since creation,
	// 34 since the first claim.
	seedTicket(t, f.ctx, f.database, ticketSpec{priority: "normal", created: -40 * day, steps: []step{
		{"open", 0}, {"in_progress", 1 * day}, {"parked", 2 * day},
		{"in_progress", 32 * day}, {"done", 35 * day},
	}})
	// Parked before it was ever claimed.
	seedTicket(t, f.ctx, f.database, ticketSpec{priority: "normal", created: -20 * day, steps: []step{
		{"open", 0}, {"parked", 1 * day}, {"open", 11 * day}, {"in_progress", 12 * day}, {"done", 13 * day},
	}})
	seedTicket(t, f.ctx, f.database, ticketSpec{priority: "high", created: -5 * day, steps: []step{{"open", 0}, {"parked", 1 * day}}})
}

func TestSummaryLeavesParkedOutByDefault(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.parkedHistory(t)

	off, err := f.store.Summary(f.ctx, f.filter(30, ""))
	require.NoError(t, err)
	// Both done tickets still count, without their parked stays: 5 days and 3
	// days from creation, 4 days and 1 day from the first claim.
	assert.EqualValues(t, 2, off.Lead.Done)
	percentilesOf(t, off.Lead.CreatedToDone, 4*day, 4*day+19*hour+12*time.Minute)
	assert.EqualValues(t, 2, off.Lead.ClaimedDone)
	percentilesOf(t, off.Lead.ClaimedToDone, 2*day+12*hour, 3*day+16*hour+48*time.Minute)
	assert.Nil(t, stateByName(off, "parked"))
	assert.Empty(t, off.Now)
	assert.EqualValues(t, 1, off.ParkedNotShown)
	assert.False(t, off.IncludeParked)

	f2 := f.filter(30, "")
	f2.IncludeParked = true
	on, err := f.store.Summary(f.ctx, f2)
	require.NoError(t, err)
	// Today's numbers: 35 and 13 days from creation, 34 and 1 from the claim.
	percentilesOf(t, on.Lead.CreatedToDone, 24*day, 32*day+19*hour+12*time.Minute)
	percentilesOf(t, on.Lead.ClaimedToDone, 17*day+12*hour, 30*day+16*hour+48*time.Minute)
	parked := stateByName(on, "parked")
	require.NotNil(t, parked)
	assert.EqualValues(t, 3, parked.Stays)
	assert.EqualValues(t, 1, parked.Ongoing)
	assert.Equal(t, []reports.WhereNow{{Status: "parked", Count: 1}}, on.Now)
	assert.Zero(t, on.ParkedNotShown)

	// Throughput does not depend on it.
	assert.Equal(t, on.Throughput, off.Throughput)
}

func TestParkedTimeIsInNoTotalWhenOff(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.parkedHistory(t)
	got, err := f.store.Summary(f.ctx, f.filter(30, ""))
	require.NoError(t, err)
	var total float64
	for _, s := range got.States {
		total += s.TotalSeconds
	}
	f2 := f.filter(30, "")
	f2.IncludeParked = true
	with, err := f.store.Summary(f.ctx, f2)
	require.NoError(t, err)
	var all float64
	for _, s := range with.States {
		all += s.TotalSeconds
	}
	assert.Greater(t, all, total)
	assert.InDelta(t, all-total, stateByName(with, "parked").TotalSeconds, 1)
}

// A hand-back and re-queue are separate stays in in_merge_queue, and a
// ticket queued now is waiting in it.
func TestSummaryInMergeQueueStaysAreSeparate(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	seedTicket(t, f.ctx, f.database, ticketSpec{priority: "normal", created: -5 * day, steps: []step{
		{"open", 0}, {"in_progress", 1 * hour}, {"in_merge_queue", 3 * hour},
		{"open", 4 * hour}, {"in_progress", 5 * hour}, {"in_merge_queue", 6 * hour},
		{"done", 6*hour + 30*time.Minute},
	}})
	queuedNow := seedTicket(t, f.ctx, f.database, ticketSpec{priority: "normal", created: -2 * day, steps: []step{
		{"open", 0}, {"in_progress", 1 * hour}, {"in_merge_queue", 2 * hour},
	}})

	got, err := f.store.Summary(f.ctx, f.filter(30, ""))
	require.NoError(t, err)

	queue := stateByName(got, "in_merge_queue")
	require.NotNil(t, queue)
	assert.EqualValues(t, 3, queue.Stays)
	assert.EqualValues(t, 1, queue.Ongoing)
	// 1h and 30m done, plus the ticket still queued, counted to the period's
	// end: 2h after its creation 2 days before now.
	assert.InDelta(t, secs(1*hour+30*time.Minute+46*hour), queue.TotalSeconds, 1)
	var nowCount int64
	for _, n := range got.Now {
		if n.Status == "in_merge_queue" {
			nowCount = n.Count
		}
	}
	assert.EqualValues(t, 1, nowCount)

	page, err := f.store.Waiting(f.ctx, "in_merge_queue", "", 10, 0)
	require.NoError(t, err)
	assert.EqualValues(t, 1, page.Total)
	require.Len(t, page.Items, 1)
	assert.Equal(t, queuedNow, page.Items[0].TicketID)
}
