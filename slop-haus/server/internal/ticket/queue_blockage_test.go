package ticket_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

// stuckBehind has agent "holder" take a ticket on server/ and files another
// on the same scope, leaving the fixture's idle "builder-1" with nothing it
// can claim.
func stuckBehind(t *testing.T, f *policyFixture) (holder uuid.UUID, held, waiting *ticket.Ticket) {
	t.Helper()
	holder = f.agent(t, "holder")
	held, err := f.svc.Create(f.ctx, &ticket.CreateParams{Title: "Held", Paths: []model.Path{{Path: "server/"}}})
	require.NoError(t, err)
	_, err = f.svc.Claim(f.ctx, holder, &held.ID)
	require.NoError(t, err)
	waiting, err = f.svc.Create(f.ctx, &ticket.CreateParams{Title: "Waiting", Paths: []model.Path{{Path: "server/internal/"}}})
	require.NoError(t, err)
	return holder, held, waiting
}

func TestQueueBlockage_NamesWhatTheIdleAgentsAreWaitingOn(t *testing.T) {
	t.Parallel()
	f := newPolicyFixture(t)
	_, held, waiting := stuckBehind(t, f)

	got, err := f.svc.QueueBlockage(f.ctx)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, waiting.ID, got.Blocked.TicketID)
	assert.Equal(t, "Waiting", got.TicketTitle)
	assert.Equal(t, held.ID, got.Blocked.ConflictingTicketID)
	assert.Equal(t, "Held", got.HolderTitle)
	assert.Equal(t, ticket.StatusInProgress, got.HolderStatus)
	assert.Equal(t, "holder", got.HolderAgentName)
	assert.Equal(t, "server/", got.Blocked.ConflictingPath)
}

func TestQueueBlockage_ClearsWhenTheScopeFrees(t *testing.T) {
	t.Parallel()
	f := newPolicyFixture(t)
	holder, _, _ := stuckBehind(t, f)

	_, err := f.svc.Release(f.ctx, holder, "done with it", nil)
	require.NoError(t, err)

	got, err := f.svc.QueueBlockage(f.ctx)
	require.NoError(t, err)
	assert.Nil(t, got, "the released ticket is claimable again, so nothing is stuck")
}

// Some idle agent can take something, so the queue is only busy.
func TestQueueBlockage_NotBlockedWhileAnyWorkIsFree(t *testing.T) {
	t.Parallel()
	f := newPolicyFixture(t)
	stuckBehind(t, f)
	_, err := f.svc.Create(f.ctx, &ticket.CreateParams{Title: "Free", Paths: []model.Path{{Path: "docs/"}}})
	require.NoError(t, err)

	got, err := f.svc.QueueBlockage(f.ctx)
	require.NoError(t, err)
	assert.Nil(t, got)
}

// With every agent busy nobody is idle to be blocked: that is a full fleet,
// not a stuck one.
func TestQueueBlockage_NeedsAnIdleAgent(t *testing.T) {
	t.Parallel()
	f := newPolicyFixture(t)
	stuckBehind(t, f)
	filler, err := f.svc.Create(f.ctx, &ticket.CreateParams{Title: "Filler", Paths: []model.Path{{Path: "docs/"}}})
	require.NoError(t, err)
	_, err = f.svc.Claim(f.ctx, f.agentID, &filler.ID)
	require.NoError(t, err)

	got, err := f.svc.QueueBlockage(f.ctx)
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestQueueBlockage_NotReportedWhileProcessingIsOff(t *testing.T) {
	t.Parallel()
	f := newPolicyFixture(t)
	stuckBehind(t, f)
	f.setProcessing(t, false)

	got, err := f.svc.QueueBlockage(f.ctx)
	require.NoError(t, err)
	assert.Nil(t, got, "nothing is meant to be picked up, so nothing is stuck")
}

func TestQueueBlockage_IsPerTenant(t *testing.T) {
	t.Parallel()
	a, b := newTwoTenants(t)
	held := a.ticket(t, "Held", "server/")
	_, err := a.svc.Claim(a.ctx, a.agentID, &held.ID)
	require.NoError(t, err)
	a.ticket(t, "Waiting", "server/")
	// An idle agent in tenant A is what makes A's queue stuck.
	_, err = a.agents.Create(a.ctx, "second-a")
	require.NoError(t, err)

	got, err := a.svc.QueueBlockage(a.ctx)
	require.NoError(t, err)
	require.NotNil(t, got)

	other, err := b.svc.QueueBlockage(b.ctx)
	require.NoError(t, err)
	assert.Nil(t, other, "tenant B has neither the work nor the holder")
}

func TestQueueBlockageRoute_SendsTheWholeBlockageToTheBoard(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	holder := f.agent(t, "holder")
	f.agent(t, "idle")
	held := f.ticket(t, "Held", "server/")
	_, err := f.svc.Claim(f.ctx, holder, &held.ID)
	require.NoError(t, err)
	waiting := f.ticket(t, "Waiting", "server/internal/")
	mux := f.boardMux()

	read := func() *ticket.QueueBlockageBody {
		rec := send(t, mux, http.MethodGet, "/api/queue/blockage", "")
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var got struct {
			Blockage *ticket.QueueBlockageBody `json:"blockage"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
		return got.Blockage
	}
	assert.Equal(t, &ticket.QueueBlockageBody{
		TicketID: waiting.ID, TicketTitle: "Waiting",
		HolderTicketID: held.ID, HolderTitle: "Held", HolderStatus: ticket.StatusInProgress,
		HolderAgentName: "holder", Path: "server/",
	}, read())

	_, err = f.svc.Release(f.ctx, holder, "done with it", nil)
	require.NoError(t, err)
	assert.Nil(t, read())
}
