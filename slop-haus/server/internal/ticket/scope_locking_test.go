package ticket_test

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/settings"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

// Each scan call site follows the tenant's ticket_scope_locking setting: on
// (the default) the existing tests pin the refusal; these pin that off lets
// the same operation through and reports the clash instead. Writing the row
// directly keeps the fixture free of the settings service.

func (f *fixture) lockingOff(t *testing.T) {
	t.Helper()
	_, err := f.database.Exec(f.ctx, `
		INSERT INTO tenant_settings (setting_name, setting_type, bool_value) VALUES ($1, 'bool', false)`,
		settings.TicketScopeLocking)
	require.NoError(t, err)
}

type everyoneOwns struct{}

func (everyoneOwns) IsTenantOwner(context.Context, uuid.UUID, uuid.UUID) (bool, error) {
	return true, nil
}

// setLocking is an owner's toggle through the real settings service, which is
// what takes the claim lock.
func (f *policyFixture) setLocking(on bool) error {
	svc := settings.NewService(settings.NewPostgresStore(f.database), audit.New(slog.DiscardHandler, nil), slog.DiscardHandler,
		settings.WithOwners(everyoneOwns{}))
	_, err := svc.Set(f.ctx, settings.TicketScopeLocking, settings.BoolValue(on), f.user)
	return err
}

func TestScopeLockingOff_ClaimTakesAnOverlappingTicketAndReportsIt(t *testing.T) {
	t.Parallel()
	for _, inReview := range []bool{false, true} {
		name := "held by a lease"
		if inReview {
			name = "held in review"
		}
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			holder, _ := f.holding(t, "holder", inReview, "a/")
			rival := f.ticket(t, "Rival", "a/x.go")
			a := f.agent(t, "rival-agent")

			blocked, err := f.svc.Claim(f.ctx, a, &rival.ID)
			require.NoError(t, err)
			require.NotNil(t, blocked.Blocked, "locking is on by default")
			require.Nil(t, blocked.Ticket)

			f.lockingOff(t)
			got, err := f.svc.Claim(f.ctx, a, &rival.ID)
			require.NoError(t, err)
			require.NotNil(t, got.Ticket)
			assert.Nil(t, got.Blocked)
			assert.Equal(t, []ticket.LockingOffOverlap{{TicketID: holder.ID, Path: "a/x.go"}}, got.LockingOffOverlaps)
		})
	}
}

func TestScopeLockingOff_PeekDoesNotReportBlocked(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.holding(t, "holder", true, "a/")
	rival := f.ticket(t, "Rival", "a/x.go")
	a := f.agent(t, "rival-agent")
	require.NotNil(t, f.peek(t, a).Blocked)

	f.lockingOff(t)
	got := f.peek(t, a)
	assert.Nil(t, got.Blocked)
	require.NotNil(t, got.Ticket)
	assert.Equal(t, rival.ID, got.Ticket.ID)
}

func TestScopeLockingOff_ExtendScopeIsGrantedOverAHeldScope(t *testing.T) {
	t.Parallel()
	for _, inReview := range []bool{false, true} {
		f := newFixture(t)
		holder, _ := f.holding(t, "holder", inReview, "db/")
		mine := f.ticket(t, "Mine", "api/")
		a := f.agent(t, "mine-agent")
		require.NotNil(t, f.claim(t, a))

		denied, err := f.svc.ExtendScope(f.ctx, a, []model.Path{{Path: "db/x.go"}})
		require.NoError(t, err)
		require.NotNil(t, denied.Denied)

		f.lockingOff(t)
		got, err := f.svc.ExtendScope(f.ctx, a, []model.Path{{Path: "db/x.go"}})
		require.NoError(t, err)
		assert.True(t, got.Granted)
		assert.Nil(t, got.Denied)
		assert.Equal(t, []ticket.LockingOffOverlap{{TicketID: holder.ID, Path: "db/x.go"}}, got.LockingOffOverlaps)
		assert.Contains(t, pathNames(f.get(t, mine.ID)), "db/x.go")
	}
}

func TestScopeLockingOff_ServerClaimIsGranted(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	gated := f.approvedTicket(t, f.agent(t, "author"), "server/")
	f.ticket(t, "Rival", "server/")
	require.NotNil(t, f.claim(t, f.agent(t, "rival")))
	got, err := f.serverClaim(f.serverAgent(t), gated.ID)
	require.NoError(t, err)
	require.NotNil(t, got.Blocked)

	f.lockingOff(t)
	got, err = f.serverClaim(f.serverAgent(t), gated.ID)
	require.NoError(t, err)
	assert.Nil(t, got.Blocked)
	require.NotNil(t, got.Ticket)
	assert.NotEmpty(t, got.LockingOffOverlaps)
}

