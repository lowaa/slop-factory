package ticket_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// servedStub returns what the test sets: the rules that decide what is served
// belong to leaseconfig, and are tested there. This only needs a config to
// record or refuse on.
type servedStub struct {
	served *ticket.ServedConfig
	err    error
}

func (s *servedStub) Resolve(context.Context, uuid.UUID) (*ticket.ServedConfig, error) {
	return s.served, s.err
}

func (f *fixture) fleetVersion(t *testing.T) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	require.NoError(t, f.database.QueryRow(f.ctx,
		`INSERT INTO fleet_instruction_versions (name, version, body) VALUES ('house', 1, 'text') RETURNING id`).Scan(&id))
	return id
}

func (f *fixture) variantVersion(t *testing.T, harness string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	require.NoError(t, f.database.QueryRow(f.ctx,
		`INSERT INTO variant_versions (name, version, harness) VALUES ('v', 1, $1) RETURNING id`, harness).Scan(&id))
	return id
}

func (f *fixture) describeSession(t *testing.T, agentID uuid.UUID, harness string) {
	t.Helper()
	_, err := f.database.Exec(f.ctx,
		`INSERT INTO sessions (agent_id, claude_session_id, harness) VALUES ($1, $2, $3)`,
		agentID, uuid.NewString(), harness)
	require.NoError(t, err)
}

type recorded struct {
	Settings string
	Variant  *uuid.UUID
	Fleet    *uuid.UUID
	Revision int64
}

func (f *fixture) leaseConfig(t *testing.T, agentID uuid.UUID) (recorded, bool) {
	t.Helper()
	var r recorded
	err := f.database.QueryRow(f.ctx, `
		SELECT c.settings::text, c.variant_id, c.fleet_instructions_version_id, c.revision
		  FROM lease_configs c JOIN leases l ON l.id = c.lease_id AND l.tenant_id = c.tenant_id
		 WHERE l.agent_id = $1 AND l.released_at IS NULL`, agentID).Scan(&r.Settings, &r.Variant, &r.Fleet, &r.Revision)
	if err != nil {
		return recorded{}, false
	}
	return r, true
}

func TestClaim_RecordsTheConfigurationServedWithTheLease(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	fleet := f.fleetVersion(t)
	stub := &servedStub{served: &ticket.ServedConfig{
		Revision: 3, Settings: map[string]map[string]string{"claude": {"model": "opus"}}, FleetInstructionsVersionID: &fleet,
	}}
	f.with(ticket.WithConfigResolver(stub))
	a := f.agent(t, "default-agent")
	f.ticket(t, "work", "a/")

	result, err := f.svc.Claim(f.ctx, a, nil)
	require.NoError(t, err)
	require.NotNil(t, result.Ticket)

	got, ok := f.leaseConfig(t, a)
	require.True(t, ok, "a default-fleet agent's lease records its configuration too")
	assert.JSONEq(t, `{"claude":{"model":"opus"}}`, got.Settings)
	assert.Equal(t, int64(3), got.Revision)
	assert.Nil(t, got.Variant)
	assert.Equal(t, &fleet, got.Fleet)

	stub.served = &ticket.ServedConfig{Revision: 4, Settings: map[string]map[string]string{"claude": {"model": "haiku"}}}
	again, ok := f.leaseConfig(t, a)
	require.True(t, ok)
	assert.Equal(t, got, again, "a later change to the config leaves the record as it was")

	_, err = f.database.Exec(f.ctx, `UPDATE lease_configs SET settings = '{}'`)
	assert.Error(t, err, "a record is immutable")
}

func TestClaim_AgentInAVariantIsRefusedOnlyOnAHarnessMismatch(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	version := f.variantVersion(t, "pi")
	served := &ticket.ServedConfig{
		Settings: map[string]map[string]string{"pi": {}},
		Variant:  &ticket.ServedVariant{VersionID: version, Name: "fast", Version: 1, Harness: "pi"},
	}
	f.with(ticket.WithConfigResolver(&servedStub{served: served}))
	routed := f.ticket(t, "work", "a/")
	f.routeTicket(t, routed.ID, version)

	t.Run("a session running another harness is told which, naming both", func(t *testing.T) {
		a := f.agentOnVariant(t, "mismatched", version)
		f.describeSession(t, a, "claude")
		_, err := f.svc.Claim(f.ctx, a, nil)
		var refused *ticket.VariantClaimError
		require.ErrorAs(t, err, &refused)
		assert.True(t, refused.HarnessMismatch())
		assert.Contains(t, err.Error(), "pi")
		assert.Contains(t, err.Error(), "claude")
		assert.ErrorIs(t, err, ticket.ErrVariantClaim)
		_, ok := f.leaseConfig(t, a)
		assert.False(t, ok)
	})

	t.Run("a session running the variant's harness claims what is routed to it", func(t *testing.T) {
		a := f.agentOnVariant(t, "matching", version)
		f.describeSession(t, a, "pi")
		result, err := f.svc.Claim(f.ctx, a, nil)
		require.NoError(t, err)
		require.NotNil(t, result.Ticket)
		assert.Equal(t, routed.ID, result.Ticket.ID)
		recorded, ok := f.leaseConfig(t, a)
		require.True(t, ok)
		assert.Equal(t, version, *recorded.Variant)
	})
}

