package reports_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/reports"
)

func hold(queued *time.Duration, acquired time.Duration, released *time.Duration) reports.LockHold {
	h := reports.LockHold{Name: "integration", TicketID: 1, AcquiredAt: at(acquired)}
	if queued != nil {
		h.QueuedAt = ptr(*queued)
	}
	if released != nil {
		h.ReleasedAt = ptr(*released)
	}
	return h
}

// Period is the last three days, so a day's holds are those acquired on it.
func lockInput(holds ...reports.LockHold) *reports.LockHistoryInput {
	return &reports.LockHistoryInput{From: at(-3 * day), To: now, Holds: holds}
}

func TestComputeLockHistory_PercentilesInterpolateAndOpenHoldsAreNotZero(t *testing.T) {
	t.Parallel()
	// Holds of 10, 20, 30 and 40 minutes acquired the same day, then one
	// still held.
	base := -10 * hour
	var holds []reports.LockHold
	for i := 1; i <= 4; i++ {
		start := base + time.Duration(i)*hour
		holds = append(holds, hold(nil, start, dur(start+time.Duration(i*10)*time.Minute)))
	}
	holds = append(holds, hold(nil, -1*hour, nil))

	got := reports.ComputeLockHistory(lockInput(holds...))

	assert.EqualValues(t, 5, got.Total.Holds)
	assert.EqualValues(t, 1, got.Total.Ongoing)
	require.NotNil(t, got.Total.Hold)
	assert.Equal(t, (25 * time.Minute).Seconds(), got.Total.Hold.P50)
	assert.InDelta(t, (38.5 * float64(time.Minute) / float64(time.Second)), got.Total.Hold.P95, 1e-9)
	assert.Nil(t, got.Total.Wait, "nobody queued")
}

func TestComputeLockHistory_WaitIsQueuedToAcquiredPerDay(t *testing.T) {
	t.Parallel()
	day1, day2 := -50*hour, -10*hour
	got := reports.ComputeLockHistory(lockInput(
		hold(dur(day1-20*time.Minute), day1, dur(day1+10*time.Minute)),
		hold(dur(day1+20*time.Minute), day1+hour, dur(day1+2*hour)),
		hold(nil, day2, dur(day2+hour)),
	))

	require.Len(t, got.Days, 2)
	assert.Less(t, got.Days[0].Day, got.Days[1].Day)
	assert.EqualValues(t, 2, got.Days[0].Queued)
	require.NotNil(t, got.Days[0].Wait)
	assert.Equal(t, (30 * time.Minute).Seconds(), got.Days[0].Wait.P50)
	assert.Zero(t, got.Days[1].Queued)
	assert.Nil(t, got.Days[1].Wait)
	assert.EqualValues(t, 1, got.Days[1].Holds)
}

func TestComputeLockHistory_OnlyHoldsAcquiredInThePeriodCount(t *testing.T) {
	t.Parallel()
	got := reports.ComputeLockHistory(lockInput(
		hold(nil, -5*day, dur(-3*day+hour)),
		hold(nil, -2*hour, dur(-hour)),
	))

	assert.EqualValues(t, 1, got.Total.Holds)
	assert.Empty(t, reports.ComputeLockHistory(lockInput()).Days)
	assert.Nil(t, reports.ComputeLockHistory(lockInput()).Total.Hold)
}

func TestComputeLockHistory_OverlapCountsWaitersDuringHoldsCappedByDepth(t *testing.T) {
	t.Parallel()
	// A holds from -10h to -6h. B queued at -9h and C at -8h, both until A is
	// done: B gets it at -6h and holds to -5h; C waits on until -5h.
	got := reports.ComputeLockHistory(lockInput(
		hold(nil, -10*hour, dur(-6*hour)),
		hold(dur(-9*hour), -6*hour, dur(-5*hour)),
		hold(dur(-8*hour), -5*hour, dur(-4*hour)),
	))

	assert.Equal(t, (6 * hour).Seconds(), got.HeldSeconds)
	byDepth := map[int]float64{}
	for _, o := range got.Overlap {
		byDepth[o.Depth] = o.Seconds
	}
	// Someone waits from -9h to -5h, all held: 4h. A second waiter from -8h
	// to -6h and again from... C alone from -6h to -5h counts once.
	assert.Equal(t, (4 * hour).Seconds(), byDepth[1])
	// Two waiting for -8h..-6h adds two more hours at depth 2 and 3.
	assert.Equal(t, (6 * hour).Seconds(), byDepth[2])
	assert.Equal(t, (6 * hour).Seconds(), byDepth[3])
}

func TestComputeLockHistory_NothingHeldIsZero(t *testing.T) {
	t.Parallel()
	got := reports.ComputeLockHistory(lockInput())

	assert.Zero(t, got.HeldSeconds)
	assert.Len(t, got.Overlap, len(reports.OverlapDepths))
	for _, o := range got.Overlap {
		assert.Zero(t, o.Seconds)
	}
}

func TestLockHistoryRows_ReadsHoldsOverlappingThePeriod(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ticket := f.scopeTicket(t, "Merging")
	agent := f.worker(t, "merger", at(-30*day))
	insert := func(queued *time.Time, acquired time.Time, released *time.Time) {
		t.Helper()
		_, err := f.database.Exec(f.ctx, `INSERT INTO lock_holds (name, ticket_id, agent_id, queued_at, acquired_at, released_at)
			VALUES ('integration', $1, $2, $3, $4, $5)`, ticket, agent, queued, acquired, released)
		require.NoError(t, err)
	}
	insert(nil, at(-10*day), ptr(-10*day+hour))     // over before the period
	insert(nil, at(-30*hour), ptr(-23*hour))        // begun before the period, over inside it
	insert(ptr(-3*hour), at(-2*hour), ptr(-1*hour)) // inside, queued
	insert(nil, at(-30*time.Minute), nil)           // held still

	in, err := f.store.LockHistoryRows(f.ctx, at(-24*hour), now)
	require.NoError(t, err)
	got := reports.ComputeLockHistory(in)

	assert.EqualValues(t, 2, got.Total.Holds, "the one acquired before the period is read for its held time only")
	assert.EqualValues(t, 1, got.Total.Ongoing)
	assert.Equal(t, (150 * time.Minute).Seconds(), got.HeldSeconds, "1h of the first, 1h and 30m of the others")
	assert.Equal(t, hour.Seconds(), got.Total.Hold.P50)
	assert.Equal(t, hour.Seconds(), got.Total.Wait.P50)
}

func TestComputeLockHistory_OverlapIsClippedToThePeriod(t *testing.T) {
	t.Parallel()
	// Held from before the period to after it, with a waiter queued from
	// before it: only the three days inside count.
	held := hold(nil, -4*day, nil)
	waiter := hold(dur(-5*day), 4*day, nil)

	got := reports.ComputeLockHistory(lockInput(held, waiter))

	assert.Equal(t, (3 * day).Seconds(), got.HeldSeconds)
	assert.Equal(t, (3 * day).Seconds(), got.Overlap[0].Seconds, "the waiter waited through the whole period")
}
