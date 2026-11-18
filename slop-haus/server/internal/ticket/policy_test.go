package ticket_test

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/agent"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/settings"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// The fleet-wide processing switch, exercised through the real settings
// table rather than a stub.
//
// A stub would be the obvious choice and is the wrong one here: the gate
// lives inside the claim transaction precisely so that it reads the same
// committed state the grant does, and a fake Policy injected at the
// service layer would pass whether or not that were true.

type policyFixture struct {
	svc      *ticket.Service
	settings *settings.Service
	agents   *agent.Service
	agentID  uuid.UUID
	user     uuid.UUID
	database *db.DB
	ctx      context.Context
}

func newPolicyFixture(t *testing.T) *policyFixture {
	t.Helper()
	database := dbtest.WithDB(t)
	svc := ticket.NewService(newStore(database), slog.DiscardHandler)
	agents := agent.NewService(agent.NewPostgresStore(database), svc, nil, slog.DiscardHandler)

	// A real global_tenants row: agent.Service.Create writes a credential
	// referencing it by foreign key.
	tenantID := dbtest.NewTenant(t, database)
	ctx := db.WithTenant(context.Background(), tenantID)
	created, err := agents.Create(ctx, "builder-1")
	require.NoError(t, err)

	return &policyFixture{
		svc:      svc,
		settings: settings.NewService(settings.NewPostgresStore(database), audit.New(slog.DiscardHandler, nil), slog.DiscardHandler),
		agents:   agents,
		agentID:  created.ID,
		user:     dbtest.NewUser(t, database),
		database: database,
		ctx:      ctx,
	}
}

func (f *policyFixture) agent(t *testing.T, name string) uuid.UUID {
	t.Helper()
	created, err := f.agents.Create(f.ctx, name)
	require.NoError(t, err)
	return created.ID
}

func (f *policyFixture) setProcessing(t *testing.T, enabled bool) {
	t.Helper()
	_, err := f.settings.Set(f.ctx, settings.TicketProcessingEnabled, settings.BoolValue(enabled), f.user)
	require.NoError(t, err)
}

// pauseDirectly commits the pause with a bare INSERT, skipping the claim
// lock that settings.Upsert takes.
//
// That lock is the subject of its own test below. Here it would be in the
// way: these tests park a claim inside that lock on purpose, so the real
// write path would simply block behind it and the scenario could not be
// built at all. Going around it isolates the property under test -- what
// the *claim* path does with a pause that has already committed.
func (f *policyFixture) pauseDirectly(t *testing.T) {
	t.Helper()
	_, err := f.database.Exec(f.ctx, `
		INSERT INTO tenant_settings (setting_name, setting_type, bool_value) VALUES ($1, 'bool', false)`,
		settings.TicketProcessingEnabled)
	require.NoError(t, err)
}

func (f *policyFixture) ticket(t *testing.T, title string) *ticket.Ticket {
	t.Helper()
	created, err := f.svc.Create(f.ctx, &ticket.CreateParams{Title: title})
	require.NoError(t, err)
	return created
}

// parkedClaim installs the claim barrier and guarantees it is released.
//
// The release is a t.Cleanup, not just a close() at the end of the test: a
// failing assertion returns early, and a claim left parked inside its
// transaction still holds the advisory lock and an open connection -- so
// the database teardown blocks and the whole package hangs instead of
// reporting the failure. A test that wedges the suite when it fails is
// worse than no test.
type parkedClaim struct {
	reached  chan struct{}
	release  chan struct{}
	released sync.Once
}

func newParkedClaim(t *testing.T) *parkedClaim {
	t.Helper()
	p := &parkedClaim{reached: make(chan struct{}), release: make(chan struct{})}

	var once sync.Once
	restore := ticket.SetClaimBarrier(func() {
		once.Do(func() {
			close(p.reached)
			<-p.release
		})
	})
	t.Cleanup(func() {
		p.Release()
		restore()
	})
	return p
}

func (p *parkedClaim) Release() {
	p.released.Do(func() { close(p.release) })
}

// Nothing is seeded, so this is also the test that the declared default
// reaches the claim path -- a fresh install drains tickets.
func TestClaim_GrantsWhenNothingHasBeenStored(t *testing.T) {
	t.Parallel()
	f := newPolicyFixture(t)
	created := f.ticket(t, "Claimable")

	result, err := f.svc.Claim(f.ctx, f.agentID, nil)
	require.NoError(t, err)
	require.NotNil(t, result.Ticket)
	assert.Equal(t, created.ID, result.Ticket.ID)
}

