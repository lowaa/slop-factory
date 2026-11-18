package schema_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
)

// attemptsFixture is one tenant in a fresh database, with the people and
// agent a ticket's rows need. Everything goes through the app role, so row
// level security is in force as it is in production.
type attemptsFixture struct {
	t        *testing.T
	database *db.DB
	ctx      context.Context
	tenant   uuid.UUID
	user     uuid.UUID
	agent    uuid.UUID
}

func newAttemptsFixture(t *testing.T) *attemptsFixture {
	t.Helper()
	database := dbtest.WithDB(t)
	tenant := dbtest.NewTenant(t, database)
	f := &attemptsFixture{
		t: t, database: database, ctx: db.WithTenant(context.Background(), tenant),
		tenant: tenant, user: dbtest.NewUser(t, database), agent: uuid.New(),
	}
	f.exec(`INSERT INTO agents (id, name) VALUES ($1, 'worker')`, f.agent)
	return f
}

func (f *attemptsFixture) exec(sql string, args ...any) {
	f.t.Helper()
	_, err := f.database.Exec(f.ctx, sql, args...)
	require.NoError(f.t, err, sql)
}

func (f *attemptsFixture) execErr(sql string, args ...any) error {
	f.t.Helper()
	_, err := f.database.Exec(f.ctx, sql, args...)
	return err
}

// ticket inserts a ticket the way the previous release did: naming nothing
// about attempts.
func (f *attemptsFixture) ticket(status string) int64 {
	f.t.Helper()
	var id int64
	require.NoError(f.t, f.database.QueryRow(f.ctx,
		`INSERT INTO tickets (title, status) VALUES ('t', $1) RETURNING id`, status).Scan(&id))
	return id
}

// addAttempt inserts attempt number for ticket, unpromoted, as C5 will.
func (f *attemptsFixture) addAttempt(ticket int64, number int) {
	f.t.Helper()
	f.exec(`INSERT INTO attempts (ticket_id, number, status) VALUES ($1, $2, 'queued')`, ticket, number)
}

func (f *attemptsFixture) lease(ticket int64) int64 {
	f.t.Helper()
	var id int64
	require.NoError(f.t, f.database.QueryRow(f.ctx, `
		INSERT INTO leases (ticket_id, attempt_number, agent_id, expires_at) VALUES ($1, 1, $2, now() + interval '1 hour') RETURNING id`,
		ticket, f.agent).Scan(&id))
	return id
}

func (f *attemptsFixture) attemptStatus(ticket int64) string {
	f.t.Helper()
	var status string
	require.NoError(f.t, f.database.QueryRow(f.ctx,
		`SELECT status FROM attempts WHERE ticket_id = $1 AND number = 1`, ticket).Scan(&status))
	return status
}

func (f *attemptsFixture) count(sql string, args ...any) int {
	f.t.Helper()
	var n int
	require.NoError(f.t, f.database.QueryRow(f.ctx, sql, args...).Scan(&n))
	return n
}

// A ticket made by a pod of the previous release has attempt 1 too,
// promoted, in the status the ticket was created in.
func TestATicketInsertCreatesPromotedAttemptOne(t *testing.T) {
	f := newAttemptsFixture(t)
	var id int64
	require.NoError(t, f.database.QueryRow(f.ctx, `
		INSERT INTO tickets (title, status, requires_approval) VALUES ('t', 'review', true) RETURNING id`).Scan(&id))

	assert.Equal(t, 1, f.count(`SELECT count(*) FROM attempts WHERE ticket_id = $1`, id), "exactly one attempt")
	var number int
	var status string
	var promoted, promotedAtCreation bool
	var resumeAgent *uuid.UUID
	var prURL *string
	require.NoError(t, f.database.QueryRow(f.ctx, `
		SELECT a.number, a.status, a.promoted_at IS NOT NULL, a.resume_agent_id, a.pr_url, a.promoted_at = t.created_at
		  FROM attempts a JOIN tickets t ON t.tenant_id = a.tenant_id AND t.id = a.ticket_id
		 WHERE t.id = $1`, id).Scan(&number, &status, &promoted, &resumeAgent, &prURL, &promotedAtCreation))
	assert.Equal(t, 1, number)
	assert.Equal(t, "review", status)
	assert.True(t, promoted)
	assert.Nil(t, resumeAgent, "a new attempt holds no moved field")
	assert.Nil(t, prURL)
	assert.True(t, promotedAtCreation)

	assert.Equal(t, 2, f.count(`SELECT next_attempt_number FROM tickets WHERE id = $1`, id),
		"attempt 1 is taken, so the next number is 2")
	assert.Equal(t, 1, f.count(`
		SELECT count(*) FROM attempt_status_changes
		 WHERE ticket_id = $1 AND attempt_number = 1 AND from_status IS NULL AND to_status = 'review'`, id),
		"the attempt's history starts with the status it was created in")
}

