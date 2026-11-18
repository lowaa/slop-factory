package llmusage_test

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	usagelib "github.com/lowaa/slop-factory/go-llm-usage"
	"github.com/lowaa/slop-factory/go-llm-usage/pricing"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/agent"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/llmusage"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

const consoleToken = "agent-console"

// console is an agent the person marked as their own session, signed in
// under consoleToken.
func (w *wire) console(t *testing.T) *agent.Agent {
	t.Helper()
	console, err := w.agents.CreateKind(w.ctx, "console", agent.KindConsole)
	require.NoError(t, err)
	got, err := w.agents.Get(w.ctx, console.ID)
	require.NoError(t, err)
	w.extra[consoleToken] = got
	return got
}

func (w *wire) declare(t *testing.T, who *agent.Agent, ticketID *int64) (*llmusage.Declaration, error) {
	t.Helper()
	return w.svc.Declare(authn.NewContext(context.Background(), who), ticketID, nil)
}

func (w *wire) mustDeclare(t *testing.T, who *agent.Agent, ticketID int64) {
	t.Helper()
	_, err := w.declare(t, who, &ticketID)
	require.NoError(t, err)
}

func (w *wire) mustDeclareNone(t *testing.T, who *agent.Agent) {
	t.Helper()
	_, err := w.declare(t, who, nil)
	require.NoError(t, err)
}

type guidanceRow struct {
	TicketID                                       int64
	Requests, Input, Output, CacheRead, CacheWrite int64
	Reported, Estimated                            *int64
}

func (w *wire) guidance(t *testing.T) []guidanceRow {
	t.Helper()
	rows, err := w.database.Query(w.ctx, `
		SELECT ticket_id, requests, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens,
		       reported_cost_microdollars, estimated_cost_microdollars
		  FROM llm_guidance_usage ORDER BY ticket_id, transcript_key`)
	require.NoError(t, err)
	defer rows.Close()
	var out []guidanceRow
	for rows.Next() {
		var r guidanceRow
		require.NoError(t, rows.Scan(&r.TicketID, &r.Requests, &r.Input, &r.Output, &r.CacheRead, &r.CacheWrite, &r.Reported, &r.Estimated))
		out = append(out, r)
	}
	require.NoError(t, rows.Err())
	return out
}

// estimateOf is what the pricer says n requests of piRecord's tokens cost: a
// delta is priced on its own tokens, not subtracted from two estimates.
func estimateOf(n int64) int64 {
	r := spanning(0, 1, n)
	pricing.Default().Estimate(&r)
	return *r.EstimatedCostMicros
}

func ptr[T any](v T) *T { return &v }

func (w *wire) report(t *testing.T, token string, requests int64) {
	t.Helper()
	rec := w.post(t, token, piReport("pi-session", piRecord(requests)).json(t))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.JSONEq(t, `{"scope":"overhead"}`, rec.Body.String())
}

// The charge is each report's difference to what was charged or passed
// before, so a ticket is billed for the stretch it was declared for.
func TestAConsolesUsageIsSplitByDeclaration(t *testing.T) {
	w := newWire(t, 100)
	c := w.console(t)
	a, b := w.ticket(t, w.ctx), w.ticket(t, w.ctx)

	w.report(t, consoleToken, 2) // before the first declaration: overhead
	declared, err := w.declare(t, c, &a)
	require.NoError(t, err)
	assert.Equal(t, "t", declared.TicketTitle)
	assert.Empty(t, w.guidance(t), "declaring charges nothing by itself")
	w.report(t, consoleToken, 5)
	w.mustDeclare(t, c, b)
	w.report(t, consoleToken, 9)
	w.mustDeclareNone(t, c)
	w.report(t, consoleToken, 12) // after null: overhead again

	want := func(ticket, n int64) guidanceRow {
		r := spanning(0, 1, n)
		return guidanceRow{TicketID: ticket, Requests: n, Input: r.InputTokens, Output: r.OutputTokens,
			CacheRead: r.CacheReadTokens, CacheWrite: r.CacheWriteTokens, Reported: ptr(1000 * n), Estimated: ptr(estimateOf(n))}
	}
	assert.Equal(t, []guidanceRow{want(a, 3), want(b, 4)}, w.guidance(t))

	// llm_usage and the metrics are as if nothing was declared.
	var requests int64
	require.NoError(t, w.database.QueryRow(w.ctx, `SELECT requests FROM llm_usage`).Scan(&requests))
	assert.Equal(t, int64(12), requests)
	assert.Equal(t, 12.0, w.counter(t, "llm_requests_total"))
}

