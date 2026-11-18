package supervisorwatch_test

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/supervisorwatch"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

// testBounds are short so a test can sit just either side of them with
// ordinary durations: the rules compare against the database's clock.
var testBounds = supervisorwatch.Bounds{IdleWithWork: 10 * time.Minute, NoHeartbeat: 20 * time.Minute, HeldWithoutRun: 3 * time.Minute, ErrorSteps: 3, PausedNotice: 5 * time.Minute}

func (f *fixture) slots(enabled bool) *supervisorwatch.Service {
	return f.service(enabled).WithBounds(testBounds)
}

func (f *fixture) report(svc *supervisorwatch.Service, agent uuid.UUID, r supervisorwatch.SlotReport) {
	f.t.Helper()
	require.NoError(f.t, svc.ReportSlot(f.ctx, agent, &r))
}

func (f *fixture) slot(svc *supervisorwatch.Service, agent uuid.UUID) *supervisorwatch.Slot {
	f.t.Helper()
	slots, err := svc.Slots(f.ctx)
	require.NoError(f.t, err)
	for _, s := range slots {
		if s.AgentID == agent {
			return s
		}
	}
	return nil
}

// claimable is an open ticket nobody holds, made the way a person would, so
// it has the attempt a claim takes.
func (f *fixture) claimable(roles ...uuid.UUID) {
	f.t.Helper()
	_, err := tickets(f.database).Create(f.ctx, &ticket.CreateParams{Title: "work", Roles: ticket.RoleRequirement{IDs: roles, Match: model.RoleMatchAny}})
	require.NoError(f.t, err)
}

func (f *fixture) role() uuid.UUID {
	id := uuid.New()
	f.exec(`INSERT INTO agent_roles (id, name) VALUES ($1, $2)`, id, "r-"+id.String())
	return id
}

func (f *fixture) lease(agent uuid.UUID, heartbeatAgo time.Duration) {
	f.exec(`INSERT INTO leases (ticket_id, attempt_number, agent_id, acquired_at, expires_at)
		VALUES ($1, 1, $2, now() - interval '3 hours', now() - $3::interval + $4::interval)`,
		f.ticket("in_progress"), agent, heartbeatAgo.String(), ticket.DefaultLeaseTTL.String())
}

func idleFor(d time.Duration) supervisorwatch.SlotReport {
	return supervisorwatch.SlotReport{Phase: supervisorwatch.PhaseIdle, PhaseAge: d}
}

func TestSlots_IdleWithClaimableWorkIsStalledPastTheBound(t *testing.T) {
	f := newFixture(t)
	svc := f.slots(true)
	f.claimable()
	long, short := f.agent(), f.agent()
	f.report(svc, long, idleFor(11*time.Minute))
	f.report(svc, short, idleFor(9*time.Minute))

	assert.Equal(t, supervisorwatch.StallIdleWithWork, f.slot(svc, long).Stall)
	assert.False(t, f.slot(svc, short).Stalled(), "idle for less than the bound")
}

func TestSlots_IdleWithNothingToTakeIsNotStalled(t *testing.T) {
	f := newFixture(t)
	svc := f.slots(true)
	slot := f.agent()
	f.report(svc, slot, idleFor(time.Hour))

	assert.False(t, f.slot(svc, slot).Stalled(), "an empty queue is not a stall")
}

// The tenant has claimable work, but not for this slot: the probe is the
// per-agent one, so a slot that rightly cannot take anything is not flagged.
func TestSlots_IdleWhileTheOnlyWorkNeedsARoleItLacksIsNotStalled(t *testing.T) {
	f := newFixture(t)
	svc := f.slots(true)
	pilot := f.role()
	f.claimable(pilot)
	plain, withRole := f.agent(), f.agent()
	f.exec(`INSERT INTO agent_role_members (agent_id, role_id) VALUES ($1, $2)`, withRole, pilot)
	f.report(svc, plain, idleFor(time.Hour))
	f.report(svc, withRole, idleFor(time.Hour))

	assert.False(t, f.slot(svc, plain).Stalled())
	assert.Equal(t, supervisorwatch.StallIdleWithWork, f.slot(svc, withRole).Stall)
}

func TestSlots_IdleWhileTicketProcessingIsOffIsNotStalled(t *testing.T) {
	f := newFixture(t)
	svc := f.slots(false)
	f.claimable()
	slot := f.agent()
	f.report(svc, slot, idleFor(time.Hour))

	assert.False(t, f.slot(svc, slot).Stalled(), "work is held back on purpose")
}

