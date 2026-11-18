package landing_test

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/google/uuid"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost/codehosttest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/landing"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

// batchHostFake answers what a test sets and records the calls. It decides
// nothing about branches.
type batchHostFake struct {
	mu      sync.Mutex
	tip     string
	tipErr  error
	files   map[int64][]string
	fileErr map[int64]error
	// mergeErr is the error a member's merge fails with.
	mergeErr map[int64]error
	// updateErr is the error a member's update with the base fails with.
	updateErr map[int64]error
	// updated is the members brought up to date with the base, in order.
	updated []int64
	// newHead is the head a member's update makes; a member not in it was up to date.
	newHead map[int64]string
	// mergedHeads is the head each member was merged at.
	mergedHeads map[int64]string
	deleteErr   error
	merged      []int64
	created     []string
	deleted     []string
	// opened and closed are the batch changes the builder opened and closed.
	opened, closed []string
	// mergedAtOpen is how many members were merged when each change was opened.
	mergedAtOpen []int
	openErr      error
	// observe runs inside each host call that builds the branch.
	observe func()
}

func newBatchHost() *batchHostFake {
	return &batchHostFake{tip: baseOld, files: map[int64][]string{}, fileErr: map[int64]error{}, mergeErr: map[int64]error{}, updateErr: map[int64]error{}, newHead: map[int64]string{}, mergedHeads: map[int64]string{}}
}

func (h *batchHostFake) ChangedFiles(_ context.Context, row *landing.LandingRow) ([]string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.files[row.TicketID], h.fileErr[row.TicketID]
}

func (h *batchHostFake) BaseTip(context.Context, *landing.LandingRow) (string, error) {
	return h.tip, h.tipErr
}

func (h *batchHostFake) CreateBranch(_ context.Context, _ *landing.LandingRow, branch, sha string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.created = append(h.created, branch+"@"+sha)
	return nil
}

