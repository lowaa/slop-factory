package ticket_test

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/agent"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

// The board revision against real Postgres: every kind of change that moves
// or redraws a card on the board moves it, through whichever path made the
// change, and the writes that show nowhere on the board leave it alone.
//
// Verified: with the trigger on any one table dropped from the
// board_revision triggers, that table's case in MovesOnAWriteToAnyTableTheBoardReads fails
// (and, for tickets, most of MovesOnEveryChangeTheBoardShows).

func (f *fixture) boardRevision(t *testing.T) int64 {
	t.Helper()
	revision, err := newStore(f.database).BoardRevision(f.ctx)
	require.NoError(t, err)
	return revision
}

// boardChange is one way the board changes: arrange sets it up, and the
// func it returns makes the change being tested.
type boardChange struct {
	name    string
	arrange func(t *testing.T, f *roleFixture) (act func())
}

func noErr[T any](t *testing.T) func(T, error) {
	return func(_ T, err error) {
		t.Helper()
		require.NoError(t, err)
	}
}

// claimedBy files a ticket and has a new agent claim it.
func claimedBy(t *testing.T, f *roleFixture, name string) (uuid.UUID, *ticket.Ticket) {
	t.Helper()
	agentID := f.agent(t, name)
	tk := f.ticket(t, "Held by "+name, name+"/")
	_, err := f.svc.Claim(f.ctx, agentID, &tk.ID)
	require.NoError(t, err)
	return agentID, tk
}