// Every status a ticket has maps to the attempt status the plan gives it,
// whether the ticket is created in it or the attempt is moved to the status
// the mapping gives it (ticket.Service writes the attempt's status itself).
func TestTicketStatusesMapToAttemptStatuses(t *testing.T) {
	f := newAttemptsFixture(t)
	cases := map[string]string{
		"preparing": "queued", "parked": "queued", "open": "queued", "blocked": "queued",
		"in_progress": "in_progress", "discussion": "discussion", "review": "review",
		"done": "done", "cancelled": "closed",
	}
	for ticketStatus, attemptStatus := range cases {
		t.Run(ticketStatus, func(t *testing.T) {
			created := f.ticket(ticketStatus)
			assert.Equal(t, attemptStatus, f.attemptStatus(created), "created in %s", ticketStatus)

			moved := f.ticket("done")
			f.exec(`UPDATE attempts SET status = attempt_status_for_ticket($2) WHERE ticket_id = $1`, moved, ticketStatus)
			assert.Equal(t, attemptStatus, f.attemptStatus(moved), "moved to %s", ticketStatus)
		})
	}
}

// Writing an attempt's status to the one it holds records no history row; a
// move that changes it records one, with the actor the transaction named.
func TestAttemptStatusHistoryFollowsTheMappedStatus(t *testing.T) {
	f := newAttemptsFixture(t)
	id := f.ticket("open")
	history := func() int {
		return f.count(`SELECT count(*) FROM attempt_status_changes WHERE ticket_id = $1`, id)
	}
	require.Equal(t, 1, history())

	f.exec(`UPDATE attempts SET status = attempt_status_for_ticket('blocked') WHERE ticket_id = $1`, id)
	assert.Equal(t, 1, history(), "open to blocked is queued to queued")

	require.NoError(t, f.database.InTx(f.ctx, func(q db.Querier) error {
		_, err := q.Exec(f.ctx, `
			SELECT set_config('slophaus.actor_kind', 'agent', true), set_config('slophaus.actor_id', $1, true),
			       set_config('slophaus.cause', 'claimed', true)`, f.agent.String())
		if err != nil {
			return err
		}
		_, err = q.Exec(f.ctx, `UPDATE attempts SET status = 'in_progress' WHERE ticket_id = $1`, id)
		return err
	}))
	assert.Equal(t, 2, history())
	var from, to, kind string
	var actor *uuid.UUID
	var cause *string
	require.NoError(t, f.database.QueryRow(f.ctx, `
		SELECT from_status, to_status, actor_kind, actor_id, cause FROM attempt_status_changes
		 WHERE ticket_id = $1 ORDER BY id DESC LIMIT 1`, id).Scan(&from, &to, &kind, &actor, &cause))
	assert.Equal(t, "queued", from)
	assert.Equal(t, "in_progress", to)
	assert.Equal(t, "agent", kind)
	require.NotNil(t, actor)
	assert.Equal(t, f.agent, *actor)
	require.NotNil(t, cause)
	assert.Equal(t, "claimed", *cause)
}

// Nothing on the ticket is copied to its attempt: the moved fields and the
// status are written on the attempt only, by ticket.Service. A write to
// tickets alone, as the release before the contract step made for the status,
// moves no attempt, which shows Go is the only writer.
func TestNothingIsCopiedFromATicketToItsAttempt(t *testing.T) {
	f := newAttemptsFixture(t)
	id := f.ticket("open")
	stamp := time.Date(2026, 10, 9, 3, 0, 0, 0, time.UTC)
	f.exec(`UPDATE attempts SET pr_url = 'https://github.example/pull/3', affinity_until = $2 WHERE ticket_id = $1`, id, stamp)
	var before, after string
	snapshot := func(into *string) {
		require.NoError(t, f.database.QueryRow(f.ctx, `
			SELECT to_jsonb(a)::text FROM attempts a WHERE ticket_id = $1 AND number = 1`, id).Scan(into))
	}
	snapshot(&before)
	require.Equal(t, "queued", f.attemptStatus(id))

	f.exec(`UPDATE tickets SET status = 'done', closed_at = $2, updated_at = $2 WHERE id = $1`, id, stamp)

	snapshot(&after)
	assert.Equal(t, before, after, "the attempt is untouched by a write to its ticket")
	assert.Equal(t, "queued", f.attemptStatus(id))
}