func (h *batchHostFake) UpdateMember(_ context.Context, row *landing.LandingRow, head string) (*codehost.BranchUpdate, error) {
	if h.observe != nil {
		h.observe()
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.updateErr[row.TicketID]; err != nil {
		return nil, err
	}
	h.updated = append(h.updated, row.TicketID)
	if sha, ok := h.newHead[row.TicketID]; ok {
		return &codehost.BranchUpdate{SHA: sha}, nil
	}
	return &codehost.BranchUpdate{SHA: head, UpToDate: true}, nil
}

func (h *batchHostFake) MergeHead(_ context.Context, row *landing.LandingRow, _, head string) (*codehost.BranchMerge, error) {
	if h.observe != nil {
		h.observe()
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.mergeErr[row.TicketID]; err != nil {
		return nil, err
	}
	h.merged = append(h.merged, row.TicketID)
	h.mergedHeads[row.TicketID] = head
	return &codehost.BranchMerge{SHA: fmt.Sprintf("prefix-%d", len(h.merged))}, nil
}

func (h *batchHostFake) OpenBatchChange(_ context.Context, _ *landing.LandingRow, branch string) (codehost.ChangeRef, error) {
	if h.observe != nil {
		h.observe()
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.openErr != nil {
		return codehost.ChangeRef{}, h.openErr
	}
	h.opened = append(h.opened, branch)
	h.mergedAtOpen = append(h.mergedAtOpen, len(h.merged))
	return codehost.ChangeRef{Repo: branch}, nil
}

func (h *batchHostFake) CloseBatchChange(_ context.Context, _ *landing.LandingRow, ref codehost.ChangeRef) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = append(h.closed, ref.Repo)
	return nil
}

func (h *batchHostFake) DeleteBranch(_ context.Context, _ *landing.LandingRow, branch string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.deleted = append(h.deleted, branch)
	return h.deleteErr
}

type membersFake struct {
	mu       sync.Mutex
	comments map[int64]string
	requeued []int64
}

func (m *membersFake) Comment(_ context.Context, _ uuid.UUID, row *landing.LandingRow, g string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.comments == nil {
		m.comments = map[int64]string{}
	}
	m.comments[row.TicketID] = g
	return nil
}

func (m *membersFake) Requeue(_ context.Context, _ uuid.UUID, row *landing.LandingRow) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.requeued = append(m.requeued, row.TicketID)
	return nil
}

type batchFixture struct {
	*fixture
	store   *landing.BatchStore
	host    *batchHostFake
	members *membersFake
	builder *landing.BatchBuilder
	changes []*finished
}

// newBatch queues n changes of one app, highest priority first, and builds a
// builder over the fake host.
func newBatch(t *testing.T, n int) *batchFixture {
	t.Helper()
	f := newFixture(t)
	b := &batchFixture{fixture: f, store: landing.NewBatchStore(f.database), host: newBatchHost(), members: &membersFake{}}
	for range n {
		b.changes = append(b.changes, f.finishedChange(model.PriorityNormal))
	}
	builder, err := landing.NewBatchBuilder(f.runner(landing.Config{}), &landing.BatchOptions{Store: b.store, Host: b.host, Members: b.members})
	require.NoError(t, err)
	b.builder = builder
	return b
}

func (b *batchFixture) ids() []int64 {
	out := make([]int64, len(b.changes))
	for i, c := range b.changes {
		out[i] = c.ticketID
	}
	return out
}

// rowOf is the ticket's landing row with its batch, nil when it has none.
func (b *batchFixture) rowOf(id int64) *landing.LandingRow {
	b.t.Helper()
	rows, err := b.queue.Ordered(b.ctx)
	require.NoError(b.t, err)
	for i := range rows {
		if rows[i].TicketID == id {
			return &rows[i]
		}
	}
	return nil
}

func (b *batchFixture) candidates() []landing.Candidate {
	b.t.Helper()
	cands, err := b.queue.Candidates(b.ctx)
	require.NoError(b.t, err)
	return cands
}

func (b *batchFixture) build() *landing.Built {
	b.t.Helper()
	built, err := b.builder.Build(b.ctx, b.candidates())
	require.NoError(b.t, err)
	if built != nil {
		b.t.Cleanup(built.Close)
	}
	return built
}

func TestBatchStore_ReserveMarksMembersInOrder(t *testing.T) {
	b := newBatch(t, 3)

	batch, err := reserveNow(b.ctx, b.store, baseOld, b.ids())
	require.NoError(t, err)

	assert.Equal(t, b.ids(), batch.Members)
	assert.Equal(t, fmt.Sprintf("slop-haus-batch-%d", batch.ID), batch.Branch)
	assert.Equal(t, landing.BatchBuilding, batch.State)
	for i, id := range b.ids() {
		row := b.rowOf(id)
		require.NotNil(t, row.BatchID)
		assert.Equal(t, batch.ID, *row.BatchID)
		assert.Equal(t, i+1, row.BatchPosition)
		assert.Equal(t, landing.PhaseBatchTesting, row.Phase, "a reserved row is no longer queued, so close-as-merged and the head leave it alone")
		assert.Equal(t, landing.WaitingChecks, row.Step)
	}
}

func TestBatchStore_CreateLeavesMembersQueued(t *testing.T) {
	b := newBatch(t, 2)

	batch, err := b.store.Create(b.ctx, baseOld, b.ids())

	require.NoError(t, err)
	assert.Equal(t, landing.BatchBuilding, batch.State)
	assert.Equal(t, fmt.Sprintf("slop-haus-batch-%d", batch.ID), batch.Branch)
	for _, id := range b.ids() {
		row := b.rowOf(id)
		assert.Nil(t, row.BatchID, "a build that can still fail does not show its members as batched")
		assert.Empty(t, row.Step)
	}
	assert.Len(t, b.candidates(), 2, "and they stay candidates")
}

func TestBatchStore_ReserveTakesEveryMemberOrNone(t *testing.T) {
	for name, spoil := range map[string]func(b *batchFixture){
		"a member handed back": func(b *batchFixture) {
			require.NoError(b.t, b.queue.HandBack(b.ctx, b.changes[1].ticketID, landing.ChecksRed, false))
		},
		"the first member handed back": func(b *batchFixture) {
			require.NoError(b.t, b.queue.HandBack(b.ctx, b.changes[0].ticketID, landing.ChecksRed, false))
		},
		"a member the serial runner began": func(b *batchFixture) {
			require.NoError(b.t, b.queue.SetStep(b.ctx, b.changes[1].ticketID, landing.Merging))
		},
		"a member removed": func(b *batchFixture) {
			require.NoError(b.t, b.queue.Remove(b.ctx, b.changes[2].ticketID, landing.PhaseDequeued))
		},
	} {
		t.Run(name, func(t *testing.T) {
			b := newBatch(t, 3)
			batch, err := b.store.Create(b.ctx, baseOld, b.ids())
			require.NoError(t, err)
			spoil(b)

			_, err = b.store.Reserve(b.ctx, batch.ID)

			require.ErrorIs(t, err, landing.ErrNotReservable)
			for _, id := range b.ids() {
				if row := b.rowOf(id); row != nil {
					assert.Nil(t, row.BatchID, "no member is left reserved")
				}
			}
			got, err := b.store.Get(b.ctx, batch.ID)
			require.NoError(t, err)
			assert.Equal(t, landing.BatchBuilding, got.State)
		})
	}
}

func TestBatchStore_ReserveMovesMembersToTestingInOrder(t *testing.T) {
	b := newBatch(t, 3)
	batch, err := b.store.Create(b.ctx, baseOld, b.ids())
	require.NoError(t, err)

	got, err := b.store.Reserve(b.ctx, batch.ID)

	require.NoError(t, err)
	assert.Equal(t, b.ids(), got.Members)
	for i, id := range b.ids() {
		var phase string
		require.NoError(t, b.database.QueryRow(b.ctx, `SELECT queue_state FROM landings WHERE ticket_id = $1`, id).Scan(&phase))
		assert.Equal(t, "batch_testing", phase)
		assert.Equal(t, i+1, b.rowOf(id).BatchPosition)
	}
	_, err = b.store.Reserve(b.ctx, batch.ID)
	assert.NotErrorIs(t, err, nil, "a batch is reserved once")
}

func TestBatchStore_ConcurrentReservationsNeverShareAMember(t *testing.T) {
	b := newBatch(t, 4)
	const workers = 8
	var wg sync.WaitGroup
	results := make([]*landing.Batch, workers)
	errs := make([]error, workers)
	created := make([]*landing.Batch, workers)
	for i := range workers {
		var err error
		created[i], err = b.store.Create(b.ctx, baseOld, b.ids())
		require.NoError(t, err)
	}
	start := make(chan struct{})
	for i := range workers {
		wg.Go(func() {
			<-start
			results[i], errs[i] = b.store.Reserve(b.ctx, created[i].ID)
		})
	}
	close(start)
	wg.Wait()

	won := 0
	for i := range workers {
		if errs[i] != nil {
			require.ErrorIs(t, errs[i], landing.ErrNotReservable)
			continue
		}
		won++
		assert.Equal(t, b.ids(), results[i].Members)
	}
	assert.Equal(t, 1, won, "the batches want the same members and one wins")
	for _, id := range b.ids() {
		require.NotNil(t, b.rowOf(id).BatchID)
	}
}

func TestBatchStore_StateOnlyMovesForward(t *testing.T) {
	b := newBatch(t, 1)
	batch, err := reserveNow(b.ctx, b.store, baseOld, b.ids())
	require.NoError(t, err)

	require.ErrorIs(t, b.store.SetState(b.ctx, batch.ID, landing.BatchMerging), landing.ErrBatchTransition, "testing comes first")
	require.NoError(t, b.store.SetState(b.ctx, batch.ID, landing.BatchTesting))
	require.ErrorIs(t, b.store.SetState(b.ctx, batch.ID, landing.BatchBuilding), landing.ErrBatchTransition)
	require.NoError(t, b.store.SetState(b.ctx, batch.ID, landing.BatchMerging))
	require.NoError(t, b.store.SetState(b.ctx, batch.ID, landing.BatchDone))
	require.ErrorIs(t, b.store.SetState(b.ctx, batch.ID, landing.BatchTesting), landing.ErrBatchTransition, "done is final")
	require.NoError(t, b.store.SetPipeline(b.ctx, batch.ID, "1234"))
	got, err := b.store.Get(b.ctx, batch.ID)
	require.NoError(t, err)
	assert.Equal(t, "1234", got.CIPipeline)
}

func TestBatchStore_LiveListsTheBatchesNotDone(t *testing.T) {
	b := newBatch(t, 2)
	first, err := reserveNow(b.ctx, b.store, baseOld, b.ids()[:1])
	require.NoError(t, err)
	second, err := reserveNow(b.ctx, b.store, baseOld, b.ids()[1:])
	require.NoError(t, err)
	require.NoError(t, b.store.SetState(b.ctx, first.ID, landing.BatchDone))

	live, err := b.store.Live(b.ctx)

	require.NoError(t, err)
	require.Len(t, live, 1)
	assert.Equal(t, second.ID, live[0].ID)
	assert.Equal(t, []int64{b.changes[1].ticketID}, live[0].Members)
}

func TestBatchStore_DropTakesTheMemberOutOfTheBuild(t *testing.T) {
	b := newBatch(t, 3)
	batch, err := b.store.Create(b.ctx, baseOld, b.ids())
	require.NoError(t, err)

	got, err := b.store.Drop(b.ctx, batch.ID, b.changes[1].ticketID)

	require.NoError(t, err)
	assert.Equal(t, []int64{b.changes[0].ticketID, b.changes[2].ticketID}, got.Members)
	_, err = b.store.Drop(b.ctx, batch.ID, b.changes[1].ticketID)
	assert.Error(t, err, "a member can only be dropped once")
	reserved, err := b.store.Reserve(b.ctx, batch.ID)
	require.NoError(t, err)
	assert.Equal(t, got.Members, reserved.Members)
	assert.Equal(t, 2, b.rowOf(b.changes[2].ticketID).BatchPosition, "positions count the members kept")
	assert.Nil(t, b.rowOf(b.changes[1].ticketID).BatchID)
}

func TestBatchStore_ADropAfterTheReservationIsRefused(t *testing.T) {
	b := newBatch(t, 2)
	batch, err := reserveNow(b.ctx, b.store, baseOld, b.ids())
	require.NoError(t, err)

	_, err = b.store.Drop(b.ctx, batch.ID, b.changes[1].ticketID)

	require.ErrorIs(t, err, landing.ErrBatchTransition)
	require.NotNil(t, b.rowOf(b.changes[1].ticketID).BatchID, "release is for reserved members")
}

func TestBatchStore_DroppingTheLastMemberEndsTheBatch(t *testing.T) {
	b := newBatch(t, 1)
	batch, err := b.store.Create(b.ctx, baseOld, b.ids())
	require.NoError(t, err)

	got, err := b.store.Drop(b.ctx, batch.ID, b.changes[0].ticketID)

	require.NoError(t, err)
	assert.Equal(t, landing.BatchDone, got.State)
}

func TestBatchStore_VoidLeavesMembersQueuedInTheirOrder(t *testing.T) {
	b := newBatch(t, 3)
	batch, err := reserveNow(b.ctx, b.store, baseOld, b.ids())
	require.NoError(t, err)
	before, err := b.queue.Ordered(b.ctx)
	require.NoError(t, err)

	require.NoError(t, b.store.Void(b.ctx, batch.ID, "test"))

	after, err := b.queue.Ordered(b.ctx)
	require.NoError(t, err)
	require.Len(t, after, 3)
	for i := range after {
		assert.Equal(t, before[i].TicketID, after[i].TicketID, "order is the one before the batch")
		assert.Nil(t, after[i].BatchID)
		assert.Empty(t, after[i].Step)
		assert.Equal(t, landing.Landing, after[i].State)
	}
	got, err := b.store.Get(b.ctx, batch.ID)
	require.NoError(t, err)
	assert.Equal(t, landing.BatchDone, got.State)
}

func TestReservedMember_CannotBeClaimedByAWorkerOrClosedAsMerged(t *testing.T) {
	b := newBatch(t, 2)
	batch, err := reserveNow(b.ctx, b.store, baseOld, b.ids())
	require.NoError(t, err)
	member := b.changes[1]

	claimed, err := b.tickets.Claim(b.ctx, member.author, &member.ticketID)
	require.NoError(t, err)
	assert.Nil(t, claimed.Ticket, "a worker is not given a row in the queue")
	b.fixture.host.Opened = member.ref
	_, err = b.tickets.CloseMergedPR(b.ctx, member.ref, b.creator)
	require.NoError(t, err)
	require.NotNil(t, b.rowOf(member.ticketID), "a person's merge leaves the reserved row, which is the runner's")
	assert.Equal(t, batch.ID, *b.rowOf(member.ticketID).BatchID)

	// Negative: once its reservation is cleared the row is waiting again, and a
	// person's merge takes it out of the queue.
	require.NoError(t, b.store.Void(b.ctx, batch.ID, "test"))
	other := b.changes[0]
	b.fixture.host.Opened = other.ref
	_, err = b.tickets.CloseMergedPR(b.ctx, other.ref, b.creator)
	require.NoError(t, err)
	assert.Nil(t, b.rowOf(other.ticketID))
}

func TestBatchBuilder_BuildsTheBranchInOrder(t *testing.T) {
	b := newBatch(t, 3)

	built := b.build()

	require.NotNil(t, built)
	assert.Equal(t, []string{fmt.Sprintf("%s@%s", built.Batch.Branch, baseOld)}, b.host.created)
	assert.Equal(t, b.ids(), b.host.merged)
	require.Len(t, built.Members, 3)
	for i, m := range built.Members {
		assert.Equal(t, b.changes[i].ticketID, m.Row.TicketID)
		assert.Equal(t, fmt.Sprintf("prefix-%d", i+1), m.PrefixSHA)
	}
	assert.Equal(t, baseOld, built.Batch.BaseSHA)
	assert.Equal(t, b.ids(), built.Batch.Members)
	assert.NoError(t, built.Held.Err())
}

func TestBatchBuilder_OpensTheChangeThatStartsCIOnTheFinishedBranchAndClosesItWhenTheBatchEnds(t *testing.T) {
	b := newBatch(t, 2)
	built := b.build()
	require.NotNil(t, built)
	assert.Equal(t, []string{built.Batch.Branch}, b.host.opened)
	assert.Equal(t, []int{2}, b.host.mergedAtOpen, "opened on the finished branch, which is the head CI must test")
	assert.Empty(t, b.host.closed)

	require.NoError(t, b.builder.Void(b.ctx, built, "test"))
	assert.Equal(t, []string{built.Batch.Branch}, b.host.closed)
}

func TestBatchBuilder_ABatchWhoseChangeCannotBeOpenedIsVoidedAndKeepsEveryPlace(t *testing.T) {
	b := newBatch(t, 2)
	b.host.openErr = errors.New("host down")

	built, err := b.builder.Build(b.ctx, b.candidates())

	require.Error(t, err)
	assert.Nil(t, built)
	assert.Len(t, b.host.deleted, 1)
	live, lerr := b.store.Live(b.ctx)
	require.NoError(t, lerr)
	assert.Empty(t, live)
}

func TestBatchBuilder_OnlyTheFirstMemberHoldsTheLease(t *testing.T) {
	b := newBatch(t, 2)
	built := b.build()
	require.NotNil(t, built)

	st, err := b.tickets.IntegrationLockState(b.ctx)
	require.NoError(t, err)
	_ = st
	var held []int64
	rows, err := b.database.Query(b.ctx, `SELECT ticket_id FROM leases WHERE released_at IS NULL ORDER BY ticket_id`)
	require.NoError(t, err)
	defer rows.Close()
	for rows.Next() {
		var id int64
		require.NoError(t, rows.Scan(&id))
		held = append(held, id)
	}
	assert.Equal(t, []int64{b.changes[0].ticketID}, held)
}

func TestBatchBuilder_ASecondMemberThatConflictsIsDroppedAndHandedBackNamingItsPredecessor(t *testing.T) {
	b := newBatch(t, 3)
	b.host.mergeErr[b.changes[1].ticketID] = codehost.ErrMergeConflict

	built := b.build()

	require.NotNil(t, built)
	require.Len(t, built.Members, 2)
	assert.Equal(t, b.changes[0].ticketID, built.Members[0].Row.TicketID)
	assert.Equal(t, b.changes[2].ticketID, built.Members[1].Row.TicketID)
	assert.Equal(t, []int64{b.changes[0].ticketID, b.changes[2].ticketID}, built.Batch.Members)

	dropped := b.rowOf(b.changes[1].ticketID)
	assert.Equal(t, landing.BackWithAgent, dropped.State)
	assert.Equal(t, landing.MergeMainConflict, dropped.BackReason)
	assert.Nil(t, dropped.BatchID, "the reservation is gone")
	assert.Empty(t, dropped.Step)
	assert.Equal(t, 2, b.rowOf(b.changes[2].ticketID).BatchPosition)
	note := b.members.comments[b.changes[1].ticketID]
	assert.Contains(t, note, fmt.Sprintf("#%d", b.changes[0].ticketID), "it names the member merged ahead of it")
	assert.Contains(t, note, "conflict")
	assert.Equal(t, []int64{b.changes[1].ticketID}, b.members.requeued, "the member has no lease: it leaves the merge queue with its row")
	rec := b.outcomeOf(b.changes[1].ticketID)
	assert.Equal(t, landing.MergeMainConflict, rec.Outcome)
	assert.Contains(t, rec.Detail, fmt.Sprint(b.changes[0].ticketID), "the predecessor is counted as a collision")
	// The members that are kept are untouched.
	assert.Empty(t, b.rowOf(b.changes[0].ticketID).BackReason)
}

func TestBatchBuilder_EachMemberIsBroughtUpToDateWithTheBaseBeforeItIsMerged(t *testing.T) {
	b := newBatch(t, 2)

	built := b.build()

	require.NotNil(t, built)
	assert.Equal(t, b.ids(), b.host.updated)
	assert.Equal(t, b.ids(), b.host.merged)
}

func TestBatchBuilder_AMemberIsMergedAtTheHeadItsUpdateMade(t *testing.T) {
	b := newBatch(t, 2)
	updated := "cccccccccccccccccccccccccccccccccccccccc"
	b.host.newHead[b.changes[0].ticketID] = updated

	built := b.build()

	require.NotNil(t, built)
	assert.Equal(t, updated, b.host.mergedHeads[b.changes[0].ticketID])
	assert.Equal(t, updated, built.Members[0].Row.HeadSHA, "the gate's evidence names the head that was tested")
	assert.Equal(t, headA, b.host.mergedHeads[b.changes[1].ticketID], "a member that was up to date keeps its head")
}

func TestBatchBuilder_AMemberThatConflictsWithTheBaseIsHandedBackWithoutBlamingItsPredecessor(t *testing.T) {
	b := newBatch(t, 3)
	b.host.updateErr[b.changes[1].ticketID] = codehost.ErrMergeConflict

	built := b.build()

	require.NotNil(t, built)
	require.Len(t, built.Members, 2)
	dropped := b.rowOf(b.changes[1].ticketID)
	assert.Equal(t, landing.MergeMainConflict, dropped.BackReason)
	assert.Nil(t, dropped.BatchID)
	assert.NotContains(t, b.members.comments[b.changes[1].ticketID], fmt.Sprintf("#%d", b.changes[0].ticketID))
	assert.NotContains(t, b.host.merged, b.changes[1].ticketID, "it was never merged into the batch")
	assert.Equal(t, []int64{b.changes[0].ticketID, b.changes[2].ticketID}, built.Batch.Members)
}

func TestBatchBuilder_AnUpdateTheHostHasNotAppliedHandsTheMemberBackKeepingItsPlace(t *testing.T) {
	b := newBatch(t, 2)
	b.host.updateErr[b.changes[1].ticketID] = codehost.ErrUpdatePending

	built := b.build()

	require.NotNil(t, built)
	assert.Len(t, built.Members, 1)
	row := b.rowOf(b.changes[1].ticketID)
	assert.Equal(t, landing.CodeHostError, row.BackReason)
	assert.True(t, row.KeepsPlace)
	assert.Nil(t, row.BatchID)
}

func TestBatchBuilder_AFirstMemberThatConflictsPassesTheLeaseOn(t *testing.T) {
	b := newBatch(t, 3)
	b.host.mergeErr[b.changes[0].ticketID] = codehost.ErrMergeConflict

	built := b.build()

	require.NotNil(t, built)
	require.Len(t, built.Members, 2)
	assert.Equal(t, b.changes[1].ticketID, built.Members[0].Row.TicketID)
	assert.Equal(t, landing.MergeMainConflict, b.rowOf(b.changes[0].ticketID).BackReason)
	assert.Contains(t, b.comments(b.changes[0].ticketID), "conflicts", "the first member is handed back by the lease")
	var held []int64
	rows, err := b.database.Query(b.ctx, `SELECT ticket_id FROM leases WHERE released_at IS NULL`)
	require.NoError(t, err)
	defer rows.Close()
	for rows.Next() {
		var id int64
		require.NoError(t, rows.Scan(&id))
		held = append(held, id)
	}
	assert.Equal(t, []int64{b.changes[1].ticketID}, held, "the lease is on the new first member")
	assert.NoError(t, built.Held.Err())
}

func TestBatchBuilder_AMemberWhoseHeadMovedIsHandedBackAsHeadChanged(t *testing.T) {
	b := newBatch(t, 2)
	b.host.mergeErr[b.changes[1].ticketID] = codehost.ErrHeadMoved

	built := b.build()

	require.NotNil(t, built)
	assert.Len(t, built.Members, 1)
	assert.Equal(t, landing.HeadChanged, b.rowOf(b.changes[1].ticketID).BackReason)
}

func TestBatchBuilder_EveryMemberConflictingLeavesNoBatch(t *testing.T) {
	b := newBatch(t, 2)
	for _, id := range b.ids() {
		b.host.mergeErr[id] = codehost.ErrMergeConflict
	}

	built, err := b.builder.Build(b.ctx, b.candidates())

	require.NoError(t, err)
	assert.Nil(t, built)
	for _, id := range b.ids() {
		assert.Equal(t, landing.MergeMainConflict, b.rowOf(id).BackReason)
	}
	live, err := b.store.Live(b.ctx)
	require.NoError(t, err)
	assert.Empty(t, live)
	assert.Len(t, b.host.deleted, 1, "the empty batch's branch is deleted")
}

func TestBatchBuilder_ACodeHostErrorVoidsTheBatchAndKeepsEveryPlace(t *testing.T) {
	b := newBatch(t, 3)
	b.host.mergeErr[b.changes[1].ticketID] = errors.New("host down")

	built, err := b.builder.Build(b.ctx, b.candidates())

	require.Error(t, err)
	assert.Nil(t, built)
	live, lerr := b.store.Live(b.ctx)
	require.NoError(t, lerr)
	assert.Empty(t, live)
	assert.Len(t, b.host.deleted, 1)
	// The holder is handed back keeping its place; the rest are queued as before.
	head := b.rowOf(b.changes[0].ticketID)
	assert.Equal(t, landing.BackWithAgent, head.State)
	assert.True(t, head.KeepsPlace)
	for _, c := range b.changes[1:] {
		row := b.rowOf(c.ticketID)
		assert.Equal(t, landing.Landing, row.State)
		assert.Nil(t, row.BatchID)
		assert.Empty(t, row.Step)
	}
}

func TestBatchBuilder_AMemberThatCannotBeClaimedIsLeftOut(t *testing.T) {
	b := newBatch(t, 3)
	cands := b.candidates()
	// A worker took the first one between the read and the claim.
	_, err := b.database.Exec(b.ctx, `UPDATE attempts SET status = 'in_progress' WHERE ticket_id = $1`, b.changes[0].ticketID)
	require.NoError(t, err)

	built, err := b.builder.Build(b.ctx, cands)

	require.NoError(t, err)
	require.NotNil(t, built)
	t.Cleanup(built.Close)
	assert.Equal(t, b.ids()[1:], built.Batch.Members)
	assert.Nil(t, b.rowOf(b.changes[0].ticketID).BatchID)
}

func TestBatchBuilder_DoneAndVoidDeleteTheBranch(t *testing.T) {
	t.Run("done", func(t *testing.T) {
		b := newBatch(t, 2)
		built := b.build()
		require.NoError(t, b.builder.Done(b.ctx, built, landing.ResultMerged, ""))
		assert.Equal(t, []string{built.Batch.Branch}, b.host.deleted)
		got, err := b.store.Get(b.ctx, built.Batch.ID)
		require.NoError(t, err)
		assert.Equal(t, landing.BatchDone, got.State)
	})
	t.Run("void", func(t *testing.T) {
		b := newBatch(t, 2)
		built := b.build()
		require.NoError(t, b.builder.Void(b.ctx, built, "test"))
		assert.Equal(t, []string{built.Batch.Branch}, b.host.deleted)
		for _, id := range b.ids() {
			assert.Nil(t, b.rowOf(id).BatchID)
			assert.Equal(t, landing.Landing, b.rowOf(id).State)
		}
	})
	t.Run("a branch that cannot be deleted still frees the members of a void", func(t *testing.T) {
		b := newBatch(t, 2)
		built := b.build()
		b.host.deleteErr = errors.New("host down")
		require.Error(t, b.builder.Void(b.ctx, built, "test"))
		assert.Nil(t, b.rowOf(b.changes[0].ticketID).BatchID)
	})
	t.Run("a done batch whose branch is left stays live", func(t *testing.T) {
		b := newBatch(t, 2)
		built := b.build()
		b.host.deleteErr = errors.New("host down")
		require.Error(t, b.builder.Done(b.ctx, built, landing.ResultMerged, ""))
		live, err := b.store.Live(b.ctx)
		require.NoError(t, err)
		assert.Len(t, live, 1, "so that it is found and its branch deleted")
	})
}

// A selector over a policy: the apps and solo paths a test sets.
func selector(host *batchHostFake) *landing.BatchSelector {
	return landing.NewBatchSelector(&landing.PathPolicy{
		Apps:         []string{"app/", "other/"},
		SoloPrefixes: []string{".woodpecker/", "test.sh"},
	}, host)
}

func candidate(id int64, paths ...string) landing.Candidate {
	return landing.Candidate{Row: &landing.LandingRow{TicketID: id, AttemptNumber: 1}, Paths: paths}
}

func selected(t *testing.T, got []landing.Candidate, err error) []int64 {
	t.Helper()
	require.NoError(t, err)
	var ids []int64
	for _, c := range got {
		ids = append(ids, c.Row.TicketID)
	}
	return ids
}

func TestBatchSelector_TakesTheHeadAndTheNextOfItsAppUpToTheSize(t *testing.T) {
	cands := []landing.Candidate{candidate(1, "app/a/"), candidate(2, "other/b/"), candidate(3, "app/c/"), candidate(4, "app/d/")}

	got, err := selector(newBatchHost()).Select(context.Background(), cands, 3)

	assert.Equal(t, []int64{1, 3, 4}, selected(t, got, err), "another app's change is passed over, not a stop")
	got, err = selector(newBatchHost()).Select(context.Background(), cands, 2)
	assert.Equal(t, []int64{1, 3}, selected(t, got, err))
}

func TestBatchSelector_SizeOneIsTheSerialLanding(t *testing.T) {
	got, err := selector(newBatchHost()).Select(context.Background(), []landing.Candidate{candidate(1, "app/a/"), candidate(2, "app/b/")}, 1)
	assert.Equal(t, []int64{1}, selected(t, got, err))
}

func TestBatchSelector_ChangesToTheHarnessOrSettingsAreABatchOfOne(t *testing.T) {
	cands := []landing.Candidate{candidate(1, "app/a/"), candidate(2, "app/b/"), candidate(3, "app/c/")}
	t.Run("a later change touching the harness is left for its own batch", func(t *testing.T) {
		host := newBatchHost()
		host.files[2] = []string{"x.go", ".woodpecker/app.yaml"}
		got, err := selector(host).Select(context.Background(), cands, 3)
		assert.Equal(t, []int64{1, 3}, selected(t, got, err))
	})
	t.Run("a head touching it is alone", func(t *testing.T) {
		host := newBatchHost()
		host.files[1] = []string{"test.sh"}
		got, err := selector(host).Select(context.Background(), cands, 3)
		assert.Equal(t, []int64{1}, selected(t, got, err))
	})
	t.Run("negative: the same changes without it are batched", func(t *testing.T) {
		got, err := selector(newBatchHost()).Select(context.Background(), cands, 3)
		assert.Equal(t, []int64{1, 2, 3}, selected(t, got, err))
	})
}

func TestBatchSelector_AChangeInNoSingleAppIsAlone(t *testing.T) {
	t.Run("head", func(t *testing.T) {
		got, err := selector(newBatchHost()).Select(context.Background(), []landing.Candidate{candidate(1, "app/a/", "other/b/"), candidate(2, "app/c/")}, 3)
		assert.Equal(t, []int64{1}, selected(t, got, err))
	})
	t.Run("a later one is skipped", func(t *testing.T) {
		got, err := selector(newBatchHost()).Select(context.Background(), []landing.Candidate{candidate(1, "app/a/"), candidate(2, "docs/x"), candidate(3, "app/c/")}, 3)
		assert.Equal(t, []int64{1, 3}, selected(t, got, err))
	})
}

func TestBatchSelector_FilesThatCannotBeReadMeanNothingIsKnownToBeSafe(t *testing.T) {
	cands := []landing.Candidate{candidate(1, "app/a/"), candidate(2, "app/b/"), candidate(3, "app/c/")}
	t.Run("a later change is left out", func(t *testing.T) {
		host := newBatchHost()
		host.fileErr[2] = errors.New("host down")
		got, err := selector(host).Select(context.Background(), cands, 3)
		assert.Equal(t, []int64{1, 3}, selected(t, got, err))
	})
	t.Run("the head is an error", func(t *testing.T) {
		host := newBatchHost()
		host.fileErr[1] = errors.New("host down")
		_, err := selector(host).Select(context.Background(), cands, 3)
		assert.Error(t, err)
	})
}

func TestQueueCandidates_AreWaitingRowsInHeadOrderWithTheirPaths(t *testing.T) {
	f := newFixture(t)
	low := f.finishedChange(model.PriorityLow)
	urgent := f.finishedChange(model.PriorityUrgent)
	begun := f.finishedChange(model.PriorityNormal)
	require.NoError(t, f.queue.SetStep(f.ctx, begun.ticketID, landing.Merging))
	reserved := f.finishedChange(model.PriorityNormal)
	store := landing.NewBatchStore(f.database)
	_, err := reserveNow(f.ctx, store, baseOld, []int64{reserved.ticketID})
	require.NoError(t, err)

	cands, err := f.queue.Candidates(f.ctx)

	require.NoError(t, err)
	require.Len(t, cands, 2, "a begun row and a reserved one are not candidates")
	assert.Equal(t, urgent.ticketID, cands[0].Row.TicketID)
	assert.Equal(t, low.ticketID, cands[1].Row.TicketID)
	assert.Equal(t, []string{"app/server/"}, cands[0].Paths)
}

func TestPostgresHost_MergesAMembersHeadBranchOnlyWhileItIsTheHeadThatWasQueued(t *testing.T) {
	f := newFixture(t)
	c := f.finishedChange(model.PriorityNormal)
	row := &landing.LandingRow{TicketID: c.ticketID, AttemptNumber: 1, HeadSHA: headA}
	repo := codehost.Repo{Owner: "acme", Name: "app"}
	head := ticket.BranchName(c.ticketID, 1)
	f.host.BranchAt = map[codehosttest.BranchKey]string{
		{Repo: repo, Branch: head}:   headA,
		{Repo: repo, Branch: "main"}: baseOld,
	}
	f.host.BranchMerged = codehost.BranchMerge{SHA: "merged"}
	host := f.hostWrap.PostgresHost

	tip, err := host.BaseTip(f.ctx, row)
	require.NoError(t, err)
	assert.Equal(t, baseOld, tip)

	got, err := host.MergeHead(f.ctx, row, "slop-haus-batch-1", headA)
	require.NoError(t, err)
	assert.Equal(t, "merged", got.SHA)
	require.Len(t, f.host.BranchMerges(), 1)
	assert.Equal(t, codehosttest.BranchMergeCall{Tenant: f.tenant, Repo: repo, Into: "slop-haus-batch-1", From: head}, f.host.BranchMerges()[0])

	// Negative: a push since the row was queued is not merged.
	f.host.BranchAt[codehosttest.BranchKey{Repo: repo, Branch: head}] = headB
	_, err = host.MergeHead(f.ctx, row, "slop-haus-batch-1", headA)
	require.ErrorIs(t, err, codehost.ErrHeadMoved)
	assert.Len(t, f.host.BranchMerges(), 1, "nothing was merged")

	f.host.BranchMergeErr = codehost.ErrMergeConflict
	f.host.BranchAt[codehosttest.BranchKey{Repo: repo, Branch: head}] = headA
	_, err = host.MergeHead(f.ctx, row, "slop-haus-batch-1", headA)
	require.ErrorIs(t, err, codehost.ErrMergeConflict)

	require.NoError(t, host.DeleteBranch(f.ctx, row, "slop-haus-batch-1"))
	assert.Equal(t, []codehosttest.BranchKey{{Repo: repo, Branch: "slop-haus-batch-1"}}, f.host.BranchDeletes())
}

func TestBatchStore_ReserveDoesNotWaitForARowAnotherTransactionHolds(t *testing.T) {
	b := newBatch(t, 2)
	held := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- b.database.InTx(b.ctx, func(q db.Querier) error {
			if _, err := q.Exec(b.ctx, `SELECT 1 FROM landings WHERE ticket_id = $1 FOR UPDATE`, b.changes[1].ticketID); err != nil {
				return err
			}
			close(held)
			<-release
			return nil
		})
	}()
	<-held
	// A reservation that waited for the lock would block here, so it is bounded.
	ctx, cancel := context.WithTimeout(b.ctx, 10*time.Second)
	defer cancel()

	created, err := b.store.Create(ctx, baseOld, b.ids())
	require.NoError(t, err)
	_, err = b.store.Reserve(ctx, created.ID)
	close(release)
	require.NoError(t, <-done)

	require.ErrorIs(t, err, landing.ErrNotReservable, "the row another transaction holds is not waited for")
	assert.Nil(t, b.rowOf(b.changes[0].ticketID).BatchID)
}