func TestSlots_IdleWhileHoldingAClaimIsStalledPastTheBound(t *testing.T) {
	f := newFixture(t)
	svc := f.slots(true)
	leased, leasedBriefly, named, free := f.agent(), f.agent(), f.agent(), f.agent()
	f.lease(leased, time.Minute)
	f.lease(leasedBriefly, time.Minute)
	ticketID := int64(f.ticket("in_progress"))
	f.report(svc, leased, idleFor(4*time.Minute))
	f.report(svc, leasedBriefly, idleFor(2*time.Minute))
	f.report(svc, named, supervisorwatch.SlotReport{Phase: supervisorwatch.PhaseIdle, PhaseAge: 4 * time.Minute, TicketID: &ticketID})
	f.report(svc, free, idleFor(4*time.Minute))

	assert.Equal(t, supervisorwatch.StallHeldWithoutRun, f.slot(svc, leased).Stall)
	assert.False(t, f.slot(svc, leasedBriefly).Stalled(), "idle with a lease for less than the bound")
	assert.Equal(t, supervisorwatch.StallHeldWithoutRun, f.slot(svc, named).Stall)
	assert.False(t, f.slot(svc, free).Stalled(), "idle with no claim is not this stall")
}

func TestSlots_RunningWithoutALeaseHeartbeatIsStalledPastTheBound(t *testing.T) {
	f := newFixture(t)
	svc := f.slots(true)
	quiet, live, pending := f.agent(), f.agent(), f.agent()
	running := supervisorwatch.SlotReport{Phase: supervisorwatch.PhaseRunning, PhaseAge: 2 * time.Hour}
	f.lease(quiet, 21*time.Minute)
	f.lease(live, 5*time.Minute)
	for _, a := range []uuid.UUID{quiet, live, pending} {
		f.report(svc, a, running)
	}

	assert.Equal(t, supervisorwatch.StallNoHeartbeat, f.slot(svc, quiet).Stall)
	assert.False(t, f.slot(svc, live).Stalled(), "its lease was heartbeaten inside the bound")
	assert.Equal(t, supervisorwatch.StallNoHeartbeat, f.slot(svc, pending).Stall, "running for hours with no live lease")
}

func TestSlots_ALeaseHeldAliveByTheIntegrationLockIsNotQuiet(t *testing.T) {
	f := newFixture(t)
	svc := f.slots(true)
	slot := f.agent()
	f.lease(slot, 2*time.Hour)
	f.exec(`INSERT INTO locks (name, holder_agent_id, holder_lease_id) SELECT 'integration', agent_id, id FROM leases WHERE agent_id = $1`, slot)
	f.report(svc, slot, supervisorwatch.SlotReport{Phase: supervisorwatch.PhaseRunning, PhaseAge: 2 * time.Hour})

	assert.False(t, f.slot(svc, slot).Stalled())
}

func TestSlots_TheSameErrorForMoreThanTheBoundIsStalled(t *testing.T) {
	f := newFixture(t)
	svc := f.slots(true)
	over, at := f.agent(), f.agent()
	f.report(svc, over, supervisorwatch.SlotReport{Phase: "backoff", ErrorClass: "claim_failed", ErrorStreak: 4})
	f.report(svc, at, supervisorwatch.SlotReport{Phase: "backoff", ErrorClass: "claim_failed", ErrorStreak: 3})

	assert.Equal(t, supervisorwatch.StallRepeatingError, f.slot(svc, over).Stall)
	assert.False(t, f.slot(svc, at).Stalled())
}

func TestSlots_ASlotThatIsMakingProgressIsNotStalled(t *testing.T) {
	f := newFixture(t)
	svc := f.slots(true)
	f.claimable()
	slot := f.agent()
	f.lease(slot, time.Minute)
	f.report(svc, slot, supervisorwatch.SlotReport{Phase: supervisorwatch.PhaseRunning, PhaseAge: 5 * time.Hour, ErrorClass: "claim_failed", ErrorStreak: 1})

	assert.False(t, f.slot(svc, slot).Stalled())
}

