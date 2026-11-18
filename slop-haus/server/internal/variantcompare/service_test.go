package variantcompare_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/attemptsignals"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/attemptverdict"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticketusage"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/variantcompare"
)

const (
	strong = "strong"
	cheap  = "cheap"
)

type fixture struct {
	t        *testing.T
	database *db.DB
	ctx      context.Context
	who      *authn.User
	svc      *variantcompare.Service
	verdicts *attemptverdict.Store
	versions map[string]uuid.UUID
}

func newFixture(t *testing.T, database *db.DB) *fixture {
	t.Helper()
	tenant := dbtest.NewTenant(t, database)
	f := &fixture{
		t: t, database: database, ctx: db.WithTenant(context.Background(), tenant),
		who: &authn.User{ID: dbtest.NewUser(t, database), TenantID: tenant}, versions: map[string]uuid.UUID{},
		verdicts: attemptverdict.NewStore(database),
	}
	f.svc = variantcompare.NewService(variantcompare.NewPostgresSource(database), attemptsignals.NewStore(database),
		ticketusage.NewPostgresStore(database), f.verdicts)
	for _, name := range []string{strong, cheap} {
		var id uuid.UUID
		require.NoError(t, database.QueryRow(f.ctx, `INSERT INTO variant_versions (name, version, harness) VALUES ($1, 1, 'pi') RETURNING id`, name).Scan(&id))
		f.versions[name] = id
	}
	return f
}

func (f *fixture) exec(sql string, args ...any) {
	f.t.Helper()
	_, err := f.database.Exec(f.ctx, sql, args...)
	require.NoError(f.t, err)
}

// ticket files a ticket whose first attempt is finished under the variant.
func (f *fixture) ticket(variant string) int64 {
	f.t.Helper()
	var id int64
	require.NoError(f.t, f.database.QueryRow(f.ctx, `INSERT INTO tickets (title, status) VALUES ('t', 'open') RETURNING id`).Scan(&id))
	f.exec(`UPDATE attempts SET status = 'done', variant_version_id = $2, promoted_at = now() WHERE ticket_id = $1 AND number = 1`, id, f.versions[variant])
	return id
}

// attempt adds a finished attempt under the variant and returns its ref.
func (f *fixture) attempt(tk int64, variant string) ticket.AttemptRef {
	f.t.Helper()
	var n int
	require.NoError(f.t, f.database.QueryRow(f.ctx, `SELECT max(number) + 1 FROM attempts WHERE ticket_id = $1`, tk).Scan(&n))
	f.exec(`INSERT INTO attempts (ticket_id, number, status, variant_version_id) VALUES ($1, $2, 'done', $3)`, tk, n, f.versions[variant])
	return ticket.AttemptRef{TicketID: tk, Number: n}
}

// lines gives the attempt stored signals saying it changed n lines.
func (f *fixture) lines(ref ticket.AttemptRef, n int) {
	f.t.Helper()
	f.exec(`INSERT INTO attempt_signals (ticket_id, attempt_number, detail) VALUES ($1, $2, $3)`, ref.TicketID, ref.Number,
		fmt.Sprintf(`{"size":{"known":true,"lines_added":%d,"lines_removed":0}}`, n))
}

// pair files a ticket with one strong and one cheap attempt, of the sizes.
func (f *fixture) pair(strongLines, cheapLines int) int64 {
	tk := f.ticket(strong)
	f.lines(ticket.AttemptRef{TicketID: tk, Number: 1}, strongLines)
	f.lines(f.attempt(tk, cheap), cheapLines)
	return tk
}

func (f *fixture) compare(tickets ...int64) *variantcompare.Result {
	f.t.Helper()
	res, err := f.svc.Compare(f.ctx, f.who, &variantcompare.Request{TicketIDs: tickets, Baseline: strong, Challengers: []string{cheap}})
	require.NoError(f.t, err)
	return res
}