// The guarantee the switch is for. Gating only the Stop hook would leave
// this open: an agent that calls ticket_next on its own initiative would
// still be handed a ticket.
func TestClaim_RefusesWhileProcessingIsDisabled(t *testing.T) {
	t.Parallel()
	f := newPolicyFixture(t)
	created := f.ticket(t, "Should stay in the queue")
	f.setProcessing(t, false)

	_, err := f.svc.Claim(f.ctx, f.agentID, nil)
	assert.ErrorIs(t, err, ticket.ErrProcessingDisabled)

	// A refusal must not half-claim.
	found, err := f.svc.Get(f.ctx, created.ID)
	require.NoError(t, err)
	assert.Equal(t, ticket.StatusOpen, found.Status)
	_, err = f.svc.LeaseFor(f.ctx, f.agentID)
	assert.ErrorIs(t, err, ticket.ErrNoLease, "no lease may have been granted")
}

// A directed claim is refused too: the switch means the fleet starts
// nothing, and a hand-picked ticket is still starting something.
func TestClaim_RefusesADirectedTicketWhileProcessingIsDisabled(t *testing.T) {
	t.Parallel()
	f := newPolicyFixture(t)
	created := f.ticket(t, "Hand-picked")
	f.setProcessing(t, false)

	_, err := f.svc.Claim(f.ctx, f.agentID, &created.ID)
	assert.ErrorIs(t, err, ticket.ErrProcessingDisabled)
}

func TestClaim_GrantsAgainOnceProcessingIsReEnabled(t *testing.T) {
	t.Parallel()
	f := newPolicyFixture(t)
	created := f.ticket(t, "Waiting for the switch")

	f.setProcessing(t, false)
	_, err := f.svc.Claim(f.ctx, f.agentID, nil)
	require.ErrorIs(t, err, ticket.ErrProcessingDisabled)

	f.setProcessing(t, true)
	result, err := f.svc.Claim(f.ctx, f.agentID, nil)
	require.NoError(t, err)
	require.NotNil(t, result.Ticket)
	assert.Equal(t, created.ID, result.Ticket.ID)
}

// The switch is read inside the claim transaction, under the same advisory
// lock every claim takes. Checked before the transaction instead, a pause
// landing in the gap still hands out one more ticket -- at exactly the
// moment somebody was trying to stop that.
//
// The write is committed before Claim is called, so this fails for a
// service-layer check only if that check is skipped entirely; what it
// really pins is that no *cached* or startup-read answer is being used.
func TestClaim_ReadsTheSwitchOnEveryClaimRatherThanCachingIt(t *testing.T) {
	t.Parallel()
	f := newPolicyFixture(t)
	f.ticket(t, "First")
	f.ticket(t, "Second")

	first, err := f.svc.Claim(f.ctx, f.agentID, nil)
	require.NoError(t, err)
	require.NotNil(t, first.Ticket)
	_, err = f.svc.Done(f.ctx, f.agentID, &ticket.DoneParams{Summary: "done"})
	require.NoError(t, err)

	f.setProcessing(t, false)

	_, err = f.svc.Claim(f.ctx, f.agentID, nil)
	assert.ErrorIs(t, err, ticket.ErrProcessingDisabled,
		"a switch read once at startup would still be granting work here")
}

// A row whose stored kind disagrees with the declaration falls back to the
// default rather than reading as a silent zero -- which for this switch
// would pause the whole fleet for no stated reason.
func TestClaim_IgnoresASettingsRowOfTheWrongKind(t *testing.T) {
	t.Parallel()
	f := newPolicyFixture(t)
	created := f.ticket(t, "Claimable")

	_, err := f.database.Exec(f.ctx,
		`INSERT INTO tenant_settings (setting_name, setting_type, str_value) VALUES ($1, 'str', 'false')`,
		settings.TicketProcessingEnabled)
	require.NoError(t, err)

	result, err := f.svc.Claim(f.ctx, f.agentID, nil)
	require.NoError(t, err)
	require.NotNil(t, result.Ticket)
	assert.Equal(t, created.ID, result.Ticket.ID)
}

// Peek is read-only and deliberately ungated: the Stop hook needs the true
// state of the queue to log a scope deadlock even while processing is off.
// If this ever starts returning nothing when disabled, a deadlocked fleet
// becomes indistinguishable from a paused one in the logs.
func TestPeek_IsNotGatedByTheSwitch(t *testing.T) {
	t.Parallel()
	f := newPolicyFixture(t)
	created := f.ticket(t, "Still visible")
	f.setProcessing(t, false)

	result, err := f.svc.Peek(f.ctx, f.agentID, nil)
	require.NoError(t, err)
	require.NotNil(t, result.Ticket, "peek must still report what is waiting")
	assert.Equal(t, created.ID, result.Ticket.ID)

	_, err = f.svc.LeaseFor(f.ctx, f.agentID)
	assert.ErrorIs(t, err, ticket.ErrNoLease, "peeking must not have granted anything")
}