// The counter hands out each number once, even to concurrent takers, and a
// deleted attempt's number is not given again.
func TestAttemptNumbersAreTakenFromTheTicketsCounter(t *testing.T) {
	f := newAttemptsFixture(t)
	id := f.ticket("open")
	take := func(ctx context.Context, q db.Querier) (int, error) {
		var n int
		err := q.QueryRow(ctx, `
			UPDATE tickets SET next_attempt_number = next_attempt_number + 1
			 WHERE id = $1 RETURNING next_attempt_number - 1`, id).Scan(&n)
		return n, err
	}

	const takers = 8
	numbers := make(chan int, takers)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < takers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			assert.NoError(t, f.database.InTx(f.ctx, func(q db.Querier) error {
				n, err := take(f.ctx, q)
				if err != nil {
					return err
				}
				_, err = q.Exec(f.ctx, `INSERT INTO attempts (ticket_id, number, status) VALUES ($1, $2, 'queued')`, id, n)
				numbers <- n
				return err
			}))
		}()
	}
	close(start)
	wg.Wait()
	close(numbers)
	seen := map[int]bool{}
	for n := range numbers {
		assert.False(t, seen[n], "number %d handed out twice", n)
		seen[n] = true
	}
	for n := 2; n < 2+takers; n++ {
		assert.True(t, seen[n], "number %d", n)
	}

	f.exec(`DELETE FROM attempts WHERE ticket_id = $1 AND number = $2`, id, 1+takers)
	require.NoError(t, f.database.InTx(f.ctx, func(q db.Querier) error {
		n, err := take(f.ctx, q)
		require.NoError(t, err)
		assert.Equal(t, 2+takers, n, "the deleted attempt's number is not reused")
		return nil
	}))
	assert.Error(t, f.execErr(`UPDATE tickets SET next_attempt_number = 1 WHERE id = $1`, id),
		"the counter cannot go back to a number attempt 1 holds")
}

// Only one attempt of a ticket is promoted, and a promotion needs its time.
func TestOnlyOneAttemptIsPromoted(t *testing.T) {
	f := newAttemptsFixture(t)
	id := f.ticket("open")
	f.addAttempt(id, 2)
	assert.Error(t, f.execErr(`UPDATE attempts SET promoted_at = now() WHERE ticket_id = $1 AND number = 2`, id),
		"attempt 1 is promoted already")
	f.exec(`UPDATE attempts SET promoted_at = NULL WHERE ticket_id = $1 AND number = 1`, id)
	f.exec(`UPDATE attempts SET promoted_at = now(), promoted_by_user_id = $2 WHERE ticket_id = $1 AND number = 2`, id, f.user)
	assert.Error(t, f.execErr(`UPDATE attempts SET promoted_by_user_id = $2 WHERE ticket_id = $1 AND number = 1`, id, f.user),
		"a promoter without a promotion")
}