func TestQueueHead_SkipsEveryReservedBatchMember(t *testing.T) {
	b := newBatch(t, 3)
	_, err := reserveNow(b.ctx, b.store, baseOld, b.ids()[:2])
	require.NoError(t, err)

	head, err := b.queue.Head(b.ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, head)
	assert.Equal(t, b.changes[2].ticketID, head.TicketID, "a batch's members are its runner's, the leased first one too")

	head, err = b.queue.Head(b.ctx, []int64{b.changes[2].ticketID})
	require.NoError(t, err)
	assert.Nil(t, head)
}

func TestBatchStore_ReleaseClearsTheReservationAndKeepsTheBatchsRecord(t *testing.T) {
	b := newBatch(t, 2)
	batch, err := reserveNow(b.ctx, b.store, baseOld, b.ids())
	require.NoError(t, err)
	require.NoError(t, b.store.SetTested(b.ctx, batch.ID, []string{"p1", "p2"}, []string{"h1", "h2"}))

	require.NoError(t, b.store.Release(b.ctx, batch.ID, b.changes[1].ticketID))

	got, err := b.store.Get(b.ctx, batch.ID)
	require.NoError(t, err)
	assert.Equal(t, b.ids(), got.Members)
	assert.Equal(t, []string{"h1", "h2"}, got.Heads)
	assert.Nil(t, b.rowOf(b.changes[1].ticketID).BatchID, "the row is the queue's again")
	assert.Error(t, b.store.Release(b.ctx, batch.ID, b.changes[1].ticketID), "not a member any more")
	assert.Error(t, b.store.Release(b.ctx, batch.ID+1, b.changes[0].ticketID), "a member of another batch is not released")
	assert.NotNil(t, b.rowOf(b.changes[0].ticketID).BatchID)
}