var boardChanges = []boardChange{
	{"a ticket filed", func(t *testing.T, f *roleFixture) func() {
		return func() { f.ticket(t, "New") }
	}},
	{"status through PATCH", func(t *testing.T, f *roleFixture) func() {
		tk := f.ticket(t, "Parked by a person")
		return func() {
			noErr[*ticket.Ticket](t)(f.svc.Update(f.ctx, tk.ID, &ticket.UpdateParams{Status: ptr(ticket.StatusParked)}))
		}
	}},
	{"priority through PATCH", func(t *testing.T, f *roleFixture) func() {
		tk := f.ticket(t, "Urgent now")
		return func() {
			noErr[*ticket.Ticket](t)(f.svc.Update(f.ctx, tk.ID, &ticket.UpdateParams{Priority: ptr(model.PriorityUrgent)}))
		}
	}},
	{"a flag through the field-only PATCH", func(t *testing.T, f *roleFixture) func() {
		tk := f.ticket(t, "Needs a plan")
		return func() {
			noErr[*ticket.Ticket](t)(f.svc.Update(f.ctx, tk.ID, &ticket.UpdateParams{NeedsDiscussion: ptr(true)}))
		}
	}},
	{"approval through the field-only PATCH", func(t *testing.T, f *roleFixture) func() {
		tk := f.ticket(t, "Gate it")
		return func() {
			noErr[*ticket.Ticket](t)(f.svc.Update(f.ctx, tk.ID, &ticket.UpdateParams{RequiresApproval: ptr(true)}))
		}
	}},
	{"paths through the field-only PATCH", func(t *testing.T, f *roleFixture) func() {
		tk := f.ticket(t, "Rescoped", "old/")
		return func() {
			noErr[*ticket.Ticket](t)(f.svc.Update(f.ctx, tk.ID, &ticket.UpdateParams{Paths: []model.Path{{Path: "new/"}}}))
		}
	}},
	{"title edited", func(t *testing.T, f *roleFixture) func() {
		tk := f.ticket(t, "Old title")
		return func() {
			noErr[*ticket.EditResult](t)(f.svc.Edit(f.ctx, tk.ID, f.human, &ticket.EditParams{Title: ptr("New title")}))
		}
	}},
	{"marked Ready", func(t *testing.T, f *roleFixture) func() {
		tk := f.preparing(t, "Shaped")
		return func() { noErr[*ticket.Ticket](t)(f.svc.MarkReady(f.ctx, tk.ID)) }
	}},
	{"bulk move", func(t *testing.T, f *roleFixture) func() {
		a, b := f.ticket(t, "A"), f.ticket(t, "B")
		return func() { f.moveMany(t, ticket.StatusParked, a.ID, b.ID) }
	}},
	{"bulk Ready", func(t *testing.T, f *roleFixture) func() {
		a, b := f.preparing(t, "A"), f.preparing(t, "B")
		return func() { noErr[[]ticket.Ticket](t)(f.svc.MarkReadyMany(f.ctx, []int64{a.ID, b.ID})) }
	}},
	{"bulk priority", func(t *testing.T, f *roleFixture) func() {
		a, b := f.ticket(t, "A"), f.ticket(t, "B")
		return func() {
			noErr[[]ticket.Ticket](t)(f.svc.SetPriorityMany(f.ctx, []int64{a.ID, b.ID}, model.PriorityHigh))
		}
	}},
	{"a blocker added", func(t *testing.T, f *roleFixture) func() {
		// Parked, so adding the blocker writes only the edge: an open
		// ticket would also move to blocked, which the tickets trigger
		// would catch on its own.
		blocked, blocker := f.parked(t, "Waits"), f.ticket(t, "First")
		return func() {
			noErr[*ticket.DependencyChange](t)(f.svc.AddDependency(f.ctx, ticket.UserAuthor(f.human), blocked.ID, blocker.ID))
		}
	}},
	{"a blocker removed", func(t *testing.T, f *roleFixture) func() {
		blocked, blocker := f.parked(t, "Waits"), f.ticket(t, "First")
		f.blockedBy(t, blocked.ID, blocker.ID)
		return func() {
			noErr[*ticket.DependencyChange](t)(f.svc.RemoveDependency(f.ctx, ticket.UserAuthor(f.human), blocked.ID, blocker.ID))
		}
	}},
	{"required roles set", func(t *testing.T, f *roleFixture) func() {
		tk, role := f.ticket(t, "Needs a reviewer"), f.role(t, "reviewer")
		return func() {
			noErr[*ticket.Ticket](t)(f.svc.SetRequiredRoles(f.ctx, testPerson(), tk.ID, &ticket.RoleRequirement{IDs: []uuid.UUID{role}}))
		}
	}},
	{"a person's comment", func(t *testing.T, f *roleFixture) func() {
		tk := f.ticket(t, "Discussed")
		return func() {
			noErr[*ticket.Comment](t)(f.svc.AddComment(f.ctx, tk.ID, ticket.UserAuthor(f.human), "searchable words"))
		}
	}},
	{"a claim", func(t *testing.T, f *roleFixture) func() {
		agentID, tk := f.agent(t, "claimer"), f.ticket(t, "Up next")
		return func() { noErr[*ticket.ClaimResult](t)(f.svc.Claim(f.ctx, agentID, &tk.ID)) }
	}},
	{"a release", func(t *testing.T, f *roleFixture) func() {
		agentID, _ := claimedBy(t, f, "releaser")
		return func() { noErr[*ticket.Ticket](t)(f.svc.Release(f.ctx, agentID, "stuck", nil)) }
	}},
	{"an expired lease taken over", func(t *testing.T, f *roleFixture) func() {
		gone, tk := claimedBy(t, f, "gone")
		f.expire(t, gone)
		next := f.agent(t, "next")
		return func() { noErr[*ticket.ClaimResult](t)(f.svc.Claim(f.ctx, next, &tk.ID)) }
	}},
	{"a discussion with a handoff", func(t *testing.T, f *roleFixture) func() {
		agentID, _ := claimedBy(t, f, "asker")
		return func() { noErr[*ticket.Ticket](t)(f.svc.Discuss(f.ctx, agentID, "which?", "where it stands")) }
	}},
	{"done", func(t *testing.T, f *roleFixture) func() {
		agentID, _ := claimedBy(t, f, "finisher")
		return func() { noErr[*ticket.Ticket](t)(f.svc.Done(f.ctx, agentID, &ticket.DoneParams{})) }
	}},
	{"scope extended", func(t *testing.T, f *roleFixture) func() {
		agentID, _ := claimedBy(t, f, "extender")
		return func() {
			noErr[*ticket.ExtendResult](t)(f.svc.ExtendScope(f.ctx, agentID, []model.Path{{Path: "more/"}}))
		}
	}},
	{"a role renamed", func(t *testing.T, f *roleFixture) func() {
		role := f.role(t, "old-name")
		f.requiring(t, "Shows the role", model.RoleMatchAny, role)
		return func() {
			_, err := f.roles.Update(f.ctx, testPerson(), role, &agent.RoleParams{Name: ptr("new-name")})
			require.NoError(t, err)
		}
	}},
	{"an agent given a role", func(t *testing.T, f *roleFixture) func() {
		role := f.role(t, "builder")
		f.requiring(t, "No one can take it", model.RoleMatchAny, role)
		agentID := f.agent(t, "builder-1")
		return func() { f.give(t, agentID, role) }
	}},
	{"an agent created", func(t *testing.T, f *roleFixture) func() {
		return func() { f.agent(t, "newcomer") }
	}},
	{"an idle agent disabled", func(t *testing.T, f *roleFixture) func() {
		agentID := f.agent(t, "retiring")
		return func() { noErr[*agent.Agent](t)(f.agents.SetDisabled(f.ctx, agentID, true)) }
	}},
	{"a ticket deleted", func(t *testing.T, f *roleFixture) func() {
		// No route deletes a ticket today; the trigger is on the table, so
		// whatever does later is covered.
		tk := f.ticket(t, "Gone")
		return func() {
			_, err := f.database.Exec(f.ctx, `DELETE FROM tickets WHERE id = $1`, tk.ID)
			require.NoError(t, err)
		}
	}},
}