func TestDeclaringAgainChargesTheSameTicketOnce(t *testing.T) {
	w := newWire(t, 100)
	c := w.console(t)
	a := w.ticket(t, w.ctx)

	w.report(t, consoleToken, 2)
	w.mustDeclare(t, c, a)
	w.report(t, consoleToken, 5)
	w.mustDeclare(t, c, a)
	w.report(t, consoleToken, 6)

	rows := w.guidance(t)
	require.Len(t, rows, 1)
	assert.Equal(t, int64(4), rows[0].Requests)
}

// A hook that fires twice is the same totals again, and a report that
// arrives late carries lower totals than the mark: neither is charged.
func TestRepeatedAndOutOfOrderReportsAreNotChargedAgain(t *testing.T) {
	w := newWire(t, 100)
	c := w.console(t)
	a := w.ticket(t, w.ctx)
	w.mustDeclare(t, c, a)

	w.report(t, consoleToken, 5)
	w.report(t, consoleToken, 5)
	w.report(t, consoleToken, 3) // out of order
	w.report(t, consoleToken, 5)
	rows := w.guidance(t)
	require.Len(t, rows, 1)
	assert.Equal(t, int64(5), rows[0].Requests)
	assert.Equal(t, ptr(int64(5000)), rows[0].Reported)

	w.report(t, consoleToken, 7)
	assert.Equal(t, int64(7), w.guidance(t)[0].Requests, "growth past the mark is charged, from the mark")
}

// Starting a declaration does not claim what the scope used before it.
func TestUsageBeforeTheFirstDeclarationStaysOverhead(t *testing.T) {
	w := newWire(t, 100)
	c := w.console(t)
	a := w.ticket(t, w.ctx)
	w.report(t, consoleToken, 8)

	w.mustDeclare(t, c, a)
	w.report(t, consoleToken, 8)

	assert.Empty(t, w.guidance(t))
}

func TestARemoteControlConversationsUsageIsSplitByDeclaration(t *testing.T) {
	w := newWire(t, 100)
	c := w.conductor(t, w.ctx, "c1")
	w.extra["conductor"] = c
	conv := w.conversation(t, w.ctx, c)
	a := w.ticket(t, w.ctx)
	conversationReport := func(n int64) {
		t.Helper()
		rec := w.post(t, "conductor", w.conversationReport(conv, n))
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	}

	conversationReport(2)
	declared, err := w.declare(t, c, &a)
	require.NoError(t, err)
	assert.Equal(t, &conv, declared.ConversationID, "a conductor's declaration follows its live conversation")
	conversationReport(6)
	conversationReport(6)

	rows := w.guidance(t)
	require.Len(t, rows, 1)
	assert.Equal(t, a, rows[0].TicketID)
	assert.Equal(t, int64(4), rows[0].Requests)
}

// A console's declaration is the agent's: a worker's usage, attributed by its
// leases, and a console's other scopes are not touched by it.
func TestAWorkersReportIsNotSplitByAnotherAgentsDeclaration(t *testing.T) {
	w := newWire(t, 100)
	c := w.console(t)
	a := w.ticket(t, w.ctx)
	w.mustDeclare(t, c, a)

	w.report(t, builderToken, 5)

	assert.Empty(t, w.guidance(t))
}

// A transcript a lease attributed to a ticket is that ticket's already.
func TestATicketAttributedReportIsNotChargedAsGuidance(t *testing.T) {
	w := newWire(t, 100)
	c := w.console(t)
	leased, declared := w.ticket(t, w.ctx), w.ticket(t, w.ctx)
	w.lease(t, w.ctx, c.ID, lease{ticket: leased, from: 0, to: 60})
	w.mustDeclare(t, c, declared)

	rec := w.post(t, consoleToken, piReport("pi-session", piRecord(5)).json(t))

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Empty(t, w.guidance(t))

	// Were the transcript attributed to overhead later, the tokens already
	// passed over are not charged to the declaration.
	_, err := w.database.Exec(w.ctx, `DELETE FROM leases`)
	require.NoError(t, err)
	w.report(t, consoleToken, 8)
	rows := w.guidance(t)
	require.Len(t, rows, 1)
	assert.Equal(t, int64(3), rows[0].Requests)
}