func TestOnlyTicketsWhereBothVariantsRanArePaired(t *testing.T) {
	f := newFixture(t, dbtest.WithDB(t))
	both := f.pair(10, 20)
	onlyBaseline := f.ticket(strong)
	onlyChallenger := f.ticket(cheap)

	res := f.compare(both, onlyBaseline, onlyChallenger)

	require.Len(t, res.Pairs, 1)
	assert.Equal(t, 1, res.Pairs[0].Tickets)
	require.Len(t, res.Rows, 1)
	assert.Equal(t, both, res.Rows[0].TicketID)
	assert.Equal(t, 3, res.TicketsInSet)
}

func TestMedianAndSpreadOfTheDifferences(t *testing.T) {
	f := newFixture(t, dbtest.WithDB(t))
	var ids []int64
	for d := 1; d <= 5; d++ {
		ids = append(ids, f.pair(100, 100+d))
	}

	got := f.compare(ids...).Pairs[0].Measures["lines_changed"]

	assert.Equal(t, variantcompare.Summary{N: 5, Median: 3, Q1: 2, Q3: 4, Min: 1, Max: 5}, got)
}

func TestSeveralRunsOfAVariantUseTheirMedianOnTheTicket(t *testing.T) {
	f := newFixture(t, dbtest.WithDB(t))
	tk := f.ticket(strong)
	f.lines(ticket.AttemptRef{TicketID: tk, Number: 1}, 10)
	for _, n := range []int{20, 100, 30} {
		f.lines(f.attempt(tk, cheap), n)
	}

	res := f.compare(tk)

	row := res.Rows[0]
	assert.Equal(t, 3, row.ChallengerSide.Runs)
	assert.Equal(t, 1, row.BaselineSide.Runs)
	assert.Equal(t, 30.0, row.ChallengerSide.Measures["lines_changed"])
	assert.Equal(t, 20.0, row.Difference["lines_changed"])
}

func TestAMeasureUnknownOnOneSideHasNoDifference(t *testing.T) {
	f := newFixture(t, dbtest.WithDB(t))
	tk := f.ticket(strong)
	f.lines(f.attempt(tk, cheap), 5)

	res := f.compare(tk)

	assert.NotContains(t, res.Rows[0].Difference, "lines_changed")
	assert.NotContains(t, res.Pairs[0].Measures, "lines_changed", "unknown is not zero")
	assert.Contains(t, res.Rows[0].Difference, "human_words", "what is known on both sides still compares")
}

func TestVerdictsAreCountedFromTheVerdictStore(t *testing.T) {
	f := newFixture(t, dbtest.WithDB(t))
	a, b := f.pair(1, 1), f.pair(1, 1)
	set := func(tk int64, n int, verdict string) {
		_, err := f.verdicts.Set(f.ctx, ticket.AttemptRef{TicketID: tk, Number: n}, verdict, "", f.who.ID)
		require.NoError(t, err)
	}
	set(a, 1, attemptverdict.WouldMerge)
	set(a, 2, attemptverdict.WouldNotMerge)
	set(b, 2, attemptverdict.WouldMergeWithChanges)
	set(b, 2, attemptverdict.WouldNotMerge) // a changed mind counts once, as the current verdict

	pair := f.compare(a, b).Pairs[0]

	assert.Equal(t, variantcompare.Verdicts{WouldMerge: 1, Unjudged: 1}, pair.Verdicts.Baseline)
	assert.Equal(t, variantcompare.Verdicts{WouldNotMerge: 2}, pair.Verdicts.Challenger)
}

