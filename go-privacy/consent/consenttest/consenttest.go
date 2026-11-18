// Package consenttest holds what an app needs to test its consent tables
// against go-privacy: the test schema, a fixed Documents, and StoreSuite.
package consenttest

import (
	"context"
	_ "embed"
	"fmt"
	"io/fs"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing/fstest"

	"github.com/lowaa/slop-factory/go-privacy/consent"
	"github.com/lowaa/slop-factory/go-privacy/consent/schema"
)

//go:embed 000001_global_users.sql
var usersSQL string

// Migrations is a migration source for go-db's dbtest: a stand-in
// global_users and the consent tables.
func Migrations() fs.FS {
	return fstest.MapFS{
		"000001_global_users.sql": {Data: []byte(usersSQL)},
		"000002_consent.sql":      {Data: []byte(schema.Consent)},
	}
}

// StaticDocuments is a Documents with a fixed list of versions per kind.
type StaticDocuments struct {
	mu       sync.Mutex
	versions map[string][]consent.DocVersion
}

func NewStaticDocuments(versions map[string][]consent.DocVersion) *StaticDocuments {
	return &StaticDocuments{versions: versions}
}

// Publish adds a version, as a new document version going live would.
func (d *StaticDocuments) Publish(kind string, v consent.DocVersion) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.versions[kind] = append(d.versions[kind], v)
}

func (d *StaticDocuments) Versions(_ context.Context, kind string) ([]consent.DocVersion, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]consent.DocVersion(nil), d.versions[kind]...), nil
}

// NewUser makes a user row the store can lock, and returns its id.
type NewUser func(t *testing.T) uuid.UUID

func ev(purpose string, granted bool, at time.Time) consent.NewEvent {
	return consent.NewEvent{Purpose: purpose, Granted: granted, DocumentKind: "cookie_policy",
		DocumentVersion: "v1", Source: consent.SourceBanner, DecidedAt: at}
}