func TestDeclaringRefusals(t *testing.T) {
	w := newWire(t, 100)
	console := w.console(t)
	mine := w.ticket(t, w.ctx)
	// Ticket numbers are per tenant: theirs is one this tenant has no number for.
	w.ticket(t, w.otherCtx)
	theirs := w.ticket(t, w.otherCtx)
	missing := int64(999_999)
	idle := w.conductor(t, w.ctx, "idle")

	_, err := w.declare(t, console, &theirs)
	assert.ErrorIs(t, err, llmusage.ErrTicketNotFound, "another tenant's ticket")
	_, err = w.declare(t, console, &missing)
	assert.ErrorIs(t, err, llmusage.ErrTicketNotFound)
	_, err = w.declare(t, w.builder, &mine)
	assert.ErrorIs(t, err, llmusage.ErrNotDeclarer, "a worker")
	_, err = w.declare(t, idle, &mine)
	assert.ErrorIs(t, err, llmusage.ErrNoLiveConversation)

	_, err = w.database.Exec(w.ctx, `UPDATE agents SET disabled_at = now() WHERE id = $1`, console.ID)
	require.NoError(t, err)
	_, err = w.declare(t, console, &mine)
	assert.ErrorIs(t, err, llmusage.ErrNotDeclarer, "a disabled console")

	var n int
	require.NoError(t, w.database.QueryRow(w.ctx, `SELECT count(*) FROM usage_declarations`).Scan(&n))
	assert.Zero(t, n, "none of them left a declaration")
}

func TestAConductorWithAStoppedConversationHasNothingToDeclareOn(t *testing.T) {
	w := newWire(t, 100)
	c := w.conductor(t, w.ctx, "c1")
	conv := w.conversation(t, w.ctx, c)
	a := w.ticket(t, w.ctx)
	_, err := w.database.Exec(w.ctx, `UPDATE conductor_conversations SET state = 'stopped' WHERE id = $1`, conv)
	require.NoError(t, err)

	_, err = w.declare(t, c, &a)

	assert.ErrorIs(t, err, llmusage.ErrNoLiveConversation)
}

// Deleting a ticket takes the guidance charged to it, and a declaration that
// named it reads as none: what follows is overhead.
func TestDeletingADeclaredTicket(t *testing.T) {
	w := newWire(t, 100)
	c := w.console(t)
	a := w.ticket(t, w.ctx)
	w.mustDeclare(t, c, a)
	w.report(t, consoleToken, 2)
	w.report(t, consoleToken, 5)
	require.Len(t, w.guidance(t), 1)

	_, err := w.database.Exec(w.ctx, `DELETE FROM tickets WHERE id = $1`, a)
	require.NoError(t, err)
	assert.Empty(t, w.guidance(t))
	w.report(t, consoleToken, 9)
	assert.Empty(t, w.guidance(t))
}

func TestGuidanceIsIsolatedBetweenTenants(t *testing.T) {
	w := newWire(t, 100)
	c := w.console(t)
	a := w.ticket(t, w.ctx)
	w.mustDeclare(t, c, a)
	w.report(t, consoleToken, 2)
	w.report(t, consoleToken, 5)

	for _, table := range []string{"llm_guidance_usage", "llm_usage_marks", "usage_declarations"} {
		var n int
		require.NoError(t, w.database.QueryRow(w.otherCtx, `SELECT count(*) FROM `+table).Scan(&n))
		assert.Zero(t, n, table)
	}
}

func modelRecord(model string, n int64, withCost bool) usagelib.Record {
	r := piRecord(n)
	r.Model = model
	if !withCost {
		r.ReportedCostMicros = nil
	}
	return r
}

