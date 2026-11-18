package agentmsg_test

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/agent"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/agentmsg"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// tenantFixture is one tenant, in a database shared with another.
type tenantFixture struct {
	database *db.DB
	ctx      context.Context
	tenant   uuid.UUID
	human    uuid.UUID
	agents   *agent.Service
	tickets  *ticket.Service
	svc      *agentmsg.Service
	agentID  uuid.UUID
}

func newTenant(t *testing.T, database *db.DB, cfg agentmsg.Config) *tenantFixture {
	t.Helper()
	tenant := dbtest.NewTenant(t, database)
	ctx := db.WithTenant(context.Background(), tenant)
	tickets := ticket.NewService(ticket.NewPostgresStore(database), slog.DiscardHandler)
	agents := agent.NewService(agent.NewPostgresStore(database), tickets, nil, slog.DiscardHandler)
	created, err := agents.Create(ctx, "builder-"+uuid.NewString()[:8])
	require.NoError(t, err)
	return &tenantFixture{
		database: database, ctx: ctx, tenant: tenant, human: dbtest.NewUser(t, database),
		agents: agents, tickets: tickets, agentID: created.ID,
		svc: agentmsg.NewService(agentmsg.NewPostgresStore(database), cfg, slog.DiscardHandler),
	}
}

func newFixture(t *testing.T) *tenantFixture {
	t.Helper()
	return newTenant(t, dbtest.WithDB(t), agentmsg.Config{})
}

func (f *tenantFixture) send(t *testing.T, body string) *agentmsg.Message {
	t.Helper()
	m, err := f.svc.Send(f.ctx, &authn.User{ID: f.human}, f.agentID, body)
	require.NoError(t, err)
	return m
}

// holds has the fixture's agent claim a fresh ticket.
func (f *tenantFixture) holds(t *testing.T, title string) *ticket.Ticket {
	t.Helper()
	created, err := f.tickets.Create(f.ctx, &ticket.CreateParams{Title: title, Status: ticket.StatusOpen})
	require.NoError(t, err)
	claimed, err := f.tickets.Claim(f.ctx, f.agentID, &created.ID)
	require.NoError(t, err)
	require.NotNil(t, claimed.Ticket, "the agent must hold the ticket")
	return created
}

func bodies(messages []agentmsg.Message) []string {
	out := []string{}
	for _, m := range messages {
		out = append(out, m.Body)
	}
	return out
}

func TestDeliver_HandsEverythingPendingOverOnceOldestFirst(t *testing.T) {
	f := newFixture(t)
	f.send(t, "first")
	f.send(t, "second")

	pending, err := f.svc.Pending(f.ctx, f.agentID)
	require.NoError(t, err)
	assert.Equal(t, 2, pending)

	got, err := f.svc.Deliver(f.ctx, f.agentID)
	require.NoError(t, err)
	assert.Equal(t, []string{"first", "second"}, bodies(got))
	for _, m := range got {
		assert.NotNil(t, m.DeliveredAt)
		require.NotNil(t, m.AuthorUserID)
		assert.Equal(t, f.human, *m.AuthorUserID)
		assert.False(t, m.AuthorUserDeleted)
	}

	again, err := f.svc.Deliver(f.ctx, f.agentID)
	require.NoError(t, err)
	assert.Empty(t, again, "delivered once")
	pending, err = f.svc.Pending(f.ctx, f.agentID)
	require.NoError(t, err)
	assert.Zero(t, pending)
}

// waitingForLocks is how many backends are blocked on a lock, which is how
// a test knows its callers have reached the contended statement without
// sleeping. It counts the whole server -- a row wait names no database --
// so waiters of tests running beside this one can only make it early.
func waitingForLocks(t *testing.T, q db.Querier, ctx context.Context) int {
	t.Helper()
	var waiting int
	require.NoError(t, q.QueryRow(ctx, `SELECT count(*) FROM pg_locks WHERE NOT granted`).Scan(&waiting))
	return waiting
}