// The race the switch would otherwise lose, and the reason the check sits
// inside the claim transaction rather than in front of it.
//
// One claim parks inside its transaction still holding the advisory lock.
// A second claim blocks waiting for that lock -- so it is already past
// anything a service layer could have checked. The switch is then turned
// off and committed before the first claim is released. Checked before the
// transaction, the second claim proceeds on an answer that is now stale
// and hands out work the human just paused; checked inside it, the second
// claim re-reads under the lock and refuses.
func TestClaim_ReadsTheSwitchUnderTheClaimLockNotBeforeIt(t *testing.T) {
	f := newPolicyFixture(t)
	f.ticket(t, "First")
	f.ticket(t, "Second")
	other := f.agent(t, "builder-2")

	parked := newParkedClaim(t)

	firstDone := make(chan error, 1)
	go func() {
		_, err := f.svc.Claim(f.ctx, f.agentID, nil)
		firstDone <- err
	}()
	<-parked.reached

	secondDone := make(chan error, 1)
	go func() {
		_, err := f.svc.Claim(f.ctx, other, nil)
		secondDone <- err
	}()

	// Wait until the second claim is genuinely blocked on the advisory
	// lock. Without this the test could disable the switch before that
	// claim ever looked at it, which would pass against either design and
	// so prove nothing about this one.
	require.Eventually(t, func() bool {
		var waiting int
		err := f.database.QueryRow(f.ctx,
			`SELECT count(*) FROM pg_locks WHERE locktype = 'advisory' AND NOT granted`).Scan(&waiting)
		return err == nil && waiting > 0
	}, 10*time.Second, 10*time.Millisecond, "the second claim never queued on the claim lock")

	f.pauseDirectly(t)
	parked.Release()

	require.NoError(t, <-firstDone, "the claim already inside its transaction completes")
	assert.ErrorIs(t, <-secondDone, ticket.ErrProcessingDisabled,
		"a claim that had not yet taken the lock must see the pause")
}

// The other half of the same race, and the reason settings writes take the
// claim lock too.
//
// Here the claim has already read the switch and is mid-transaction. A
// pause that could commit in that window would land *between* the read and
// the grant, so the claim would hand out a ticket after the fleet was
// paused -- the exact thing the switch exists to prevent, and invisible
// from either side afterwards. Taking the lock makes the pause wait, so
// every claim either sees it or finishes entirely before it.
func TestSetProcessing_CannotCommitWhileAClaimIsMidTransaction(t *testing.T) {
	f := newPolicyFixture(t)
	f.ticket(t, "In flight")

	parked := newParkedClaim(t)

	claimDone := make(chan error, 1)
	go func() {
		_, err := f.svc.Claim(f.ctx, f.agentID, nil)
		claimDone <- err
	}()
	<-parked.reached

	pauseDone := make(chan error, 1)
	go func() {
		_, err := f.settings.Set(f.ctx, settings.TicketProcessingEnabled, settings.BoolValue(false), f.user)
		pauseDone <- err
	}()

	// The pause must be queued on the claim lock, not committed.
	require.Eventually(t, func() bool {
		var waiting int
		err := f.database.QueryRow(f.ctx,
			`SELECT count(*) FROM pg_locks WHERE locktype = 'advisory' AND NOT granted`).Scan(&waiting)
		return err == nil && waiting > 0
	}, 10*time.Second, 10*time.Millisecond, "the pause never queued on the claim lock")

	select {
	case err := <-pauseDone:
		t.Fatalf("the pause committed while a claim was mid-transaction (err=%v)", err)
	case <-time.After(150 * time.Millisecond):
	}

	// Meanwhile the switch is still on, because that write has not
	// committed -- so a reader cannot observe a pause that a concurrent
	// claim is about to ignore.
	enabled, err := f.settings.TicketProcessingEnabled(f.ctx)
	require.NoError(t, err)
	assert.True(t, enabled, "an uncommitted pause must not be visible")

	parked.Release()
	require.NoError(t, <-claimDone)
	require.NoError(t, <-pauseDone)

	// And once it has committed, the next claim is refused.
	f.ticket(t, "After the pause")
	_, err = f.svc.Done(f.ctx, f.agentID, &ticket.DoneParams{Summary: "done"})
	require.NoError(t, err)
	_, err = f.svc.Claim(f.ctx, f.agentID, nil)
	assert.ErrorIs(t, err, ticket.ErrProcessingDisabled)
}
