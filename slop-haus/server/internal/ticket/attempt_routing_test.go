package ticket_test

import (
	"fmt"
	"log/slog"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit/audittest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

func (f *fixture) namedVariantVersion(t *testing.T, name string, version int) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	require.NoError(t, f.database.QueryRow(f.ctx,
		`INSERT INTO variant_versions (name, version, harness) VALUES ($1, $2, 'claude') RETURNING id`, name, version).Scan(&id))
	return id
}

func (f *fixture) archiveVariant(t *testing.T, version uuid.UUID) {
	t.Helper()
	_, err := f.database.Exec(f.ctx, `INSERT INTO variant_archives (version_id) VALUES ($1)`, version)
	require.NoError(t, err)
}

// routeTicket names a variant on the ticket's attempt directly, as C5's
// tests set up several attempts: the service's own path is tested apart.
func (f *fixture) routeTicket(t *testing.T, ticketID int64, version uuid.UUID) {
	t.Helper()
	f.setAttempt(t, ticketID, "variant_version_id = $2", version)
}

func (f *fixture) release(t *testing.T, agentID uuid.UUID) {
	t.Helper()
	_, err := f.svc.Release(f.ctx, agentID, "test", nil)
	require.NoError(t, err)
}

func (f *fixture) agentOnVariant(t *testing.T, name string, version uuid.UUID) uuid.UUID {
	t.Helper()
	id := f.agent(t, name)
	f.moveAgent(t, id, &version)
	return id
}

func (f *fixture) moveAgent(t *testing.T, agentID uuid.UUID, version *uuid.UUID) {
	t.Helper()
	revision := (*int)(nil)
	if version != nil {
		one := 1
		revision = &one
	}
	_, err := f.database.Exec(f.ctx, `UPDATE agents SET variant_id = $2, variant_revision = $3 WHERE id = $1`, agentID, version, revision)
	require.NoError(t, err)
}

func TestClaim_RoutesAnAttemptToAgentsOnExactlyItsVariantVersion(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	v1 := f.namedVariantVersion(t, "fast", 1)
	v2 := f.namedVariantVersion(t, "fast", 2)
	routed := f.ticket(t, "routed to v1", "a/")
	f.routeTicket(t, routed.ID, v1)
	plain := f.ticket(t, "routed to none", "b/")

	claimedBy := func(t *testing.T, agentID uuid.UUID) []int64 {
		t.Helper()
		var got []int64
		for {
			result, err := f.svc.Claim(f.ctx, agentID, nil)
			require.NoError(t, err)
			if result.Ticket == nil {
				return got
			}
			got = append(got, result.Ticket.ID)
			// One lease per agent: hand it back, to see what else it could take.
			f.release(t, agentID)
			f.forceStatus(t, result.Ticket.ID, ticket.StatusPreparing)
		}
	}

	t.Run("an agent on the version claims only what names it", func(t *testing.T) {
		assert.Equal(t, []int64{routed.ID}, claimedBy(t, f.agentOnVariant(t, "on-v1", v1)))
	})
	t.Run("another version of the same name claims neither", func(t *testing.T) {
		f.forceStatus(t, routed.ID, ticket.StatusOpen)
		f.forceStatus(t, plain.ID, ticket.StatusOpen)
		assert.Empty(t, claimedBy(t, f.agentOnVariant(t, "on-v2", v2)))
	})
	t.Run("an agent in none claims only what names none", func(t *testing.T) {
		f.forceStatus(t, routed.ID, ticket.StatusOpen)
		f.forceStatus(t, plain.ID, ticket.StatusOpen)
		assert.Equal(t, []int64{plain.ID}, claimedBy(t, f.agent(t, "in-none")))
	})
	t.Run("a directed claim is routed alike", func(t *testing.T) {
		f.forceStatus(t, routed.ID, ticket.StatusOpen)
		f.forceStatus(t, plain.ID, ticket.StatusOpen)
		in := f.agentOnVariant(t, "directed-v2", v2)
		for _, id := range []int64{routed.ID, plain.ID} {
			result, err := f.svc.Claim(f.ctx, in, &id)
			require.NoError(t, err)
			assert.Nil(t, result.Ticket, "ticket %d", id)
		}
	})
}