func TestClaim_ConfigThatCannotBeResolvedGrantsNothing(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.with(ticket.WithConfigResolver(&servedStub{err: assert.AnError}))
	a := f.agent(t, "unresolvable")
	created := f.ticket(t, "work", "a/")

	_, err := f.svc.Claim(f.ctx, a, nil)
	require.ErrorIs(t, err, assert.AnError)

	got, err := f.svc.Get(f.ctx, created.ID)
	require.NoError(t, err)
	assert.Equal(t, ticket.StatusOpen, got.Status, "a lease is never granted without the record of what it ran under")
}

func TestClaim_ConfigThatCannotBeRecordedGrantsNothing(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.with(ticket.WithConfigResolver(&servedStub{served: &ticket.ServedConfig{Settings: make(chan int)}}))
	a := f.agent(t, "unrecordable")
	created := f.ticket(t, "work", "a/")

	_, err := f.svc.Claim(f.ctx, a, nil)
	require.Error(t, err)

	got, err := f.svc.Get(f.ctx, created.ID)
	require.NoError(t, err)
	assert.Equal(t, ticket.StatusOpen, got.Status)
	var leases int
	require.NoError(t, f.database.QueryRow(f.ctx, `SELECT count(*) FROM leases WHERE agent_id = $1`, a).Scan(&leases))
	assert.Zero(t, leases, "the lease insert rolled back with the failed record")
}

func TestClaim_DeclaredHarnessStandsInForAMissingSession(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	version := f.variantVersion(t, "pi")
	f.with(ticket.WithConfigResolver(&servedStub{served: &ticket.ServedConfig{
		Settings: map[string]map[string]string{"pi": {}},
		Variant:  &ticket.ServedVariant{VersionID: version, Name: "fast", Version: 1, Harness: "pi"},
	}}))
	routed := f.ticket(t, "work", "a/")
	f.routeTicket(t, routed.ID, version)

	t.Run("a conflicting declaration is refused as a session's is", func(t *testing.T) {
		a := f.agentOnVariant(t, "declares-claude", version)
		_, err := f.svc.ClaimWith(f.ctx, a, &ticket.ClaimOptions{Harness: "claude"})
		var refused *ticket.VariantClaimError
		require.ErrorAs(t, err, &refused)
		assert.Equal(t, "claude", refused.SessionHarness)
	})

	t.Run("a matching declaration claims", func(t *testing.T) {
		a := f.agentOnVariant(t, "declares-pi", version)
		result, err := f.svc.ClaimWith(f.ctx, a, &ticket.ClaimOptions{Harness: "pi"})
		require.NoError(t, err)
		require.NotNil(t, result.Ticket)
	})

	t.Run("a session that exists wins over the declaration", func(t *testing.T) {
		a := f.agentOnVariant(t, "session-says-claude", version)
		f.describeSession(t, a, "claude")
		_, err := f.svc.ClaimWith(f.ctx, a, &ticket.ClaimOptions{Harness: "pi"})
		assert.ErrorIs(t, err, ticket.ErrVariantClaim)
	})

	t.Run("an unknown harness is refused before anything is granted", func(t *testing.T) {
		a := f.agent(t, "declares-nonsense")
		_, err := f.svc.ClaimWith(f.ctx, a, &ticket.ClaimOptions{Harness: "gpt"})
		assert.ErrorIs(t, err, ticket.ErrInvalidHarness)
	})
}