func TestBoardRevision_MovesOnEveryChangeTheBoardShows(t *testing.T) {
	t.Parallel()
	for _, c := range boardChanges {
		t.Run(c.name, func(t *testing.T) {
			f := newRoleFixture(t)
			act := c.arrange(t, f)
			before := f.boardRevision(t)

			act()

			assert.Greater(t, f.boardRevision(t), before)
		})
	}
}

// Each table the board reads moves it on its own, written alone: the
// service paths above mostly touch the ticket row too, which would hide a
// table whose trigger was missing until some later path wrote only that
// table.
func TestBoardRevision_MovesOnAWriteToAnyTableTheBoardReads(t *testing.T) {
	t.Parallel()
	writes := []boardChange{
		{"ticket_paths", func(t *testing.T, f *roleFixture) func() {
			tk := f.ticket(t, "Scoped")
			return f.exec(t, `INSERT INTO ticket_paths (ticket_id, path) VALUES ($1, 'x/')`, tk.ID)
		}},
		{"ticket_deps", func(t *testing.T, f *roleFixture) func() {
			a, b := f.ticket(t, "A"), f.ticket(t, "B")
			return f.exec(t, `INSERT INTO ticket_deps (ticket_id, depends_on_ticket_id) VALUES ($1, $2)`, a.ID, b.ID)
		}},
		{"ticket_required_roles", func(t *testing.T, f *roleFixture) func() {
			tk, role := f.ticket(t, "Gated by role"), f.role(t, "r")
			return f.exec(t, `INSERT INTO ticket_required_roles (ticket_id, role_id) VALUES ($1, $2)`, tk.ID, role)
		}},
		{"ticket_comments", func(t *testing.T, f *roleFixture) func() {
			tk := f.ticket(t, "Commented")
			return f.exec(t, `INSERT INTO ticket_comments (ticket_id, author_user_id, body) VALUES ($1, $2, 'hi')`, tk.ID, f.human)
		}},
		{"agent_contexts", func(t *testing.T, f *roleFixture) func() {
			tk, agentID := f.ticket(t, "Handed off"), f.agent(t, "writer")
			return f.exec(t, `INSERT INTO agent_contexts (ticket_id, attempt_number, agent_id, body) VALUES ($1, 1, $2, 'notes')`, tk.ID, agentID)
		}},
		{"leases, claimed", func(t *testing.T, f *roleFixture) func() {
			tk, agentID := f.ticket(t, "Leased"), f.agent(t, "holder")
			return f.exec(t, `INSERT INTO leases (ticket_id, attempt_number, agent_id, expires_at) VALUES ($1, 1, $2, now() + interval '1 hour')`, tk.ID, agentID)
		}},
		{"leases, released", func(t *testing.T, f *roleFixture) func() {
			agentID, _ := claimedBy(t, f, "letting-go")
			return f.exec(t, `UPDATE leases SET released_at = now() WHERE agent_id = $1`, agentID)
		}},
		{"agent_roles", func(t *testing.T, f *roleFixture) func() {
			role := f.role(t, "before")
			return f.exec(t, `UPDATE agent_roles SET name = 'after' WHERE id = $1`, role)
		}},
		{"agent_role_members", func(t *testing.T, f *roleFixture) func() {
			role, agentID := f.role(t, "member"), f.agent(t, "joiner")
			return f.exec(t, `INSERT INTO agent_role_members (agent_id, role_id) VALUES ($1, $2)`, agentID, role)
		}},
		{"agents, disabled", func(t *testing.T, f *roleFixture) func() {
			agentID := f.agent(t, "switched-off")
			return f.exec(t, `UPDATE agents SET disabled_at = now() WHERE id = $1`, agentID)
		}},
	}
	for _, c := range writes {
		t.Run(c.name, func(t *testing.T) {
			f := newRoleFixture(t)
			act := c.arrange(t, f)
			before := f.boardRevision(t)

			act()

			assert.Greater(t, f.boardRevision(t), before)
		})
	}
}