func TestPostgresHost_ReadsAMembersFilesAndCreatesTheBatchBranchInItsRepo(t *testing.T) {
	f := newFixture(t)
	c := f.finishedChange(model.PriorityNormal)
	row := &landing.LandingRow{TicketID: c.ticketID, AttemptNumber: 1, HeadSHA: headA}
	f.host.DiffOf = map[string]*codehost.Diff{headA: {Files: []codehost.FileDiff{{Path: "a/x.go"}, {Path: ".woodpecker/ci.yaml"}}}}
	host := f.hostWrap.PostgresHost

	files, err := host.ChangedFiles(f.ctx, row)
	require.NoError(t, err)
	assert.Equal(t, []string{"a/x.go", ".woodpecker/ci.yaml"}, files)
	_, err = host.ChangedFiles(f.ctx, &landing.LandingRow{TicketID: c.ticketID, AttemptNumber: 1, HeadSHA: headB})
	assert.ErrorIs(t, err, codehost.ErrChangeNotFound, "a head the host has no diff for is an error, not no files")

	ref, err := host.OpenBatchChange(f.ctx, row, "slop-haus-batch-7")
	require.NoError(t, err)
	opens := f.host.Opens()
	require.Len(t, opens, 2, "the fixture's own change, then the batch's")
	open := opens[1]
	assert.Equal(t, "slop-haus-batch-7", open.Request.Head)
	assert.True(t, open.Request.Draft, "a batch's change is a draft")
	require.NoError(t, host.CloseBatchChange(f.ctx, row, ref))
	require.Len(t, f.host.Closed(), 1)
	assert.Equal(t, ref, f.host.Closed()[0].Change)

	require.NoError(t, host.CreateBranch(f.ctx, row, "slop-haus-batch-7", baseNew))
	assert.Equal(t, baseNew, f.host.BranchAt[codehosttest.BranchKey{Repo: codehost.Repo{Owner: "acme", Name: "app"}, Branch: "slop-haus-batch-7"}])
}

