package landing_test

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/landing"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

var invariantPolicy = &landing.PathPolicy{Apps: []string{"app-a/", "app-b/"}}

// queued is a finished ticket on path, in the merge queue as ticket_finish leaves it:
// a landings row and an in_merge_queue attempt.
func (f *fixture) queued(path string) *finished {
	f.t.Helper()
	c := f.finishedChangeAt(model.PriorityNormal, path)
	f.enqueue(c)
	f.exec(`UPDATE attempts SET status = 'in_merge_queue' WHERE ticket_id = $1 AND number = $2`, c.ticketID, c.attempt)
	return c
}

func (f *fixture) exec(sql string, args ...any) {
	f.t.Helper()
	_, err := f.database.Exec(f.ctx, sql, args...)
	require.NoError(f.t, err)
}

func (f *fixture) violations() []landing.Violation {
	f.t.Helper()
	v, err := landing.CheckInvariants(f.ctx, f.database, f.tenant, invariantPolicy)
	require.NoError(f.t, err)
	return v
}

func ruleOf(vs []landing.Violation, rule string) []landing.Violation {
	var out []landing.Violation
	for _, v := range vs {
		if v.Rule == rule {
			out = append(out, v)
		}
	}
	return out
}

func ticketIDs(cs ...*finished) []int64 {
	out := make([]int64, len(cs))
	for i, c := range cs {
		out[i] = c.ticketID
	}
	return out
}

func reserve(t *testing.T, f *fixture, cs ...*finished) int64 {
	t.Helper()
	b, err := reserveNow(f.ctx, landing.NewBatchStore(f.database), baseOld, ticketIDs(cs...))
	require.NoError(t, err)
	return b.ID
}