func TestHumanCostCountsWhatPeopleWroteInTheAttemptsConversation(t *testing.T) {
	f := newFixture(t, dbtest.WithDB(t))
	tk := f.pair(1, 1)
	agent := uuid.New()
	f.exec(`INSERT INTO agents (id, name) VALUES ($1, 'rc')`, agent)
	comment := func(attempt any, body string) {
		f.exec(`INSERT INTO ticket_comments (ticket_id, attempt_number, author_user_id, body) VALUES ($1, $2, $3, $4)`, tk, attempt, f.who.ID, body)
	}
	comment(2, "please use the helper")           // 4 words, to the cheap attempt
	comment(2, "no")                              // 1 word
	comment(nil, "ticket level goes to promoted") // 5 words, to the promoted attempt 1
	f.exec(`INSERT INTO ticket_comments (ticket_id, attempt_number, author_agent_id, on_behalf_of_user_id, body) VALUES ($1, 2, $2, $3, 'said through remote control')`,
		tk, agent, f.who.ID)

	row := f.compare(tk).Rows[0]

	assert.Equal(t, 5.0, row.ChallengerSide.Measures["human_words"])
	assert.Equal(t, 2.0, row.ChallengerSide.Measures["human_comments"])
	assert.Equal(t, 5.0, row.BaselineSide.Measures["human_words"])
	assert.Equal(t, 0.0, row.Difference["human_words"], "difference is challenger minus baseline")
	assert.Equal(t, 1.0, row.Difference["human_comments"])
}

func TestGuidanceChargedToAnAttemptIsCost(t *testing.T) {
	f := newFixture(t, dbtest.WithDB(t))
	tk := f.pair(1, 1)
	agent := uuid.New()
	f.exec(`INSERT INTO agents (id, name) VALUES ($1, 'g')`, agent)
	f.exec(`INSERT INTO llm_guidance_usage (agent_id, transcript_key, ticket_id, attempt_number, provider, model, harness, billing_mode,
	            requests, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, reasoning_tokens,
	            estimated_cost_microdollars, price_version, unpriced, ended_at)
	        VALUES ($1, 'k', $2, 2, 'p', 'm', 'pi', 'subscription', 1, 700, 30, 0, 0, 0, 55, 'v', false, now())`, agent, tk)

	res := f.compare(tk)

	assert.Equal(t, 700.0, res.Rows[0].ChallengerSide.Measures["guidance_input_tokens"])
	assert.Equal(t, 0.0, res.Rows[0].BaselineSide.Measures["guidance_input_tokens"])
	assert.Equal(t, 55.0, res.Rows[0].ChallengerSide.Measures["guidance_cost_subscription_micros"])
	assert.Equal(t, 700.0, res.Pairs[0].Measures["guidance_input_tokens"].Median)
}

func (f *fixture) run(ref ticket.AttemptRef, harnessVersion, model, bundle string) {
	f.t.Helper()
	agent := uuid.New()
	f.exec(`INSERT INTO agents (id, name) VALUES ($1, $2)`, agent, agent.String())
	var lease int64
	require.NoError(f.t, f.database.QueryRow(f.ctx, `
		INSERT INTO leases (ticket_id, attempt_number, agent_id, acquired_at, expires_at, released_at, release_reason)
		VALUES ($1, $2, $3, $4, $5, $5, 'x') RETURNING id`, ref.TicketID, ref.Number, agent, time.Now().Add(-2*time.Hour), time.Now().Add(-time.Hour)).Scan(&lease))
	f.exec(`INSERT INTO lease_configs (lease_id, revision, settings, harness_version, model, bundle_hash) VALUES ($1, 0, '{}', $2, $3, $4)`,
		lease, harnessVersion, model, bundle)
}

func TestRunWithDifferencesBesidesTheVariantAreFlagged(t *testing.T) {
	f := newFixture(t, dbtest.WithDB(t))
	clean, drifted, mixed := f.pair(1, 1), f.pair(1, 1), f.pair(1, 1)
	for _, tk := range []int64{clean, drifted, mixed} {
		f.run(ticket.AttemptRef{TicketID: tk, Number: 1}, "1.0", "big", "bundle-a")
	}
	f.run(ticket.AttemptRef{TicketID: clean, Number: 2}, "1.0", "small", "bundle-a") // the model is what the variant sets
	f.run(ticket.AttemptRef{TicketID: drifted, Number: 2}, "1.1", "small", "bundle-a")
	f.run(ticket.AttemptRef{TicketID: mixed, Number: 2}, "1.0", "small", "bundle-a")
	f.run(ticket.AttemptRef{TicketID: mixed, Number: 2}, "1.0", "tiny", "bundle-a")

	res := f.compare(clean, drifted, mixed)

	flags := map[int64][]string{}
	for _, r := range res.Rows {
		flags[r.TicketID] = r.RunWithFlags
	}
	assert.Empty(t, flags[clean])
	assert.Equal(t, []string{"harness_version_differs"}, flags[drifted])
	assert.Equal(t, []string{"challenger_mixed_model"}, flags[mixed])
	assert.Equal(t, 2, res.Pairs[0].FlaggedTickets)
}