// Each record key has its own mark: a report where one model grows and the
// other does not charges only the growth, to the right model.
func TestEachModelHasItsOwnMark(t *testing.T) {
	w := newWire(t, 100)
	c := w.console(t)
	a := w.ticket(t, w.ctx)
	post := func(opus, sonnet int64) {
		t.Helper()
		body := piReport("pi-session", modelRecord("claude-opus-5-5", opus, true), modelRecord("claude-sonnet-5-5", sonnet, true))
		require.Equal(t, http.StatusOK, w.post(t, consoleToken, body.json(t)).Code)
	}
	post(1, 1)
	w.mustDeclare(t, c, a)
	post(4, 1)
	post(4, 6)

	rows, err := w.database.Query(w.ctx, `SELECT model, requests, reported_cost_microdollars FROM llm_guidance_usage ORDER BY model`)
	require.NoError(t, err)
	defer rows.Close()
	got := map[string][2]int64{}
	for rows.Next() {
		var model string
		var requests, reported int64
		require.NoError(t, rows.Scan(&model, &requests, &reported))
		got[model] = [2]int64{requests, reported}
	}
	require.NoError(t, rows.Err())
	assert.Equal(t, map[string][2]int64{
		"claude-opus-5-5":   {3, 3000},
		"claude-sonnet-5-5": {5, 5000},
	}, got)
}

// A harness that reports no cost leaves it unknown, not zero, in what is
// charged -- before and after the usage grows.
func TestAnUnreportedCostStaysUnknown(t *testing.T) {
	w := newWire(t, 100)
	c := w.console(t)
	a := w.ticket(t, w.ctx)
	post := func(n int64) {
		t.Helper()
		require.Equal(t, http.StatusOK, w.post(t, consoleToken, piReport("pi-session", modelRecord("claude-opus-5-5", n, false)).json(t)).Code)
	}
	post(1)
	w.mustDeclare(t, c, a)
	post(3)
	post(3)
	post(5)

	rows := w.guidance(t)
	require.Len(t, rows, 1)
	assert.Equal(t, int64(4), rows[0].Requests)
	assert.Nil(t, rows[0].Reported)
	assert.NotNil(t, rows[0].Estimated, "the server still prices the tokens")
}

// holdAgentLock holds the agent lock a declaration and a report both take,
// in a transaction of its own until release is called.
func (w *wire) holdAgentLock(t *testing.T, agentID uuid.UUID) (release func()) {
	t.Helper()
	held, releaseLock, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		_ = w.database.InTx(w.ctx, func(q db.Querier) error {
			if err := db.TakeSubjectAdvisoryLock(w.ctx, q, db.LLMUsageLockKey, llmusage.AgentLockSubject(agentID)); err != nil {
				return err
			}
			close(held)
			<-releaseLock
			return nil
		})
	}()
	<-held
	var once sync.Once
	release = func() { once.Do(func() { close(releaseLock) }); <-done }
	// So a failing assertion does not leave the lock held, and the test hung.
	t.Cleanup(release)
	return release
}

// waitForLockWaiter returns once a backend is blocked on an advisory lock,
// read from the database rather than guessed with a sleep.
func (w *wire) waitForLockWaiter(t *testing.T) {
	t.Helper()
	require.Eventually(t, func() bool {
		var n int
		require.NoError(t, w.database.Global().QueryRow(context.Background(),
			`SELECT count(*) FROM pg_locks WHERE locktype = 'advisory' AND NOT granted`).Scan(&n))
		return n > 0
	}, 10*time.Second, 5*time.Millisecond)
}

// A declaration and a report of one agent take turns: each waits for the
// other's transaction, so a declaration never falls in the middle of a report
// (nor its snapshot of the totals in the middle of one being stored).
func TestADeclarationAndAReportOfOneAgentTakeTurns(t *testing.T) {
	w := newWire(t, 100)
	c := w.console(t)
	a := w.ticket(t, w.ctx)

	t.Run("a declaration waits for a report in flight", func(t *testing.T) {
		release := w.holdAgentLock(t, c.ID)
		done := make(chan error, 1)
		go func() { _, err := w.declare(t, c, &a); done <- err }()
		w.waitForLockWaiter(t)
		select {
		case err := <-done:
			t.Fatalf("declared while the agent's lock was held: %v", err)
		default:
		}
		release()
		require.NoError(t, <-done)
	})

	t.Run("a report waits for a declaration in flight", func(t *testing.T) {
		release := w.holdAgentLock(t, c.ID)
		done := make(chan int, 1)
		go func() { done <- w.post(t, consoleToken, piReport("pi-session", piRecord(2)).json(t)).Code }()
		w.waitForLockWaiter(t)
		select {
		case code := <-done:
			t.Fatalf("reported (%d) while the agent's lock was held", code)
		default:
		}
		release()
		assert.Equal(t, http.StatusOK, <-done)
	})
}

