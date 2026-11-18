package steward_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward/agreement"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

func (r *rig) supervision() *steward.Supervision {
	return steward.NewSupervision(r.database, agreement.NewReporter(agreement.NewScorer(r.store, r.database)), nil)
}

func TestBoardEventsWakeOnDiscussionAndAgentFiledPreparing(t *testing.T) {
	r := newRig(t, dbtest.WithDB(t))
	sup := r.supervision()
	start, err := sup.Head(r.ctx)
	require.NoError(t, err)

	byUser, err := r.tickets.Create(r.ctx, &ticket.CreateParams{Title: "user's", Status: ticket.StatusPreparing})
	require.NoError(t, err)
	filer, err := r.agents.Create(r.ctx, "filer")
	require.NoError(t, err)
	byAgent, err := r.tickets.Create(r.ctx, &ticket.CreateParams{Title: "agent's", Status: ticket.StatusPreparing, CreatedByAgentID: &filer.ID})
	require.NoError(t, err)
	_, err = r.database.Exec(r.ctx, `UPDATE tickets SET status = 'discussion' WHERE id = $1`, byUser.ID)
	require.NoError(t, err)

	events, cursor, err := sup.Events(r.ctx, start, 0)
	require.NoError(t, err)
	require.Len(t, events, 2, "a ticket the user filed in preparing wakes nobody")
	assert.Equal(t, steward.BoardEvent{Kind: steward.EventAgentFiled, TicketID: byAgent.ID, Cursor: events[0].Cursor}, events[0])
	assert.Equal(t, steward.EventDiscussion, events[1].Kind)
	assert.Equal(t, byUser.ID, events[1].TicketID)

	again, next, err := sup.Events(r.ctx, cursor, 0)
	require.NoError(t, err)
	assert.Empty(t, again, "events behind the cursor are not repeated")
	assert.Equal(t, cursor, next)
}

func TestBoardEventsCursorMovesPastChangesThatWakeNobody(t *testing.T) {
	r := newRig(t, dbtest.WithDB(t))
	sup := r.supervision()
	start, err := sup.Head(r.ctx)
	require.NoError(t, err)
	_, err = r.tickets.Create(r.ctx, &ticket.CreateParams{Title: "x", Status: ticket.StatusOpen})
	require.NoError(t, err)

	events, cursor, err := sup.Events(r.ctx, start, 0)
	require.NoError(t, err)
	assert.Empty(t, events)
	assert.Greater(t, cursor, start)
}

func TestBoardEventsAreTenantScoped(t *testing.T) {
	database := dbtest.WithDB(t)
	mine, theirs := newRig(t, database), newRig(t, database)
	_, err := theirs.tickets.Create(theirs.ctx, &ticket.CreateParams{Title: "x", Status: ticket.StatusPreparing})
	require.NoError(t, err)
	_, err = theirs.database.Exec(theirs.ctx, `UPDATE tickets SET status = 'discussion'`)
	require.NoError(t, err)

	events, _, err := mine.supervision().Events(mine.ctx, 0, 0)
	require.NoError(t, err)
	assert.Empty(t, events)
}

func TestTheStewardLeaseIsOneAtATimeAndExpires(t *testing.T) {
	r := newRig(t, dbtest.WithDB(t))
	sup := r.supervision()
	a, err := r.agents.Create(r.ctx, "sup-a")
	require.NoError(t, err)
	b, err := r.agents.Create(r.ctx, "sup-b")
	require.NoError(t, err)

	ok, expires, err := sup.AcquireLease(r.ctx, a.ID)
	require.NoError(t, err)
	assert.True(t, ok)
	assert.WithinDuration(t, time.Now().Add(steward.LeaseTTL), expires, time.Minute)

	ok, _, err = sup.AcquireLease(r.ctx, b.ID)
	require.NoError(t, err)
	assert.False(t, ok, "another holder cannot take a live lease")

	ok, _, err = sup.AcquireLease(r.ctx, a.ID)
	require.NoError(t, err)
	assert.True(t, ok, "the holder renews")

	_, err = r.database.Exec(r.ctx, `UPDATE steward_leases SET expires_at = now() - interval '1 second'`)
	require.NoError(t, err)
	ok, _, err = sup.AcquireLease(r.ctx, b.ID)
	require.NoError(t, err)
	assert.True(t, ok, "an expired lease is free")

	require.NoError(t, sup.ReleaseLease(r.ctx, a.ID))
	ok, _, err = sup.AcquireLease(r.ctx, a.ID)
	require.NoError(t, err)
	assert.False(t, ok, "releasing a lease one does not hold changes nothing")

	require.NoError(t, sup.ReleaseLease(r.ctx, b.ID))
	ok, _, err = sup.AcquireLease(r.ctx, a.ID)
	require.NoError(t, err)
	assert.True(t, ok)
}

func TestOnlyOneOfManyConcurrentAcquirersGetsTheLease(t *testing.T) {
	r := newRig(t, dbtest.WithDB(t))
	sup := r.supervision()
	const n = 8
	holders := make([]uuid.UUID, n)
	for i := range holders {
		h, err := r.agents.Create(r.ctx, fmt.Sprintf("sup-%d", i))
		require.NoError(t, err)
		holders[i] = h.ID
	}
	var won atomic.Int32
	start := make(chan struct{})
	var wg sync.WaitGroup
	for _, h := range holders {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			ok, _, err := sup.AcquireLease(r.ctx, h)
			assert.NoError(t, err)
			if ok {
				won.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()
	assert.Equal(t, int32(1), won.Load())
}

func TestTheAgreementTextNamesCategoriesAndDisagreements(t *testing.T) {
	r := newRig(t, dbtest.WithDB(t))
	sup := r.supervision()
	empty, err := sup.AgreementText(r.ctx, time.Time{}, time.Time{})
	require.NoError(t, err)
	assert.Empty(t, empty)

	_, a := r.assess(t, "merge_continue", steward.VerdictWouldAuto, epoch)
	changed, err := r.store.RecordDecision(r.ctx, a.ID, steward.DecisionRejected, epoch.Add(time.Minute))
	require.NoError(t, err)
	require.True(t, changed)
	text, err := sup.AgreementText(r.ctx, time.Time{}, time.Time{})
	require.NoError(t, err)
	assert.Contains(t, text, "merge_continue: 1 decided")
	assert.Contains(t, text, "disagreed 1")
	assert.Contains(t, text, "you said would_auto; the owner rejected (you gave: because)")
}

type fakeReporter struct {
	since, until time.Time
	err          error
}

func (f *fakeReporter) Text(_ context.Context, since, until time.Time) (string, error) {
	f.since, f.until = since, until
	return "report", f.err
}

func TestAgreementTextForwardsItsWindowAndTheReportersError(t *testing.T) {
	r := newRig(t, dbtest.WithDB(t))
	fake := &fakeReporter{}
	sup := steward.NewSupervision(r.database, fake, nil)
	text, err := sup.AgreementText(r.ctx, epoch, epoch.Add(time.Hour))
	require.NoError(t, err)
	assert.Equal(t, "report", text)
	assert.Equal(t, epoch, fake.since)
	assert.Equal(t, epoch.Add(time.Hour), fake.until)

	fake.err = errors.New("boom")
	_, err = sup.AgreementText(r.ctx, epoch, epoch)
	assert.ErrorIs(t, err, fake.err)
}