func TestTheSizeFilterUsesTheOriginalAttempt(t *testing.T) {
	f := newFixture(t, dbtest.WithDB(t))
	small, big, huge, unknown := f.pair(10, 1), f.pair(500, 1), f.pair(5000, 1), f.ticket(strong)
	f.lines(f.attempt(unknown, cheap), 1)
	lo, hi := 100, 1000

	res, err := f.svc.Compare(f.ctx, f.who, &variantcompare.Request{TicketIDs: []int64{small, big, huge, unknown}, MinDiffLines: &lo, MaxDiffLines: &hi,
		Baseline: strong, Challengers: []string{cheap}})

	require.NoError(t, err)
	require.Len(t, res.Rows, 1)
	assert.Equal(t, big, res.Rows[0].TicketID)
	assert.Equal(t, 3, res.SizeFiltered)
}

func TestAReplayBatchChoosesTheTickets(t *testing.T) {
	f := newFixture(t, dbtest.WithDB(t))
	in, out := f.pair(1, 1), f.pair(1, 1)
	batch := uuid.New()
	f.exec(`UPDATE attempts SET replay_batch_id = $2 WHERE ticket_id = $1 AND number = 2`, in, batch)

	res, err := f.svc.Compare(f.ctx, f.who, &variantcompare.Request{ReplayBatchID: &batch, Baseline: strong, Challengers: []string{cheap}})

	require.NoError(t, err)
	require.Len(t, res.Rows, 1)
	assert.Equal(t, in, res.Rows[0].TicketID)
	assert.NotEqual(t, out, res.Rows[0].TicketID)
}

func TestUnfinishedAttemptsAreLeftOutAndCounted(t *testing.T) {
	f := newFixture(t, dbtest.WithDB(t))
	tk := f.pair(1, 1)
	f.exec(`INSERT INTO attempts (ticket_id, number, status, variant_version_id) VALUES ($1, 3, 'in_progress', $2)`, tk, f.versions[cheap])

	res := f.compare(tk)

	assert.Equal(t, 1, res.UnfinishedAttempts)
	assert.Equal(t, 1, res.Rows[0].ChallengerSide.Runs)
}

func TestAnotherTenantSeesNothingOfTheTickets(t *testing.T) {
	database := dbtest.WithDB(t)
	a := newFixture(t, database)
	tk := a.pair(1, 2)
	b := newFixture(t, database)

	res, err := b.svc.Compare(b.ctx, b.who, &variantcompare.Request{TicketIDs: []int64{tk}, Baseline: strong, Challengers: []string{cheap}})

	require.NoError(t, err)
	assert.Empty(t, res.Rows)
	assert.Zero(t, res.Pairs[0].Tickets)
	assert.Empty(t, res.Pairs[0].Measures)
}

func TestTheSetIsBounded(t *testing.T) {
	f := newFixture(t, dbtest.WithDB(t))
	ids := make([]int64, variantcompare.MaxTickets+1)
	for i := range ids {
		ids[i] = int64(i + 1)
	}

	_, err := f.svc.Compare(f.ctx, f.who, &variantcompare.Request{TicketIDs: ids, Baseline: strong, Challengers: []string{cheap}})

	assert.ErrorIs(t, err, variantcompare.ErrTooManyTickets)
}

