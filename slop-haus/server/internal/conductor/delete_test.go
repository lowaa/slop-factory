package conductor_test

import (
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/agent"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/conductor"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
)

func (f *fixture) count(t *testing.T, query string, args ...any) int {
	t.Helper()
	var n int
	require.NoError(t, f.database.QueryRow(f.ctx, query, args...).Scan(&n))
	return n
}

// stopped is a conversation that has ended and whose work the host has
// acknowledged: the state in which a delete goes through.
func (f *fixture) stopped(t *testing.T, owned *agent.OwnedConductor) (*conductor.Conversation, *host) {
	t.Helper()
	c := f.start(t, owned)
	h := f.polling(t, owned)
	_, err := f.svc.Disconnect(f.ctx, owned, c.ID)
	require.NoError(t, err)
	res := f.poll(t, f.svc, h, 0)
	f.poll(t, f.svc, h, res.Items[len(res.Items)-1].Seq)
	return c, h
}

func TestDeletingARestingConversationRemovesItAndItsTranscriptButKeepsUsage(t *testing.T) {
	f := newFixture(t)
	owned := f.owned(t, f.owner, "c1")
	c, _ := f.stopped(t, owned)
	_, err := f.database.Exec(f.ctx, `
		INSERT INTO llm_usage (agent_id, transcript_key, harness, session_id, scope_hint, scope,
			provider, model, billing_mode, requests, input_tokens, output_tokens, cache_read_tokens,
			cache_write_tokens, reasoning_tokens, price_version, unpriced, started_at, ended_at, updated_at, conversation_id)
		VALUES ($1, 'k', 'pi', 's', 'main', 'overhead', 'anthropic', 'claude-opus-5-5', 'subscription',
			1, 7, 1, 0, 0, 0, '', true, now(), now(), now(), $2)`, owned.Agent().ID, c.ID)
	require.NoError(t, err)
	require.Equal(t, 1, f.count(t, `SELECT count(*) FROM conductor_conversation_events WHERE conversation_id = $1`, c.ID))

	require.NoError(t, f.svc.Delete(f.ctx, owned, c.ID))

	_, err = f.svc.Get(f.ctx, owned, c.ID)
	assert.ErrorIs(t, err, conductor.ErrNotFound)
	assert.Zero(t, f.count(t, `SELECT count(*) FROM conductor_conversation_events WHERE conversation_id = $1`, c.ID), "the transcript cascades")
	assert.Equal(t, 1, f.count(t, `SELECT count(*) FROM llm_usage WHERE input_tokens = 7 AND conversation_id IS NULL`),
		"usage stays, with the reference emptied")
}

func TestDeletingALiveConversationEndsItFirstAndWaitsForTheHostsAck(t *testing.T) {
	f := newFixture(t)
	owned := f.owned(t, f.owner, "c1")
	c := f.start(t, owned)
	h := f.polling(t, owned)

	err := f.svc.Delete(f.ctx, owned, c.ID)

	require.ErrorIs(t, err, conductor.ErrWorkPending)
	got, err := f.svc.Get(f.ctx, owned, c.ID)
	require.NoError(t, err)
	assert.Equal(t, conductor.StateStopped, got.State, "it was ended, and the ending committed")
	res := f.poll(t, f.svc, h, 0)
	assert.Equal(t, []conductor.WorkKind{conductor.WorkStart, conductor.WorkStop}, kinds(res.Items), "the host is told to stop it")

	f.poll(t, f.svc, h, res.Items[1].Seq)
	require.NoError(t, f.svc.Delete(f.ctx, owned, c.ID))
	_, err = f.svc.Get(f.ctx, owned, c.ID)
	assert.ErrorIs(t, err, conductor.ErrNotFound)
}

func TestDeletingIsRefusedWhileWorkIsUnacknowledged(t *testing.T) {
	f := newFixture(t)
	owned := f.owned(t, f.owner, "c1")
	c, _ := f.stopped(t, owned)
	// Work for the ended conversation the host has not acknowledged.
	_, err := f.database.Exec(f.ctx, `
		INSERT INTO conductor_work (conductor_id, seq, kind, conversation_id) VALUES ($1, 99, 'stop', $2)`,
		owned.Agent().ID, c.ID)
	require.NoError(t, err)

	err = f.svc.Delete(f.ctx, owned, c.ID)

	assert.ErrorIs(t, err, conductor.ErrWorkPending)
	_, err = f.svc.Get(f.ctx, owned, c.ID)
	assert.NoError(t, err, "and it is still there")
}

func TestOnlyTheConductorsOwnerOrItsOwnTenantCanDelete(t *testing.T) {
	f := newFixture(t)
	mine := f.owned(t, f.owner, "mine")
	theirs := f.owned(t, f.other, "theirs")
	c, _ := f.stopped(t, mine)

	assert.ErrorIs(t, f.svc.Delete(f.ctx, theirs, c.ID), conductor.ErrNotFound, "another conductor's")
	_, err := f.conductors.AuthorizeOwner(f.ctx, f.other, mine.Agent().ID)
	assert.ErrorIs(t, err, agent.ErrNotOwner, "and the other person cannot get the proof for this one")

	otherTenant := db.WithTenant(f.ctx, dbtest.NewTenant(t, f.database))
	assert.ErrorIs(t, f.svc.Delete(otherTenant, mine, c.ID), conductor.ErrNotFound, "another tenant's")
	assert.ErrorIs(t, f.svc.Delete(f.ctx, mine, uuid.New()), conductor.ErrNotFound)

	_, err = f.svc.Get(f.ctx, mine, c.ID)
	assert.NoError(t, err, "and nothing was deleted")
}

func TestDeleteOverHTTP(t *testing.T) {
	r := newRest(t)
	c := r.start(t, map[string]any{})
	path := r.base + "/" + c.ID

	assert.Equal(t, http.StatusForbidden, r.api.do("other", http.MethodDelete, path, nil).Code, "not the owner's, as for every route")
	live := r.api.do("owner", http.MethodDelete, path, nil)
	assert.Equal(t, http.StatusConflict, live.Code, live.Body.String())

	_, err := r.database.Exec(r.ctx, `DELETE FROM conductor_work WHERE conversation_id = $1`, c.ID)
	require.NoError(t, err)
	done := r.api.do("owner", http.MethodDelete, path, nil)
	assert.Equal(t, http.StatusNoContent, done.Code, done.Body.String())
	assert.Equal(t, http.StatusNotFound, r.api.do("owner", http.MethodGet, path, nil).Code)
}

func TestDeletingOneConversationLeavesTheConductorsOthersAndTheirWorkAlone(t *testing.T) {
	f := newFixture(t)
	owned := f.owned(t, f.owner, "c1")
	old, _ := f.stopped(t, owned)
	newer := f.start(t, owned) // its start is unacknowledged work of the same conductor

	require.NoError(t, f.svc.Delete(f.ctx, owned, old.ID), "another conversation's work does not hold it")

	got, err := f.svc.Get(f.ctx, owned, newer.ID)
	require.NoError(t, err)
	assert.Equal(t, conductor.StateStarting, got.State)
	assert.Equal(t, 1, f.count(t, `SELECT count(*) FROM conductor_work WHERE conversation_id = $1`, newer.ID))
}