func (w *wire) addAttempt(t *testing.T, ticketID int64, number int, status string) {
	t.Helper()
	_, err := w.database.Exec(w.ctx, `INSERT INTO attempts (ticket_id, number, status) VALUES ($1, $2, $3)`, ticketID, number, status)
	require.NoError(t, err)
}

func (w *wire) declareAttempt(t *testing.T, who *agent.Agent, ticketID int64, attempt *int) (*llmusage.Declaration, error) {
	t.Helper()
	return w.svc.Declare(authn.NewContext(context.Background(), who), &ticketID, attempt)
}

type attemptCharge struct {
	TicketID int64
	Attempt  *int
	Requests int64
}

func (w *wire) charges(t *testing.T) []attemptCharge {
	t.Helper()
	rows, err := w.database.Query(w.ctx, `SELECT ticket_id, attempt_number, requests FROM llm_guidance_usage ORDER BY ticket_id, attempt_number NULLS FIRST`)
	require.NoError(t, err)
	defer rows.Close()
	var out []attemptCharge
	for rows.Next() {
		var c attemptCharge
		require.NoError(t, rows.Scan(&c.TicketID, &c.Attempt, &c.Requests))
		out = append(out, c)
	}
	require.NoError(t, rows.Err())
	return out
}

func TestGuidanceIsChargedToTheDeclaredAttempt(t *testing.T) {
	w := newWire(t, 100)
	c := w.console(t)
	a := w.ticket(t, w.ctx)
	w.addAttempt(t, a, 2, "queued")

	_, err := w.declareAttempt(t, c, a, nil)
	assert.ErrorIs(t, err, ticket.ErrAttemptAmbiguous, "two live attempts and none named")
	_, err = w.declareAttempt(t, c, a, ptr(9))
	assert.ErrorIs(t, err, ticket.ErrNoSuchAttempt)

	declared, err := w.declareAttempt(t, c, a, ptr(2))
	require.NoError(t, err)
	assert.Equal(t, ptr(2), declared.Attempt)
	w.report(t, consoleToken, 2)
	w.report(t, consoleToken, 5)

	assert.Equal(t, []attemptCharge{{TicketID: a, Attempt: ptr(2), Requests: 5}}, w.charges(t))
	var declaredAttempt *int
	require.NoError(t, w.database.QueryRow(w.ctx, `SELECT attempt_number FROM usage_declarations ORDER BY id DESC LIMIT 1`).Scan(&declaredAttempt))
	assert.Equal(t, ptr(2), declaredAttempt)
}

func TestADeclarationDefaultsToTheOnlyLiveAttempt(t *testing.T) {
	w := newWire(t, 100)
	c := w.console(t)
	a := w.ticket(t, w.ctx)
	w.addAttempt(t, a, 2, "closed")

	declared, err := w.declareAttempt(t, c, a, nil)
	require.NoError(t, err)
	assert.Equal(t, ptr(1), declared.Attempt)
	_, err = w.declareAttempt(t, c, a, ptr(2))
	assert.ErrorIs(t, err, ticket.ErrAttemptNotLive)

	w.report(t, consoleToken, 2)
	w.report(t, consoleToken, 5)
	assert.Equal(t, []attemptCharge{{TicketID: a, Attempt: ptr(1), Requests: 5}}, w.charges(t))
}

// The ledger's key names the attempt, so a transcript that guides two attempts
// of one ticket keeps a row, and its attempt, for each.
func TestATranscriptGuidingTwoAttemptsKeepsARowForEach(t *testing.T) {
	w := newWire(t, 100)
	c := w.console(t)
	a := w.ticket(t, w.ctx)
	w.addAttempt(t, a, 2, "queued")

	_, err := w.declareAttempt(t, c, a, ptr(1))
	require.NoError(t, err)
	w.report(t, consoleToken, 2)
	w.report(t, consoleToken, 5)
	_, err = w.declareAttempt(t, c, a, ptr(2))
	require.NoError(t, err)
	w.report(t, consoleToken, 9)

	assert.Equal(t, []attemptCharge{{TicketID: a, Attempt: ptr(1), Requests: 5}, {TicketID: a, Attempt: ptr(2), Requests: 4}}, w.charges(t))
}

