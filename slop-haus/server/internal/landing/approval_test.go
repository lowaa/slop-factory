package landing_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/change"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/landing"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

func modeOf(on bool) landing.Mode {
	return landing.ModeFunc(func(context.Context) (bool, error) { return on, nil })
}

func TestApprovalRouting_EnqueuesOnlyWithTheFlagAndTheMode(t *testing.T) {
	for _, tc := range []struct{ flag, mode, want bool }{
		{false, false, false}, {false, true, false}, {true, false, false}, {true, true, true},
	} {
		got, err := landing.NewApprovalRouting(modeOf(tc.mode), tc.flag).Enqueues(context.Background())
		require.NoError(t, err)
		assert.Equal(t, tc.want, got, "flag %v mode %v", tc.flag, tc.mode)
	}
}

func TestApprovalRouting_FlagOffReadsNoSetting(t *testing.T) {
	mode := landing.ModeFunc(func(context.Context) (bool, error) { return false, errors.New("read") })
	got, err := landing.NewApprovalRouting(mode, false).Enqueues(context.Background())
	require.NoError(t, err)
	assert.False(t, got)
}

type fakeApprovalQueue struct{ rows []*landing.LandingRow }

func (q *fakeApprovalQueue) Enqueue(_ context.Context, r *landing.LandingRow) error {
	q.rows = append(q.rows, r)
	return nil
}

func TestApprovalHook_EnqueuesAndKicksWhenRouted(t *testing.T) {
	q, k, tenant := &fakeApprovalQueue{}, &kicks{}, uuid.New()
	h := landing.NewApprovalHook(landing.NewApprovalRouting(modeOf(true), true), fakeFinishHost{}, q, k)
	require.NoError(t, h.Approved(context.Background(), &ticket.Approval{TenantID: tenant, TicketID: 5, Attempt: 2}))
	require.Len(t, q.rows, 1)
	assert.Equal(t, &landing.LandingRow{TicketID: 5, AttemptNumber: 2, ChangeURL: "https://example.test/pr/1", HeadSHA: "abc"}, q.rows[0])
	assert.Equal(t, kicks{tenant}, *k)
}

func TestApprovalHook_LeavesAnUnroutedApprovalAlone(t *testing.T) {
	for name, routing := range map[string]*landing.ApprovalRouting{
		"flag off":    landing.NewApprovalRouting(modeOf(true), false),
		"worker mode": landing.NewApprovalRouting(modeOf(false), true),
	} {
		t.Run(name, func(t *testing.T) {
			q, k := &fakeApprovalQueue{}, &kicks{}
			h := landing.NewApprovalHook(routing, fakeFinishHost{}, q, k)
			require.NoError(t, h.Approved(context.Background(), &ticket.Approval{TenantID: uuid.New(), TicketID: 5, Attempt: 2}))
			assert.Empty(t, q.rows)
			assert.Empty(t, *k)
		})
	}
}

func TestApprovalHook_AnAttemptWithoutAChangeIsNotEnqueued(t *testing.T) {
	q, k := &fakeApprovalQueue{}, &kicks{}
	h := landing.NewApprovalHook(landing.NewApprovalRouting(modeOf(true), true), fakeFinishHost{err: change.ErrNoChange}, q, k)
	require.NoError(t, h.Approved(context.Background(), &ticket.Approval{TenantID: uuid.New(), TicketID: 5, Attempt: 2}))
	assert.Empty(t, q.rows)
	assert.Empty(t, *k)
}

func TestApprovalHook_ApprovedChangeLandsThroughTheQueue(t *testing.T) {
	f := newFixture(t)
	c := f.finishedChangeAt(model.PriorityNormal, "app/server/")
	r := f.runner(landing.Config{})
	h := landing.NewApprovalHook(landing.NewApprovalRouting(modeOf(true), true), f.hostWrap, landing.NewLandingStore(f.database), r)

	require.NoError(t, h.Approved(context.Background(), &ticket.Approval{TenantID: f.tenant, TicketID: c.ticketID, Attempt: c.attempt, ApproverID: f.approver}))
	r.Wait()

	f.assertMerged(c)
}
