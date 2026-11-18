package landing_test

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/landing"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

// delay makes the ticket unclaimable by the server until undelay.
func (f *fixture) delay(id int64) {
	f.t.Helper()
	_, err := f.database.Exec(f.ctx, `UPDATE tickets SET blocked_until = now() + interval '1 hour' WHERE id = $1`, id)
	require.NoError(f.t, err)
}

func (f *fixture) undelay(id int64) {
	f.t.Helper()
	_, err := f.database.Exec(f.ctx, `UPDATE tickets SET blocked_until = NULL WHERE id = $1`, id)
	require.NoError(f.t, err)
}

// orderRecorder notes the order outcomes are written in.
type orderRecorder struct {
	landing.Outcomes
	mu  sync.Mutex
	ids []int64
}

func (o *orderRecorder) Record(ctx context.Context, r *landing.Record) error {
	o.mu.Lock()
	o.ids = append(o.ids, r.TicketID)
	o.mu.Unlock()
	return o.Outcomes.Record(ctx, r)
}

func TestRun_ATicketNotClaimableAtFirstIsLandedInTheSameSweepOnceItIs(t *testing.T) {
	f := newFixture(t)
	a := f.finishedChangeAt(model.PriorityUrgent, "app/a/")
	b := f.finishedChangeAt(model.PriorityNormal, "app/b/")
	c := f.finishedChangeAt(model.PriorityLow, "app/c/")
	for _, x := range []*finished{a, b, c} {
		f.enqueue(x)
	}
	f.delay(a.ticketID)
	// a becomes claimable while b is being landed.
	cleared := false
	f.onChecks = func(context.Context) {
		if !cleared {
			cleared = true
			f.undelay(a.ticketID)
		}
	}
	order := &orderRecorder{Outcomes: f.outcomes}
	f.recorder = order
	r := f.runner(landing.Config{})

	f.land(r)

	assert.True(t, cleared)
	f.assertMerged(b)
	assert.Equal(t, []int64{b.ticketID, a.ticketID, c.ticketID}, order.ids, "a is tried again right after the landing that freed it, ahead of c")
	got, err := f.tickets.Get(f.ctx, a.ticketID)
	require.NoError(t, err)
	assert.Equal(t, ticket.StatusDone, got.Status, "a was parked once and must not wait for the next sweep")
	assert.Nil(t, f.row(c.ticketID), "and the rest of the queue still lands")
}