func TestBatchStore_DropTakesTheMembersTestedHeadOutWithIt(t *testing.T) {
	b := newBatch(t, 3)
	batch, err := b.store.Create(b.ctx, baseOld, b.ids())
	require.NoError(t, err)
	require.NoError(t, b.store.SetTested(b.ctx, batch.ID, []string{"p1", "p2", "p3"}, []string{"h1", "h2", "h3"}))

	got, err := b.store.Drop(b.ctx, batch.ID, b.changes[1].ticketID)

	require.NoError(t, err)
	assert.Equal(t, []string{"p1", "p3"}, got.Prefixes)
	assert.Equal(t, []string{"h1", "h3"}, got.Heads)
}

func TestLandingStore_HandBackClearsTheReservation(t *testing.T) {
	b := newBatch(t, 2)
	_, err := reserveNow(b.ctx, b.store, baseOld, b.ids())
	require.NoError(t, err)

	require.NoError(t, b.queue.HandBack(b.ctx, b.changes[1].ticketID, landing.ChecksRed, false))

	row := b.rowOf(b.changes[1].ticketID)
	require.NotNil(t, row)
	assert.Equal(t, landing.BackWithAgent, row.State)
	assert.Nil(t, row.BatchID)
	assert.Zero(t, row.BatchPosition)
}

