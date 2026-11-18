package ticket_test

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

// scrape is what the collector reports now, by metric name.
func (f *fixture) scrape(t *testing.T) map[string]*dto.MetricFamily {
	t.Helper()
	registry := prometheus.NewRegistry()
	require.NoError(t, registry.Register(ticket.NewBoardCollector(f.database, slog.DiscardHandler)))
	families, err := registry.Gather()
	require.NoError(t, err)
	byName := map[string]*dto.MetricFamily{}
	for _, family := range families {
		byName[family.GetName()] = family
	}
	return byName
}

// labelled finds the one series of family whose labels are exactly want.
func labelled(t *testing.T, family *dto.MetricFamily, want map[string]string) *dto.Metric {
	t.Helper()
	require.NotNil(t, family)
	for _, m := range family.GetMetric() {
		got := map[string]string{}
		for _, l := range m.GetLabel() {
			got[l.GetName()] = l.GetValue()
		}
		if assert.ObjectsAreEqual(want, got) {
			return m
		}
	}
	require.Failf(t, "no such series", "%v in %v", want, family.GetMetric())
	return nil
}

func TestBoardCollector_CountsTheBoardAcrossTenantsByStatusAndPriority(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	urgent := model.PriorityUrgent
	mk := func(ctx context.Context, p model.Priority, s ticket.Status) {
		_, err := f.svc.Create(ctx, &ticket.CreateParams{Title: "t", Priority: p, Status: s})
		require.NoError(t, err)
	}
	mk(f.ctx, model.PriorityNormal, ticket.StatusOpen)
	mk(f.ctx, urgent, ticket.StatusPreparing)
	held, err := f.svc.Create(f.ctx, &ticket.CreateParams{Title: "held", Priority: urgent, Status: ticket.StatusOpen})
	require.NoError(t, err)
	_, err = f.svc.Claim(f.ctx, a, &held.ID)
	require.NoError(t, err)
	// Another tenant's tickets are on the same board metric.
	otherCtx := db.WithTenant(context.Background(), dbtest.NewTenant(t, f.database))
	mk(otherCtx, model.PriorityNormal, ticket.StatusOpen)

	tickets := f.scrape(t)["slophaus_tickets"]

	count := func(status, priority string) float64 {
		return labelled(t, tickets, map[string]string{"status": status, "priority": priority}).GetGauge().GetValue()
	}
	assert.Equal(t, 2.0, count("open", "normal"), "both tenants' open tickets")
	assert.Equal(t, 1.0, count("preparing", "urgent"))
	assert.Equal(t, 1.0, count("in_progress", "urgent"))
	assert.Equal(t, 0.0, count("done", "low"), "a status nothing is in still has its series")
	assert.Len(t, tickets.GetMetric(), len(model.Statuses())*4)
}

// The histogram is a running total of finished stays: each observed once, in
// the state it was in and under the priority it had on entering it, with its
// real duration.
func TestBoardCollector_ObservesEachFinishedStayWithItsStateAndDuration(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	tk, err := f.svc.Create(f.ctx, &ticket.CreateParams{Title: "Timed", Priority: model.PriorityHigh, Paths: []model.Path{{Path: "timed/"}}})
	require.NoError(t, err)
	_, err = f.svc.Claim(f.ctx, a, &tk.ID)
	require.NoError(t, err)
	_, err = f.svc.Done(f.ctx, a, &ticket.DoneParams{Summary: "ok"})
	require.NoError(t, err)
	// Reprioritised since: the stays keep the priority the ticket had on entering them.
	_, err = f.database.Exec(f.ctx, `UPDATE tickets SET priority = 'urgent' WHERE id = $1`, tk.ID)
	require.NoError(t, err)
	// open for ten minutes, then in progress for two hours, then done.
	start := time.Now().Add(-24 * time.Hour)
	for i, at := range []time.Time{start, start.Add(10 * time.Minute), start.Add(130 * time.Minute)} {
		_, err := f.database.Exec(f.ctx, `
			UPDATE ticket_status_changes SET changed_at = $2
			 WHERE ticket_id = $1 AND id = (SELECT id FROM ticket_status_changes WHERE ticket_id = $1 ORDER BY id OFFSET $3 LIMIT 1)`,
			tk.ID, at, i)
		require.NoError(t, err)
	}

	family := f.scrape(t)["slophaus_ticket_state_duration_seconds"]

	require.Len(t, family.GetMetric(), 2, "done is where the ticket is now: not a finished stay")
	open := labelled(t, family, map[string]string{"state": "open", "priority": "high"}).GetHistogram()
	assert.Equal(t, uint64(1), open.GetSampleCount())
	assert.InDelta(t, 600, open.GetSampleSum(), 0.001)
	working := labelled(t, family, map[string]string{"state": "in_progress", "priority": "high"}).GetHistogram()
	assert.Equal(t, uint64(1), working.GetSampleCount())
	assert.InDelta(t, 7200, working.GetSampleSum(), 0.001)

	within := func(h *dto.Histogram, le float64) uint64 {
		for _, b := range h.GetBucket() {
			if b.GetUpperBound() == le {
				return b.GetCumulativeCount()
			}
		}
		require.Failf(t, "no such bucket", "%v", le)
		return 0
	}
	assert.Zero(t, within(open, 300), "ten minutes is not within five")
	assert.Equal(t, uint64(1), within(open, 900))
	assert.Zero(t, within(working, 3600))
	assert.Equal(t, uint64(1), within(working, 7200))
}

// Lead time is creation to done, whatever the ticket went through between.
func TestBoardCollector_ObservesLeadTimeFromCreationToDone(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	done := f.ticket(t, "Finished", "done/")
	_ = f.ticket(t, "Still open", "open/")
	_, err := f.svc.Claim(f.ctx, a, &done.ID)
	require.NoError(t, err)
	_, err = f.svc.Done(f.ctx, a, &ticket.DoneParams{Summary: "ok"})
	require.NoError(t, err)
	// created three hours before it was done.
	_, err = f.database.Exec(f.ctx, `
		UPDATE ticket_status_changes SET changed_at = now() - interval '3 hours' WHERE ticket_id = $1 AND from_status IS NULL`, done.ID)
	require.NoError(t, err)

	family := f.scrape(t)["slophaus_ticket_lead_time_seconds"]

	require.Len(t, family.GetMetric(), 1, "only the finished ticket is observed")
	h := labelled(t, family, map[string]string{"priority": "normal"}).GetHistogram()
	assert.Equal(t, uint64(1), h.GetSampleCount())
	assert.InDelta(t, 3*3600, h.GetSampleSum(), 5)
}