// A reference to an attempt the ticket does not have is refused, even when
// another ticket has an attempt of that number.
func TestAChildCannotNameAnotherTicketsAttempt(t *testing.T) {
	f := newAttemptsFixture(t)
	mine, other := f.ticket("open"), f.ticket("open")
	f.addAttempt(other, 2)

	inserts := map[string]func(ticket int64, attempt int) error{
		"leases": func(ticket int64, attempt int) error {
			return f.execErr(`INSERT INTO leases (ticket_id, attempt_number, agent_id, expires_at)
				VALUES ($1, $2, $3, now() + interval '1 hour')`, ticket, attempt, f.agent)
		},
		"agent_contexts": func(ticket int64, attempt int) error {
			return f.execErr(`INSERT INTO agent_contexts (ticket_id, attempt_number, agent_id, body) VALUES ($1, $2, $3, 'b')`,
				ticket, attempt, f.agent)
		},
		"ticket_comments": func(ticket int64, attempt int) error {
			return f.execErr(`INSERT INTO ticket_comments (ticket_id, attempt_number, author_agent_id, body) VALUES ($1, $2, $3, 'b')`,
				ticket, attempt, f.agent)
		},
		"ticket_changes": func(ticket int64, attempt int) error {
			return f.execErr(`INSERT INTO ticket_changes (ticket_id, attempt_number, host, owner, repo, number, head_branch, base_branch, draft, opened_by_agent_id)
				VALUES ($1, $2, 'h', 'o', 'r', 1, 'b', 'main', true, $3)`, ticket, attempt, f.agent)
		},
		"pending_actions": func(ticket int64, attempt int) error {
			return f.execErr(`INSERT INTO pending_actions (id, ticket_id, attempt_number, agent_id, kind, payload, base_status, base_revision, expires_at)
				VALUES (gen_random_uuid(), $1, $2, $3, 'discussion_answer', '{}', 'open', 0, now() + interval '1 hour')`,
				ticket, attempt, f.agent)
		},
		"agent_messages": func(ticket int64, attempt int) error {
			return f.execErr(`INSERT INTO agent_messages (agent_id, kind, author_user_id, ticket_id, attempt_number, body)
				VALUES ($1, 'ticket_comment', $2, $3, $4, 'b')`, f.agent, f.user, ticket, attempt)
		},
		"github_deliveries": func(ticket int64, attempt int) error {
			return f.execErr(`INSERT INTO github_deliveries (delivery_id, installation_id, event, outcome, ticket_id, attempt_number)
				VALUES (gen_random_uuid()::text, 1, 'e', 'applied', $1, $2)`, ticket, attempt)
		},
		"llm_usage": func(ticket int64, attempt int) error {
			return f.execErr(llmUsageInsert("'ticket'", "$2", "$3"), f.agent, ticket, attempt)
		},
	}
	for table, insert := range inserts {
		t.Run(table, func(t *testing.T) {
			require.Error(t, insert(mine, 2), "attempt 2 belongs to another ticket")
			// The same row on the ticket's own attempt is fine, so the
			// refusal above is the attempt and nothing else in the statement.
			require.NoError(t, insert(mine, 1), "attempt 1 is this ticket's")
		})
	}
}

// llmUsageInsert is an llm_usage row with the given scope, ticket and
// attempt SQL; the agent is $1.
func llmUsageInsert(scope, ticket, attempt string) string {
	return fmt.Sprintf(`
		INSERT INTO llm_usage (agent_id, transcript_key, harness, session_id, scope_hint, scope, ticket_id, attempt_number,
			provider, model, billing_mode, requests, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens,
			reasoning_tokens, price_version, unpriced, started_at, ended_at)
		VALUES ($1, gen_random_uuid()::text, 'h', 's', 'main', %s, %s, %s, 'p', 'm', 'metered', 1, 1, 1, 0, 0, 0, 'v', false, now(), now())`,
		scope, ticket, attempt)
}

// The pending action kinds that are about an attempt need one; the ones
// about the ticket refuse one.
func TestAPendingActionsAttemptFollowsItsKind(t *testing.T) {
	f := newAttemptsFixture(t)
	// One live proposal per ticket and kind, so each insert has a ticket of
	// its own, with the second attempt the first would not.
	insert := func(kind string, attempt any) error {
		id := f.ticket("open")
		f.addAttempt(id, 2)
		return f.execErr(`
			INSERT INTO pending_actions (id, ticket_id, attempt_number, agent_id, kind, payload, base_status, base_revision, expires_at)
			VALUES (gen_random_uuid(), $1, $2, $3, $4, '{}', 'open', 0, now() + interval '1 hour')`,
			id, attempt, f.agent, kind)
	}

	for _, kind := range []string{"discussion_answer", "review_decision"} {
		assert.NoError(t, insert(kind, 2), "%s names an attempt", kind)
		assert.Error(t, insert(kind, nil), "%s naming none is refused: nothing defaults it to attempt 1", kind)
		assert.Error(t, insert(kind, 3), "%s names an attempt the ticket lacks", kind)
	}
	for _, kind := range []string{"ready", "status"} {
		assert.NoError(t, insert(kind, nil), "%s is the ticket's", kind)
		assert.Error(t, insert(kind, 1), "%s refuses an attempt", kind)
	}
	assert.Zero(t, f.count(`SELECT count(*) FROM pending_actions WHERE attempt_number = 1`))
	assert.Equal(t, 2, f.count(`SELECT count(*) FROM pending_actions WHERE attempt_number = 2`))
	assert.Equal(t, 2, f.count(`SELECT count(*) FROM pending_actions WHERE attempt_number IS NULL`),
		"and the ticket-level ones stayed the ticket's")
	assert.Error(t, f.execErr(`UPDATE pending_actions SET attempt_number = NULL WHERE kind = 'review_decision'`),
		"nor can a decision lose its attempt")
}