func TestSlots_AStallClearsWhenTheSlotMoves(t *testing.T) {
	f := newFixture(t)
	svc := f.slots(true)
	f.claimable()
	slot := f.agent()
	f.report(svc, slot, idleFor(time.Hour))
	require.True(t, f.slot(svc, slot).Stalled())

	f.report(svc, slot, supervisorwatch.SlotReport{Phase: "wait_lock", PhaseAge: time.Second})

	assert.False(t, f.slot(svc, slot).Stalled())
}

func TestSlots_PausedIsDeliberateAndNeverStalled(t *testing.T) {
	f := newFixture(t)
	svc := f.slots(true)
	f.claimable()
	slot := f.agent()
	f.report(svc, slot, supervisorwatch.SlotReport{Phase: supervisorwatch.PhasePaused, PhaseAge: 24 * time.Hour, ErrorClass: "x", ErrorStreak: 99})

	assert.False(t, f.slot(svc, slot).Stalled())
}

func TestSlots_APausedSlotKeepsItsReasonAndIsLoudOnlyPastTheBound(t *testing.T) {
	f := newFixture(t)
	svc := f.slots(true)
	long, short, idle := f.agent(), f.agent(), f.agent()
	f.report(svc, long, supervisorwatch.SlotReport{Phase: supervisorwatch.PhasePaused, PhaseAge: 6 * time.Minute, PauseReason: "git push refused"})
	f.report(svc, short, supervisorwatch.SlotReport{Phase: supervisorwatch.PhasePaused, PhaseAge: 4 * time.Minute, PauseReason: "disk full"})
	f.report(svc, idle, supervisorwatch.SlotReport{Phase: supervisorwatch.PhaseIdle, PhaseAge: time.Hour, PauseReason: "stale"})

	assert.Equal(t, "git push refused", f.slot(svc, long).PauseReason)
	assert.True(t, f.slot(svc, long).PausedLong)
	assert.Equal(t, "disk full", f.slot(svc, short).PauseReason)
	assert.False(t, f.slot(svc, short).PausedLong, "paused for less than the bound")
	assert.False(t, f.slot(svc, idle).PausedLong)
	assert.Empty(t, f.slot(svc, idle).PauseReason, "a reason only goes with the paused phase")
}

func TestSlots_AnOfflinePausedSlotIsNotLoud(t *testing.T) {
	f := newFixture(t)
	svc := f.slots(true)
	slot := f.agent()
	f.report(svc, slot, supervisorwatch.SlotReport{Phase: supervisorwatch.PhasePaused, PhaseAge: time.Hour})
	f.exec(`UPDATE supervisor_slot_states SET reported_at = now() - interval '1 hour' WHERE agent_id = $1`, slot)

	assert.False(t, f.slot(svc, slot).PausedLong)
}

func TestReportSlot_CutsALongPauseReasonOnACharacterBoundary(t *testing.T) {
	f := newFixture(t)
	svc := f.slots(true)
	slot := f.agent()
	f.report(svc, slot, supervisorwatch.SlotReport{Phase: supervisorwatch.PhasePaused, PauseReason: strings.Repeat("é", supervisorwatch.MaxPauseReasonLen+50)})

	assert.Equal(t, strings.Repeat("é", supervisorwatch.MaxPauseReasonLen), f.slot(svc, slot).PauseReason)
}

func TestReportSlot_StoresTheReasonAndBoundsWhatItDoesNotKnow(t *testing.T) {
	f := newFixture(t)
	svc := f.slots(true)
	known, newer, old := f.agent(), f.agent(), f.agent()
	f.report(svc, known, supervisorwatch.SlotReport{Phase: supervisorwatch.PhaseIdle, Reason: "lock_queued", ReasonDetail: strings.Repeat("é", supervisorwatch.MaxReasonDetailLen+9)})
	f.report(svc, newer, supervisorwatch.SlotReport{Phase: supervisorwatch.PhaseIdle, Reason: "a_code_from_a_newer_supervisor"})
	f.report(svc, old, supervisorwatch.SlotReport{Phase: supervisorwatch.PhaseIdle})

	assert.Equal(t, "lock_queued", f.slot(svc, known).Reason)
	assert.Equal(t, strings.Repeat("é", supervisorwatch.MaxReasonDetailLen), f.slot(svc, known).ReasonDetail)
	assert.Equal(t, "other", f.slot(svc, newer).Reason, "an unknown code is kept as other, not refused")
	assert.Empty(t, f.slot(svc, old).Reason)
	assert.Equal(t, supervisorwatch.ReasonUnknown, supervisorwatch.ReasonOf(f.slot(svc, old).Reason))
}