func TestClaim_RecordsWhatTheRunRanWith(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	fleet := f.fleetVersion(t)
	f.with(ticket.WithConfigResolver(&servedStub{served: &ticket.ServedConfig{
		Settings:                   map[string]map[string]string{"claude": {"model": "opus"}, "pi": {"model": "other"}},
		FleetInstructionsVersionID: &fleet,
	}}))
	a := f.agent(t, "default-agent")
	tk := f.ticket(t, "work", "a/")

	_, err := f.svc.ClaimWith(f.ctx, a, &ticket.ClaimOptions{
		Harness: "claude", HarnessVersion: "2.1.0 (Claude Code)", BundleHash: "abc123",
	})
	require.NoError(t, err)

	attempts, err := f.svc.Attempts(f.ctx, tk.ID)
	require.NoError(t, err)
	require.Len(t, attempts, 1)
	require.Len(t, attempts[0].Runs, 1)
	run := attempts[0].Runs[0]
	assert.Equal(t, "claude", *run.Harness)
	assert.Equal(t, "2.1.0 (Claude Code)", *run.HarnessVersion)
	assert.Equal(t, "opus", *run.Model, "the model is the one served for the run's harness")
	assert.Equal(t, "abc123", *run.BundleHash)
	assert.Equal(t, "house", *run.FleetInstructionsName)
	assert.Equal(t, 1, *run.FleetInstructionsVersion)
}

func TestClaim_WithoutTheRunReportIsStillRecorded(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.with(ticket.WithConfigResolver(&servedStub{served: &ticket.ServedConfig{Settings: map[string]map[string]string{}}}))
	a := f.agent(t, "old-supervisor")
	tk := f.ticket(t, "work", "a/")

	result, err := f.svc.ClaimWith(f.ctx, a, &ticket.ClaimOptions{})
	require.NoError(t, err)
	require.NotNil(t, result.Ticket)

	attempts, err := f.svc.Attempts(f.ctx, tk.ID)
	require.NoError(t, err)
	require.Len(t, attempts[0].Runs, 1)
	run := attempts[0].Runs[0]
	assert.Nil(t, run.Harness)
	assert.Nil(t, run.HarnessVersion)
	assert.Nil(t, run.Model)
	assert.Nil(t, run.BundleHash)
	assert.Nil(t, run.FleetInstructionsName)
}

func TestClaim_DropsWhatTheRunReportsThatDoesNotFit(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.with(ticket.WithConfigResolver(&servedStub{served: &ticket.ServedConfig{Settings: map[string]map[string]string{}}}))
	a := f.agent(t, "odd-supervisor")
	tk := f.ticket(t, "work", "a/")

	_, err := f.svc.ClaimWith(f.ctx, a, &ticket.ClaimOptions{HarnessVersion: "1.0\x1b[31m", BundleHash: "not hex!"})
	require.NoError(t, err, "a report that does not fit must not refuse the claim")

	attempts, err := f.svc.Attempts(f.ctx, tk.ID)
	require.NoError(t, err)
	require.Len(t, attempts[0].Runs, 1)
	assert.Nil(t, attempts[0].Runs[0].HarnessVersion)
	assert.Nil(t, attempts[0].Runs[0].BundleHash)
}

func TestTicketDetail_ShowsEachLeasesRunOldestFirstThroughTheHTTPClaim(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	fleet := f.fleetVersion(t)
	f.with(ticket.WithConfigResolver(&servedStub{served: &ticket.ServedConfig{
		Settings:                   map[string]map[string]string{"claude": {"model": "opus"}},
		FleetInstructionsVersionID: &fleet,
	}}))
	a := f.agent(t, "builder")
	tk := f.ticket(t, "work", "a/")

	rec := f.agentPost(t, a, "/api/claims", `{"harness":"claude","harness_version":"2.1.0","bundle_hash":"aaa111"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	f.release(t, a)
	// An older supervisor: neither field.
	rec = f.agentPost(t, a, "/api/claims", `{}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	got := f.detail(t, tk.ID).Attempts
	require.Len(t, got, 1)
	require.Len(t, got[0].Runs, 2, "one run per lease")
	first, second := got[0].Runs[0], got[0].Runs[1]
	assert.Equal(t, "claude", *first.Harness)
	assert.Equal(t, "2.1.0", *first.HarnessVersion)
	assert.Equal(t, "opus", *first.Model)
	assert.Equal(t, "aaa111", *first.BundleHash)
	require.NotNil(t, first.FleetInstructions)
	assert.Equal(t, ticket.FleetInstructionsRefBody{Name: "house", Version: 1}, *first.FleetInstructions)
	assert.True(t, first.AcquiredAt.Before(second.AcquiredAt))
	assert.Nil(t, second.Harness)
	assert.Nil(t, second.HarnessVersion)
	assert.Nil(t, second.BundleHash)
}