// StoreSuite is the contract every consent Store passes: an app runs it
// against its own migrated database, so drift between its tables and the
// SQL here fails its build.
func StoreSuite(t *testing.T, store consent.Store, newUser NewUser) {
	ctx := context.Background()
	t0 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	t.Run("current is the latest event per purpose and history keeps every one", func(t *testing.T) {
		u := newUser(t)
		_, err := store.Apply(ctx, u, func(map[string]*consent.Event) (*consent.Plan, error) {
			return &consent.Plan{Events: []consent.NewEvent{
				ev("analytics", true, t0), ev("marketing", true, t0), ev("analytics", false, t0.Add(time.Minute)),
			}}, nil
		})
		require.NoError(t, err)

		cur, err := store.Current(ctx, u)
		require.NoError(t, err)
		require.Len(t, cur, 2)
		assert.False(t, cur["analytics"].Granted)
		assert.True(t, cur["marketing"].Granted)

		hist, err := store.History(ctx, u)
		require.NoError(t, err)
		require.Len(t, hist, 3)
		assert.True(t, hist[0].Granted, "history is oldest first and keeps the superseded grant")
	})

	t.Run("an earlier decided_at never beats a later one, and a tie goes to the later id", func(t *testing.T) {
		u := newUser(t)
		_, err := store.Apply(ctx, u, func(map[string]*consent.Event) (*consent.Plan, error) {
			return &consent.Plan{Events: []consent.NewEvent{
				ev("analytics", false, t0.Add(time.Hour)), ev("analytics", true, t0), // written later, decided earlier
				ev("marketing", true, t0), ev("marketing", false, t0),
			}}, nil
		})
		require.NoError(t, err)
		cur, err := store.Current(ctx, u)
		require.NoError(t, err)
		assert.False(t, cur["analytics"].Granted)
		assert.False(t, cur["marketing"].Granted)
	})

	t.Run("users' records are separate", func(t *testing.T) {
		a, b := newUser(t), newUser(t)
		_, err := store.Apply(ctx, a, func(map[string]*consent.Event) (*consent.Plan, error) {
			return &consent.Plan{Events: []consent.NewEvent{ev("analytics", true, t0)}}, nil
		})
		require.NoError(t, err)
		cur, err := store.Current(ctx, b)
		require.NoError(t, err)
		assert.Empty(t, cur)
	})

	t.Run("a plan error appends nothing", func(t *testing.T) {
		u := newUser(t)
		boom := fmt.Errorf("boom")
		_, err := store.Apply(ctx, u, func(map[string]*consent.Event) (*consent.Plan, error) { return nil, boom })
		require.ErrorIs(t, err, boom)
		hist, err := store.History(ctx, u)
		require.NoError(t, err)
		assert.Empty(t, hist)
	})

	t.Run("an event that fails after earlier ones leaves nothing behind", func(t *testing.T) {
		u := newUser(t)
		bad := ev("marketing", true, t0)
		bad.Source = "not-a-source"
		_, err := store.Apply(ctx, u, func(map[string]*consent.Event) (*consent.Plan, error) {
			return &consent.Plan{Events: []consent.NewEvent{ev("analytics", true, t0), bad}}, nil
		})
		require.Error(t, err)
		hist, err := store.History(ctx, u)
		require.NoError(t, err)
		assert.Empty(t, hist, "the first event was rolled back with the second")
	})

	t.Run("an unknown user is refused", func(t *testing.T) {
		_, err := store.Apply(ctx, uuid.New(), func(map[string]*consent.Event) (*consent.Plan, error) {
			return &consent.Plan{Events: []consent.NewEvent{ev("analytics", true, t0)}}, nil
		})
		require.ErrorIs(t, err, consent.ErrUnknownUser)
	})

	t.Run("a merge token is claimed once across users, even when nothing was written", func(t *testing.T) {
		a, b := newUser(t), newUser(t)
		merged := ev("analytics", true, t0)
		merged.MergedFromAnonymous, merged.MergeToken = true, "token-1"

		first, err := store.Apply(ctx, a, func(map[string]*consent.Event) (*consent.Plan, error) {
			return &consent.Plan{MergeToken: "token-1", Events: []consent.NewEvent{merged}}, nil
		})
		require.NoError(t, err)
		assert.False(t, first.TokenAlreadyMerged)
		assert.Len(t, first.Events, 1)

		second, err := store.Apply(ctx, b, func(map[string]*consent.Event) (*consent.Plan, error) {
			return &consent.Plan{MergeToken: "token-1", Events: []consent.NewEvent{merged}}, nil
		})
		require.NoError(t, err)
		assert.True(t, second.TokenAlreadyMerged)
		assert.Empty(t, second.Events)
		cur, err := store.Current(ctx, b)
		require.NoError(t, err)
		assert.Empty(t, cur, "the second account got nothing")

		// A spent token skips the merged events and nothing else.
		plain := ev("marketing", false, t0)
		mixed, err := store.Apply(ctx, b, func(map[string]*consent.Event) (*consent.Plan, error) {
			return &consent.Plan{MergeToken: "token-1", Events: []consent.NewEvent{merged, plain}}, nil
		})
		require.NoError(t, err)
		assert.True(t, mixed.TokenAlreadyMerged)
		require.Len(t, mixed.Events, 1)
		assert.Equal(t, "marketing", mixed.Events[0].Purpose)

		// A token claimed with no events still can't be used again.
		_, err = store.Apply(ctx, a, func(map[string]*consent.Event) (*consent.Plan, error) {
			return &consent.Plan{MergeToken: "token-2"}, nil
		})
		require.NoError(t, err)
		again, err := store.Apply(ctx, b, func(map[string]*consent.Event) (*consent.Plan, error) {
			return &consent.Plan{MergeToken: "token-2", Events: []consent.NewEvent{merged}}, nil
		})
		require.NoError(t, err)
		assert.True(t, again.TokenAlreadyMerged)
	})

	t.Run("concurrent applies for one user run one after another and lose no event", func(t *testing.T) {
		const n = 16
		u := newUser(t)
		start := make(chan struct{})
		var wg sync.WaitGroup
		var mu sync.Mutex
		var seen []int
		errs := make(chan error, n)
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				_, err := store.Apply(ctx, u, func(cur map[string]*consent.Event) (*consent.Plan, error) {
					mu.Lock()
					seen = append(seen, len(cur))
					mu.Unlock()
					return &consent.Plan{Events: []consent.NewEvent{ev(fmt.Sprintf("purpose_%d", i), true, t0)}}, nil
				})
				errs <- err
			}(i)
		}
		close(start)
		wg.Wait()
		close(errs)
		for err := range errs {
			require.NoError(t, err)
		}
		hist, err := store.History(ctx, u)
		require.NoError(t, err)
		assert.Len(t, hist, n, "every decision landed")
		var got []string
		for _, e := range hist {
			got = append(got, e.Purpose)
		}
		var want []string
		for i := 0; i < n; i++ {
			want = append(want, fmt.Sprintf("purpose_%d", i))
		}
		assert.ElementsMatch(t, want, got, "and each one is its own")
		// Each plan saw every event before it, so the sizes are 0..n-1: two
		// plans that overlapped would have seen the same size.
		assert.ElementsMatch(t, func() []int {
			want := make([]int, n)
			for i := range want {
				want[i] = i
			}
			return want
		}(), seen)
	})
}