// Callers racing for the same messages must split them with no duplicate and
// no loss. The overlap is forced rather than hoped for: a transaction holds
// the pending rows locked while every caller starts, so each is inside its
// claim -- an atomic UPDATE waits on the rows and then finds them taken; a
// SELECT followed by an UPDATE would have read them all already -- before
// any is let through.
func TestDeliver_ConcurrentCallsDeliverEachMessageExactlyOnce(t *testing.T) {
	f := newFixture(t)
	const total = 15
	for i := range total {
		f.send(t, strings.Repeat("x", i+1))
	}

	// The pool holds at least four connections on any machine, one of them
	// the lock-holding transaction below.
	const callers = 3
	results := make([][]agentmsg.Message, callers)
	var wg sync.WaitGroup
	err := f.database.InTx(f.ctx, func(tx db.Querier) error {
		_, err := tx.Exec(f.ctx, `SELECT id FROM agent_messages WHERE agent_id = $1 FOR UPDATE`, f.agentID)
		require.NoError(t, err)
		for i := range callers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				got, err := f.svc.Deliver(f.ctx, f.agentID)
				assert.NoError(t, err)
				results[i] = got
			}()
		}
		require.Eventually(t, func() bool { return waitingForLocks(t, tx, f.ctx) >= callers },
			10*time.Second, 10*time.Millisecond, "every caller must be inside its claim")
		return nil
	})
	require.NoError(t, err)
	wg.Wait()

	seen := map[int64]int{}
	for _, got := range results {
		for _, m := range got {
			seen[m.ID]++
		}
	}
	assert.Len(t, seen, total, "none lost")
	for id, n := range seen {
		assert.Equal(t, 1, n, "message %d delivered to more than one call", id)
	}
}

func TestSend_Refusals(t *testing.T) {
	database := dbtest.WithDB(t)
	f := newTenant(t, database, agentmsg.Config{MaxBody: 10, MaxPending: 2})

	_, err := f.svc.Send(f.ctx, &authn.User{ID: f.human}, f.agentID, "  \n ")
	assert.ErrorIs(t, err, agentmsg.ErrEmptyBody)

	_, err = f.svc.Send(f.ctx, &authn.User{ID: f.human}, f.agentID, "12345678901")
	assert.ErrorIs(t, err, agentmsg.ErrTooLong)
	// Characters, not bytes.
	_, err = f.svc.Send(f.ctx, &authn.User{ID: f.human}, f.agentID, strings.Repeat("é", 10))
	assert.NoError(t, err)

	_, err = f.svc.Send(f.ctx, &authn.User{ID: f.human}, f.agentID, "second")
	require.NoError(t, err)
	_, err = f.svc.Send(f.ctx, &authn.User{ID: f.human}, f.agentID, "third")
	assert.ErrorIs(t, err, agentmsg.ErrTooManyPending)

	_, err = f.svc.Deliver(f.ctx, f.agentID)
	require.NoError(t, err)
	_, err = f.svc.Send(f.ctx, &authn.User{ID: f.human}, f.agentID, "room again")
	assert.NoError(t, err, "delivered messages no longer count")

	_, err = f.agents.SetDisabled(f.ctx, f.agentID, true)
	require.NoError(t, err)
	_, err = f.svc.Send(f.ctx, &authn.User{ID: f.human}, f.agentID, "off")
	assert.ErrorIs(t, err, agentmsg.ErrAgentDisabled)
}

func TestTenancy_AnotherTenantsAgentCannotBeAddressedOrRead(t *testing.T) {
	database := dbtest.WithDB(t)
	a := newTenant(t, database, agentmsg.Config{})
	b := newTenant(t, database, agentmsg.Config{})
	a.send(t, "for A only")

	_, err := b.svc.Send(b.ctx, &authn.User{ID: b.human}, a.agentID, "across the wall")
	assert.ErrorIs(t, err, agentmsg.ErrAgentNotFound)
	_, err = b.svc.Send(b.ctx, &authn.User{ID: b.human}, uuid.New(), "nobody")
	assert.ErrorIs(t, err, agentmsg.ErrAgentNotFound)

	got, err := b.svc.Deliver(b.ctx, a.agentID)
	require.NoError(t, err)
	assert.Empty(t, got, "B cannot take A's messages")
	recent, err := b.svc.Recent(b.ctx, &authn.User{ID: uuid.New()}, 100)
	require.NoError(t, err)
	assert.Empty(t, recent)

	got, err = a.svc.Deliver(a.ctx, a.agentID)
	require.NoError(t, err)
	assert.Equal(t, []string{"for A only"}, bodies(got), "and A's are still there")
}

