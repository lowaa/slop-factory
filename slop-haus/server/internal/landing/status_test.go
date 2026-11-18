package landing_test

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2/humatest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/landing"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/settings"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

func TestOrdered_NumbersLandingRowsInTheOrderHeadTakesThem(t *testing.T) {
	f := newFixture(t)
	low := f.finishedChange(model.PriorityLow)
	urgent := f.finishedChange(model.PriorityUrgent)
	normal := f.finishedChange(model.PriorityNormal)
	gone := f.finishedChange(model.PriorityNormal)
	require.NoError(t, f.queue.HandBack(f.ctx, gone.ticketID, landing.ChecksRed, false))

	positions := func() map[int64]int {
		rows, err := f.queue.Ordered(f.ctx)
		require.NoError(t, err)
		out := map[int64]int{}
		for _, r := range rows {
			out[r.TicketID] = r.Position
		}
		return out
	}
	assert.Equal(t, map[int64]int{urgent.ticketID: 1, normal.ticketID: 2, low.ticketID: 3, gone.ticketID: 0}, positions(), "priority, not seq")

	_, err := f.database.Exec(f.ctx, `UPDATE landings SET updated_at = now() - interval '4 hours' WHERE ticket_id = $1`, low.ticketID)
	require.NoError(t, err)
	assert.Equal(t, map[int64]int{low.ticketID: 1, urgent.ticketID: 2, normal.ticketID: 3, gone.ticketID: 0}, positions(), "aging")
	aged, err := f.queue.Ordered(f.ctx)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, aged[0].SinceUpdate, 4*time.Hour, "the time since the row last changed is the database clock's")
	assert.Less(t, aged[1].SinceUpdate, time.Hour)

	require.NoError(t, f.queue.SetStep(f.ctx, normal.ticketID, landing.MergingMain)) // a row begins at merge-main
	require.NoError(t, f.queue.SetStep(f.ctx, normal.ticketID, landing.WaitingChecks))
	rows, err := f.queue.Ordered(f.ctx)
	require.NoError(t, err)
	assert.Equal(t, normal.ticketID, rows[0].TicketID, "a begun landing leads, as Head does")
	assert.Equal(t, landing.WaitingChecks, rows[0].Step)
	assert.Equal(t, gone.ticketID, rows[len(rows)-1].TicketID, "handed-back rows come last")
}

type settingsFake struct{ method string }

func (s settingsFake) Get(context.Context, string) (settings.Setting, error) {
	return settings.Setting{Name: landing.SettingMergeMethod, Value: settings.StrValue(s.method)}, nil
}

type toolsFake struct {
	connected bool
	err       error
}

func (t toolsFake) Connected(context.Context) (bool, error) { return t.connected, t.err }

type queueFake struct{ rows []landing.LandingRow }

func (q queueFake) Ordered(context.Context) ([]landing.LandingRow, error) { return q.rows, nil }

func TestCapabilities_MergeQueueNeedsTheSwitchAndChangeTools(t *testing.T) {
	for _, tc := range []struct {
		name           string
		enabled, tools bool
		wantAvailable  bool
	}{
		{"both", true, true, true},
		{"switch off", false, true, false},
		{"no credential", true, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := landing.NewStatus(queueFake{}, settingsFake{landing.MergeMethodWorker}, toolsFake{connected: tc.tools}, tc.enabled)
			c, err := s.Capabilities(context.Background())
			require.NoError(t, err)
			assert.Equal(t, tc.wantAvailable, c.MergeQueueAvailable())
			assert.Equal(t, landing.MergeMethodWorker, c.MergeMethod)
		})
	}
	c, err := landing.NewStatus(queueFake{}, settingsFake{landing.MergeMethodWorker}, nil, true).Capabilities(context.Background())
	require.NoError(t, err)
	assert.False(t, c.ChangeTools, "no code host at all is no change tools")

	_, err = landing.NewStatus(queueFake{}, settingsFake{}, toolsFake{err: errors.New("boom")}, true).Capabilities(context.Background())
	assert.Error(t, err)
}

func TestHandler_ServesQueueEntriesAndCapabilities(t *testing.T) {
	batch := int64(4)
	rows := []landing.LandingRow{
		{TicketID: 7, AttemptNumber: 2, ChangeURL: "https://x/pull/1", State: landing.Landing, Step: landing.MergingMain, Position: 1, BatchID: &batch, BatchPosition: 2, SinceUpdate: 90 * time.Second},
		{TicketID: 9, State: landing.BackWithAgent, BackReason: landing.ChecksRed},
	}
	status := landing.NewStatus(queueFake{rows}, settingsFake{landing.MergeMethodQueue}, toolsFake{connected: true}, true)
	_, api := humatest.New(t)
	landing.RegisterRoutes(api, landing.NewHandler(status, slog.DiscardHandler))

	resp := api.Get("/api/landing/queue")
	require.Equal(t, http.StatusOK, resp.Code)
	assert.JSONEq(t, `{"entries":[
	 {"ticket_id":7,"attempt_number":2,"change_url":"https://x/pull/1","state":"landing","position":1,"step":"merging_main","back_reason":"","batch_id":4,"batch_position":2,"since_update_seconds":90},
	 {"ticket_id":9,"attempt_number":0,"change_url":"","state":"back_with_agent","position":0,"step":"","back_reason":"`+string(landing.ChecksRed)+`","batch_id":null,"batch_position":0,"since_update_seconds":0}]}`, resp.Body.String())

	resp = api.Get("/api/landing/capabilities")
	assert.JSONEq(t, `{"merge_method":"merge_queue","merge_queue_enabled":true,"change_tools":true,"merge_queue_available":true}`, resp.Body.String())
}