func TestClaim_ARoutedAttemptStillNeedsTheTicketsRoles(t *testing.T) {
	t.Parallel()
	f := newRoleFixture(t)
	v1 := f.namedVariantVersion(t, "fast", 1)
	role := f.role(t, "deploy")
	tk := f.requiring(t, "needs deploy on v1", model.RoleMatchAny, role)
	f.routeTicket(t, tk.ID, v1)

	lacking := f.agentOnVariant(t, "right-variant-no-role", v1)
	assert.Zero(t, f.claimed(t, lacking))

	other := f.agent(t, "role-no-variant")
	f.give(t, other, role)
	assert.Zero(t, f.claimed(t, other))

	both := f.agentOnVariant(t, "both", v1)
	f.give(t, both, role)
	assert.Equal(t, tk.ID, f.claimed(t, both))
}

func TestClaim_ArchivingAVersionDoesNotStopItsAttemptsRouting(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	v1 := f.namedVariantVersion(t, "fast", 1)
	tk := f.ticket(t, "routed", "a/")
	f.routeTicket(t, tk.ID, v1)
	f.archiveVariant(t, v1)

	result, err := f.svc.Claim(f.ctx, f.agentOnVariant(t, "on-v1", v1), nil)
	require.NoError(t, err)
	require.NotNil(t, result.Ticket)
	assert.Equal(t, tk.ID, result.Ticket.ID)
}

func TestPeek_AgreesWithClaimOnRouting(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	v1 := f.namedVariantVersion(t, "fast", 1)
	tk := f.ticket(t, "routed", "a/")
	f.routeTicket(t, tk.ID, v1)
	store := newStore(f.database)

	peeked, err := store.Peek(f.ctx, f.agent(t, "in-none"), nil)
	require.NoError(t, err)
	assert.Nil(t, peeked.Ticket)

	peeked, err = store.Peek(f.ctx, f.agentOnVariant(t, "on-v1", v1), nil)
	require.NoError(t, err)
	require.NotNil(t, peeked.Ticket)
	assert.Equal(t, tk.ID, peeked.Ticket.ID)
}

func TestNoEligibleAgent_CountsTheVariantMatch(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	v1 := f.namedVariantVersion(t, "fast", 1)
	v2 := f.namedVariantVersion(t, "fast", 2)
	onV1 := f.agentOnVariant(t, "on-v1", v1)
	tk := f.ticket(t, "routed to v2", "a/")
	f.routeTicket(t, tk.ID, v2)
	flag := func() bool {
		got, err := f.svc.Get(f.ctx, tk.ID)
		require.NoError(t, err)
		return got.NoEligibleAgent
	}

	assert.True(t, flag(), "the only agent is on v1, not v2")
	f.moveAgent(t, onV1, &v2)
	assert.False(t, flag(), "an agent on v2 will take it")
	f.disable(t, onV1)
	assert.True(t, flag(), "a disabled agent takes nothing")
	f.enable(t, onV1)
	f.moveAgent(t, onV1, nil)
	assert.True(t, flag(), "an agent in none does not take an attempt that names v2")

	plain := f.ticket(t, "no variant, no roles", "b/")
	f.moveAgent(t, onV1, &v1)
	got, err := f.svc.Get(f.ctx, plain.ID)
	require.NoError(t, err)
	assert.False(t, got.NoEligibleAgent, "a ticket that asks for neither is never flagged")
}