func TestSlotsByReason_CountsOnlineNotRunningSlotsAndEveryCodeIsPresent(t *testing.T) {
	f := newFixture(t)
	svc := f.slots(true)
	f.report(svc, f.agent(), supervisorwatch.SlotReport{Phase: supervisorwatch.PhaseIdle, Reason: "no_claimable"})
	f.report(svc, f.agent(), supervisorwatch.SlotReport{Phase: supervisorwatch.PhaseIdle, Reason: "no_claimable"})
	f.report(svc, f.agent(), supervisorwatch.SlotReport{Phase: "wait_lock", Reason: "lock_queued"})
	f.report(svc, f.agent(), supervisorwatch.SlotReport{Phase: supervisorwatch.PhaseRunning, Reason: "no_claimable"})
	f.report(svc, f.agent(), supervisorwatch.SlotReport{Phase: supervisorwatch.PhaseIdle})

	counts, err := svc.SlotsByReason(f.ctx)
	require.NoError(t, err)
	assert.Equal(t, 2, counts[supervisorwatch.ReasonNoClaimable])
	assert.Equal(t, 1, counts[supervisorwatch.ReasonLockQueued])
	assert.Equal(t, 1, counts[supervisorwatch.ReasonUnknown])
	assert.Zero(t, counts[supervisorwatch.ReasonCooldown])
	assert.Len(t, counts, len(supervisorwatch.IdleReasons)+1)
}