// exec returns a func running one statement in its own transaction.
func (f *fixture) exec(t *testing.T, sql string, args ...any) func() {
	return func() {
		_, err := f.database.Exec(f.ctx, sql, args...)
		require.NoError(t, err)
	}
}

// The writes that change nothing on the board leave the revision alone, or
// every open board would refetch on each of them: a heartbeat comes from
// every working agent every few minutes, and a session is touched on each
// call an agent makes.
func TestBoardRevision_IgnoresWritesTheBoardDoesNotShow(t *testing.T) {
	t.Parallel()
	quiet := []boardChange{
		{"a heartbeat", func(t *testing.T, f *roleFixture) func() {
			agentID, _ := claimedBy(t, f, "working")
			return func() { require.NoError(t, f.svc.Heartbeat(f.ctx, agentID)) }
		}},
		{"an agent's session touched", func(t *testing.T, f *roleFixture) func() {
			agentID := f.agent(t, "reader")
			_, err := f.database.Exec(f.ctx, `INSERT INTO sessions (agent_id, claude_session_id) VALUES ($1, 'run')`, agentID)
			require.NoError(t, err)
			return func() {
				_, err := f.database.Exec(f.ctx, `UPDATE sessions SET last_seen_at = now(), stuck_reason = 'x', stuck_at = now() WHERE agent_id = $1`, agentID)
				require.NoError(t, err)
			}
		}},
		{"an old released lease pruned", func(t *testing.T, f *roleFixture) func() {
			agentID, _ := claimedBy(t, f, "pruned")
			_, err := f.svc.Release(f.ctx, agentID, "done with it", nil)
			require.NoError(t, err)
			return func() {
				_, err := f.database.Exec(f.ctx, `DELETE FROM leases WHERE released_at IS NOT NULL`)
				require.NoError(t, err)
			}
		}},
	}
	for _, c := range quiet {
		t.Run(c.name, func(t *testing.T) {
			f := newRoleFixture(t)
			act := c.arrange(t, f)
			before := f.boardRevision(t)

			act()

			assert.Equal(t, before, f.boardRevision(t))
		})
	}
}

// One bump per transaction, however many rows it wrote: the stream sends
// one event, not one per ticket of a bulk move.
func TestBoardRevision_MovesOncePerTransaction(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a, b, c := f.ticket(t, "A"), f.ticket(t, "B"), f.ticket(t, "C")
	before := f.boardRevision(t)

	f.moveMany(t, ticket.StatusParked, a.ID, b.ID, c.ID)

	assert.Equal(t, before+1, f.boardRevision(t))
}