func TestARowFromBeforeAttemptsKeepsNoAttemptAndItsTotal(t *testing.T) {
	w := newWire(t, 100)
	c := w.console(t)
	a := w.ticket(t, w.ctx)
	w.addAttempt(t, a, 2, "queued")
	_, err := w.database.Exec(w.ctx, `
		INSERT INTO llm_guidance_usage (agent_id, transcript_key, ticket_id, provider, model, harness, billing_mode,
			requests, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, reasoning_tokens,
			price_version, unpriced, ended_at)
		VALUES ($1, 'old', $2, 'p', 'm', 'pi', 'metered', 4, 1, 1, 0, 0, 0, 'v', false, now())`, c.ID, a)
	require.NoError(t, err)
	assert.Equal(t, []attemptCharge{{TicketID: a, Requests: 4}}, w.charges(t))

	_, err = w.declareAttempt(t, c, a, ptr(2))
	require.NoError(t, err)
	w.report(t, consoleToken, 2)
	w.report(t, consoleToken, 5)
	assert.Equal(t, []attemptCharge{{TicketID: a, Requests: 4}, {TicketID: a, Attempt: ptr(2), Requests: 5}}, w.charges(t))
}

// Deleting an attempt keeps what was spent on it as the ticket's.
func TestDeletingAnAttemptKeepsItsGuidanceOnTheTicket(t *testing.T) {
	w := newWire(t, 100)
	c := w.console(t)
	a := w.ticket(t, w.ctx)
	w.addAttempt(t, a, 2, "queued")
	_, err := w.declareAttempt(t, c, a, ptr(2))
	require.NoError(t, err)
	w.report(t, consoleToken, 2)
	w.report(t, consoleToken, 5)

	_, err = w.database.Exec(w.ctx, `DELETE FROM attempts WHERE ticket_id = $1 AND number = 2`, a)
	require.NoError(t, err)
	assert.Equal(t, []attemptCharge{{TicketID: a, Requests: 5}}, w.charges(t))
	var declaredTicket *int64
	var declaredAttempt *int
	require.NoError(t, w.database.QueryRow(w.ctx, `SELECT ticket_id, attempt_number FROM usage_declarations ORDER BY id DESC LIMIT 1`).
		Scan(&declaredTicket, &declaredAttempt))
	assert.Equal(t, &a, declaredTicket)
	assert.Nil(t, declaredAttempt)
}

// Documents the cost of the NULL key: an attempt delete that would make a
// second NULL-attempt row for one transcript is refused, not merged.
func TestDeletingAnAttemptIsRefusedWhenItsRowWouldCollideWithTheUnattributedOne(t *testing.T) {
	w := newWire(t, 100)
	c := w.console(t)
	a := w.ticket(t, w.ctx)
	w.addAttempt(t, a, 2, "queued")
	for _, attempt := range []any{nil, 2} {
		_, err := w.database.Exec(w.ctx, `
			INSERT INTO llm_guidance_usage (agent_id, transcript_key, ticket_id, attempt_number, provider, model, harness, billing_mode,
				requests, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, reasoning_tokens,
				price_version, unpriced, ended_at)
			VALUES ($1, 'k', $2, $3, 'p', 'm', 'pi', 'metered', 4, 1, 1, 0, 0, 0, 'v', false, now())`, c.ID, a, attempt)
		require.NoError(t, err)
	}

	_, err := w.database.Exec(w.ctx, `DELETE FROM attempts WHERE ticket_id = $1 AND number = 2`, a)
	assert.ErrorContains(t, err, "llm_guidance_usage_key")
}

func TestADeclarationOnATicketWithNoLiveAttemptIsTheTickets(t *testing.T) {
	w := newWire(t, 100)
	c := w.console(t)
	a := w.ticket(t, w.ctx)
	_, err := w.database.Exec(w.ctx, `UPDATE attempts SET status = 'closed' WHERE ticket_id = $1`, a)
	require.NoError(t, err)

	declared, err := w.declareAttempt(t, c, a, nil)
	require.NoError(t, err)
	assert.Nil(t, declared.Attempt)
	w.report(t, consoleToken, 2)
	w.report(t, consoleToken, 5)
	assert.Equal(t, []attemptCharge{{TicketID: a, Requests: 5}}, w.charges(t))
}
