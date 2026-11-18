package supervisorwatch_test

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/supervisorwatch"
)

func TestHandler_ReportsStatus(t *testing.T) {
	f := newFixture(t)
	f.ticket("open")
	f.polledAgo(10 * time.Minute)

	mux := http.NewServeMux()
	supervisorwatch.RegisterRoutes(humago.New(mux, huma.DefaultConfig("test", "0")), supervisorwatch.NewHandler(f.service(true), slog.DiscardHandler))
	req := httptest.NewRequest(http.MethodGet, "/api/supervisor/status", nil).WithContext(f.ctx)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var body supervisorwatch.StatusBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.EqualValues(t, 1, body.Claimable)
	assert.True(t, body.Offline)
	require.NotNil(t, body.LastPolledAt)
	assert.WithinDuration(t, time.Now().Add(-10*time.Minute), *body.LastPolledAt, time.Minute)
}

func TestHandler_NeverPolledIsNullNotOffline(t *testing.T) {
	f := newFixture(t)
	mux := http.NewServeMux()
	supervisorwatch.RegisterRoutes(humago.New(mux, huma.DefaultConfig("test", "0")), supervisorwatch.NewHandler(f.service(true), slog.DiscardHandler))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/supervisor/status", nil).WithContext(f.ctx))

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"last_polled_at":null`)
	var body supervisorwatch.StatusBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.False(t, body.Offline)
	assert.Nil(t, body.LastPolledAt)
}

func TestHandler_ReportsEachSlotAndWhyItIsStalled(t *testing.T) {
	f := newFixture(t)
	svc := f.slots(true)
	stuck, fine := f.agent(), f.agent()
	f.report(svc, stuck, supervisorwatch.SlotReport{Phase: "backoff", PhaseAge: time.Minute, ErrorClass: "claim_failed", ErrorStreak: 9, Reason: "backoff_after_error", ReasonDetail: "ticket #7"})
	f.report(svc, fine, supervisorwatch.SlotReport{Phase: supervisorwatch.PhasePaused, PhaseAge: 6 * time.Minute, PauseReason: "push refused"})

	mux := http.NewServeMux()
	supervisorwatch.RegisterRoutes(humago.New(mux, huma.DefaultConfig("test", "0")), supervisorwatch.NewHandler(svc, slog.DiscardHandler))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/supervisor/status", nil).WithContext(f.ctx))

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var body supervisorwatch.StatusBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	byAgent := map[uuid.UUID]supervisorwatch.SlotBody{}
	for _, s := range body.Slots {
		byAgent[s.AgentID] = s
	}
	require.Len(t, byAgent, 2)
	assert.True(t, byAgent[stuck].Stalled)
	assert.Equal(t, "repeating_error", byAgent[stuck].StallReason)
	assert.Equal(t, "claim_failed", byAgent[stuck].ErrorClass)
	assert.False(t, byAgent[fine].Stalled)
	assert.Empty(t, byAgent[fine].StallReason)

	assert.Equal(t, "push refused", byAgent[fine].PauseReason)
	require.NotNil(t, byAgent[fine].PausedAt)
	assert.WithinDuration(t, time.Now().Add(-6*time.Minute), *byAgent[fine].PausedAt, time.Minute)
	assert.True(t, byAgent[fine].PausedLong)
	assert.Nil(t, byAgent[stuck].PausedAt)
	assert.False(t, byAgent[stuck].PausedLong)
	assert.Empty(t, byAgent[stuck].PauseReason)

	assert.Equal(t, supervisorwatch.ReasonBackoff, byAgent[stuck].Reason)
	assert.Equal(t, "ticket #7", byAgent[stuck].ReasonDetail)
	assert.Equal(t, supervisorwatch.ReasonUnknown, byAgent[fine].Reason, "a supervisor that sent no reason shows unknown")
}

func TestHandler_ServesTheSameStatusOnTheSupervisorView(t *testing.T) {
	f := newFixture(t)
	svc := f.slots(true)
	f.report(svc, f.agent(), supervisorwatch.SlotReport{Phase: "idle", PhaseAge: time.Minute, Reason: "lock_queued", ReasonDetail: "behind #9"})

	mux := http.NewServeMux()
	supervisorwatch.RegisterRoutes(humago.New(mux, huma.DefaultConfig("test", "0")), supervisorwatch.NewHandler(svc, slog.DiscardHandler))
	read := func(path string) string {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil).WithContext(f.ctx))
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		return rec.Body.String()
	}

	view := read("/api/supervisor/view")
	assert.Contains(t, view, `"reason":"lock_queued"`)
	assert.Regexp(t, `"agent_name":"[^"]+"`, view)
	assert.JSONEq(t, read("/api/supervisor/status"), view)
}