func TestMalformedRequestsAndAgentsAreRefused(t *testing.T) {
	f := newFixture(t, dbtest.WithDB(t))
	batch := uuid.New()
	for name, req := range map[string]*variantcompare.Request{
		"no set":               {Baseline: strong, Challengers: []string{cheap}},
		"batch and tickets":    {ReplayBatchID: &batch, TicketIDs: []int64{1}, Baseline: strong, Challengers: []string{cheap}},
		"no challenger":        {TicketIDs: []int64{1}, Baseline: strong},
		"baseline challenging": {TicketIDs: []int64{1}, Baseline: strong, Challengers: []string{strong}},
	} {
		_, err := f.svc.Compare(f.ctx, f.who, req)
		assert.ErrorIs(t, err, variantcompare.ErrInvalid, name)
	}

	_, err := f.svc.Compare(f.ctx, nil, &variantcompare.Request{TicketIDs: []int64{1}, Baseline: strong, Challengers: []string{cheap}})
	assert.ErrorIs(t, err, authn.ErrNotPerson)
}

func (f *fixture) workerUsage(ref ticket.AttemptRef, key, mode string, input, output int64, cost *int64, unpriced bool) {
	f.t.Helper()
	agent := uuid.New()
	f.exec(`INSERT INTO agents (id, name) VALUES ($1, $2)`, agent, agent.String())
	f.exec(`INSERT INTO llm_usage (agent_id, transcript_key, harness, session_id, scope_hint, scope, ticket_id, attempt_number, provider, model,
	            billing_mode, requests, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, reasoning_tokens,
	            estimated_cost_microdollars, price_version, unpriced, started_at, ended_at)
	        VALUES ($1, $2, 'pi', 's', 'main', 'ticket', $3, $4, 'p', 'm', $5, 1, $6, $7, 0, 0, 0, $8, 'v', $9, now(), now())`,
		agent, key, ref.TicketID, ref.Number, mode, input, output, cost, unpriced)
}

func TestWorkerCostKeepsBillingModesApartAndLeavesOutUnpricedUsage(t *testing.T) {
	f := newFixture(t, dbtest.WithDB(t))
	tk := f.pair(1, 1)
	base, other := ticket.AttemptRef{TicketID: tk, Number: 1}, ticket.AttemptRef{TicketID: tk, Number: 2}
	cost := func(v int64) *int64 { return &v }
	f.workerUsage(base, "a", "metered", 1000, 100, cost(500), false)
	f.workerUsage(other, "b", "subscription", 300, 30, cost(40), false)
	f.workerUsage(other, "c", "metered", 200, 20, nil, true)
	f.workerUsage(other, "d", "metered", 0, 0, cost(70), false) // priced beside unpriced: a lower bound only

	row := f.compare(tk).Rows[0]

	assert.Equal(t, 500.0, row.BaselineSide.Measures["worker_cost_metered_micros"])
	assert.Equal(t, 40.0, row.ChallengerSide.Measures["worker_cost_subscription_micros"])
	assert.NotContains(t, row.ChallengerSide.Measures, "worker_cost_metered_micros", "a cost missing unpriced usage is unknown, not the priced part")
	assert.NotContains(t, row.Difference, "worker_cost_metered_micros")
	assert.NotContains(t, row.BaselineSide.Measures, "worker_cost_subscription_micros", "no subscription usage reported is unknown")
	assert.Equal(t, 500.0, row.ChallengerSide.Measures["worker_input_tokens"], "tokens add across the modes")
	assert.Equal(t, -500.0, row.Difference["worker_input_tokens"])
}

func TestQualityMeasuresReadChecksAndChangesRequested(t *testing.T) {
	f := newFixture(t, dbtest.WithDB(t))
	tk := f.pair(1, 1)
	f.exec(`UPDATE attempt_signals SET detail = jsonb_set(detail, '{checks}', '{"passed":"no"}') WHERE ticket_id = $1 AND attempt_number = 2`, tk)
	f.exec(`UPDATE attempt_signals SET detail = jsonb_set(detail, '{checks}', '{"passed":"yes"}') WHERE ticket_id = $1 AND attempt_number = 1`, tk)
	f.exec(`UPDATE attempts SET changes_requested_at = now() WHERE ticket_id = $1 AND number = 2`, tk)

	row := f.compare(tk).Rows[0]

	assert.Equal(t, -1.0, row.Difference["checks_passed"])
	assert.Equal(t, 1.0, row.Difference["changes_requested"])
}
