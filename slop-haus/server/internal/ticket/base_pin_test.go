package ticket_test

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost/codehosttest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/scope"
)

const headOne, headTwo = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

var acmeApp = codehost.Repo{Owner: "acme", Name: "app"}

// placedIn is a workspace that puts every scope in the given repos.
type placedIn struct{ repos []scope.WorkspaceRepo }

func (p *placedIn) Place(context.Context, []string) (*ticket.Placement, error) {
	return &ticket.Placement{Declared: true, All: p.repos, Repos: p.repos}, nil
}

type pinRig struct {
	*fixture
	host *codehosttest.Fake
	ws   *placedIn
}

// newPinRig is a fixture whose service can read a head: the fake host
// answers headOne for acme/app, and every ticket's scopes fall in it.
func newPinRig(t *testing.T) *pinRig {
	t.Helper()
	ws := &placedIn{repos: []scope.WorkspaceRepo{{Path: "/", Remote: "github.com/acme/app"}}}
	f := newFixture(t)
	host := codehosttest.NewLinkHost(prHost)
	host.HeadOf = map[codehost.Repo]*codehost.BranchHead{acmeApp: {Branch: "main", SHA: headOne}}
	svc := ticket.NewService(newStore(f.database), slog.DiscardHandler,
		ticket.WithCodeHosts(codehosttest.Registry(host)), ticket.WithWorkspace(ws))
	f.svc = svc
	return &pinRig{fixture: f, host: host, ws: ws}
}

func (r *pinRig) base(t *testing.T, id int64) *string {
	t.Helper()
	var sha *string
	require.NoError(t, r.database.QueryRow(r.ctx, `SELECT base_commit FROM tickets WHERE id = $1`, id).Scan(&sha))
	return sha
}

func TestClaim_PinsAtTheFirstClaimAndKeepsItAcrossHandoffsAndReclaims(t *testing.T) {
	t.Parallel()
	r := newPinRig(t)
	tk := r.ticket(t, "Pinned", "a/")
	first, second := r.agent(t, "first"), r.agent(t, "second")

	granted := r.claim(t, first)

	require.Equal(t, tk.ID, granted.ID)
	require.NotNil(t, granted.BaseCommit, "the claim hands out the pin")
	assert.Equal(t, headOne, *granted.BaseCommit)
	assert.Equal(t, headOne, *r.base(t, tk.ID))

	// main moves on; a handoff and a re-claim by someone else keep the pin.
	r.host.HeadOf[acmeApp] = &codehost.BranchHead{Branch: "main", SHA: headTwo}
	r.release(t, first)
	reclaimed := r.claim(t, second)
	require.Equal(t, tk.ID, reclaimed.ID)
	require.NotNil(t, reclaimed.BaseCommit)
	assert.Equal(t, headOne, *reclaimed.BaseCommit)
	assert.Equal(t, headOne, *r.base(t, tk.ID), "a later lease never moves it")
	assert.Len(t, r.host.HeadReads(), 1, "a pinned ticket costs no host read")

	// A claim that read the ticket before the pin landed still ends with the
	// stored value, not the head it read.
	stale := *reclaimed
	stale.BaseCommit = nil
	late := r.svc.PinBase(r.ctx, &stale)
	assert.Equal(t, headOne, *late.BaseCommit)
	assert.Equal(t, headOne, *r.base(t, tk.ID))
}

func TestClaim_AHostThatIsDownLeavesTheClaimUnpinnedAndTheNextClaimPinsIt(t *testing.T) {
	t.Parallel()
	r := newPinRig(t)
	tk := r.ticket(t, "Host down", "a/")
	agent := r.agent(t, "worker")
	r.host.HeadErr = codehost.ErrUnreachable

	granted := r.claim(t, agent)

	require.Equal(t, tk.ID, granted.ID, "the claim stands")
	assert.Nil(t, granted.BaseCommit)
	assert.Nil(t, r.base(t, tk.ID))

	r.host.HeadErr = nil
	r.release(t, agent)
	again := r.claim(t, agent)

	require.NotNil(t, again.BaseCommit)
	assert.Equal(t, headOne, *r.base(t, tk.ID))
}

