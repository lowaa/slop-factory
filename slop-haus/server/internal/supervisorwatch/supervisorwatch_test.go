package supervisorwatch_test

import (
	"errors"
	"net/http"
	"net/http/httptest"

	"context"
	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"github.com/prometheus/client_golang/prometheus"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/supervisorwatch"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

func tickets(database *db.DB) *ticket.Service {
	return ticket.NewService(ticket.NewPostgresStore(database), slog.DiscardHandler)
}

type policy struct{ enabled bool }

func (p policy) TicketProcessingEnabled(context.Context) (bool, error) { return p.enabled, nil }

type fixture struct {
	t        *testing.T
	database *db.DB
	tenant   uuid.UUID
	ctx      context.Context
}

func newFixture(t *testing.T) *fixture {
	database := dbtest.WithDB(t)
	tenant := dbtest.NewTenant(t, database)
	return &fixture{t: t, database: database, tenant: tenant, ctx: db.WithTenant(context.Background(), tenant)}
}

func (f *fixture) service(enabled bool) *supervisorwatch.Service {
	return supervisorwatch.NewService(supervisorwatch.NewPostgresStore(f.database), policy{enabled}, tickets(f.database))
}

func (f *fixture) exec(sql string, args ...any) {
	f.t.Helper()
	_, err := f.database.Exec(f.ctx, sql, args...)
	require.NoError(f.t, err)
}

func (f *fixture) agent() uuid.UUID {
	id := uuid.New()
	f.exec(`INSERT INTO agents (id, name) VALUES ($1, $2)`, id, "a-"+id.String())
	return id
}

func (f *fixture) ticket(status string) int64 {
	var id int64
	require.NoError(f.t, f.database.QueryRow(f.ctx, `INSERT INTO tickets (title, status) VALUES ('t', $1) RETURNING id`, status).Scan(&id))
	return id
}

func (f *fixture) polledAgo(d time.Duration) {
	f.exec(`INSERT INTO worker_polls (agent_id, last_polled_at) VALUES ($1, now() - $2::interval)`, f.agent(), d.String())
}

func TestStatus_ClaimableCountsOnlyWhatNothingHoldsBack(t *testing.T) {
	f := newFixture(t)
	f.ticket("open")
	f.ticket("done")
	f.ticket("in_merge_queue")
	waiting := f.ticket("open")
	dep := f.ticket("open")
	f.exec(`INSERT INTO ticket_deps (ticket_id, depends_on_ticket_id) VALUES ($1, $2)`, waiting, dep)
	lapsed := f.ticket("in_progress")
	f.exec(`INSERT INTO leases (ticket_id, attempt_number, agent_id, expires_at) VALUES ($1, 1, $2, now() - interval '1 minute')`, lapsed, f.agent())
	held := f.ticket("in_progress")
	f.exec(`INSERT INTO leases (ticket_id, attempt_number, agent_id, expires_at) VALUES ($1, 1, $2, now() + interval '1 hour')`, held, f.agent())
	later := f.ticket("open")
	f.exec(`UPDATE tickets SET blocked_until = now() + interval '1 hour' WHERE id = $1`, later)

	s, err := f.service(true).Status(f.ctx)
	require.NoError(t, err)
	// open, dep itself and the one whose lease lapsed; not the one blocked
	// on dep, the deferred one, the one a live lease holds, nor the done one or the one in the merge queue.
	assert.EqualValues(t, 3, s.Claimable)
}

func TestStatus_ProcessingOffMeansNothingIsWaitingForAnAgent(t *testing.T) {
	f := newFixture(t)
	f.ticket("open")

	s, err := f.service(false).Status(f.ctx)
	require.NoError(t, err)
	assert.Zero(t, s.Claimable)
}

func TestStatus_Offline(t *testing.T) {
	for name, tc := range map[string]struct {
		polled  *time.Duration
		offline bool
	}{
		"never polled is a tenant without a supervisor": {nil, false},
		"polled a minute ago":                           {ptr(time.Minute), false},
		"quiet past five minutes":                       {ptr(6 * time.Minute), true},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			if tc.polled != nil {
				f.polledAgo(*tc.polled)
			}
			s, err := f.service(true).Status(f.ctx)
			require.NoError(t, err)
			assert.Equal(t, tc.offline, s.Offline)
			assert.Equal(t, tc.polled != nil, s.LastPolledAt != nil)
		})
	}
}

func TestStatus_NewestPollOfAnyAgent(t *testing.T) {
	f := newFixture(t)
	f.polledAgo(time.Hour)
	f.polledAgo(time.Minute)

	s, err := f.service(true).Status(f.ctx)
	require.NoError(t, err)
	assert.False(t, s.Offline)
}