func TestBatchStore_VoidClearsAHandedBackMemberToo(t *testing.T) {
	b := newBatch(t, 2)
	batch, err := reserveNow(b.ctx, b.store, baseOld, b.ids())
	require.NoError(t, err)
	// A row handed back by a path that predates the clearing keeps its
	// reservation.
	_, err = b.database.Exec(b.ctx, `UPDATE landings SET queue_state = 'back_with_agent', back_reason = 'checks_red' WHERE ticket_id = $1`, b.changes[0].ticketID)
	require.NoError(t, err)

	require.NoError(t, b.store.Void(b.ctx, batch.ID, "test"))

	row := b.rowOf(b.changes[0].ticketID)
	require.NotNil(t, row)
	assert.Nil(t, row.BatchID)
	assert.Zero(t, row.BatchPosition)
}

func TestLandingStore_ReEnqueueClearsAStaleReservation(t *testing.T) {
	b := newBatch(t, 1)
	c := b.changes[0]
	batch, err := reserveNow(b.ctx, b.store, baseOld, b.ids())
	require.NoError(t, err)
	_, err = b.database.Exec(b.ctx, `
UPDATE landings SET queue_state = 'back_with_agent', back_reason = 'checks_red',
       batch_id = $2, batch_position = 3 WHERE ticket_id = $1`, c.ticketID, batch.ID)
	require.NoError(t, err)

	require.NoError(t, b.queue.Enqueue(b.ctx, &landing.LandingRow{TicketID: c.ticketID, AttemptNumber: 1, ChangeURL: "u", HeadSHA: "h"}))

	row := b.rowOf(c.ticketID)
	require.NotNil(t, row)
	assert.Equal(t, landing.Landing, row.State)
	assert.Nil(t, row.BatchID)
	assert.Zero(t, row.BatchPosition)
	assert.Len(t, b.candidates(), 1, "the row can be batched again")
}

func TestBatchStore_SetChangeKeepsTheWholeRef(t *testing.T) {
	b := newBatch(t, 2)
	batch, err := reserveNow(b.ctx, b.store, baseOld, b.ids())
	require.NoError(t, err)
	assert.Nil(t, batch.Change, "none until the change is opened")
	ref := codehost.ChangeRef{Host: "git.example", Owner: "acme", Repo: "app", Number: 1567}

	require.NoError(t, b.store.SetChange(b.ctx, batch.ID, ref))

	got, err := b.store.Get(b.ctx, batch.ID)
	require.NoError(t, err)
	require.NotNil(t, got.Change)
	assert.Equal(t, ref, *got.Change)
}