func TestHumanComment_OnAHeldTicketIsDeliveredToTheHolder(t *testing.T) {
	f := newFixture(t)
	held := f.holds(t, "Being worked")

	_, err := f.tickets.AddComment(f.ctx, held.ID, ticket.UserAuthor(f.human), "use the other approach")
	require.NoError(t, err)

	got, err := f.svc.Deliver(f.ctx, f.agentID)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, agentmsg.KindTicketComment, got[0].Kind)
	require.NotNil(t, got[0].TicketID)
	assert.Equal(t, held.ID, *got[0].TicketID)
	assert.Equal(t, "use the other approach", got[0].Body)
	require.NotNil(t, got[0].AuthorUserID)
	assert.Equal(t, f.human, *got[0].AuthorUserID)
}

func TestComment_NeverReachesAnyoneButAHumansCommentOnAHeldTicket(t *testing.T) {
	f := newFixture(t)
	held := f.holds(t, "Being worked")
	unheld, err := f.tickets.Create(f.ctx, &ticket.CreateParams{Title: "Nobody's", Status: ticket.StatusOpen})
	require.NoError(t, err)

	// The holder's own comment does not echo back, and neither does
	// another agent's.
	other, err := f.agents.Create(f.ctx, "other")
	require.NoError(t, err)
	_, err = f.tickets.AddComment(f.ctx, held.ID, ticket.AgentAuthor(f.agentID), "my own note")
	require.NoError(t, err)
	_, err = f.tickets.AddComment(f.ctx, held.ID, ticket.AgentAuthor(other.ID), "a peer's note")
	require.NoError(t, err)
	// A human's comment on a ticket nobody holds goes nowhere.
	_, err = f.tickets.AddComment(f.ctx, unheld.ID, ticket.UserAuthor(f.human), "to no one")
	require.NoError(t, err)

	for _, id := range []uuid.UUID{f.agentID, other.ID} {
		got, err := f.svc.Deliver(f.ctx, id)
		require.NoError(t, err)
		assert.Empty(t, got)
	}

	// And once the agent has let go of the ticket, it is nobody's again.
	_, err = f.tickets.Release(f.ctx, f.agentID, "done here", nil)
	require.NoError(t, err)
	_, err = f.tickets.AddComment(f.ctx, held.ID, ticket.UserAuthor(f.human), "after release")
	require.NoError(t, err)
	got, err := f.svc.Deliver(f.ctx, f.agentID)
	require.NoError(t, err)
	assert.Empty(t, got)
}

// A comment must never fail because of the copy made for the holder: it is
// cut, not refused, and capped, not an error.
func TestHumanComment_CopyIsBoundedAndNeverFailsTheComment(t *testing.T) {
	f := newFixture(t)
	held := f.holds(t, "Being worked")

	long := strings.Repeat("é", 5000) // multibyte: a byte cut would split one
	_, err := f.tickets.AddComment(f.ctx, held.ID, ticket.UserAuthor(f.human), long)
	require.NoError(t, err)
	got, err := f.svc.Deliver(f.ctx, f.agentID)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Contains(t, got[0].Body, "read the rest on the ticket")
	assert.Less(t, len(got[0].Body), len(long))
	comments, err := f.tickets.ListComments(f.ctx, held.ID)
	require.NoError(t, err)
	assert.Equal(t, long, comments[len(comments)-1].Body, "the comment itself is whole")

	for range agentmsg.DefaultMaxPending + 5 {
		_, err := f.tickets.AddComment(f.ctx, held.ID, ticket.UserAuthor(f.human), "again")
		require.NoError(t, err, "past the pending cap the comment still lands")
	}
	pending, err := f.svc.Pending(f.ctx, f.agentID)
	require.NoError(t, err)
	assert.Equal(t, agentmsg.DefaultMaxPending, pending)
}