func TestCheckInvariants_Rules(t *testing.T) {
	type tc struct {
		name string
		rule string
		// setup builds the fixture's rows; it returns the tickets the violations must name.
		setup func(t *testing.T, f *fixture) []int64
		// wantBatch is whether the violation names a batch.
		holds bool
	}
	cases := []tc{
		{name: "a live reserved batch agrees", rule: landing.RuleBatchMembersAgree, holds: true, setup: func(t *testing.T, f *fixture) []int64 {
			reserve(t, f, f.queued("app-a/t1/"), f.queued("app-a/t2/"))
			return nil
		}},
		{name: "a done batch still reserving its rows", rule: landing.RuleBatchMembersAgree, setup: func(t *testing.T, f *fixture) []int64 {
			a, b := f.queued("app-a/t1/"), f.queued("app-a/t2/")
			id := reserve(t, f, a, b)
			f.exec(`UPDATE batches SET state = 'done' WHERE id = $1`, id)
			return ticketIDs(a, b)
		}},
		{name: "a live batch no member of which is reserved", rule: landing.RuleBatchMembersAgree, setup: func(t *testing.T, f *fixture) []int64 {
			a := f.queued("app-a/t1/")
			id := reserve(t, f, a)
			f.exec(`UPDATE batches SET state = 'testing' WHERE id = $1`, id)
			f.exec(`UPDATE landings SET batch_id = NULL, batch_position = NULL, queue_state = 'queued' WHERE ticket_id = $1`, a.ticketID)
			return []int64{0}
		}},
		{name: "a building batch whose members are not reserved yet", rule: landing.RuleBatchMembersAgree, holds: true, setup: func(t *testing.T, f *fixture) []int64 {
			a := f.queued("app-a/t1/")
			_, err := landing.NewBatchStore(f.database).Create(f.ctx, baseOld, ticketIDs(a))
			require.NoError(t, err)
			return nil
		}},
		{name: "a member merged while its batch was still testing", rule: landing.RuleBatchMembersAgree, setup: func(t *testing.T, f *fixture) []int64 {
			a, b := f.queued("app-a/t1/"), f.queued("app-a/t2/")
			id := reserve(t, f, a, b)
			f.exec(`UPDATE batches SET state = 'testing' WHERE id = $1`, id)
			f.batchOutcomeNone(a, landing.Merged)
			f.exec(`DELETE FROM landings WHERE ticket_id = $1`, a.ticketID)
			return ticketIDs(a)
		}},
		{name: "a member merged once its batch was merging", rule: landing.RuleBatchMembersAgree, holds: true, setup: func(t *testing.T, f *fixture) []int64 {
			a, b := f.queued("app-a/t1/"), f.queued("app-a/t2/")
			id := reserve(t, f, a, b)
			f.exec(`UPDATE batches SET state = 'merging' WHERE id = $1`, id)
			f.batchOutcomeNone(a, landing.Merged)
			f.exec(`DELETE FROM landings WHERE ticket_id = $1`, a.ticketID)
			return nil
		}},
		{name: "a member merged by its batch's split while the batch is testing", rule: landing.RuleBatchMembersAgree, holds: true, setup: func(t *testing.T, f *fixture) []int64 {
			a, b := f.queued("app-a/t1/"), f.queued("app-a/t2/")
			id := reserve(t, f, a, b)
			f.exec(`UPDATE batches SET state = 'testing' WHERE id = $1`, id)
			f.batchOutcome(a, id, landing.Merged)
			f.exec(`DELETE FROM landings WHERE ticket_id = $1`, a.ticketID)
			return nil
		}},
		{name: "a batch-phase row that names no batch", rule: landing.RuleBatchMembersAgree, setup: func(t *testing.T, f *fixture) []int64 {
			a := f.queued("app-a/t1/")
			f.exec(`UPDATE landings SET queue_state = 'batch_testing' WHERE ticket_id = $1`, a.ticketID)
			return ticketIDs(a)
		}},
		{name: "a row reserved by a batch that does not list it", rule: landing.RuleBatchMembersAgree, setup: func(t *testing.T, f *fixture) []int64 {
			a, b := f.queued("app-a/t1/"), f.queued("app-a/t2/")
			reserve(t, f, a)
			f.exec(`UPDATE landings SET batch_id = (SELECT batch_id FROM landings WHERE ticket_id = $1), batch_position = 2, queue_state = 'batch_testing' WHERE ticket_id = $2`, a.ticketID, b.ticketID)
			return ticketIDs(b)
		}},

		{name: "serial landings in different apps", rule: landing.RuleOneEnginePerApp, holds: true, setup: func(t *testing.T, f *fixture) []int64 {
			a, b := f.queued("app-a/t1/"), f.queued("app-b/t2/")
			f.exec(`UPDATE landings SET queue_state = 'serial_waiting_checks' WHERE ticket_id = ANY($1)`, ticketIDs(a, b))
			return nil
		}},
		{name: "a batch in one app and a waiting row in the same app", rule: landing.RuleOneEnginePerApp, holds: true, setup: func(t *testing.T, f *fixture) []int64 {
			reserve(t, f, f.queued("app-a/t1/"))
			f.queued("app-a/t2/")
			return nil
		}},
		{name: "two serial landings in one app", rule: landing.RuleOneEnginePerApp, setup: func(t *testing.T, f *fixture) []int64 {
			a, b := f.queued("app-a/t1/"), f.queued("app-a/t2/")
			f.exec(`UPDATE landings SET queue_state = 'serial_merging' WHERE ticket_id = ANY($1)`, ticketIDs(a, b))
			return ticketIDs(b)
		}},
		{name: "a serial landing in the app of a live batch", rule: landing.RuleOneEnginePerApp, setup: func(t *testing.T, f *fixture) []int64 {
			reserve(t, f, f.queued("app-a/t1/"))
			b := f.queued("app-a/t2/")
			f.exec(`UPDATE landings SET queue_state = 'serial_merging_main' WHERE ticket_id = $1`, b.ticketID)
			return ticketIDs(b)
		}},
		{name: "a serial landing on a shared path beside another app's batch", rule: landing.RuleOneEnginePerApp, setup: func(t *testing.T, f *fixture) []int64 {
			reserve(t, f, f.queued("app-b/t1/"))
			b := f.queued("shared/t2/")
			f.exec(`UPDATE landings SET queue_state = 'serial_merging_main' WHERE ticket_id = $1`, b.ticketID)
			return ticketIDs(b)
		}},

		{name: "attempts in the queue have rows", rule: landing.RuleQueueStatusMatches, holds: true, setup: func(t *testing.T, f *fixture) []int64 {
			f.queued("app-a/t1/")
			return nil
		}},
		{name: "a live row whose attempt is not in the queue", rule: landing.RuleQueueStatusMatches, setup: func(t *testing.T, f *fixture) []int64 {
			a := f.queued("app-a/t1/")
			f.exec(`UPDATE attempts SET status = 'queued' WHERE ticket_id = $1`, a.ticketID)
			return ticketIDs(a)
		}},
		{name: "an attempt in the queue without a row", rule: landing.RuleQueueStatusMatches, setup: func(t *testing.T, f *fixture) []int64 {
			a := f.queued("app-a/t1/")
			f.exec(`DELETE FROM landings WHERE ticket_id = $1`, a.ticketID)
			return ticketIDs(a)
		}},
		{name: "an attempt in the queue whose row is handed back", rule: landing.RuleQueueStatusMatches, setup: func(t *testing.T, f *fixture) []int64 {
			a := f.queued("app-a/t1/")
			f.exec(`UPDATE landings SET queue_state = 'back_with_agent', back_reason = 'checks_red' WHERE ticket_id = $1`, a.ticketID)
			return ticketIDs(a)
		}},

		{name: "positions 1..n", rule: landing.RulePositionsOrdered, holds: true, setup: func(t *testing.T, f *fixture) []int64 {
			reserve(t, f, f.queued("app-a/t1/"), f.queued("app-a/t2/"), f.queued("app-a/t3/"))
			return nil
		}},
		{name: "positions that start at 2 with a gap", rule: landing.RulePositionsOrdered, holds: true, setup: func(t *testing.T, f *fixture) []int64 {
			a, b := f.queued("app-a/t1/"), f.queued("app-a/t2/")
			reserve(t, f, a, b)
			f.exec(`UPDATE landings SET batch_position = batch_position + 1 WHERE batch_id IS NOT NULL`)
			f.exec(`UPDATE landings SET batch_position = 5 WHERE ticket_id = $1`, b.ticketID)
			return nil
		}},
		{name: "duplicate positions", rule: landing.RulePositionsOrdered, setup: func(t *testing.T, f *fixture) []int64 {
			a, b := f.queued("app-a/t1/"), f.queued("app-a/t2/")
			reserve(t, f, a, b)
			f.exec(`UPDATE landings SET batch_position = 2 WHERE batch_id IS NOT NULL`)
			return []int64{0}
		}},
		{name: "positions reversed against the tested order", rule: landing.RulePositionsOrdered, setup: func(t *testing.T, f *fixture) []int64 {
			a, b := f.queued("app-a/t1/"), f.queued("app-a/t2/")
			reserve(t, f, a, b)
			f.exec(`UPDATE landings SET batch_position = 2 WHERE ticket_id = $1`, a.ticketID)
			f.exec(`UPDATE landings SET batch_position = 1 WHERE ticket_id = $1`, b.ticketID)
			return []int64{0}
		}},

		{name: "a done batch whose members merged or were voided", rule: landing.RuleFinishedAccountedFor, holds: true, setup: func(t *testing.T, f *fixture) []int64 {
			a, b := f.queued("app-a/t1/"), f.queued("app-a/t2/")
			id := reserve(t, f, a, b)
			f.finishBatch(id)
			f.batchOutcome(a, id, landing.Merged)
			f.batchOutcome(b, id, landing.BatchVoided)
			return nil
		}},
		{name: "a done batch whose member was handed back with a reason", rule: landing.RuleFinishedAccountedFor, holds: true, setup: func(t *testing.T, f *fixture) []int64 {
			a := f.queued("app-a/t1/")
			id := reserve(t, f, a)
			f.finishBatch(id)
			f.batchOutcome(a, id, landing.ChecksRed)
			f.exec(`UPDATE landings SET queue_state = 'back_with_agent', back_reason = 'checks_red' WHERE ticket_id = $1`, a.ticketID)
			return nil
		}},
		{name: "a done batch with a member without an outcome", rule: landing.RuleFinishedAccountedFor, setup: func(t *testing.T, f *fixture) []int64 {
			a, b := f.queued("app-a/t1/"), f.queued("app-a/t2/")
			id := reserve(t, f, a, b)
			f.finishBatch(id)
			f.batchOutcome(a, id, landing.Merged)
			return ticketIDs(b)
		}},
		{name: "a done batch whose member failed and is queued again", rule: landing.RuleFinishedAccountedFor, setup: func(t *testing.T, f *fixture) []int64 {
			a := f.queued("app-a/t1/")
			id := reserve(t, f, a)
			f.finishBatch(id)
			f.batchOutcome(a, id, landing.ChecksRed)
			return ticketIDs(a)
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			want := c.setup(t, f)
			got := ruleOf(f.violations(), c.rule)
			if c.holds {
				assert.Empty(t, got)
				return
			}
			var tickets []int64
			for _, v := range got {
				tickets = append(tickets, v.TicketID)
			}
			assert.ElementsMatch(t, want, tickets, fmt.Sprint(got))
			for _, v := range got {
				assert.NotEmpty(t, v.Detail)
			}
		})
	}
}