// A write that names no attempt no longer lands on attempt 1: where the row
// is an attempt's it is refused, and where it may be the ticket's or belong to
// no ticket it stays that.
func TestWritesThatNameNoAttemptAreRefusedOrStayTheTickets(t *testing.T) {
	f := newAttemptsFixture(t)
	id := f.ticket("open")
	attemptOf := func(table string, where string) *int {
		var n *int
		require.NoError(t, f.database.QueryRow(f.ctx, fmt.Sprintf(`SELECT attempt_number FROM %s WHERE %s`, table, where)).Scan(&n))
		return n
	}

	assert.Error(t, f.execErr(`INSERT INTO leases (ticket_id, agent_id, expires_at) VALUES ($1, $2, now() + interval '1 hour')`,
		id, f.agent), "lease")
	assert.Error(t, f.execErr(`INSERT INTO agent_contexts (ticket_id, agent_id, body) VALUES ($1, $2, 'handoff')`, id, f.agent), "handoff")
	assert.Error(t, f.execErr(`INSERT INTO ticket_changes (ticket_id, host, owner, repo, number, head_branch, base_branch, draft, opened_by_agent_id)
		VALUES ($1, 'h', 'o', 'r', 1, 'b', 'main', true, $2)`, id, f.agent), "change")
	assert.Error(t, f.execErr(`
		INSERT INTO pending_actions (id, ticket_id, agent_id, kind, payload, base_status, base_revision, expires_at)
		VALUES (gen_random_uuid(), $1, $2, 'review_decision', '{}', 'open', 0, now() + interval '1 hour')`, id, f.agent), "decision")
	assert.Error(t, f.execErr(`INSERT INTO agent_messages (agent_id, kind, author_user_id, ticket_id, body)
		VALUES ($1, 'ticket_comment', $2, $3, 'b')`, f.agent, f.user, id), "a ticket's message names its attempt")
	assert.Zero(t, f.count(`SELECT count(*) FROM leases`)+f.count(`SELECT count(*) FROM agent_contexts`)+
		f.count(`SELECT count(*) FROM ticket_changes`)+f.count(`SELECT count(*) FROM pending_actions`)+
		f.count(`SELECT count(*) FROM agent_messages`), "and none of them was written")

	f.exec(`INSERT INTO ticket_comments (ticket_id, author_agent_id, body) VALUES ($1, $2, 'c')`, id, f.agent)
	assert.Nil(t, attemptOf("ticket_comments", "ticket_id = "+fmt.Sprint(id)), "a comment stays the ticket's")

	f.exec(`INSERT INTO agent_messages (agent_id, kind, author_user_id, body) VALUES ($1, 'message', $2, 'hi')`, f.agent, f.user)
	assert.Nil(t, attemptOf("agent_messages", "kind = 'message'"), "a message to an agent has no ticket, so no attempt")

	f.exec(`INSERT INTO github_deliveries (delivery_id, installation_id, event, outcome) VALUES ('d1', 1, 'e', 'processing')`)
	f.exec(`UPDATE github_deliveries SET outcome = 'applied', ticket_id = $1 WHERE delivery_id = 'd1'`, id)
	assert.Nil(t, attemptOf("github_deliveries", "delivery_id = 'd1'"), "a delivery's ticket is no longer given attempt 1")
	f.exec(llmUsageInsert("'ticket'", "$2", "NULL"), f.agent, id)
	assert.Equal(t, 1, f.count(`SELECT count(*) FROM llm_usage WHERE ticket_id = $1 AND attempt_number IS NULL`, id))
	f.exec(llmUsageInsert("'overhead'", "NULL", "NULL"), f.agent)
}