func TestSetAttemptVariant_SetsClearsAndRefusesOnceLeased(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	v1 := f.namedVariantVersion(t, "fast", 1)
	v2 := f.namedVariantVersion(t, "fast", 2)
	tk := f.ticket(t, "routed", "a/")
	variantOf := func() *model.AttemptVariant {
		attempts, err := f.svc.Attempts(f.ctx, tk.ID)
		require.NoError(t, err)
		return attempts[0].Variant
	}

	change, err := f.svc.SetAttemptVariant(f.ctx, f.human, tk.ID, &v1)
	require.NoError(t, err)
	assert.True(t, change.Changed)
	require.NotNil(t, variantOf())
	assert.Equal(t, v1, variantOf().VersionID)
	assert.Equal(t, "fast", variantOf().Name)
	assert.False(t, variantOf().Archived)

	_, err = f.svc.SetAttemptVariant(f.ctx, f.human, tk.ID, &v2)
	require.NoError(t, err)
	assert.Equal(t, v2, variantOf().VersionID)

	change, err = f.svc.SetAttemptVariant(f.ctx, f.human, tk.ID, &v2)
	require.NoError(t, err)
	assert.False(t, change.Changed, "setting what it already is changes nothing")

	_, err = f.svc.SetAttemptVariant(f.ctx, f.human, tk.ID, nil)
	require.NoError(t, err)
	assert.Nil(t, variantOf())

	_, err = f.svc.SetAttemptVariant(f.ctx, f.human, tk.ID, &v1)
	require.NoError(t, err)
	holder := f.agentOnVariant(t, "holder", v1)
	_, err = f.svc.Claim(f.ctx, holder, &tk.ID)
	require.NoError(t, err)

	_, err = f.svc.SetAttemptVariant(f.ctx, f.human, tk.ID, nil)
	require.ErrorIs(t, err, ticket.ErrVariantLocked, "while leased")
	f.release(t, holder)
	_, err = f.svc.SetAttemptVariant(f.ctx, f.human, tk.ID, &v2)
	require.ErrorIs(t, err, ticket.ErrVariantLocked, "a handoff does not reopen it")
	assert.Equal(t, v1, variantOf().VersionID, "refused changes leave it alone")
	attempts, err := f.svc.Attempts(f.ctx, tk.ID)
	require.NoError(t, err)
	assert.True(t, attempts[0].Leased, "it reads as leased")

	// The pruner deletes released leases; the attempt stays leased.
	_, err = f.database.Exec(f.ctx, `DELETE FROM leases WHERE ticket_id = $1`, tk.ID)
	require.NoError(t, err)
	_, err = f.svc.SetAttemptVariant(f.ctx, f.human, tk.ID, nil)
	require.ErrorIs(t, err, ticket.ErrVariantLocked, "a pruned lease does not reopen it")
}

func TestSetAttemptVariant_RefusesAnArchivedOrUnknownVersionButKeepsAnExistingOne(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	old := f.namedVariantVersion(t, "fast", 1)
	tk := f.ticket(t, "routed", "a/")
	_, err := f.svc.SetAttemptVariant(f.ctx, f.human, tk.ID, &old)
	require.NoError(t, err)
	f.archiveVariant(t, old)

	other := f.ticket(t, "other", "b/")
	_, err = f.svc.SetAttemptVariant(f.ctx, f.human, other.ID, &old)
	require.ErrorIs(t, err, ticket.ErrInvalidVariant, "newly choosing an archived version")

	change, err := f.svc.SetAttemptVariant(f.ctx, f.human, tk.ID, &old)
	require.NoError(t, err, "an attempt already routed to it keeps it")
	assert.False(t, change.Changed)

	missing := uuid.New()
	_, err = f.svc.SetAttemptVariant(f.ctx, f.human, tk.ID, &missing)
	require.ErrorIs(t, err, ticket.ErrVariantNotFound)
	_, err = f.svc.SetAttemptVariant(f.ctx, f.human, 999999, &old)
	require.ErrorIs(t, err, ticket.ErrNotFound)
}

