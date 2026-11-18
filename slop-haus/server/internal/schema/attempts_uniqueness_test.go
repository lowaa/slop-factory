package schema_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func (f *attemptsFixture) newAgent(name string) uuid.UUID {
	f.t.Helper()
	id := uuid.New()
	f.exec(`INSERT INTO agents (id, name) VALUES ($1, $2)`, id, name)
	return id
}

func (f *attemptsFixture) leaseOn(ticket int64, attempt int, agent uuid.UUID) error {
	f.t.Helper()
	return f.execErr(`INSERT INTO leases (ticket_id, attempt_number, agent_id, expires_at)
		VALUES ($1, $2, $3, now() + interval '1 hour')`, ticket, attempt, agent)
}

func (f *attemptsFixture) changeOn(ticket int64, attempt int, number int) error {
	f.t.Helper()
	return f.execErr(`INSERT INTO ticket_changes (ticket_id, attempt_number, host, owner, repo, number, head_branch, base_branch, draft, opened_by_agent_id)
		VALUES ($1, $2, 'h', 'o', 'r', $3, 'b', 'main', true, $4)`, ticket, attempt, number, f.agent)
}

// attempt is NULL for a ticket-level kind.
func (f *attemptsFixture) proposal(ticket int64, attempt *int, kind string) error {
	f.t.Helper()
	return f.execErr(`INSERT INTO pending_actions (id, ticket_id, attempt_number, agent_id, kind, payload, base_status, base_revision, expires_at)
		VALUES (gen_random_uuid(), $1, $2, $3, $4, '{}', 'open', 0, now() + interval '1 hour')`, ticket, attempt, f.agent, kind)
}

func TestLeasesAreUniquePerAttemptNotPerTicket(t *testing.T) {
	f := newAttemptsFixture(t)
	id := f.ticket("open")
	f.addAttempt(id, 2)
	a1, a2, a3 := f.agent, f.newAgent("two"), f.newAgent("three")

	require.NoError(t, f.leaseOn(id, 1, a1))
	assert.NoError(t, f.leaseOn(id, 2, a2), "a second attempt of the ticket is leased beside the first")
	assert.Error(t, f.leaseOn(id, 1, a3), "a second live lease on the same attempt is refused")
	assert.Error(t, f.leaseOn(id, 2, a3), "and on the other")

	other := f.ticket("open")
	assert.Error(t, f.leaseOn(other, 1, a1), "one agent still holds one lease")

	f.exec(`UPDATE leases SET released_at = now() WHERE ticket_id = $1 AND attempt_number = 1`, id)
	assert.NoError(t, f.leaseOn(id, 1, a3), "a released lease frees its attempt")
}

func TestLiveChangesAreUniquePerAttempt(t *testing.T) {
	f := newAttemptsFixture(t)
	id := f.ticket("open")
	f.addAttempt(id, 2)

	require.NoError(t, f.changeOn(id, 1, 1))
	assert.NoError(t, f.changeOn(id, 2, 2), "each attempt has a live change")
	assert.Error(t, f.changeOn(id, 1, 3), "a second live change on an attempt is refused")

	f.exec(`UPDATE ticket_changes SET superseded_at = now() WHERE number = 1`)
	assert.NoError(t, f.changeOn(id, 1, 3), "a superseded change frees the attempt")
}

func TestOnePendingActionPerKindPerAttempt(t *testing.T) {
	f := newAttemptsFixture(t)
	id := f.ticket("open")
	f.addAttempt(id, 2)
	one, two := 1, 2

	require.NoError(t, f.proposal(id, &one, "review_decision"))
	assert.NoError(t, f.proposal(id, &two, "review_decision"), "the same kind on another attempt")
	assert.NoError(t, f.proposal(id, &one, "discussion_answer"), "another kind on the same attempt")
	assert.Error(t, f.proposal(id, &one, "review_decision"), "the same kind on the same attempt is refused")

	require.NoError(t, f.proposal(id, nil, "ready"))
	assert.Error(t, f.proposal(id, nil, "ready"), "ticket-level actions, with no attempt, stay one per ticket")
	assert.NoError(t, f.proposal(id, nil, "status"), "per kind")
}

func TestAShadowAttemptIsNeverPromoted(t *testing.T) {
	f := newAttemptsFixture(t)
	id := f.ticket("open")
	f.addAttempt(id, 2)

	assert.False(t, f.shadow(id, 2), "an attempt is not a shadow unless it says so")
	f.exec(`UPDATE attempts SET shadow = true WHERE ticket_id = $1 AND number = 2`, id)
	assert.True(t, f.shadow(id, 2))
	assert.Error(t, f.execErr(`UPDATE attempts SET promoted_at = now() WHERE ticket_id = $1 AND number = 2`, id), "a shadow is not promoted")
	assert.Error(t, f.execErr(`UPDATE attempts SET shadow = true WHERE ticket_id = $1 AND number = 1`, id), "the promoted attempt is not made a shadow")
	assert.Error(t, f.execErr(`INSERT INTO attempts (ticket_id, number, status, shadow, promoted_at) VALUES ($1, 3, 'queued', true, now())`, id))
}

func (f *attemptsFixture) shadow(ticket int64, attempt int) bool {
	f.t.Helper()
	var s bool
	require.NoError(f.t, f.database.QueryRow(f.ctx,
		`SELECT shadow FROM attempts WHERE ticket_id = $1 AND number = $2`, ticket, attempt).Scan(&s))
	return s
}