func TestRender_FramesWhoSaidItAndWhatToDo(t *testing.T) {
	ticketID := int64(12)
	out := agentmsg.Render([]agentmsg.Message{
		{Kind: agentmsg.KindMessage, AuthorName: "Ada", Body: "stop"},
		{Kind: agentmsg.KindTicketComment, AuthorName: "Ada", TicketID: &ticketID, Body: "also this"},
	})
	assert.Equal(t, 2, strings.Count(out, "MESSAGE FROM THE HUMAN:"))
	assert.Contains(t, out, "from Ada")
	assert.Contains(t, out, "ticket #12, which you hold")
	assert.Contains(t, out, "instruction from the human")
	assert.Contains(t, out, "ticket comment")
	assert.Empty(t, agentmsg.Render(nil))
}

func TestRender_ACommentMadeThroughAnAgentNamesBoth(t *testing.T) {
	ticketID := int64(12)
	out := agentmsg.Render([]agentmsg.Message{
		{Kind: agentmsg.KindTicketComment, AuthorName: "Ada", ViaAgentName: "my-console", TicketID: &ticketID, Body: "also this"},
	})
	assert.Contains(t, out, "(agent my-console, for Ada, commented on ticket #12, which you hold, at ")
	assert.Contains(t, out, "MESSAGE FROM THE HUMAN:")
}

// A lease past its deadline that the sweep has not reached is not a holder:
// a comment then must not be copied to an agent about to lose the ticket.
func TestHumanComment_OnAnExpiredLeaseGoesNowhere(t *testing.T) {
	f := newFixture(t)
	held := f.holds(t, "Being worked")
	_, err := f.database.Exec(f.ctx, `UPDATE leases SET expires_at = now() - interval '1 hour' WHERE ticket_id = $1`, held.ID)
	require.NoError(t, err)

	_, err = f.tickets.AddComment(f.ctx, held.ID, ticket.UserAuthor(f.human), "too late")
	require.NoError(t, err)

	got, err := f.svc.Deliver(f.ctx, f.agentID)
	require.NoError(t, err)
	assert.Empty(t, got)
}

// The cap holds when a send and a comment's copy race for the last place:
// both count and insert under the agent's lock, so one is admitted and the
// other refused. They are forced to overlap by holding the lock until both
// are waiting for it.
func TestPendingCap_OneOfASendAndACommentCopyTakesTheLastPlace(t *testing.T) {
	f := newFixture(t)
	held := f.holds(t, "Being worked")
	for range agentmsg.DefaultMaxPending - 1 {
		f.send(t, "filler")
	}

	sendErr := make(chan error, 1)
	commentErr := make(chan error, 1)
	err := f.database.InTx(f.ctx, func(tx db.Querier) error {
		_, err := tx.Exec(f.ctx, `SELECT pg_advisory_xact_lock(hashtextextended('agent_messages:' || $1::text, 0))`, f.agentID)
		require.NoError(t, err)
		go func() {
			_, err := f.svc.Send(f.ctx, &authn.User{ID: f.human}, f.agentID, "sent")
			sendErr <- err
		}()
		go func() {
			_, err := f.tickets.AddComment(f.ctx, held.ID, ticket.UserAuthor(f.human), "commented")
			commentErr <- err
		}()
		require.Eventually(t, func() bool {
			var waiting int
			require.NoError(t, tx.QueryRow(f.ctx, `
				SELECT count(*) FROM pg_locks l
				 WHERE l.locktype = 'advisory' AND NOT l.granted
				   AND l.database = (SELECT oid FROM pg_database WHERE datname = current_database())`).Scan(&waiting))
			return waiting >= 2
		}, 10*time.Second, 10*time.Millisecond, "both producers must be waiting for the agent's lock")
		return nil
	})
	require.NoError(t, err)

	require.NoError(t, <-commentErr, "a comment is never refused for the copy")
	refused := <-sendErr
	pending, err := f.svc.Pending(f.ctx, f.agentID)
	require.NoError(t, err)
	assert.Equal(t, agentmsg.DefaultMaxPending, pending, "exactly one of the two took the last place")
	got, err := f.svc.Deliver(f.ctx, f.agentID)
	require.NoError(t, err)
	copied := 0
	for _, m := range got {
		if m.Kind == agentmsg.KindTicketComment {
			copied++
		}
	}
	if refused != nil {
		assert.ErrorIs(t, refused, agentmsg.ErrTooManyPending)
		assert.Equal(t, 1, copied, "the send lost the place to the copy")
	} else {
		assert.Zero(t, copied, "the copy lost the place to the send")
	}
}