// Concurrent writers each move it: the counter is one row per tenant, and
// a lost update would leave a change behind a revision a board already has.
func TestBoardRevision_CountsConcurrentWriters(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	const writers = 8
	before := f.boardRevision(t)

	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for i := range writers {
		wg.Go(func() {
			_, err := f.svc.Create(f.ctx, &ticket.CreateParams{Title: "Concurrent", Paths: []model.Path{{Path: string(rune('a'+i)) + "/"}}})
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}

	assert.Equal(t, before+writers, f.boardRevision(t))
}

// A transaction mid-way through holds nothing another writer in the tenant
// needs: the counter is bumped at commit, not when the row is written.
// Bumped as each row was written, the open transaction would hold the
// tenant's one counter row until it finished, and every other write --
// in production, every claim and edit -- would queue behind it.
//
// Verified: with the triggers made NOT DEFERRABLE, the second writer waits
// on the lock and this fails.
func TestBoardRevision_AnOpenTransactionDoesNotBlockOtherWriters(t *testing.T) {
	f := newFixture(t)
	first, second := f.ticket(t, "First"), f.ticket(t, "Second")
	before := f.boardRevision(t)

	written := make(chan struct{})
	finish := make(chan struct{})
	held := make(chan error, 1)
	go func() {
		held <- f.database.InTx(f.ctx, func(q db.Querier) error {
			if _, err := q.Exec(f.ctx, `UPDATE tickets SET priority = 'high' WHERE id = $1`, first.ID); err != nil {
				return err
			}
			close(written)
			<-finish
			return nil
		})
	}()
	<-written

	other := make(chan error, 1)
	go func() {
		_, err := f.database.Exec(f.ctx, `UPDATE tickets SET priority = 'low' WHERE id = $1`, second.ID)
		other <- err
	}()
	deadline := time.Now().Add(10 * time.Second)
	for done := false; !done; {
		select {
		case err := <-other:
			require.NoError(t, err)
			done = true
		case <-time.After(5 * time.Millisecond):
			if f.lockWaiters(t) > 0 {
				close(finish)
				require.NoError(t, <-held)
				t.Fatal("the second writer waited on the open transaction")
			}
			require.True(t, time.Now().Before(deadline), "the second writer neither finished nor waited")
		}
	}
	close(finish)
	require.NoError(t, <-held)

	assert.Equal(t, before+2, f.boardRevision(t))
}

// Each tenant's board has its own revision: another tenant's changes
// neither move it nor show through it.
func TestBoardRevision_IsPerTenant(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	other := db.WithTenant(context.Background(), dbtest.NewTenant(t, f.database))
	store := newStore(f.database)
	f.ticket(t, "Mine")
	mine := f.boardRevision(t)

	otherSvc := ticket.NewService(store, slog.DiscardHandler)
	for range 3 {
		_, err := otherSvc.Create(other, &ticket.CreateParams{Title: "Theirs"})
		require.NoError(t, err)
	}

	assert.Equal(t, mine, f.boardRevision(t), "another tenant's tickets don't move mine")
	theirs, err := store.BoardRevision(other)
	require.NoError(t, err)
	assert.Equal(t, int64(3), theirs, "theirs counts only their own three")
}

// A write with no tenant set -- the owner's, in a data migration or a test
// fixture -- moves the board of each tenant whose rows it changed, one
// transaction across several of them included, rather than failing for
// want of a tenant.
func TestBoardRevision_MovesForEachTenantAnUnscopedWriteChanged(t *testing.T) {
	t.Parallel()
	app, owner := dbtest.WithOwnerDB(t)
	store := newStore(app)
	svc := ticket.NewService(store, slog.DiscardHandler)
	tenants := []context.Context{
		db.WithTenant(context.Background(), dbtest.NewTenant(t, app)),
		db.WithTenant(context.Background(), dbtest.NewTenant(t, app)),
	}
	before := make([]int64, len(tenants))
	for i, ctx := range tenants {
		_, err := svc.Create(ctx, &ticket.CreateParams{Title: "Everyone's"})
		require.NoError(t, err)
		before[i], err = store.BoardRevision(ctx)
		require.NoError(t, err)
	}

	require.NoError(t, owner.InGlobalTx(context.Background(), func(q db.Querier) error {
		_, err := q.Exec(context.Background(), `UPDATE tickets SET priority = 'high'`)
		return err
	}))

	for i, ctx := range tenants {
		after, err := store.BoardRevision(ctx)
		require.NoError(t, err)
		assert.Greater(t, after, before[i], "tenant %d", i)
	}
}

// The log behind the stream's delta, against real Postgres: a change names
// the tickets it touched, and says "reload" for anything it cannot name.

func (f *fixture) boardDelta(t *testing.T, since int64) *ticket.BoardDelta {
	t.Helper()
	store := newStore(f.database)
	delta, err := store.BoardDelta(f.ctx, since, f.boardRevision(t))
	require.NoError(t, err)
	return delta
}

func deltaIDs(delta *ticket.BoardDelta) []int64 {
	ids := []int64{}
	for _, tk := range delta.Tickets {
		ids = append(ids, tk.ID)
	}
	return ids
}

func TestBoardDelta_NamesTheTicketAWriteTouched(t *testing.T) {
	t.Parallel()
	writes := []struct {
		name string
		act  func(t *testing.T, f *roleFixture, tk *ticket.Ticket) func()
	}{
		{"ticket", func(t *testing.T, f *roleFixture, tk *ticket.Ticket) func() {
			return f.exec(t, `UPDATE tickets SET title = 'renamed' WHERE id = $1`, tk.ID)
		}},
		{"ticket_paths", func(t *testing.T, f *roleFixture, tk *ticket.Ticket) func() {
			return f.exec(t, `INSERT INTO ticket_paths (ticket_id, path) VALUES ($1, 'x/')`, tk.ID)
		}},
		{"ticket_comments", func(t *testing.T, f *roleFixture, tk *ticket.Ticket) func() {
			return f.exec(t, `INSERT INTO ticket_comments (ticket_id, author_user_id, body) VALUES ($1, $2, 'hi')`, tk.ID, f.human)
		}},
		{"leases", func(t *testing.T, f *roleFixture, tk *ticket.Ticket) func() {
			agentID := f.agent(t, "holder")
			return f.exec(t, `INSERT INTO leases (ticket_id, attempt_number, agent_id, expires_at) VALUES ($1, 1, $2, now() + interval '1 hour')`, tk.ID, agentID)
		}},
	}
	for _, w := range writes {
		t.Run(w.name, func(t *testing.T) {
			f := newRoleFixture(t)
			tk := f.ticket(t, "Changed")
			f.ticket(t, "Bystander")
			act := w.act(t, f, tk)
			before := f.boardRevision(t)

			act()

			delta := f.boardDelta(t, before)
			assert.False(t, delta.Reload)
			assert.Equal(t, []int64{tk.ID}, deltaIDs(delta))
			assert.Empty(t, delta.Tickets[0].Body, "a summary, as the list returns")
		})
	}
}

// A ticket shows its blockers' titles and status, so a blocker's change
// redraws what it blocks.
func TestBoardDelta_IncludesTheTicketsABlockerBlocks(t *testing.T) {
	t.Parallel()
	f := newRoleFixture(t)
	blocker, blocked := f.ticket(t, "Blocker"), f.ticket(t, "Blocked")
	f.exec(t, `INSERT INTO ticket_deps (ticket_id, depends_on_ticket_id) VALUES ($1, $2)`, blocked.ID, blocker.ID)()
	before := f.boardRevision(t)

	f.exec(t, `UPDATE tickets SET title = 'renamed' WHERE id = $1`, blocker.ID)()

	assert.ElementsMatch(t, []int64{blocker.ID, blocked.ID}, deltaIDs(f.boardDelta(t, before)))
}

func TestBoardDelta_SpansEveryRevisionSinceTheCursor(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a, b := f.ticket(t, "A"), f.ticket(t, "B")
	before := f.boardRevision(t)

	f.exec(t, `UPDATE tickets SET title = 'a2' WHERE id = $1`, a.ID)()
	f.exec(t, `UPDATE tickets SET title = 'b2' WHERE id = $1`, b.ID)()

	delta := f.boardDelta(t, before)
	assert.False(t, delta.Reload)
	assert.ElementsMatch(t, []int64{a.ID, b.ID}, deltaIDs(delta))
}

func TestBoardDelta_CarriesTheTicketsCurrentValues(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	tk := f.ticket(t, "Before")
	before := f.boardRevision(t)

	f.exec(t, `UPDATE tickets SET title = 'After', priority = 'high' WHERE id = $1`, tk.ID)()

	delta := f.boardDelta(t, before)
	require.Len(t, delta.Tickets, 1)
	assert.Equal(t, "After", delta.Tickets[0].Title)
	assert.Equal(t, model.Priority("high"), delta.Tickets[0].Priority)
}

func TestBoardDelta_NamesExactlyTheMostTicketsItMay(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	for i := 0; i < 50; i++ {
		f.ticket(t, "Bulk")
	}
	before := f.boardRevision(t)

	f.exec(t, `UPDATE tickets SET priority = 'high'`)()

	delta := f.boardDelta(t, before)
	assert.False(t, delta.Reload)
	assert.Len(t, delta.Tickets, 50)
}

func TestBoardDelta_NoChangeIsEmpty(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.ticket(t, "A")

	delta := f.boardDelta(t, f.boardRevision(t))

	assert.False(t, delta.Reload)
	assert.Empty(t, delta.Tickets)
}

func TestBoardDelta_AsksForAReloadWhenItCannotNameTheTickets(t *testing.T) {
	t.Parallel()
	cases := map[string]func(t *testing.T, f *roleFixture) func(){
		"a change that names no ticket": func(t *testing.T, f *roleFixture) func() {
			role := f.role(t, "before")
			return f.exec(t, `UPDATE agent_roles SET name = 'after' WHERE id = $1`, role)
		},
		"a deleted ticket": func(t *testing.T, f *roleFixture) func() {
			tk := f.ticket(t, "Doomed")
			return f.exec(t, `DELETE FROM tickets WHERE id = $1`, tk.ID)
		},
		"more tickets than a delta names": func(t *testing.T, f *roleFixture) func() {
			for i := 0; i < 51; i++ {
				f.ticket(t, "Bulk")
			}
			return f.exec(t, `UPDATE tickets SET priority = 'high'`)
		},
		"a log pruned past the cursor": func(t *testing.T, f *roleFixture) func() {
			tk := f.ticket(t, "Changed")
			return func() {
				f.exec(t, `UPDATE tickets SET title = 'x' WHERE id = $1`, tk.ID)()
				f.exec(t, `DELETE FROM board_changes`)()
			}
		},
	}
	for name, arrange := range cases {
		t.Run(name, func(t *testing.T) {
			f := newRoleFixture(t)
			act := arrange(t, f)
			before := f.boardRevision(t)

			act()

			delta := f.boardDelta(t, before)
			assert.True(t, delta.Reload)
			assert.Empty(t, delta.Tickets)
		})
	}
}

// Only the log is bounded: the oldest rows go as new revisions land.
func TestBoardDelta_KeepsAboundedLog(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	tk := f.ticket(t, "Busy")
	for i := 0; i < 1005; i++ {
		f.exec(t, `UPDATE tickets SET title = $2 WHERE id = $1`, tk.ID, "t"+string(rune('a'+i%26)))()
	}

	var rows int
	require.NoError(t, f.database.QueryRow(f.ctx, `SELECT count(*) FROM board_changes`).Scan(&rows))

	assert.LessOrEqual(t, rows, 1001)
	assert.True(t, f.boardDelta(t, 1).Reload, "a cursor from before the log")
}
