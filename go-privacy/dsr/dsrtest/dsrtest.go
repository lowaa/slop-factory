// Package dsrtest holds what an app needs to test its request tables against
// go-privacy/dsr: the test schema and StoreSuite.
package dsrtest

import (
	"context"
	"io/fs"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing/fstest"

	"github.com/lowaa/slop-factory/go-privacy/dsr"
	"github.com/lowaa/slop-factory/go-privacy/dsr/schema"
)

// Migrations is a migration source for go-db's dbtest: the request tables.
func Migrations() fs.FS {
	return fstest.MapFS{"000001_dsr.sql": {Data: []byte(schema.DSR)}}
}

func newRequest(email string, due time.Time) (*dsr.Request, *dsr.Event) {
	id := uuid.New()
	received := due.Add(-30 * 24 * time.Hour)
	return &dsr.Request{
			ID: id, RequesterName: "Pat", RequesterEmail: email, Type: dsr.TypeAccess, Source: dsr.SourceEmail,
			Status: dsr.StatusReceived, ReceivedAt: received, DueAt: due,
		}, &dsr.Event{
			RequestID: id, At: received, Actor: "test", Kind: dsr.EventReceived, ToStatus: dsr.StatusReceived,
		}
}

// StoreSuite is the contract every dsr Store passes: an app runs it against
// its own migrated database, so drift between its tables and the SQL here
// fails its build.
func StoreSuite(t *testing.T, store dsr.Store) {
	ctx := context.Background()
	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	const lease = 30 * time.Minute
	// Every subtest shares one database, so each names its requests by a
	// marker in the email and looks only at those.
	insert := func(t *testing.T, due time.Time) *dsr.Request {
		t.Helper()
		r, ev := newRequest(uuid.NewString()+"@example.test", due)
		require.NoError(t, store.Insert(ctx, r, ev))
		return r
	}
	only := func(rs []*dsr.Request, want ...*dsr.Request) []uuid.UUID {
		wanted := map[uuid.UUID]bool{}
		for _, w := range want {
			wanted[w.ID] = true
		}
		var out []uuid.UUID
		for _, r := range rs {
			if wanted[r.ID] {
				out = append(out, r.ID)
			}
		}
		return out
	}

	t.Run("a request and its first event round-trip", func(t *testing.T) {
		r := insert(t, now.Add(24*time.Hour))
		got, err := store.Get(ctx, r.ID)
		require.NoError(t, err)
		assert.Equal(t, r.RequesterEmail, got.RequesterEmail)
		assert.Equal(t, dsr.StatusReceived, got.Status)
		assert.True(t, r.DueAt.Equal(got.DueAt))
		assert.False(t, got.Verified)
		assert.Nil(t, got.ClosedAt)
		evs, err := store.Events(ctx, r.ID)
		require.NoError(t, err)
		require.Len(t, evs, 1)
		assert.Equal(t, dsr.EventReceived, evs[0].Kind)
		assert.Equal(t, dsr.StatusReceived, evs[0].ToStatus)
		assert.Empty(t, evs[0].FromStatus)
	})

	t.Run("an unknown request is not found", func(t *testing.T) {
		_, err := store.Get(ctx, uuid.New())
		assert.ErrorIs(t, err, dsr.ErrNotFound)
		_, err = store.Update(ctx, uuid.New(), func(*dsr.Request) (*dsr.Event, error) { return nil, nil })
		assert.ErrorIs(t, err, dsr.ErrNotFound)
	})

	t.Run("an insert the database refuses leaves no event behind", func(t *testing.T) {
		r, ev := newRequest(uuid.NewString()+"@example.test", now)
		r.Type = "not-a-type"
		require.Error(t, store.Insert(ctx, r, ev))
		evs, err := store.Events(ctx, r.ID)
		require.NoError(t, err)
		assert.Empty(t, evs)
	})

	t.Run("an update writes the request and its event together; a failed change writes neither", func(t *testing.T) {
		r := insert(t, now.Add(24*time.Hour))
		boom := assert.AnError
		_, err := store.Update(ctx, r.ID, func(r *dsr.Request) (*dsr.Event, error) {
			r.Status = dsr.StatusInProgress
			return nil, boom
		})
		require.ErrorIs(t, err, boom)
		got, _ := store.Get(ctx, r.ID)
		assert.Equal(t, dsr.StatusReceived, got.Status)

		_, err = store.Update(ctx, r.ID, func(r *dsr.Request) (*dsr.Event, error) {
			r.Status = dsr.StatusInProgress
			return &dsr.Event{At: now, Actor: "op", Kind: dsr.EventStatus, FromStatus: dsr.StatusReceived, ToStatus: dsr.StatusInProgress, Detail: "started"}, nil
		})
		require.NoError(t, err)
		got, _ = store.Get(ctx, r.ID)
		assert.Equal(t, dsr.StatusInProgress, got.Status)
		evs, _ := store.Events(ctx, r.ID)
		require.Len(t, evs, 2)
		assert.Equal(t, "started", evs[1].Detail)
		assert.Equal(t, dsr.StatusReceived, evs[1].FromStatus)
	})

	t.Run("an event the database refuses rolls the request change back", func(t *testing.T) {
		r := insert(t, now.Add(24*time.Hour))
		_, err := store.Update(ctx, r.ID, func(r *dsr.Request) (*dsr.Event, error) {
			r.Status = dsr.StatusInProgress
			return &dsr.Event{At: now, Actor: "op", Kind: "not-a-kind"}, nil
		})
		require.Error(t, err)
		got, _ := store.Get(ctx, r.ID)
		assert.Equal(t, dsr.StatusReceived, got.Status)
	})

	t.Run("concurrent updates to one request do not lose each other", func(t *testing.T) {
		r := insert(t, now.Add(24*time.Hour))
		const n = 8
		var wg sync.WaitGroup
		for range n {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, err := store.Update(ctx, r.ID, func(r *dsr.Request) (*dsr.Event, error) {
					r.ExtensionReason += "x"
					return nil, nil
				})
				assert.NoError(t, err)
			}()
		}
		wg.Wait()
		got, _ := store.Get(ctx, r.ID)
		assert.Equal(t, strings.Repeat("x", n), got.ExtensionReason)
	})

	t.Run("list puts open requests soonest due first, then closed ones, and can leave closed out", func(t *testing.T) {
		later := insert(t, now.Add(48*time.Hour))
		sooner := insert(t, now.Add(24*time.Hour))
		closed := insert(t, now.Add(time.Hour))
		_, err := store.Update(ctx, closed.ID, func(r *dsr.Request) (*dsr.Event, error) {
			c := now
			r.Status, r.ClosedAt = dsr.StatusCompleted, &c
			return nil, nil
		})
		require.NoError(t, err)

		all, err := store.List(ctx, &dsr.Filter{Limit: 10000})
		require.NoError(t, err)
		assert.Equal(t, []uuid.UUID{sooner.ID, later.ID, closed.ID}, only(all, later, sooner, closed))
		open, err := store.List(ctx, &dsr.Filter{OpenOnly: true, Limit: 10000})
		require.NoError(t, err)
		assert.Equal(t, []uuid.UUID{sooner.ID, later.ID}, only(open, later, sooner, closed))
		one, err := store.List(ctx, &dsr.Filter{Limit: 1})
		require.NoError(t, err)
		assert.Len(t, one, 1)
	})

	t.Run("alerts are claimed once per threshold and only for open requests", func(t *testing.T) {
		week := 7 * 24 * time.Hour
		far := insert(t, now.Add(30*24*time.Hour))
		near := insert(t, now.Add(3*24*time.Hour))
		late := insert(t, now.Add(-time.Hour))
		done := insert(t, now.Add(-time.Hour))
		_, err := store.Update(ctx, done.ID, func(r *dsr.Request) (*dsr.Event, error) {
			c := now
			r.Status, r.ClosedAt = dsr.StatusRefused, &c
			return nil, nil
		})
		require.NoError(t, err)

		claimed, err := store.ClaimAlerts(ctx, now, week, lease)
		require.NoError(t, err)
		kinds := map[uuid.UUID]dsr.AlertKind{}
		for _, a := range claimed {
			kinds[a.Request.ID] = a.Kind
		}
		assert.Equal(t, dsr.AlertDueSoon, kinds[near.ID])
		assert.Equal(t, dsr.AlertOverdue, kinds[late.ID])
		assert.NotContains(t, kinds, far.ID)
		assert.NotContains(t, kinds, done.ID)

		again, err := store.ClaimAlerts(ctx, now, week, lease)
		require.NoError(t, err)
		assert.Empty(t, only(alertRequests(again), near, late), "already alerted")

		require.NoError(t, store.ReleaseAlerts(ctx, claimed))
		released, err := store.ClaimAlerts(ctx, now, week, lease)
		require.NoError(t, err)
		assert.ElementsMatch(t, []uuid.UUID{near.ID, late.ID}, only(alertRequests(released), near, late), "released alerts are offered again")

		// Past the due date a request already told it is near is told again, as overdue.
		overdue, err := store.ClaimAlerts(ctx, now.Add(4*24*time.Hour), week, lease)
		require.NoError(t, err)
		assert.Equal(t, []uuid.UUID{near.ID}, only(alertRequests(overdue), near))
	})

	t.Run("a claim whose alert never went out is taken over after the lease, and one that did is not", func(t *testing.T) {
		abandoned := insert(t, now.Add(24*time.Hour))
		sent := insert(t, now.Add(24*time.Hour))
		first, err := store.ClaimAlerts(ctx, now, 7*24*time.Hour, lease)
		require.NoError(t, err)
		require.NotEmpty(t, only(alertRequests(first), abandoned, sent))
		// Only one of them was recorded as sent.
		require.NoError(t, store.AppendEvents(ctx, []*dsr.Event{{RequestID: sent.ID, At: now, Actor: "system", Kind: dsr.EventAlertSoon}}))

		early, err := store.ClaimAlerts(ctx, now.Add(lease-time.Minute), 7*24*time.Hour, lease)
		require.NoError(t, err)
		assert.Empty(t, only(alertRequests(early), abandoned, sent), "inside the lease the claimer may still be sending")

		late, err := store.ClaimAlerts(ctx, now.Add(lease+time.Minute), 7*24*time.Hour, lease)
		require.NoError(t, err)
		assert.Equal(t, []uuid.UUID{abandoned.ID}, only(alertRequests(late), abandoned, sent))
	})

	t.Run("concurrent claims of one alert yield it once", func(t *testing.T) {
		near := insert(t, now.Add(24*time.Hour))
		const n = 6
		results := make([][]*dsr.Alert, n)
		var wg sync.WaitGroup
		for i := range n {
			wg.Add(1)
			go func() {
				defer wg.Done()
				as, err := store.ClaimAlerts(ctx, now, 7*24*time.Hour, lease)
				assert.NoError(t, err)
				results[i] = as
			}()
		}
		wg.Wait()
		claims := 0
		for _, as := range results {
			claims += len(only(alertRequests(as), near))
		}
		assert.Equal(t, 1, claims)
	})

	t.Run("releasing a claim leaves a newer one alone", func(t *testing.T) {
		near := insert(t, now.Add(24*time.Hour))
		first, err := store.ClaimAlerts(ctx, now, 7*24*time.Hour, lease)
		require.NoError(t, err)
		// An extension clears the mark, and a later check claims it afresh.
		_, err = store.Update(ctx, near.ID, func(r *dsr.Request) (*dsr.Event, error) {
			r.AlertedSoonAt = nil
			return nil, nil
		})
		require.NoError(t, err)
		second, err := store.ClaimAlerts(ctx, now.Add(time.Minute), 7*24*time.Hour, lease)
		require.NoError(t, err)
		require.NotEmpty(t, only(alertRequests(second), near))

		require.NoError(t, store.ReleaseAlerts(ctx, first))
		third, err := store.ClaimAlerts(ctx, now.Add(2*time.Minute), 7*24*time.Hour, lease)
		require.NoError(t, err)
		assert.Empty(t, only(alertRequests(third), near), "the stale release did not undo the newer claim")
	})
}

func alertRequests(as []*dsr.Alert) []*dsr.Request {
	out := make([]*dsr.Request, len(as))
	for i, a := range as {
		out[i] = a.Request
	}
	return out
}