func TestCollector_AbsentTimestampsMeanNever(t *testing.T) {
	f := newFixture(t)
	f.ticket("open")
	c := supervisorwatch.NewCollector(f.service(true), slog.NewTextHandler(io.Discard, nil))

	require.NoError(t, testutil.CollectAndCompare(c, strings.NewReader(`
# HELP slophaus_claimable_tickets Open tickets nothing but an agent's taking them is holding up. Zero while ticket processing is off.
# TYPE slophaus_claimable_tickets gauge
slophaus_claimable_tickets 1
# HELP slophaus_supervisor_slots_by_reason Online supervisor slots that are not running a ticket, by the reason they report. One series per reason code, so a slot is never named.
# TYPE slophaus_supervisor_slots_by_reason gauge
slophaus_supervisor_slots_by_reason{reason="backoff_after_error"} 0
slophaus_supervisor_slots_by_reason{reason="cooldown"} 0
slophaus_supervisor_slots_by_reason{reason="fleet_paused"} 0
slophaus_supervisor_slots_by_reason{reason="lock_queued"} 0
slophaus_supervisor_slots_by_reason{reason="no_claimable"} 0
slophaus_supervisor_slots_by_reason{reason="other"} 0
slophaus_supervisor_slots_by_reason{reason="reviewing"} 0
slophaus_supervisor_slots_by_reason{reason="slot_paused"} 0
slophaus_supervisor_slots_by_reason{reason="starting"} 0
slophaus_supervisor_slots_by_reason{reason="unknown"} 0
slophaus_supervisor_slots_by_reason{reason="updating"} 0
slophaus_supervisor_slots_by_reason{reason="waiting_ci"} 0
# HELP slophaus_supervisor_slots_stalled Supervisor slots of enabled agents that are stalled: idle while they could take work, running without a lease heartbeat, or repeating one error.
# TYPE slophaus_supervisor_slots_stalled gauge
slophaus_supervisor_slots_stalled 0
# HELP slophaus_supervisor_slots_paused Paused supervisor slots of enabled agents, in tenants that have tickets waiting. How long they have been paused is the alert's to judge.
# TYPE slophaus_supervisor_slots_paused gauge
slophaus_supervisor_slots_paused 0
`)))
}

// The fleet is every tenant together: tickets summed, the newest poll and
// claim of any, and each tenant's own ticket-processing switch applied.
func TestCollector_CountsPausedSlotsWhileWorkIsWaiting(t *testing.T) {
	f := newFixture(t)
	svc := f.slots(true)
	f.claimable()
	f.report(svc, f.agent(), supervisorwatch.SlotReport{Phase: supervisorwatch.PhasePaused, PauseReason: "x"})
	f.report(svc, f.agent(), supervisorwatch.SlotReport{Phase: supervisorwatch.PhaseIdle})
	c := supervisorwatch.NewCollector(svc, slog.NewTextHandler(io.Discard, nil))

	require.NoError(t, testutil.CollectAndCompare(c, strings.NewReader(`
# HELP slophaus_supervisor_slots_paused Paused supervisor slots of enabled agents, in tenants that have tickets waiting. How long they have been paused is the alert's to judge.
# TYPE slophaus_supervisor_slots_paused gauge
slophaus_supervisor_slots_paused 1
`), "slophaus_supervisor_slots_paused"))
}

func TestCollector_CountsSlotsByReasonWithoutNamingThem(t *testing.T) {
	f := newFixture(t)
	svc := f.slots(true)
	f.report(svc, f.agent(), supervisorwatch.SlotReport{Phase: supervisorwatch.PhaseIdle, Reason: "no_claimable"})
	f.report(svc, f.agent(), supervisorwatch.SlotReport{Phase: "wait_checks", Reason: "waiting_ci"})
	c := supervisorwatch.NewCollector(svc, slog.NewTextHandler(io.Discard, nil))

	require.NoError(t, testutil.CollectAndCompare(c, strings.NewReader(`
# HELP slophaus_supervisor_slots_by_reason Online supervisor slots that are not running a ticket, by the reason they report. One series per reason code, so a slot is never named.
# TYPE slophaus_supervisor_slots_by_reason gauge
slophaus_supervisor_slots_by_reason{reason="backoff_after_error"} 0
slophaus_supervisor_slots_by_reason{reason="cooldown"} 0
slophaus_supervisor_slots_by_reason{reason="fleet_paused"} 0
slophaus_supervisor_slots_by_reason{reason="lock_queued"} 0
slophaus_supervisor_slots_by_reason{reason="no_claimable"} 1
slophaus_supervisor_slots_by_reason{reason="other"} 0
slophaus_supervisor_slots_by_reason{reason="reviewing"} 0
slophaus_supervisor_slots_by_reason{reason="slot_paused"} 0
slophaus_supervisor_slots_by_reason{reason="starting"} 0
slophaus_supervisor_slots_by_reason{reason="unknown"} 0
slophaus_supervisor_slots_by_reason{reason="updating"} 0
slophaus_supervisor_slots_by_reason{reason="waiting_ci"} 1
`), "slophaus_supervisor_slots_by_reason"))
}

