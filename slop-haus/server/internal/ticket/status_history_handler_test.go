package ticket_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/pendingaction"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

func (f *fixture) statusHistory(t *testing.T, mux http.Handler, id int64) []ticket.StatusChangeBody {
	t.Helper()
	rec := send(t, mux, http.MethodGet, fmt.Sprintf("/api/tickets/%d/status_history", id), "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var listed struct {
		Changes []ticket.StatusChangeBody `json:"changes"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &listed))
	return listed.Changes
}

func (f *fixture) moveAs(t *testing.T, id int64, to ticket.Status, cause string) {
	t.Helper()
	ctx := ticket.WithCause(ticket.WithUser(f.ctx, f.human), cause)
	_, err := f.svc.Update(ctx, id, &ticket.UpdateParams{Status: &to})
	require.NoError(t, err)
}

// The history reads oldest first, each change naming what moved it: the
// person who filed and parked it, the agent that claimed it, and the system
// when the lease ran out.
func TestStatusHistory_OldestFirstWithEachKindOfActor(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	mux := f.boardMux()
	a := f.agent(t, "builder-1")
	filed, err := f.svc.Create(f.ctx, &ticket.CreateParams{Title: "Filed by a person", CreatedByUserID: &f.human})
	require.NoError(t, err)
	f.claim(t, a)
	f.expire(t, a)
	f.claim(t, f.agent(t, "builder-2")) // runs the sweep that expires a's lease

	got := f.statusHistory(t, mux, filed.ID)

	require.Len(t, got, 4)
	assert.Nil(t, got[0].FromStatus, "the first change is the ticket's creation")
	for i, c := range got {
		if i > 0 {
			assert.False(t, c.ChangedAt.Before(got[i-1].ChangedAt), "oldest first")
			assert.Equal(t, got[i-1].ToStatus, *c.FromStatus, "each change picks up where the last left off")
		}
	}
	assert.Equal(t, f.human.String(), *got[0].ActorID)
	assert.Equal(t, "user", got[0].ActorKind)

	// created by the person, claimed by a, put back by the expiry sweep,
	// claimed by builder-2.
	claimed, expired := got[1], got[2]
	assert.Equal(t, "agent", claimed.ActorKind)
	assert.Equal(t, ptr(a.String()), claimed.ActorID)
	assert.Equal(t, &ticket.StatusCauseBody{Kind: "other", Text: ptr("claimed")}, claimed.Cause)
	assert.Equal(t, "system", expired.ActorKind)
	assert.Nil(t, expired.ActorID)
	assert.False(t, expired.ActorIDDeleted)
	assert.Equal(t, &ticket.StatusCauseBody{Kind: "other", Text: ptr("lease_expired")}, expired.Cause)
	assert.Equal(t, "agent", got[3].ActorKind)
	assert.NotEqual(t, claimed.ActorID, got[3].ActorID, "the second claim is another agent's")
}

func TestStatusHistory_ParsesTheCause(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	mux := f.boardMux()
	tk := f.ticket(t, "Moved for reasons", "a/")
	proposal, auto := uuid.New(), uuid.New()
	f.moveAs(t, tk.ID, ticket.StatusParked, pendingaction.CauseFor(proposal))
	f.moveAs(t, tk.ID, ticket.StatusPreparing, pendingaction.AutoCauseFor(auto))
	f.moveAs(t, tk.ID, ticket.StatusParked, "proposal:not-a-proposal-id")
	f.moveAs(t, tk.ID, ticket.StatusPreparing, "proposal:"+proposal.String()+" something_else")
	f.moveAs(t, tk.ID, ticket.StatusParked, "")

	got := f.statusHistory(t, mux, tk.ID)

	require.Len(t, got, 6)
	assert.Equal(t, &ticket.StatusCauseBody{Kind: "proposal", ProposalID: ptr(proposal.String())}, got[1].Cause)
	assert.Equal(t, &ticket.StatusCauseBody{Kind: "proposal", ProposalID: ptr(auto.String()), AutoApproved: true}, got[2].Cause)
	assert.Equal(t, &ticket.StatusCauseBody{Kind: "other", Text: ptr("proposal:not-a-proposal-id")}, got[3].Cause,
		"a malformed proposal cause is shown as it was recorded")
	assert.Equal(t, &ticket.StatusCauseBody{Kind: "other", Text: ptr("proposal:" + proposal.String() + " something_else")}, got[4].Cause)
	assert.Nil(t, got[5].Cause)
}

// A removed account leaves no id on the wire, only the flag.
func TestStatusHistory_ADepartedPersonsMoveIsFlaggedWithoutTheirID(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	mux := f.boardMux()
	tk := f.ticket(t, "Parked by someone who left", "a/")
	f.moveAs(t, tk.ID, ticket.StatusParked, "")
	_, err := f.database.Global().Exec(context.Background(), `DELETE FROM global_users WHERE id = $1`, f.human)
	require.NoError(t, err)

	rec := send(t, mux, http.MethodGet, fmt.Sprintf("/api/tickets/%d/status_history", tk.ID), "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.NotContains(t, rec.Body.String(), f.human.String())
	var listed struct {
		Changes []ticket.StatusChangeBody `json:"changes"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &listed))
	last := listed.Changes[len(listed.Changes)-1]
	assert.Equal(t, "user", last.ActorKind)
	assert.Nil(t, last.ActorID)
	assert.True(t, last.ActorIDDeleted)
}