func TestProgress_AgesTheBegunLandingNotTheQueue(t *testing.T) {
	f := newFixture(t)
	p, err := f.queue.Progress(f.ctx)
	require.NoError(t, err)
	assert.Equal(t, landing.Progress{}, p, "an empty queue")

	old := f.finishedChange(model.PriorityNormal)
	f.finishedChange(model.PriorityUrgent)
	gone := f.finishedChange(model.PriorityNormal)
	require.NoError(t, f.queue.HandBack(f.ctx, gone.ticketID, landing.ChecksRed, false))

	_, err = f.database.Exec(f.ctx, `UPDATE landings SET updated_at = now() - interval '2 hours' WHERE ticket_id = $1`, old.ticketID)
	require.NoError(t, err)
	p, err = f.queue.Progress(f.ctx)
	require.NoError(t, err)
	assert.Equal(t, 2, p.Landing, "a handed-back row is not waiting to land")
	assert.Greater(t, p.OldestWait, 119*time.Minute)
	assert.Empty(t, p.Step, "nothing has begun, so no step is aging however long the queue is")
	assert.Zero(t, p.StepAge)

	require.NoError(t, f.queue.SetStep(f.ctx, old.ticketID, landing.MergingMain)) // a row begins at merge-main
	require.NoError(t, f.queue.SetStep(f.ctx, old.ticketID, landing.WaitingChecks))
	p, err = f.queue.Progress(f.ctx)
	require.NoError(t, err)
	assert.Equal(t, landing.WaitingChecks, p.Step)
	assert.Less(t, p.StepAge, time.Minute, "SetStep restarts the step's age")

	_, err = f.database.Exec(f.ctx, `UPDATE landings SET updated_at = now() - interval '50 minutes' WHERE ticket_id = $1`, old.ticketID)
	require.NoError(t, err)
	p, err = f.queue.Progress(f.ctx)
	require.NoError(t, err)
	assert.Greater(t, p.StepAge, 49*time.Minute)
	assert.Less(t, p.StepAge, 51*time.Minute)
	assert.Less(t, p.OldestWait, time.Minute, "the begun row is being worked; the other row only just joined")

	// Of two begun rows (a batch) the one on its step longest is the one reported.
	other := f.finishedChange(model.PriorityNormal)
	require.NoError(t, f.queue.SetStep(f.ctx, other.ticketID, landing.Merging))
	_, err = f.database.Exec(f.ctx, `UPDATE landings SET updated_at = now() - interval '5 minutes' WHERE ticket_id = $1`, other.ticketID)
	require.NoError(t, err)
	p, err = f.queue.Progress(f.ctx)
	require.NoError(t, err)
	assert.Equal(t, landing.WaitingChecks, p.Step)
	assert.Greater(t, p.StepAge, 49*time.Minute)
}

func TestProgress_TopWaitIsTheWaitOfTheBestRankedWaitingRow(t *testing.T) {
	f := newFixture(t)
	normal := f.finishedChange(model.PriorityNormal)
	urgent := f.finishedChange(model.PriorityUrgent)
	_, err := f.database.Exec(f.ctx, `UPDATE landings SET updated_at = now() - interval '25 minutes' WHERE ticket_id = $1`, normal.ticketID)
	require.NoError(t, err)
	_, err = f.database.Exec(f.ctx, `UPDATE landings SET updated_at = now() - interval '20 minutes' WHERE ticket_id = $1`, urgent.ticketID)
	require.NoError(t, err)

	p, err := f.queue.Progress(f.ctx)
	require.NoError(t, err)
	assert.Greater(t, p.OldestWait, 24*time.Minute)
	assert.Greater(t, p.TopWait, 19*time.Minute)
	assert.Less(t, p.TopWait, 21*time.Minute, "the urgent row ranks first, though the other has waited longer")

	require.NoError(t, f.queue.SetStep(f.ctx, urgent.ticketID, landing.MergingMain)) // a row begins at merge-main
	require.NoError(t, f.queue.SetStep(f.ctx, urgent.ticketID, landing.WaitingChecks))
	p, err = f.queue.Progress(f.ctx)
	require.NoError(t, err)
	assert.Greater(t, p.TopWait, 24*time.Minute, "a begun row is not waiting, so the next one ranks first")
}

func TestProgress_CountsTestingBatchesWithNoPipeline(t *testing.T) {
	b := newBatch(t, 2)
	p, err := b.queue.Progress(b.ctx)
	require.NoError(t, err)
	assert.Zero(t, p.BatchesWithoutCI)

	batch, err := reserveNow(b.ctx, b.store, baseOld, b.ids())
	require.NoError(t, err)
	p, err = b.queue.Progress(b.ctx)
	require.NoError(t, err)
	assert.Zero(t, p.BatchesWithoutCI, "a building batch is not waiting on CI yet")

	require.NoError(t, b.store.SetState(b.ctx, batch.ID, landing.BatchTesting))
	_, err = b.database.Exec(b.ctx, `UPDATE batches SET updated_at = now() - interval '7 minutes' WHERE id = $1`, batch.ID)
	require.NoError(t, err)
	p, err = b.queue.Progress(b.ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, p.BatchesWithoutCI)
	assert.Greater(t, p.OldestBatchWithoutCI, 6*time.Minute)

	require.NoError(t, b.store.SetPipeline(b.ctx, batch.ID, "42"))
	p, err = b.queue.Progress(b.ctx)
	require.NoError(t, err)
	assert.Zero(t, p.BatchesWithoutCI)
	assert.Zero(t, p.OldestBatchWithoutCI)
}
