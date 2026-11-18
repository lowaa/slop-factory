package llmusage_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// storeInflated writes a stored row the guard would refuse now: a session
// whose total was inflated before the fix and carried across a restart.
func (w *wire) storeInflated(t *testing.T, key string, requests, cacheRead int64) {
	t.Helper()
	r := piRecord(requests)
	_, err := w.database.Exec(w.ctx, `
INSERT INTO llm_usage (agent_id, transcript_key, harness, session_id, scope_hint, scope, provider, model, billing_mode,
    requests, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, reasoning_tokens, price_version, unpriced,
    started_at, ended_at)
VALUES ($1, $2, 'pi', 'pi-session', 'main', 'overhead', $3, $4, $5, $6, 0, 0, $7, 0, 0, 'v', true, $8, $9)`,
		w.builder.ID, key, r.Provider, r.Model, string(r.BillingMode), requests, cacheRead, r.StartedAt, r.EndedAt)
	require.NoError(t, err)
}

// The guard judges what a report adds, so a session with an inflated past is
// not locked out: its honest new turns are taken.
func TestAnHonestIncrementOnAnInflatedTotalIsAccepted(t *testing.T) {
	w := newWire(t, 100)
	w.storeInflated(t, "carried", 1_907, 34_100_000_000)

	r := piRecord(1_910)
	r.CacheReadTokens = 34_100_000_000 + 3*150_000
	rec := w.post(t, builderToken, piReport("carried", r).json(t))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}

func TestAnImpossibleIncrementIsStillRefused(t *testing.T) {
	w := newWire(t, 100)
	w.storeInflated(t, "carried", 1_907, 34_100_000_000)

	r := piRecord(1_910)
	r.CacheReadTokens = 34_100_000_000 + 3*2_000_001
	rec := w.post(t, builderToken, piReport("carried", r).json(t))
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "ceiling")
	var requests int64
	require.NoError(t, w.database.QueryRow(w.ctx, `SELECT requests FROM llm_usage WHERE transcript_key = 'carried'`).Scan(&requests))
	assert.Equal(t, int64(1_907), requests, "a refused report leaves the stored row alone")
}

func (w *wire) flags(t *testing.T) (usage []bool, daily map[bool]int64) {
	t.Helper()
	rows, err := w.database.Query(w.ctx, `SELECT unreliable FROM llm_usage ORDER BY transcript_key`)
	require.NoError(t, err)
	for rows.Next() {
		var f bool
		require.NoError(t, rows.Scan(&f))
		usage = append(usage, f)
	}
	rows.Close()
	daily = map[bool]int64{}
	rows, err = w.database.Query(w.ctx, `SELECT unreliable, sum(requests) FROM llm_usage_daily GROUP BY 1`)
	require.NoError(t, err)
	defer rows.Close()
	for rows.Next() {
		var f bool
		var n int64
		require.NoError(t, rows.Scan(&f, &n))
		daily[f] = n
	}
	return usage, daily
}

// The flag follows what sent the report, so an old supervisor is flagged
// whenever it reports, and a current one on the same day is not.
func TestReportsFromAnOldReporterAreFlaggedAndKeptApartByDay(t *testing.T) {
	w := newWire(t, 100)

	old := piReport("a-old", piRecord(2))
	old.Source = "supervisor"
	require.Equal(t, http.StatusOK, w.post(t, builderToken, old.json(t)).Code)
	current := piReport("b-current", piRecord(3))
	current.Source, current.ReporterRevision = "supervisor", 1
	require.Equal(t, http.StatusOK, w.post(t, builderToken, current.json(t)).Code)
	legacy := piReport("c-legacy", piRecord(5))
	require.Equal(t, http.StatusOK, w.post(t, builderToken, legacy.json(t)).Code)
	hook := piReport("d-hook", piRecord(7))
	hook.Source = "hook"
	require.Equal(t, http.StatusOK, w.post(t, builderToken, hook.json(t)).Code)

	usage, daily := w.flags(t)
	assert.Equal(t, []bool{true, false, false, false}, usage)
	assert.Equal(t, map[bool]int64{true: 2, false: 15}, daily, "one day holds a flagged and a reliable row")

	// Its totals still hold what the old reporter counted, so the row stays
	// flagged; what the current reporter adds goes to the reliable day.
	old.ReporterRevision = 1
	old.Records = piReport("a-old", piRecord(4)).Records
	require.Equal(t, http.StatusOK, w.post(t, builderToken, old.json(t)).Code)
	usage, daily = w.flags(t)
	assert.Equal(t, []bool{true, false, false, false}, usage)
	assert.Equal(t, map[bool]int64{true: 2, false: 17}, daily)
}

// Guidance charged from a flagged report is flagged, and stays so when a
// reliable report adds to it.
func TestGuidanceChargedFromAnOldReporterIsFlagged(t *testing.T) {
	w := newWire(t, 100)
	c := w.console(t)
	a := w.ticket(t, w.ctx)
	w.mustDeclare(t, c, a)
	flags := func() []bool {
		rows, err := w.database.Query(w.ctx, `SELECT unreliable FROM llm_guidance_usage ORDER BY transcript_key`)
		require.NoError(t, err)
		defer rows.Close()
		var out []bool
		for rows.Next() {
			var f bool
			require.NoError(t, rows.Scan(&f))
			out = append(out, f)
		}
		return out
	}

	old := piReport("old", piRecord(2))
	old.Source = "host"
	require.Equal(t, http.StatusOK, w.post(t, consoleToken, old.json(t)).Code)
	current := piReport("current", piRecord(2))
	current.Source, current.ReporterRevision = "host", 1
	require.Equal(t, http.StatusOK, w.post(t, consoleToken, current.json(t)).Code)
	assert.Equal(t, []bool{false, true}, flags(), "ordered by key: current, old")

	old.ReporterRevision = 1
	old.Records = piReport("old", piRecord(5)).Records
	require.Equal(t, http.StatusOK, w.post(t, consoleToken, old.json(t)).Code)
	assert.Equal(t, []bool{false, true}, flags())
}