func (f *fixture) batchOutcomeNone(c *finished, o landing.Outcome) {
	f.t.Helper()
	require.NoError(f.t, f.outcomes.Record(f.ctx, &landing.Record{TicketID: c.ticketID, AttemptNumber: c.attempt, ChangeURL: f.hostWrapURL(c.ref), HeadSHA: headA, Outcome: o}))
}

// finishBatch ends a batch as the runner does: its members' reservations cleared.
func (f *fixture) finishBatch(id int64) {
	f.t.Helper()
	require.NoError(f.t, landing.NewBatchStore(f.database).Void(f.ctx, id, "test"))
}

func (f *fixture) batchOutcome(c *finished, batch int64, o landing.Outcome) {
	f.t.Helper()
	require.NoError(f.t, f.outcomes.Record(f.ctx, &landing.Record{
		TicketID: c.ticketID, AttemptNumber: c.attempt, ChangeURL: f.hostWrapURL(c.ref), HeadSHA: headA, Outcome: o, BatchID: &batch, BatchSize: 2, BatchPosition: 1,
	}))
}

// A violation of one rule does not hide in a healthy queue: the clean queue is clean for all five.
func TestCheckInvariants_HealthyQueueHasNone(t *testing.T) {
	f := newFixture(t)
	a, b := f.queued("app-a/t1/"), f.queued("app-a/t2/")
	reserve(t, f, a, b)
	f.exec(`UPDATE landings SET queue_state = 'serial_merging' WHERE ticket_id = $1`, f.queued("app-b/t3/").ticketID)
	assert.Empty(t, f.violations())
}