func TestCollector_CombinesTenants(t *testing.T) {
	database := dbtest.WithDB(t)
	policies := &perTenant{enabled: map[uuid.UUID]bool{}}
	svc := supervisorwatch.NewService(supervisorwatch.NewPostgresStore(database), policies, tickets(database))
	c := supervisorwatch.NewCollector(svc, slog.NewTextHandler(io.Discard, nil))

	mk := func(enabled bool, open int, pollAgo, claimAgo time.Duration) {
		tenant := dbtest.NewTenant(t, database)
		policies.enabled[tenant] = enabled
		f := &fixture{t: t, database: database, tenant: tenant, ctx: db.WithTenant(context.Background(), tenant)}
		for range open {
			f.ticket("open")
		}
		f.exec(`INSERT INTO worker_polls (agent_id, last_polled_at) VALUES ($1, now() - $2::interval)`, f.agent(), pollAgo.String())
		f.exec(`INSERT INTO leases (ticket_id, attempt_number, agent_id, acquired_at, expires_at, released_at)
			VALUES ($1, 1, $2, now() - $3::interval, now() - $3::interval, now() - $3::interval)`, f.ticket("done"), f.agent(), claimAgo.String())
	}
	mk(true, 2, 10*time.Minute, 3*time.Hour)
	mk(true, 3, 2*time.Minute, 2*time.Hour)
	mk(false, 5, time.Second, time.Hour)

	families := map[string]float64{}
	reg := prometheus.NewPedanticRegistry()
	require.NoError(t, reg.Register(c))
	mfs, err := reg.Gather()
	require.NoError(t, err)
	for _, mf := range mfs {
		families[mf.GetName()] = mf.GetMetric()[0].GetGauge().GetValue()
	}
	assert.EqualValues(t, 5, families["slophaus_claimable_tickets"], "the switched-off tenant's tickets are not waiting for anyone")
	assert.InDelta(t, float64(time.Now().Add(-time.Second).Unix()), families["slophaus_last_supervisor_poll_timestamp_seconds"], 5)
	assert.InDelta(t, float64(time.Now().Add(-time.Hour).Unix()), families["slophaus_last_claim_timestamp_seconds"], 5)
}

type perTenant struct{ enabled map[uuid.UUID]bool }

func (p *perTenant) TicketProcessingEnabled(ctx context.Context) (bool, error) {
	id, err := db.TenantFrom(ctx)
	return p.enabled[id], err
}

type failingStore struct{ err error }

func (s failingStore) Facts(context.Context) (*supervisorwatch.Facts, error) { return nil, s.err }
func (s failingStore) ReportSlot(context.Context, uuid.UUID, *supervisorwatch.SlotReport) error {
	return s.err
}
func (s failingStore) Slots(context.Context) ([]*supervisorwatch.SlotRow, error) { return nil, s.err }
func (s failingStore) Supervisors(context.Context) ([]*supervisorwatch.SupervisorRow, error) {
	return nil, s.err
}
func (s failingStore) Tenants(context.Context) ([]uuid.UUID, error) {
	return []uuid.UUID{uuid.New()}, nil
}

// A read that fails is a gap in the series, never a zero that would read as
// an empty queue; and the status route says 500.
func TestFailuresAreNotReportedAsAnEmptyQueue(t *testing.T) {
	svc := supervisorwatch.NewService(failingStore{errors.New("db down")}, policy{true}, nil)

	_, err := svc.Status(context.Background())
	require.Error(t, err)

	reg := prometheus.NewPedanticRegistry()
	require.NoError(t, reg.Register(supervisorwatch.NewCollector(svc, slog.DiscardHandler)))
	_, err = reg.Gather()
	require.Error(t, err)

	mux := http.NewServeMux()
	supervisorwatch.RegisterRoutes(humago.New(mux, huma.DefaultConfig("test", "0")), supervisorwatch.NewHandler(svc, slog.DiscardHandler))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/supervisor/status", nil))
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

type slotsFailing struct {
	*supervisorwatch.PostgresStore
}

func (slotsFailing) Slots(context.Context) ([]*supervisorwatch.SlotRow, error) {
	return nil, errors.New("db down")
}

// Slots that cannot be read are a gap in the gauge, never a zero that would
// read as "nothing is stalled".
func TestCollector_UnreadableSlotsAreNotReportedAsNoneStalled(t *testing.T) {
	database := dbtest.WithDB(t)
	dbtest.NewTenant(t, database)
	svc := supervisorwatch.NewService(slotsFailing{supervisorwatch.NewPostgresStore(database)}, policy{true}, tickets(database))

	reg := prometheus.NewPedanticRegistry()
	require.NoError(t, reg.Register(supervisorwatch.NewCollector(svc, slog.DiscardHandler)))
	_, err := reg.Gather()
	require.Error(t, err)
}

func ptr[T any](v T) *T { return &v }
