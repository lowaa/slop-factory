package ticket_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// heldLease has agentID claim a ticket, returning it and its lease.
func (f *fixture) heldLease(t *testing.T, agentID uuid.UUID) (*ticket.Ticket, *ticket.Lease) {
	t.Helper()
	claimed := f.claim(t, agentID)
	require.NotNil(t, claimed)
	lease, err := f.svc.LeaseFor(f.ctx, agentID)
	require.NoError(t, err)
	return claimed, lease
}

func (f *fixture) leaseEnd(t *testing.T, leaseID int64) *ticket.LeaseEnd {
	t.Helper()
	end, err := f.svc.LeaseEnd(f.ctx, leaseID)
	require.NoError(t, err)
	return end
}

func TestLeaseEnd_NilWhileTheLeaseIsHeld(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	f.ticket(t, "Work", "server/")
	_, lease := f.heldLease(t, a)

	assert.Nil(t, f.leaseEnd(t, lease.ID))
}

// Each way the holder gives a ticket up is a different thing for the
// dispatcher to do next, so each is told apart.
func TestLeaseEnd_NamesTheExitTheHolderTook(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		exit func(f *fixture, a uuid.UUID) error
		want ticket.LeaseExit
	}{
		"done": {want: ticket.ExitDone, exit: func(f *fixture, a uuid.UUID) error {
			_, err := f.svc.Done(f.ctx, a, &ticket.DoneParams{Summary: "finished"})
			return err
		}},
		"review": {want: ticket.ExitReview, exit: func(f *fixture, a uuid.UUID) error {
			_, err := f.svc.Review(f.ctx, a, &ticket.ReviewParams{Reason: "reviewed by a person", PRURL: githubRefLink, Handoff: "PR open."})
			return err
		}},
		"discussion": {want: ticket.ExitDiscussion, exit: func(f *fixture, a uuid.UUID) error {
			_, err := f.svc.Discuss(f.ctx, a, "Which shape?", "Half done.")
			return err
		}},
		"preparing": {want: ticket.ExitPreparing, exit: func(f *fixture, a uuid.UUID) error {
			_, err := f.svc.Prepare(f.ctx, a, "Not ready.")
			return err
		}},
		"released": {want: ticket.ExitReleased, exit: func(f *fixture, a uuid.UUID) error {
			_, err := f.svc.Release(f.ctx, a, "Handing back.", nil)
			return err
		}},
		// A release's reason is free text: saying "done" finishes nothing.
		"a release that says done": {want: ticket.ExitReleased, exit: func(f *fixture, a uuid.UUID) error {
			_, err := f.svc.Release(f.ctx, a, "done", nil)
			return err
		}},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			a := f.agent(t, "builder-1")
			f.ticket(t, "Work", "server/")
			held, lease := f.heldLease(t, a)

			require.NoError(t, tc.exit(f, a))

			end := f.leaseEnd(t, lease.ID)
			require.NotNil(t, end)
			assert.Equal(t, ticket.LeaseFinished, end.Kind)
			assert.Equal(t, tc.want, end.Exit)
			assert.Equal(t, held.ID, end.TicketID)
		})
	}
}

func TestLeaseEnd_AHumanTakingTheTicketBackIsARevocation(t *testing.T) {
	t.Parallel()
	for _, status := range []ticket.Status{ticket.StatusPreparing, ticket.StatusCancelled} {
		t.Run(string(status), func(t *testing.T) {
			f := newFixture(t)
			a := f.agent(t, "builder-1")
			f.ticket(t, "Work", "server/")
			held, lease := f.heldLease(t, a)

			f.moveMany(t, status, held.ID)

			end := f.leaseEnd(t, lease.ID)
			require.NotNil(t, end)
			assert.Equal(t, ticket.LeaseRevoked, end.Kind)
			assert.Equal(t, status, end.Status)
			assert.Equal(t, held.ID, end.TicketID)
		})
	}
}

func TestLeaseEnd_ALapsedLease(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	f.ticket(t, "Work", "server/")
	held, lease := f.heldLease(t, a)

	f.expire(t, a)
	end := f.leaseEnd(t, lease.ID)
	require.NotNil(t, end, "past its deadline it is lost, swept or not")
	assert.Equal(t, ticket.LeaseLapsed, end.Kind)
	assert.Equal(t, held.ID, end.TicketID)

	// Another agent's claim sweeps it and takes the ticket: the same answer.
	b := f.agent(t, "builder-2")
	f.claim(t, b)
	end = f.leaseEnd(t, lease.ID)
	require.NotNil(t, end)
	assert.Equal(t, ticket.LeaseLapsed, end.Kind)
	assert.Equal(t, held.ID, end.TicketID)
}

func TestLeaseEnd_ALeaseThatIsGoneIsLapsed(t *testing.T) {
	t.Parallel()
	f := newFixture(t)

	end := f.leaseEnd(t, 999999)
	require.NotNil(t, end)
	assert.Equal(t, ticket.LeaseLapsed, end.Kind)
}

func TestLeaseEnd_ADisabledAgentLosesItsLease(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	f.ticket(t, "Work", "server/")
	held, lease := f.heldLease(t, a)

	_, err := f.agents.SetDisabled(f.ctx, a, true)
	require.NoError(t, err)

	end := f.leaseEnd(t, lease.ID)
	require.NotNil(t, end)
	assert.Equal(t, ticket.LeaseLapsed, end.Kind, "not the holder's own exit")
	assert.Equal(t, held.ID, end.TicketID)
}

// A watcher of a lease is not its holder: reading must not keep it alive,
// nor take the notice a human's revocation owes the holder.
func TestLeaseEnd_ReadsWithoutRenewingOrTakingTheNotice(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	f.ticket(t, "Work", "server/")
	held, lease := f.heldLease(t, a)

	expiry := func() string {
		t.Helper()
		var at string
		require.NoError(t, f.database.QueryRow(f.ctx,
			`SELECT expires_at::text FROM leases WHERE id = $1`, lease.ID).Scan(&at))
		return at
	}
	before := expiry()
	require.Nil(t, f.leaseEnd(t, lease.ID))
	assert.Equal(t, before, expiry(), "reading a held lease extends nothing")

	f.moveMany(t, ticket.StatusCancelled, held.ID)
	require.NotNil(t, f.leaseEnd(t, lease.ID))
	require.NotNil(t, f.leaseEnd(t, lease.ID), "and can be read again")

	var notice *ticket.LeaseRevokedError
	require.ErrorAs(t, f.svc.Heartbeat(f.ctx, a), &notice, "the holder's next call is still owed the notice")
	assert.Equal(t, held.ID, notice.TicketID)
}

func TestLeaseEnd_NamesTheHolderSoACallerCanCheckItIsItsOwn(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	f.ticket(t, "Work", "server/")
	_, lease := f.heldLease(t, a)
	_, err := f.svc.Release(f.ctx, a, "Handing back.", nil)
	require.NoError(t, err)

	end := f.leaseEnd(t, lease.ID)

	assert.Equal(t, a, end.AgentID)
}