func TestPausedSlots_CountsOnlyWhileWorkIsWaiting(t *testing.T) {
	f := newFixture(t)
	svc := f.slots(true)
	slot := f.agent()
	f.report(svc, slot, supervisorwatch.SlotReport{Phase: supervisorwatch.PhasePaused, PhaseAge: time.Minute})

	n, err := svc.PausedSlots(f.ctx)
	require.NoError(t, err)
	assert.Zero(t, n, "nothing to take, so the pause costs nothing")

	f.claimable()
	n, err = svc.PausedSlots(f.ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
}

func TestSlots_AReportOlderThanOfflineAfterIsOfflineNotStalled(t *testing.T) {
	f := newFixture(t)
	svc := f.slots(true)
	f.claimable()
	slot := f.agent()
	f.report(svc, slot, idleFor(time.Hour))
	f.exec(`UPDATE supervisor_slot_states SET reported_at = now() - $1::interval`, (supervisorwatch.OfflineAfter + time.Minute).String())

	got := f.slot(svc, slot)
	assert.True(t, got.Offline)
	assert.False(t, got.Stalled())
}

func TestSlots_ADisabledOrArchivedAgentHasNoSlot(t *testing.T) {
	f := newFixture(t)
	svc := f.slots(true)
	f.claimable()
	disabled, archived, live := f.agent(), f.agent(), f.agent()
	for _, a := range []uuid.UUID{disabled, archived, live} {
		f.report(svc, a, supervisorwatch.SlotReport{Phase: "backoff", ErrorClass: "claim_failed", ErrorStreak: 50})
	}
	f.exec(`UPDATE agents SET disabled_at = now() WHERE id = ANY($1)`, []uuid.UUID{disabled, archived})
	f.exec(`UPDATE agents SET archived_at = now() WHERE id = $1`, archived)

	assert.Nil(t, f.slot(svc, disabled))
	assert.Nil(t, f.slot(svc, archived))
	require.NotNil(t, f.slot(svc, live))
	count, err := svc.StalledSlots(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, count, "only the live slot is in the gauge")
}

func TestReportSlot_PhaseEnteredAtMovesOnlyWithThePhaseOrTheTicket(t *testing.T) {
	f := newFixture(t)
	svc := f.slots(true)
	slot := f.agent()
	t1, t2 := f.ticket("in_progress"), f.ticket("in_progress")
	entered := func() time.Time { return f.slot(svc, slot).PhaseEnteredAt }

	f.report(svc, slot, supervisorwatch.SlotReport{Phase: supervisorwatch.PhaseRunning, PhaseAge: time.Hour, TicketID: &t1})
	first := entered()
	assert.WithinDuration(t, time.Now().Add(-time.Hour), first, time.Minute)

	f.report(svc, slot, supervisorwatch.SlotReport{Phase: supervisorwatch.PhaseRunning, PhaseAge: 2 * time.Hour, TicketID: &t1})
	assert.Equal(t, first, entered(), "same phase and ticket: the age it reports now does not move it")

	f.report(svc, slot, supervisorwatch.SlotReport{Phase: supervisorwatch.PhaseRunning, PhaseAge: time.Minute, TicketID: &t2})
	assert.WithinDuration(t, time.Now().Add(-time.Minute), entered(), time.Minute, "a new ticket")

	f.report(svc, slot, supervisorwatch.SlotReport{Phase: "wait_lock", PhaseAge: 0, TicketID: &t2})
	assert.WithinDuration(t, time.Now(), entered(), time.Minute, "a new phase")
}

func TestReportSlot_RefusesWhatItCannotKeep(t *testing.T) {
	f := newFixture(t)
	svc := f.slots(true)
	slot := f.agent()
	zero := int64(0)
	for name, r := range map[string]supervisorwatch.SlotReport{
		"unknown phase":    {Phase: "sleeping"},
		"empty phase":      {},
		"negative age":     {Phase: "idle", PhaseAge: -time.Second},
		"absurd age":       {Phase: "idle", PhaseAge: supervisorwatch.MaxPhaseAge + time.Hour},
		"ticket zero":      {Phase: "idle", TicketID: &zero},
		"message as class": {Phase: "idle", ErrorClass: "claim failed: token abc"},
		"upper case class": {Phase: "idle", ErrorClass: "ClaimFailed"},
		"long class":       {Phase: "idle", ErrorClass: strings.Repeat("a", supervisorwatch.MaxErrorClassLen+1)},
		"negative streak":  {Phase: "idle", ErrorStreak: -1},
		"absurd streak":    {Phase: "idle", ErrorStreak: supervisorwatch.MaxErrorStreak + 1},
	} {
		t.Run(name, func(t *testing.T) {
			require.ErrorIs(t, svc.ReportSlot(f.ctx, slot, &r), supervisorwatch.ErrInvalidReport)
		})
	}
	assert.Nil(t, f.slot(svc, slot), "nothing was stored")
}

func TestSlots_AreEachTenantsOwnAndTheGaugeSumsThem(t *testing.T) {
	database := dbtest.WithDB(t)
	svc := supervisorwatch.NewService(supervisorwatch.NewPostgresStore(database), policy{true}, tickets(database)).WithBounds(testBounds)
	mk := func(stalled int) *fixture {
		tenant := dbtest.NewTenant(t, database)
		f := &fixture{t: t, database: database, tenant: tenant, ctx: db.WithTenant(context.Background(), tenant)}
		for range stalled {
			f.report(svc, f.agent(), supervisorwatch.SlotReport{Phase: "backoff", ErrorClass: "e", ErrorStreak: 10})
		}
		f.report(svc, f.agent(), supervisorwatch.SlotReport{Phase: "backoff"})
		return f
	}
	a, b := mk(2), mk(3)

	slotsA, err := svc.Slots(a.ctx)
	require.NoError(t, err)
	slotsB, err := svc.Slots(b.ctx)
	require.NoError(t, err)
	assert.Len(t, slotsA, 3)
	assert.Len(t, slotsB, 4)

	reg := prometheus.NewPedanticRegistry()
	require.NoError(t, reg.Register(supervisorwatch.NewCollector(svc, slog.NewTextHandler(io.Discard, nil))))
	mfs, err := reg.Gather()
	require.NoError(t, err)
	found := false
	for _, mf := range mfs {
		if mf.GetName() == "slophaus_supervisor_slots_stalled" {
			found = true
			require.Len(t, mf.GetMetric(), 1)
			assert.Empty(t, mf.GetMetric()[0].GetLabel(), "no tenant label")
			assert.EqualValues(t, 5, mf.GetMetric()[0].GetGauge().GetValue())
		}
	}
	assert.True(t, found)
}

func TestReportSlot_StoresIdleAndTakesItBackWhenOmitted(t *testing.T) {
	f := newFixture(t)
	svc := f.slots(true)
	slot := f.agent()

	f.report(svc, slot, supervisorwatch.SlotReport{Phase: supervisorwatch.PhaseIdle, Idle: true})
	assert.True(t, f.slot(svc, slot).Idle)

	f.report(svc, slot, supervisorwatch.SlotReport{Phase: supervisorwatch.PhaseRunning})
	assert.False(t, f.slot(svc, slot).Idle, "a report that does not say idle is not idle: nothing is restarted on a guess")
}
