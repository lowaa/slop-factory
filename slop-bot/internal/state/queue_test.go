package state_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/ident"
	"github.com/lowaa/slop-factory/slop-bot/internal/state"
)

func (f *fixture) queued(t *testing.T, pool state.Pool) []string {
	t.Helper()
	list, err := f.Queue.List(context.Background(), pool)
	require.NoError(t, err)
	out := make([]string, len(list))
	for i, q := range list {
		out[i] = string(q.Ticket)
		if q.Front {
			out[i] += "*"
		}
	}
	return out
}

func TestQueueIsFirstComeFirstServedPerPool(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	for _, tk := range []ident.TicketID{"1", "2", "3"} {
		require.NoError(t, f.Queue.PushBack(ctx, state.PoolNormal, tk))
	}
	require.NoError(t, f.Queue.PushBack(ctx, state.PoolUrgent, "u1"))

	assert.Equal(t, []string{"1", "2", "3"}, f.queued(t, state.PoolNormal))
	assert.Equal(t, []string{"u1"}, f.queued(t, state.PoolUrgent))
	for _, want := range []ident.TicketID{"1", "2", "3"} {
		got, ok, err := f.Queue.Pop(ctx, state.PoolNormal)
		require.NoError(t, err)
		require.True(t, ok)
		assert.Equal(t, want, got)
	}
	_, ok, err := f.Queue.Pop(ctx, state.PoolNormal)
	require.NoError(t, err)
	assert.False(t, ok)
	assert.Equal(t, []string{"u1"}, f.queued(t, state.PoolUrgent))
}

func TestPushFrontGoesAheadOfNewWorkInArrivalOrder(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	require.NoError(t, f.Queue.PushBack(ctx, state.PoolNormal, "new1"))
	require.NoError(t, f.Queue.PushBack(ctx, state.PoolNormal, "new2"))
	require.NoError(t, f.Queue.PushFront(ctx, state.PoolNormal, "answered1"))
	require.NoError(t, f.Queue.PushBack(ctx, state.PoolNormal, "new3"))
	require.NoError(t, f.Queue.PushFront(ctx, state.PoolNormal, "answered2"))

	// Answered agents first, the earlier answer first; then new work.
	assert.Equal(t, []string{"answered1*", "answered2*", "new1", "new2", "new3"}, f.queued(t, state.PoolNormal))
	got, _, err := f.Queue.Pop(ctx, state.PoolNormal)
	require.NoError(t, err)
	assert.Equal(t, ident.TicketID("answered1"), got)
}

func TestPushingAQueuedTicketAgain(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	for _, tk := range []ident.TicketID{"1", "2", "3"} {
		require.NoError(t, f.Queue.PushBack(ctx, state.PoolNormal, tk))
	}
	require.NoError(t, f.Queue.PushFront(ctx, state.PoolNormal, "f1"))
	require.NoError(t, f.Queue.PushFront(ctx, state.PoolNormal, "f2"))

	require.NoError(t, f.Queue.PushBack(ctx, state.PoolNormal, "1"))
	assert.Equal(t, []string{"f1*", "f2*", "1", "2", "3"}, f.queued(t, state.PoolNormal), "re-reconciling keeps its place")
	require.NoError(t, f.Queue.PushBack(ctx, state.PoolNormal, "f1"))
	assert.Equal(t, []string{"f1*", "f2*", "1", "2", "3"}, f.queued(t, state.PoolNormal), "PushBack doesn't demote")
	require.NoError(t, f.Queue.PushFront(ctx, state.PoolNormal, "f1"))
	assert.Equal(t, []string{"f1*", "f2*", "1", "2", "3"}, f.queued(t, state.PoolNormal), "already at the front")

	require.NoError(t, f.Queue.PushFront(ctx, state.PoolNormal, "3"))
	assert.Equal(t, []string{"f1*", "f2*", "3*", "1", "2"}, f.queued(t, state.PoolNormal), "moves up")

	require.NoError(t, f.Queue.PushBack(ctx, state.PoolUrgent, "2"))
	assert.Equal(t, []string{"f1*", "f2*", "3*", "1"}, f.queued(t, state.PoolNormal))
	assert.Equal(t, []string{"2"}, f.queued(t, state.PoolUrgent), "moved pools")
}

func TestQueueRemove(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	require.NoError(t, f.Queue.PushBack(ctx, state.PoolNormal, "1"))
	require.NoError(t, f.Queue.PushBack(ctx, state.PoolNormal, "2"))
	removed, err := f.Queue.Remove(ctx, "1")
	require.NoError(t, err)
	assert.True(t, removed)
	removed, err = f.Queue.Remove(ctx, "1")
	require.NoError(t, err)
	assert.False(t, removed)
	require.NoError(t, f.Queue.PushBack(ctx, state.PoolNormal, "3"))
	assert.Equal(t, []string{"2", "3"}, f.queued(t, state.PoolNormal))
}

func TestQueueEntriesRecordWhenTheyWereQueued(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	f.clock.Advance(time.Minute)
	require.NoError(t, f.Queue.PushBack(ctx, state.PoolUrgent, "1"))
	list, err := f.Queue.List(ctx, state.PoolUrgent)
	require.NoError(t, err)
	assert.Equal(t, []state.QueuedTicket{{Ticket: "1", Pool: state.PoolUrgent, EnqueuedAt: t0.Add(time.Minute)}}, list)
}

func TestConcurrentPopsTakeEachTicketOnce(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	const n = 40
	for i := range n {
		require.NoError(t, f.Queue.PushBack(ctx, state.PoolNormal, ident.TicketID(fmt.Sprint(i))))
	}
	var (
		mu  sync.Mutex
		got []ident.TicketID
		wg  sync.WaitGroup
	)
	for range 8 {
		wg.Go(func() {
			for {
				tk, ok, err := f.Queue.Pop(ctx, state.PoolNormal)
				if !assert.NoError(t, err) || !ok {
					return
				}
				mu.Lock()
				got = append(got, tk)
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	assert.Len(t, got, n)
	seen := map[ident.TicketID]bool{}
	for _, tk := range got {
		assert.False(t, seen[tk], "%s popped twice", tk)
		seen[tk] = true
	}
}

func TestMovingPoolsKeepsAFrontLanePlace(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	require.NoError(t, f.Queue.PushBack(ctx, state.PoolUrgent, "new"))
	require.NoError(t, f.Queue.PushFront(ctx, state.PoolNormal, "answered"))

	require.NoError(t, f.Queue.PushBack(ctx, state.PoolUrgent, "answered"))
	assert.Equal(t, []string{"answered*", "new"}, f.queued(t, state.PoolUrgent))
	assert.Empty(t, f.queued(t, state.PoolNormal))
}