// The Agents screen follows delivery over the board stream, so a message
// turning delivered must move the board revision -- and a send, which the
// screen refetches after by itself, must not.
func TestDeliver_MovesTheBoardRevisionTheAgentsScreenFollows(t *testing.T) {
	f := newFixture(t)
	revision := func() int64 {
		r, err := ticket.NewPostgresStore(f.database).BoardRevision(f.ctx)
		require.NoError(t, err)
		return r
	}
	beforeSend := revision()
	f.send(t, "use the other approach")
	afterSend := revision()
	assert.Equal(t, beforeSend, afterSend, "a send is refetched by the screen itself")

	delivered, err := f.svc.Deliver(f.ctx, f.agentID)
	require.NoError(t, err)
	require.NotEmpty(t, delivered)

	assert.Greater(t, revision(), afterSend)
}

// A person's comment on a held ticket inserts a copy for the agent, which
// the Agents screen lists too. It needs no trigger of its own on
// agent_messages: the comment's insert moves the board revision in the same
// transaction.
func TestHumanComment_CopyAppearsOnTheAgentsScreenViaTheCommentsRevisionBump(t *testing.T) {
	f := newFixture(t)
	held := f.holds(t, "Being worked")
	store := ticket.NewPostgresStore(f.database)
	before, err := store.BoardRevision(f.ctx)
	require.NoError(t, err)

	_, err = f.tickets.AddComment(f.ctx, held.ID, ticket.UserAuthor(f.human), "use the other approach")
	require.NoError(t, err)

	after, err := store.BoardRevision(f.ctx)
	require.NoError(t, err)
	assert.Greater(t, after, before)
	listed, err := f.svc.Recent(f.ctx, &authn.User{ID: uuid.New()}, 10)
	require.NoError(t, err)
	assert.Equal(t, []string{"use the other approach"}, bodies(listed))
}

// A conductor is its owner's: a message from another member of the tenant
// would be steering it. Messages to it are conversations, not this route.
func TestSend_AConductorIsNotAddressable(t *testing.T) {
	f := newFixture(t)
	_, err := f.database.Global().Exec(f.ctx,
		`INSERT INTO global_tenant_memberships (id, tenant, user_id, role) VALUES (gen_random_uuid(), $1, $2, 'member')`, f.tenant, f.human)
	require.NoError(t, err)
	conductor := uuid.New()
	_, err = f.database.Exec(f.ctx,
		`INSERT INTO agents (id, name, kind, owner_user_id) VALUES ($1, 'conductor', 'conductor', $2)`, conductor, f.human)
	require.NoError(t, err)

	_, err = f.svc.Send(f.ctx, &authn.User{ID: f.human}, conductor, "do this instead")

	assert.ErrorIs(t, err, agentmsg.ErrAgentNotFound, "not even its owner, here")
}

func TestRender_AnAnonymousAuthorIsAPersonUnlessTheirAccountWasDeleted(t *testing.T) {
	live := agentmsg.Render([]agentmsg.Message{{Kind: agentmsg.KindMessage, Body: "stop"}})
	gone := agentmsg.Render([]agentmsg.Message{{Kind: agentmsg.KindMessage, AuthorUserDeleted: true, Body: "stop"}})

	assert.Contains(t, live, "from a person")
	assert.NotContains(t, live, "deleted")
	assert.Contains(t, gone, "from a deleted user")
}

// A supervisor runs no model, so a message to it would sit unread for good.
func TestSend_ASupervisorIsNotAddressable(t *testing.T) {
	f := newFixture(t)
	supervisor := uuid.New()
	_, err := f.database.Exec(f.ctx,
		`INSERT INTO agents (id, name, kind, slot_cap) VALUES ($1, 'supervisor', 'supervisor', 2)`, supervisor)
	require.NoError(t, err)

	_, err = f.svc.Send(f.ctx, &authn.User{ID: f.human}, supervisor, "hello")

	assert.ErrorIs(t, err, agentmsg.ErrAgentNotFound)
}

func (f *tenantFixture) comment(t *testing.T, ticketID int64, body string) {
	t.Helper()
	_, err := f.tickets.AddComment(f.ctx, ticketID, ticket.UserAuthor(f.human), body)
	require.NoError(t, err)
}

