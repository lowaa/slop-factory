// Package conformance is the suite every TicketingSystem runs, so that
// local mode and slop-tickets are held to the same rules: the permission
// rules, the blocker graph (lifted from slop-haus's blocker tests), the
// status transitions, and the event feed's ordering, resume and
// backpressure.
//
// An implementation's test calls Run with a NewBackend. The suite tests
// behaviour only through the TicketingSystem interface, so a failure here is
// a rule the implementation doesn't keep, not a detail of how it keeps it.
package conformance

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/ident"
	"github.com/lowaa/slop-factory/slop-bot/internal/tickets"
)

// Backend is one store of tickets, empty when made.
type Backend interface {
	// Open returns a new TicketingSystem over the backend's store. Every
	// system one Backend opens sees the same tickets and the same feed, the
	// way a restarted daemon sees what the last one left: the suite's resume
	// tests stop one and open the next. Open releases what it opens when
	// the test ends.
	Open(t *testing.T) tickets.TicketingSystem
}

// NewBackend makes an empty Backend enforcing cfg. It is called once per
// test, from a parallel test, so backends must not share a store.
//
// The suite takes a backend rather than a system because a system can't be
// restarted (Start runs once), and resuming from a cursor is only proven
// across a real restart.
type NewBackend func(t *testing.T, cfg *tickets.Config) Backend

type test struct {
	name string
	run  func(t *testing.T, nb NewBackend)
}

// Run runs the whole suite, each test in parallel on a backend of its own.
func Run(t *testing.T, nb NewBackend) {
	t.Helper()
	var all []test
	for _, group := range [][]test{crudTests, statusTests, permissionTests, blockerTests, feedTests} {
		all = append(all, group...)
	}
	for _, tc := range all {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tc.run(t, nb)
		})
	}
}

// fixture is one test's system and the helpers its tests share.
type fixture struct {
	t       *testing.T
	ctx     context.Context
	backend Backend
	sys     tickets.TicketingSystem
}

func newFixture(t *testing.T, nb NewBackend) *fixture {
	return newFixtureWith(t, nb, &tickets.Config{AgentsMaySetUrgent: true})
}

func newFixtureWith(t *testing.T, nb NewBackend, cfg *tickets.Config) *fixture {
	t.Helper()
	b := nb(t, cfg)
	return &fixture{t: t, ctx: t.Context(), backend: b, sys: b.Open(t)}
}

var user = ident.UserPrincipal()

func (f *fixture) create(by ident.Principal, nt *tickets.NewTicket) *tickets.Ticket {
	f.t.Helper()
	created, err := f.sys.Create(f.ctx, by, nt)
	require.NoError(f.t, err)
	return created
}

// ticket is a person's ticket, open.
func (f *fixture) ticket(title string) *tickets.Ticket {
	f.t.Helper()
	return f.create(user, &tickets.NewTicket{Title: title, Status: tickets.StatusOpen})
}

// agent is an agent working a ticket of its own, which a person filed and
// the work on which has started.
func (f *fixture) agent(name string) ident.Principal {
	f.t.Helper()
	home := f.ticket(name + "'s ticket")
	f.reach(home.ID, tickets.StatusInProgress)
	return ident.AgentPrincipal(ident.AgentID(name), home.ID)
}

// filedBy is a ticket agent a filed, which starts preparing.
func (f *fixture) filedBy(a ident.Principal, title string, blockedOn ...ident.TicketID) *tickets.Ticket {
	f.t.Helper()
	return f.create(a, &tickets.NewTicket{Title: title, BlockedOn: blockedOn})
}

func (f *fixture) get(id ident.TicketID) *tickets.Ticket {
	f.t.Helper()
	got, err := f.sys.Get(f.ctx, id)
	require.NoError(f.t, err)
	return got
}

func (f *fixture) setStatus(by ident.Principal, id ident.TicketID, to tickets.Status) (*tickets.Ticket, error) {
	return f.sys.Update(f.ctx, by, id, &tickets.TicketChange{Status: &to})
}

// pathFromOpen is how the user takes an open ticket to each status.
var pathFromOpen = map[tickets.Status][]tickets.Status{
	tickets.StatusPreparing:     {tickets.StatusPreparing},
	tickets.StatusDiscussion:    {tickets.StatusDiscussion},
	tickets.StatusOpen:          {},
	tickets.StatusInProgress:    {tickets.StatusInProgress},
	tickets.StatusMisconfigured: {tickets.StatusMisconfigured},
	tickets.StatusDone:          {tickets.StatusInProgress, tickets.StatusDone},
	tickets.StatusCancelled:     {tickets.StatusCancelled},
}

// reach has the user move the ticket to status, by way of open.
func (f *fixture) reach(id ident.TicketID, status tickets.Status) {
	f.t.Helper()
	if f.get(id).Status != tickets.StatusOpen {
		_, err := f.setStatus(user, id, tickets.StatusOpen)
		require.NoError(f.t, err)
	}
	for _, s := range pathFromOpen[status] {
		_, err := f.setStatus(user, id, s)
		require.NoError(f.t, err, "moving %s to %s", id, s)
	}
}

// blockedBy has the user make ticket wait on blocker.
func (f *fixture) blockedBy(ticket, blocker ident.TicketID) {
	f.t.Helper()
	change, err := f.sys.AddBlocker(f.ctx, user, ticket, blocker)
	require.NoError(f.t, err)
	require.True(f.t, change.Changed)
}

func (f *fixture) comments(id ident.TicketID) []tickets.Comment {
	f.t.Helper()
	return f.get(id).Comments
}

func ids(refs []tickets.TicketRef) []ident.TicketID {
	out := []ident.TicketID{}
	for _, r := range refs {
		out = append(out, r.ID)
	}
	return out
}

func ptr[T any](v T) *T { return &v }

// missing is an id no test's backend issues.
const missing ident.TicketID = "999999"