func TestScopeLockingOff_PathsEditOverAHeldScopeIsAllowed(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.holding(t, "other", false, "a/")
	edited, _ := f.holding(t, "edited", false, "b/")
	_, err := f.editPaths(t, edited.ID, "b/", "a/x.go")
	require.ErrorIs(t, err, ticket.ErrScopeConflict)

	f.lockingOff(t)
	got, err := f.editPaths(t, edited.ID, "b/", "a/x.go")
	require.NoError(t, err)
	assert.Equal(t, []string{"a/x.go", "b/"}, pathNames(got))
}

func TestScopeLockingOff_MoveIntoReviewOverAHeldScopeIsAllowed(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	moved := f.ticket(t, "Open", "a/x.go")
	f.holding(t, "other", true, "a/")
	_, err := f.setStatus(t, moved.ID, ticket.StatusReview)
	require.ErrorIs(t, err, ticket.ErrScopeConflict)

	f.lockingOff(t)
	_, err = f.setStatus(t, moved.ID, ticket.StatusReview)
	require.NoError(t, err)
	assert.Equal(t, ticket.StatusReview, f.status(t, moved.ID))
}

func TestScopeLockingOff_PromotionOverAHeldScopeIsAllowed(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	id, a1 := f.promoteTwo(t)
	a3 := f.agent(t, "rival")
	a2 := f.agent(t, "second")
	require.NotNil(t, f.claim(t, a2))
	f.release(t, a1)
	rival := f.ticket(t, "Rival", "a/")
	_, err := f.svc.Claim(f.ctx, a3, &rival.ID)
	require.NoError(t, err)
	_, err = f.svc.PromoteAttempt(f.ctx, f.human, id, 2)
	require.ErrorIs(t, err, ticket.ErrScopeConflict)

	f.lockingOff(t)
	_, err = f.svc.PromoteAttempt(f.ctx, f.human, id, 2)
	require.NoError(t, err)
	assert.Equal(t, 2, f.promotedOf(t, id))
}

func TestScopeLockingOff_ScopeOverlapsAreMarkedAsBlockingNothing(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	live := f.ticket(t, "Live", "a/")
	mine := f.ticket(t, "Mine", "a/x.go")

	got, err := f.svc.ScopeOverlaps(f.ctx, mine.ID)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.False(t, got[0].LockingOff)

	f.lockingOff(t)
	got, err = f.svc.ScopeOverlaps(f.ctx, mine.ID)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, live.ID, got[0].TicketID)
	assert.True(t, got[0].LockingOff)
}

func TestScopeLockingOff_QueueBlockageClears(t *testing.T) {
	t.Parallel()
	f := newPolicyFixture(t)
	stuckBehind(t, f)
	got, err := f.svc.QueueBlockage(f.ctx)
	require.NoError(t, err)
	require.NotNil(t, got)

	require.NoError(t, f.setLocking(false))
	got, err = f.svc.QueueBlockage(f.ctx)
	require.NoError(t, err)
	assert.Nil(t, got)
	locking, err := f.svc.ScopeLocking(f.ctx)
	require.NoError(t, err)
	assert.False(t, locking)
}

// The flag is read inside the claim transaction and settings writes take the
// claim lock, so a toggle waits for a claim already deciding and cannot flip
// the rule between its scan and its grant. The claim is parked inside its
// transaction; the toggle must be seen queued on the advisory lock, and not
// committed, before the claim is let go.
func TestScopeLockingOff_ATogglePendingBehindAClaimDoesNotChangeItsDecision(t *testing.T) {
	f := newPolicyFixture(t)
	_, _, waiting := stuckBehind(t, f)
	parked := newParkedClaim(t)

	claimDone := make(chan *ticket.ClaimResult, 1)
	go func() {
		res, _ := f.svc.Claim(f.ctx, f.agentID, &waiting.ID)
		claimDone <- res
	}()
	<-parked.reached

	toggled := make(chan error, 1)
	go func() { toggled <- f.setLocking(false) }()

	require.Eventually(t, func() bool {
		var queued int
		err := f.database.QueryRow(f.ctx,
			`SELECT count(*) FROM pg_locks WHERE locktype = 'advisory' AND NOT granted`).Scan(&queued)
		return err == nil && queued > 0
	}, 10*time.Second, 10*time.Millisecond, "the toggle never queued on the claim lock")
	select {
	case err := <-toggled:
		t.Fatalf("the toggle committed while a claim was mid-transaction (err=%v)", err)
	default:
	}
	locking, err := f.svc.ScopeLocking(f.ctx)
	require.NoError(t, err)
	assert.True(t, locking, "an uncommitted toggle is not visible")

	parked.Release()
	res := <-claimDone
	require.NotNil(t, res)
	assert.NotNil(t, res.Blocked, "the claim that began under locking on is decided under it")
	require.NoError(t, <-toggled)

	res, err = f.svc.Claim(f.ctx, f.agentID, &waiting.ID)
	require.NoError(t, err)
	assert.NotNil(t, res.Ticket, "the next claim sees locking off")
}