func TestHumanComment_ForALeaseThatEndedIsNeverDeliveredOnAnotherTicket(t *testing.T) {
	f := newFixture(t)
	first := f.holds(t, "First")
	f.comment(t, first.ID, "about the first")

	_, err := f.tickets.Release(f.ctx, f.agentID, "done here", nil)
	require.NoError(t, err)
	f.holds(t, "Second")

	got, err := f.svc.Deliver(f.ctx, f.agentID)
	require.NoError(t, err)
	assert.Empty(t, got)

	pending, err := f.svc.Pending(f.ctx, f.agentID)
	require.NoError(t, err)
	assert.Zero(t, pending, "a dropped copy does not take a place in the cap")

	recent, err := f.svc.Recent(f.ctx, &authn.User{ID: uuid.New()}, 10)
	require.NoError(t, err)
	require.Len(t, recent, 1)
	assert.NotNil(t, recent[0].DroppedAt)
	assert.Nil(t, recent[0].DeliveredAt)
}

func TestHumanComment_WhileTheLeaseIsLiveIsDeliveredAndASecondTicketIsNotMixedIn(t *testing.T) {
	f := newFixture(t)
	held := f.holds(t, "Held")
	f.comment(t, held.ID, "still mine")

	got, err := f.svc.Deliver(f.ctx, f.agentID)
	require.NoError(t, err)
	assert.Equal(t, []string{"still mine"}, bodies(got))
}

func TestDirectMessage_SurvivesALeaseChange(t *testing.T) {
	f := newFixture(t)
	f.holds(t, "First")
	f.send(t, "heads up")

	_, err := f.tickets.Release(f.ctx, f.agentID, "done here", nil)
	require.NoError(t, err)
	f.holds(t, "Second")

	got, err := f.svc.Deliver(f.ctx, f.agentID)
	require.NoError(t, err)
	assert.Equal(t, []string{"heads up"}, bodies(got))
}

// A lease past its deadline that a lock still pins keeps its holder's
// comments queued; when the lease ends they are dropped, and the agent never
// receives them on its next ticket.
func TestHumanComment_OnAStrandedLockedLeaseIsHeldThenDroppedNeverMoved(t *testing.T) {
	f := newFixture(t)
	held := f.holds(t, "Stranded")
	_, err := f.database.Exec(f.ctx, `INSERT INTO locks (name, holder_agent_id, holder_lease_id)
		SELECT 'integration', agent_id, id FROM leases WHERE ticket_id = $1`, held.ID)
	require.NoError(t, err)
	_, err = f.database.Exec(f.ctx, `UPDATE leases SET expires_at = now() - interval '1 hour' WHERE ticket_id = $1`, held.ID)
	require.NoError(t, err)

	f.comment(t, held.ID, "queued behind the stranded lock")
	pending, err := f.svc.Pending(f.ctx, f.agentID)
	require.NoError(t, err)
	assert.Equal(t, 1, pending, "held while the lease is not yet ended")

	_, err = f.database.Exec(f.ctx, `UPDATE leases SET released_at = now(), release_reason = 'force' WHERE ticket_id = $1`, held.ID)
	require.NoError(t, err)
	_, err = f.database.Exec(f.ctx, `DELETE FROM locks`)
	require.NoError(t, err)
	f.holds(t, "Next")

	got, err := f.svc.Deliver(f.ctx, f.agentID)
	require.NoError(t, err)
	assert.Empty(t, got)
}

// A message speaks for a person to another agent, so the service refuses a
// sender or reader that is not one.
func TestSendAndReadRefuseACallerThatIsNotAPerson(t *testing.T) {
	f := newFixture(t)
	f.send(t, "hello")

	_, err := f.svc.Send(f.ctx, nil, f.agentID, "from nobody")
	assert.ErrorIs(t, err, authn.ErrNotPerson)
	_, err = f.svc.Recent(f.ctx, nil, 10)
	assert.ErrorIs(t, err, authn.ErrNotPerson)

	recent, err := f.svc.Recent(f.ctx, &authn.User{ID: f.human}, 10)
	require.NoError(t, err)
	assert.Len(t, recent, 1, "only the person's message was stored")
}