func TestClaim_ReadsNothingWhenTheTicketHasNoOneRepoOnAKnownHost(t *testing.T) {
	t.Parallel()
	tests := map[string][]scope.WorkspaceRepo{
		"two repos":    {{Path: "a", Remote: "github.com/acme/app"}, {Path: "b", Remote: "github.com/acme/other"}},
		"unknown host": {{Path: "/", Remote: "gitlab.example/acme/app"}},
	}
	for name, repos := range tests {
		t.Run(name, func(t *testing.T) {
			r := newPinRig(t)
			tk := r.ticket(t, "Unplaceable", "a/")
			r.ws.repos = repos

			got := r.claim(t, r.agent(t, "worker"))

			assert.Nil(t, got.BaseCommit)
			assert.Nil(t, r.base(t, tk.ID))
			assert.Empty(t, r.host.HeadReads())
		})
	}

	t.Run("no workspace", func(t *testing.T) {
		f := newFixture(t)
		tk := f.ticket(t, "No manifest", "a/")
		got := f.claim(t, f.agent(t, "worker"))
		assert.Nil(t, got.BaseCommit)
		var sha *string
		require.NoError(t, f.database.QueryRow(f.ctx, `SELECT base_commit FROM tickets WHERE id = $1`, tk.ID).Scan(&sha))
		assert.Nil(t, sha)
	})
}

// A pin that lands while another is waiting on the ticket's row: the waiter
// is told the winner's value, which it can only read after the winner has
// committed.
func TestPinBaseCommit_APinLandingWhileAnotherWaitsEndsWithTheFirstWritersValue(t *testing.T) {
	r := newPinRig(t)
	tk := r.ticket(t, "Raced", "a/")
	store := newStore(r.database)

	written, commit := make(chan struct{}), make(chan struct{})
	winner := make(chan error, 1)
	go func() {
		winner <- r.database.InTx(r.ctx, func(q db.Querier) error {
			if _, err := q.Exec(r.ctx, `UPDATE tickets SET base_commit = $2 WHERE id = $1`, tk.ID, headOne); err != nil {
				return err
			}
			close(written)
			<-commit
			return nil
		})
	}()
	<-written

	loser := make(chan string, 1)
	go func() {
		stored, err := store.PinBaseCommit(r.ctx, tk.ID, headTwo)
		assert.NoError(t, err)
		loser <- stored
	}()
	require.Eventually(t, func() bool {
		var waiting int
		require.NoError(t, r.database.QueryRow(r.ctx,
			`SELECT count(*) FROM pg_locks WHERE locktype = 'transactionid' AND NOT granted`).Scan(&waiting))
		return waiting > 0
	}, 30*time.Second, 10*time.Millisecond, "the second pin waits on the first's row")
	close(commit)
	require.NoError(t, <-winner)

	assert.Equal(t, headOne, <-loser)
	assert.Equal(t, headOne, *r.base(t, tk.ID))
}

func TestPinBaseCommit_AnUnknownTicketIsNotFound(t *testing.T) {
	t.Parallel()
	r := newPinRig(t)
	_, err := newStore(r.database).PinBaseCommit(r.ctx, 999999, headOne)
	assert.True(t, errors.Is(err, ticket.ErrNotFound), "%v", err)
}

// The head is read with no claim lock held: a claim by another agent
// completes while the read waits at a barrier in the host.
func TestClaim_TheClaimLockIsNotHeldDuringTheRead(t *testing.T) {
	t.Parallel()
	r := newPinRig(t)
	r.ticket(t, "Pinning", "a/")
	otherTk := r.ticket(t, "Other", "b/")
	pinner, other := r.agent(t, "pinner"), r.agent(t, "other")

	inside, proceed := make(chan struct{}), make(chan struct{})
	var once, unblock sync.Once
	release := func() { unblock.Do(func() { close(proceed) }) }
	defer release()
	// Only the first read is held; the other agent's own claim reads too.
	r.host.BeforeHead = func() {
		held := false
		once.Do(func() { held = true; close(inside) })
		if held {
			<-proceed
		}
	}

	pinned := make(chan *ticket.Ticket, 1)
	go func() { pinned <- r.claim(t, pinner) }()
	<-inside

	claimed := make(chan *ticket.Ticket, 1)
	go func() { claimed <- r.claim(t, other) }()
	select {
	case got := <-claimed:
		require.NotNil(t, got, "the other agent got the other ticket")
		assert.Equal(t, otherTk.ID, got.ID)
	case <-time.After(30 * time.Second):
		t.Fatal("another agent's claim waited on the code host read")
	}
	release()
	got := <-pinned
	require.NotNil(t, got.BaseCommit)
	assert.Equal(t, headOne, *got.BaseCommit)
}

func TestClaim_AnAnswerThatIsNotACommitIdIsNeverPinned(t *testing.T) {
	t.Parallel()
	r := newPinRig(t)
	tk := r.ticket(t, "Odd host", "a/")
	r.host.HeadOf[acmeApp] = &codehost.BranchHead{Branch: "main", SHA: "main; rm -rf /"}

	got := r.claim(t, r.agent(t, "worker"))

	assert.Nil(t, got.BaseCommit)
	assert.Nil(t, r.base(t, tk.ID))
}