func statusOf(t *testing.T, f *fixture, svc *supervisorwatch.Service) supervisorwatch.StatusBody {
	t.Helper()
	mux := http.NewServeMux()
	supervisorwatch.RegisterRoutes(humago.New(mux, huma.DefaultConfig("test", "0")), supervisorwatch.NewHandler(svc, slog.DiscardHandler))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/supervisor/status", nil).WithContext(f.ctx))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var body supervisorwatch.StatusBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	return body
}

// A slot reports the bundle its workspace installed; the board marks one that
// is not the bundle served now, and does not guess about one that never said.
func TestHandler_MarksSlotsWhoseBundleIsOlderThanCurrent(t *testing.T) {
	f := newFixture(t)
	svc := f.slots(true).WithVersions(supervisorwatch.Versions{Bundle: "bbbb2222", Protocol: 2})
	current, old, silent, junk := f.agent(), f.agent(), f.agent(), f.agent()
	f.report(svc, current, supervisorwatch.SlotReport{Phase: "idle", BundleHash: "bbbb2222"})
	f.report(svc, old, supervisorwatch.SlotReport{Phase: "idle", BundleHash: "aaaa1111"})
	f.report(svc, silent, supervisorwatch.SlotReport{Phase: "idle"})
	f.report(svc, junk, supervisorwatch.SlotReport{Phase: "idle", BundleHash: "not a hash\x1b[31m"})

	body := statusOf(t, f, svc)
	assert.Equal(t, "bbbb2222", body.CurrentBundleHash)
	byAgent := map[uuid.UUID]supervisorwatch.SlotBody{}
	for _, s := range body.Slots {
		byAgent[s.AgentID] = s
	}
	assert.Equal(t, supervisorwatch.VersionCurrent, byAgent[current].BundleStatus)
	assert.Equal(t, supervisorwatch.VersionStale, byAgent[old].BundleStatus)
	assert.Equal(t, "aaaa1111", byAgent[old].BundleHash)
	assert.Equal(t, supervisorwatch.VersionUnknown, byAgent[silent].BundleStatus)
	assert.Equal(t, supervisorwatch.VersionUnknown, byAgent[junk].BundleStatus)
	assert.Empty(t, byAgent[junk].BundleHash, "a value that is not a hash is dropped, not stored")
}

func TestHandler_MarksSupervisorsWhoseProtocolIsOlderThanCurrent(t *testing.T) {
	f := newFixture(t)
	svc := f.slots(true).WithVersions(supervisorwatch.Versions{Bundle: "x", Protocol: 2})
	for name, protocol := range map[string]int{"sup-new": 2, "sup-old": 1, "sup-none": 0} {
		f.exec(`INSERT INTO agents (id, name, kind, slot_cap) VALUES (gen_random_uuid(), $1, 'supervisor', 1)`, name)
		f.exec(`INSERT INTO worker_polls (agent_id, version, protocol) SELECT id, 'v', $2 FROM agents WHERE name = $1`, name, protocol)
	}

	// Not supervisors the board should list: a worker that polls, and a
	// supervisor that is disabled or archived.
	f.exec(`INSERT INTO worker_polls (agent_id, protocol) VALUES ($1, 1)`, f.agent())
	for name, columns := range map[string]string{"disabled": "disabled_at", "archived": "disabled_at, archived_at"} {
		id := uuid.New()
		values := "now()" + map[bool]string{true: ", now()"}[name == "archived"]
		f.exec(`INSERT INTO agents (id, name, kind, slot_cap, `+columns+`) VALUES ($1, $2, 'supervisor', 1, `+values+`)`, id, "gone-"+name)
		f.exec(`INSERT INTO worker_polls (agent_id, protocol) VALUES ($1, 1)`, id)
	}

	body := statusOf(t, f, svc)
	assert.Equal(t, 2, body.CurrentProtocol)
	got := map[string]supervisorwatch.PolledSupervisorBody{}
	for _, s := range body.Supervisors {
		got[s.AgentName] = s
	}
	require.Len(t, got, 3)
	assert.Equal(t, supervisorwatch.VersionCurrent, got["sup-new"].ProtocolStatus)
	assert.Equal(t, supervisorwatch.VersionStale, got["sup-old"].ProtocolStatus)
	assert.Equal(t, supervisorwatch.VersionUnknown, got["sup-none"].ProtocolStatus)
}