func TestStatusHistory_MissingTicketIsNotFound(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	rec := send(t, f.boardMux(), http.MethodGet, "/api/tickets/9999/status_history", "")
	assert.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
}

// Tenant A asking for an id only tenant B has is refused, and for an id both
// have gets its own history, not B's.
func TestStatusHistory_AnotherTenantsTicketIsNotFound(t *testing.T) {
	t.Parallel()
	a, b := newTwoTenants(t)
	a.ticket(t, "A's only ticket")
	b.ticket(t, "B's first")
	tb2 := b.ticket(t, "B's second")

	_, err := a.svc.ListStatusChanges(a.ctx, tb2.ID)
	assert.ErrorIs(t, err, ticket.ErrNotFound)

	own, err := a.svc.ListStatusChanges(a.ctx, 1)
	require.NoError(t, err)
	require.Len(t, own, 1)
	other, err := b.svc.ListStatusChanges(b.ctx, 1)
	require.NoError(t, err)
	assert.NotEqual(t, own[0].ID, other[0].ID, "#1 in each tenant is a different ticket's history")
}

func TestParseStatusCause(t *testing.T) {
	t.Parallel()
	id := uuid.New()
	for _, c := range []struct {
		raw  string
		want *ticket.StatusCause
	}{
		{"", nil},
		{pendingaction.CauseFor(id), &ticket.StatusCause{Kind: ticket.CauseProposal, ProposalID: &id}},
		{pendingaction.AutoCauseFor(id), &ticket.StatusCause{Kind: ticket.CauseProposal, ProposalID: &id, AutoApproved: true}},
		{pendingaction.StewardCauseFor(id), &ticket.StatusCause{Kind: ticket.CauseProposal, ProposalID: &id, AutoApproved: true}},
		{"claimed", &ticket.StatusCause{Kind: ticket.CauseOther, Text: "claimed"}},
		{"proposal:", &ticket.StatusCause{Kind: ticket.CauseOther, Text: "proposal:"}},
		{"proposal: auto_approved", &ticket.StatusCause{Kind: ticket.CauseOther, Text: "proposal: auto_approved"}},
		{"proposal:" + id.String() + "x", &ticket.StatusCause{Kind: ticket.CauseOther, Text: "proposal:" + id.String() + "x"}},
	} {
		assert.Equal(t, c.want, ticket.ParseStatusCause(c.raw), c.raw)
	}
}
