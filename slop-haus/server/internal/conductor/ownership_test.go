package conductor_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/agent"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/conductor"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
)

// A person reaches a conversation through the proof of owning its conductor,
// and the conversation must be that conductor's: someone else's conductor,
// even another of the same tenant's, finds it absent.
func TestAnotherConductorsConversationIsNotFound(t *testing.T) {
	f := newFixture(t)
	mine := f.owned(t, f.owner, "mine")
	theirs := f.owned(t, f.other, "theirs")
	c := f.start(t, mine)
	id := uuid.New()

	_, err := f.svc.Get(f.ctx, theirs, c.ID)
	assert.ErrorIs(t, err, conductor.ErrNotFound)
	_, err = f.svc.Send(f.ctx, theirs, c.ID, &conductor.SendRequest{Text: "hi"})
	assert.ErrorIs(t, err, conductor.ErrNotFound)
	_, err = f.svc.Disconnect(f.ctx, theirs, c.ID)
	assert.ErrorIs(t, err, conductor.ErrNotFound)
	_, err = f.svc.Events(f.ctx, theirs, c.ID, 0, 0)
	assert.ErrorIs(t, err, conductor.ErrNotFound)
	_, err = f.svc.Get(f.ctx, mine, id)
	assert.ErrorIs(t, err, conductor.ErrNotFound, "and so is one that does not exist")

	list, err := f.svc.List(f.ctx, theirs, 0)
	require.NoError(t, err)
	assert.Empty(t, list)
	got, err := f.svc.Get(f.ctx, mine, c.ID)
	require.NoError(t, err)
	assert.Equal(t, conductor.StateStarting, got.State, "and nothing of it was touched")
}

func TestAHostReachesOnlyItsOwnConductorsConversationsAndWork(t *testing.T) {
	f := newFixture(t)
	mine := f.owned(t, f.owner, "mine")
	theirs := f.owned(t, f.other, "theirs")
	c := f.start(t, mine)
	intruder := f.polling(t, theirs)

	_, err := f.svc.Report(f.ctx, intruder.proof, c.ID, &conductor.ReportRequest{HostID: intruder.id, Events: []conductor.HostEvent{text("x", 1)}})

	assert.ErrorIs(t, err, conductor.ErrNotFound)
	assert.Empty(t, f.poll(t, f.svc, intruder, 0).Items, "it is not offered the other conductor's work")
	assert.Empty(t, f.transcript(t, mine, c))
}

func TestAnotherTenantsConversationIsNotFound(t *testing.T) {
	f := newFixture(t)
	mine := f.owned(t, f.owner, "mine")
	c := f.start(t, mine)
	elsewhere := dbtest.NewTenant(t, f.database)
	ctx := db.WithTenant(context.Background(), elsewhere)

	// The proof is the tenant's, but the request runs as another: row level
	// security hides the rows whatever the code asks.
	_, err := f.svc.Get(ctx, mine, c.ID)
	assert.ErrorIs(t, err, conductor.ErrNotFound)
	_, err = f.svc.Send(ctx, mine, c.ID, &conductor.SendRequest{Text: "hi"})
	assert.ErrorIs(t, err, conductor.ErrNotFound)
	_, err = f.svc.Events(ctx, mine, c.ID, 0, 0)
	assert.ErrorIs(t, err, conductor.ErrNotFound)
	list, err := f.svc.List(ctx, mine, 0)
	require.NoError(t, err)
	assert.Empty(t, list)
	// And it cannot reach the queue.
	proof, err := f.svc.AuthenticateHost(authn.NewContext(f.ctx, mine.Agent()))
	require.NoError(t, err)
	_, err = f.svc.Poll(ctx, proof, &conductor.PollRequest{HostID: uuid.New()})
	assert.ErrorIs(t, err, conductor.ErrNotFound)
}

func TestOnlyAConductorsOwnCredentialIsAHost(t *testing.T) {
	f := newFixture(t)
	worker, err := f.agents.Create(f.ctx, "a-worker")
	require.NoError(t, err)

	_, err = f.svc.AuthenticateHost(authn.NewContext(f.ctx, worker))
	assert.ErrorIs(t, err, conductor.ErrNotConductor)
	_, err = f.svc.AuthenticateHost(f.ctx)
	assert.ErrorIs(t, err, authn.ErrNoAgent)
}

func TestDisablingAConductorEndsItsLiveConversationAndTellsTheTranscript(t *testing.T) {
	f := newFixture(t)
	owned := f.owned(t, f.owner, "c1")
	c := f.start(t, owned)

	_, err := f.conductors.SetDisabled(f.ctx, f.admin, owned.Agent().ID, true)
	require.NoError(t, err)

	got, err := f.svc.Get(f.ctx, owned, c.ID)
	require.NoError(t, err)
	assert.Equal(t, conductor.StateStopped, got.State)
	events := f.transcript(t, owned, c)
	require.Len(t, events, 1)
	assert.Equal(t, conductor.EventStopped, events[0].Kind)

	// Switching it back on does not revive the conversation, and the
	// conductor is free for a new one.
	_, err = f.conductors.SetDisabled(f.ctx, f.owner, owned.Agent().ID, false)
	require.NoError(t, err)
	got, err = f.svc.Get(f.ctx, owned, c.ID)
	require.NoError(t, err)
	assert.Equal(t, conductor.StateStopped, got.State)
	f.start(t, owned)
}

func TestTheOwnerLeavingTheTenantEndsTheConversation(t *testing.T) {
	f := newFixture(t)
	owned := f.owned(t, f.owner, "c1")
	c := f.start(t, owned)

	_, err := f.database.Global().Exec(context.Background(),
		`DELETE FROM global_tenant_memberships WHERE tenant = $1 AND user_id = $2`, f.tenant, f.owner.ID)
	require.NoError(t, err)

	got, err := f.svc.Get(f.ctx, owned, c.ID)
	require.NoError(t, err)
	assert.Equal(t, conductor.StateStopped, got.State)
}

// Every method that acts for a person takes the proof that its conductor
// was checked for them, and every one that acts for a host takes a *Host: a
// method added with neither would let a handler reach a conversation without
// an ownership check.
func TestEveryServiceMethodTakesAProof(t *testing.T) {
	exempt := map[string]string{
		"AuthenticateHost": "makes the *Host proof",
		"Close":            "no conversation",
		"Closed":           "no conversation",
		"MaxWait":          "no conversation",
	}
	owned, host := reflect.TypeOf(&agent.OwnedConductor{}), reflect.TypeOf(&conductor.Host{})
	typ := reflect.TypeOf(&conductor.Service{})
	require.NotZero(t, typ.NumMethod())
	for i := range typ.NumMethod() {
		m := typ.Method(i)
		if _, ok := exempt[m.Name]; ok {
			continue
		}
		// In: receiver, context, proof.
		require.GreaterOrEqual(t, m.Type.NumIn(), 3, m.Name)
		proof := m.Type.In(2)
		assert.True(t, proof == owned || proof == host, "%s takes %s, not a proof of ownership or a host", m.Name, proof)
	}
}