func TestSetAttemptVariant_IsAuditedOnlyWhenItChangesSomething(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	events, counter := audittest.NewRecorder()
	svc := ticket.NewService(newStore(f.database), slog.DiscardHandler, ticket.WithEvents(events))
	v1 := f.namedVariantVersion(t, "fast", 1)
	tk := f.ticket(t, "routed", "a/")

	_, err := svc.SetAttemptVariant(f.ctx, f.human, tk.ID, &v1)
	require.NoError(t, err)
	_, err = svc.SetAttemptVariant(f.ctx, f.human, tk.ID, &v1)
	require.NoError(t, err)
	assert.Equal(t, 1, counter.Count(audit.AttemptVariantChanged))
	_, err = svc.SetAttemptVariant(f.ctx, f.human, tk.ID, nil)
	require.NoError(t, err)
	assert.Equal(t, 2, counter.Count(audit.AttemptVariantChanged))
}

func TestSetAttemptVariant_BumpsTheBoardRevision(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	v1 := f.namedVariantVersion(t, "fast", 1)
	tk := f.ticket(t, "routed", "a/")
	revision := func() int64 {
		var r int64
		require.NoError(t, f.database.QueryRow(f.ctx, `SELECT revision FROM board_revisions`).Scan(&r))
		return r
	}
	before := revision()
	_, err := f.svc.SetAttemptVariant(f.ctx, f.human, tk.ID, &v1)
	require.NoError(t, err)
	assert.Greater(t, revision(), before)
}

func TestPutTicketVariant_Handler(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	mux := f.boardMux()
	v1 := f.namedVariantVersion(t, "fast", 1)
	tk := f.ticket(t, "routed", "a/")
	put := func(body string) (int, ticket.TicketBody) {
		rec := send(t, mux, http.MethodPut, fmt.Sprintf("/api/tickets/%d/variant", tk.ID), body)
		if rec.Code != http.StatusOK {
			return rec.Code, ticket.TicketBody{}
		}
		return rec.Code, decodeTicket(t, rec.Body.Bytes())
	}

	code, got := put(fmt.Sprintf(`{"version_id": %q}`, v1))
	require.Equal(t, http.StatusOK, code)
	require.Len(t, got.Attempts, 1)
	require.NotNil(t, got.Attempts[0].Variant)
	assert.Equal(t, v1.String(), got.Attempts[0].Variant.VersionID)
	assert.True(t, got.Attempts[0].VariantSettable)

	code, _ = put(`{"version_id": "nope"}`)
	assert.Equal(t, http.StatusUnprocessableEntity, code)
	code, _ = put(fmt.Sprintf(`{"version_id": %q}`, uuid.New()))
	assert.Equal(t, http.StatusNotFound, code)

	other := f.namedVariantVersion(t, "slow", 1)
	f.archiveVariant(t, other)
	code, _ = put(fmt.Sprintf(`{"version_id": %q}`, other))
	assert.Equal(t, http.StatusUnprocessableEntity, code, "archived")

	_, err := f.svc.Claim(f.ctx, f.agentOnVariant(t, "holder", v1), &tk.ID)
	require.NoError(t, err)
	code, _ = put(`{"version_id": null}`)
	assert.Equal(t, http.StatusConflict, code, "leased")
	rec := send(t, mux, http.MethodGet, fmt.Sprintf("/api/tickets/%d", tk.ID), "")
	assert.False(t, decodeTicket(t, rec.Body.Bytes()).Attempts[0].VariantSettable)
}

// The candidate query is the first check; attemptToClaim re-checks at the
// grant, so a routing change that lands between them (made without the claim
// lock, as only a bug or a hand edit could) is not leased across.
func TestClaim_RechecksTheAttemptsVariantAtTheGrant(t *testing.T) {
	f := newFixture(t)
	v1 := f.namedVariantVersion(t, "fast", 1)
	tk := f.ticket(t, "plain at first", "a/")
	agentID := f.agent(t, "in-none")
	restore := ticket.SetClaimBarrier(func() { f.routeTicket(t, tk.ID, v1) })
	defer restore()

	_, err := f.svc.Claim(f.ctx, agentID, nil)

	require.ErrorIs(t, err, ticket.ErrNoAttemptForAgent)
	got, err := f.svc.Get(f.ctx, tk.ID)
	require.NoError(t, err)
	assert.Equal(t, ticket.StatusOpen, got.Status, "nothing was granted")
}