// Deleting a ticket takes its attempts and everything on them; the rows that
// outlive a ticket keep their place but lose the ticket and attempt together.
func TestDeletingATicketClearsItsAttemptsAndChildren(t *testing.T) {
	f := newAttemptsFixture(t)
	id := f.ticket("open")
	f.addAttempt(id, 2)
	f.lease(id)
	f.exec(`INSERT INTO ticket_comments (ticket_id, attempt_number, author_agent_id, body) VALUES ($1, 2, $2, 'c')`, id, f.agent)
	f.exec(`INSERT INTO github_deliveries (delivery_id, installation_id, event, outcome, ticket_id, attempt_number) VALUES ('d', 1, 'e', 'applied', $1, 1)`, id)
	f.exec(llmUsageInsert("'ticket'", "$2", "2"), f.agent, id)
	f.exec(`INSERT INTO agent_contexts (ticket_id, attempt_number, agent_id, body) VALUES ($1, 2, $2, 'h')`, id, f.agent)
	f.exec(`INSERT INTO ticket_changes (ticket_id, attempt_number, host, owner, repo, number, head_branch, base_branch, draft, opened_by_agent_id)
		VALUES ($1, 2, 'h', 'o', 'r', 1, 'b', 'main', true, $2)`, id, f.agent)
	f.exec(`INSERT INTO pending_actions (id, ticket_id, attempt_number, agent_id, kind, payload, base_status, base_revision, expires_at)
		VALUES (gen_random_uuid(), $1, 2, $2, 'review_decision', '{}', 'open', 0, now() + interval '1 hour')`, id, f.agent)
	f.exec(`INSERT INTO agent_messages (agent_id, kind, author_user_id, ticket_id, attempt_number, body)
		VALUES ($1, 'ticket_comment', $2, $3, 2, 'b')`, f.agent, f.user, id)

	f.exec(`DELETE FROM tickets WHERE id = $1`, id)

	assert.Zero(t, f.count(`SELECT count(*) FROM attempts WHERE ticket_id = $1`, id))
	assert.Zero(t, f.count(`SELECT count(*) FROM attempt_status_changes WHERE ticket_id = $1`, id))
	for _, table := range []string{"leases", "ticket_comments", "agent_contexts", "ticket_changes", "pending_actions", "agent_messages"} {
		assert.Zero(t, f.count(`SELECT count(*) FROM `+table+` WHERE ticket_id = $1`, id), table)
	}
	assert.Equal(t, 1, f.count(`SELECT count(*) FROM github_deliveries WHERE ticket_id IS NULL AND attempt_number IS NULL`))
	assert.Equal(t, 1, f.count(`SELECT count(*) FROM llm_usage WHERE ticket_id IS NULL AND attempt_number IS NULL`))
}

// A person's comment reaches whoever holds the attempt it is on; one on no
// attempt goes to attempt 1's holder.
func TestAHumanCommentGoesToTheHolderOfItsAttempt(t *testing.T) {
	f := newAttemptsFixture(t)
	id := f.ticket("in_progress")
	f.addAttempt(id, 2)
	f.lease(id) // attempt 1's
	comment := func(attempt any) {
		f.exec(`INSERT INTO ticket_comments (ticket_id, attempt_number, author_user_id, body) VALUES ($1, $2, $3, 'hello')`,
			id, attempt, f.user)
	}

	comment(nil)
	require.Equal(t, 1, f.count(`SELECT count(*) FROM agent_messages WHERE kind = 'ticket_comment'`), "a comment on no attempt")
	assert.Equal(t, 1, f.count(`SELECT attempt_number FROM agent_messages WHERE kind = 'ticket_comment'`), "reaches attempt 1's holder")

	comment(1)
	assert.Equal(t, 2, f.count(`SELECT count(*) FROM agent_messages WHERE kind = 'ticket_comment' AND attempt_number = 1`))

	comment(2)
	assert.Equal(t, 2, f.count(`SELECT count(*) FROM agent_messages WHERE kind = 'ticket_comment'`),
		"attempt 2 has no holder, so its comment is delivered to no one")

	f.exec(`UPDATE leases SET attempt_number = 2 WHERE ticket_id = $1`, id)
	comment(2)
	assert.Equal(t, 1, f.count(`SELECT count(*) FROM agent_messages WHERE kind = 'ticket_comment' AND attempt_number = 2`),
		"now that attempt 2 is held, its comment reaches the holder")
	comment(1)
	assert.Equal(t, 2, f.count(`SELECT count(*) FROM agent_messages WHERE kind = 'ticket_comment' AND attempt_number = 1`),
		"and attempt 1's comment goes to no one")
}
