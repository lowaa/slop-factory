package landing_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/change"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/landing"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

type fakeLander struct{ landed []*ticket.LandParams }

func (f *fakeLander) Land(_ context.Context, _ uuid.UUID, p *ticket.LandParams) (*ticket.Ticket, error) {
	f.landed = append(f.landed, p)
	return &ticket.Ticket{ID: 5}, nil
}

func (f *fakeLander) ChangeGateFor(context.Context, uuid.UUID) (*ticket.ChangeGate, error) {
	return &ticket.ChangeGate{Attempt: ticket.AttemptRef{TicketID: 5, Number: 2}}, nil
}

type fakeFinishHost struct{ err error }

func (f fakeFinishHost) State(context.Context, uuid.UUID, int64, int) (*landing.ChangeState, error) {
	return &landing.ChangeState{URL: "https://example.test/pr/1", HeadSHA: "abc"}, f.err
}

func (fakeFinishHost) UpdateBranch(context.Context, uuid.UUID, int64, int, string) (*codehost.BranchUpdate, error) {
	panic("a finish never updates the branch")
}

func (fakeFinishHost) MarkMerged(context.Context, int64, int, uuid.UUID) error {
	panic("a finish never marks a merge")
}

type kicks []uuid.UUID

func (k *kicks) Kick(t uuid.UUID) { *k = append(*k, t) }

func finisher(on bool, hostErr error) (*landing.Finisher, *fakeLander, *kicks) {
	l, k := &fakeLander{}, &kicks{}
	f := landing.NewFinisher(l, fakeFinishHost{err: hostErr}, landing.ModeFunc(func(context.Context) (bool, error) { return on, nil }))
	f.SetKicker(k)
	return f, l, k
}

func TestFinisher_EnqueuesAndKicksTheRunner(t *testing.T) {
	f, l, k := finisher(true, nil)
	tenant := uuid.New()
	left, landed, err := f.Land(context.Background(), tenant, uuid.New(), "handoff", 0)
	require.NoError(t, err)
	assert.True(t, landed)
	assert.Equal(t, int64(5), left.ID)
	require.Len(t, l.landed, 1)
	assert.Equal(t, "handoff", l.landed[0].Handoff)
	assert.Equal(t, kicks{tenant}, *k)
}

func TestFinisher_LeavesATenantOffTheQueueAlone(t *testing.T) {
	f, l, k := finisher(false, nil)
	_, landed, err := f.Land(context.Background(), uuid.New(), uuid.New(), "h", 0)
	require.NoError(t, err)
	assert.False(t, landed)
	assert.Empty(t, l.landed)
	assert.Empty(t, *k)
}

func TestFinisher_AnAttemptWithoutAChangeIsLandedByItsAgent(t *testing.T) {
	f, l, _ := finisher(true, change.ErrNoChange)
	_, landed, err := f.Land(context.Background(), uuid.New(), uuid.New(), "h", 0)
	require.NoError(t, err)
	assert.False(t, landed)
	assert.Empty(t, l.landed)
}

// An ungated ticket finished through the Finisher is written to the queue in
// the hand-over's own transaction and the runner then lands it with no agent
// holding the ticket or the lock.
func TestFinisher_AFinishedUngatedTicketIsLandedByTheRunner(t *testing.T) {
	f := newFixture(t)
	f.nextPR++
	ref := f.ref(f.nextPR)
	f.host.Opened = ref
	f.host.DiffOf = map[string]*codehost.Diff{headA: f.diff(baseOld)}
	f.setChecks(ref, headA, true, green)
	author, err := f.agents.Create(f.ctx, "author")
	require.NoError(t, err)
	created, err := f.tickets.Create(f.ctx, &ticket.CreateParams{
		Title: "Add the thing", Paths: []model.Path{{Path: "app/server/"}}, Status: ticket.StatusOpen, CreatedByUserID: &f.creator,
	})
	require.NoError(t, err)
	claimed, err := f.tickets.Claim(f.ctx, author.ID, &created.ID)
	require.NoError(t, err)
	_, err = f.changes.Open(f.ctx, f.tenant, author.ID, &change.OpenInput{Branch: ticket.BranchName(created.ID, claimed.Attempt.Number), Title: "Add the thing", Body: "b"})
	require.NoError(t, err)

	fin := landing.NewFinisher(f.tickets, f.hostWrap, landing.ModeFunc(func(context.Context) (bool, error) { return true, nil }))
	r := f.runner(landing.Config{})
	fin.SetKicker(r)
	_, landed, err := fin.Land(f.ctx, f.tenant, author.ID, "ready", 0)
	require.NoError(t, err)
	require.True(t, landed)
	row := f.row(created.ID)
	require.NotNil(t, row)
	assert.Equal(t, headA, row.HeadSHA)
	assert.Equal(t, f.hostWrapURL(ref), row.ChangeURL)

	r.Wait()
	f.assertMerged(&finished{author: author.ID, ticketID: created.ID, ref: ref})
}

type fixedReds int

func (r fixedReds) CountForHead(context.Context, int64, string, landing.Outcome) (int, error) {
	return int(r), nil
}

func TestFinisher_RefusesARedHeadWithoutQueueingIt(t *testing.T) {
	f, l, k := finisher(true, nil)
	f.SetGuard(landing.NewEnqueueGuard(fixedReds(1), nil, nil))
	_, landed, err := f.Land(context.Background(), uuid.New(), uuid.New(), "h", 0)
	require.ErrorIs(t, err, landing.ErrRedRequeue)
	assert.False(t, landed)
	assert.Empty(t, l.landed)
	assert.Empty(t, *k)
}

type recordingReds struct{ heads []string }

func (r *recordingReds) CountForHead(_ context.Context, _ int64, head string, _ landing.Outcome) (int, error) {
	r.heads = append(r.heads, head)
	return 1, nil
}

type tickets map[int64]*ticket.Ticket

func (m tickets) Get(_ context.Context, id int64) (*ticket.Ticket, error) {
	if t, ok := m[id]; ok {
		return t, nil
	}
	return nil, ticket.ErrNotFound
}

func TestFinisher_PassesTheCitedFlakeTicketToTheGuard(t *testing.T) {
	f, l, _ := finisher(true, nil)
	reds := &recordingReds{}
	f.SetGuard(landing.NewEnqueueGuard(reds, tickets{9: {ID: 9, Title: "CI flake: x", Status: ticket.StatusOpen}}, nil))
	_, landed, err := f.Land(context.Background(), uuid.New(), uuid.New(), "h", 9)
	require.NoError(t, err)
	assert.True(t, landed)
	assert.Equal(t, []string{"abc"}, reds.heads)
	assert.Len(t, l.landed, 1)
}

func TestFinisher_AGuardDependencyFailureIsNotARefusal(t *testing.T) {
	f, l, _ := finisher(true, nil)
	f.SetGuard(landing.NewEnqueueGuard(failingReds{}, nil, nil))
	_, _, err := f.Land(context.Background(), uuid.New(), uuid.New(), "h", 0)
	require.Error(t, err)
	assert.NotErrorIs(t, err, landing.ErrRedRequeue)
	assert.Empty(t, l.landed)
}

type failingReds struct{}

func (failingReds) CountForHead(context.Context, int64, string, landing.Outcome) (int, error) {
	return 0, errors.New("db down")
}
