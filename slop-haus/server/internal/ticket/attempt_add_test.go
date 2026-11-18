package ticket_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-auth/authn/authntest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit/audittest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

func (f *fixture) add(t *testing.T, id int64) *ticket.AttemptAdded {
	t.Helper()
	added, err := f.svc.AddAttempt(f.ctx, f.human, id, nil)
	require.NoError(t, err)
	return added
}

const pinnedBase = "1111111111111111111111111111111111111111"

// pinBase gives the ticket the base commit a first claim pins, without a
// code host: a shadow is only added to a ticket that has one.
func (f *fixture) pinBase(t *testing.T, id int64) {
	t.Helper()
	_, err := f.database.Exec(f.ctx, `UPDATE tickets SET base_commit = $2 WHERE id = $1`, id, pinnedBase)
	require.NoError(t, err)
}

type attemptRow struct {
	Status   string
	Promoted bool
	Shadow   bool
}

func (f *fixture) attemptRows(t *testing.T, id int64) map[int]attemptRow {
	t.Helper()
	rows, err := f.database.Query(f.ctx,
		`SELECT number, status, promoted_at IS NOT NULL, shadow FROM attempts WHERE ticket_id = $1`, id)
	require.NoError(t, err)
	defer rows.Close()
	out := map[int]attemptRow{}
	for rows.Next() {
		var n int
		var r attemptRow
		require.NoError(t, rows.Scan(&n, &r.Status, &r.Promoted, &r.Shadow))
		out[n] = r
	}
	require.NoError(t, rows.Err())
	return out
}

// awaitLockWaiter returns once some session waits on an advisory lock: proof
// that a write is queued behind the claim lock rather than racing past it.
func (f *fixture) awaitLockWaiter(t *testing.T) {
	t.Helper()
	require.Eventually(t, func() bool {
		var waiting int
		require.NoError(t, f.database.QueryRow(f.ctx,
			`SELECT count(*) FROM pg_locks WHERE locktype = 'advisory' AND NOT granted`).Scan(&waiting))
		return waiting > 0
	}, 30*time.Second, 10*time.Millisecond, "a write waits on the claim lock")
}

// Two adds that reach the number together are serialised by the claim lock:
// the first is held inside its transaction until the second is seen waiting
// on the lock, so without it both would read 2 and one would fail.
func TestAddAttempt_ConcurrentAddsGetDistinctNumbers(t *testing.T) {
	f := newFixture(t)
	tk := f.ticket(t, "Numbered", "a/")

	inside, proceed := make(chan struct{}), make(chan struct{})
	var once, unblock sync.Once
	release := func() { unblock.Do(func() { close(proceed) }) }
	defer release()
	defer ticket.SetAddBarrier(func() {
		once.Do(func() { close(inside) })
		<-proceed
	})()

	numbers := make(chan int, 2)
	var wg sync.WaitGroup
	add := func() {
		defer wg.Done()
		added, err := f.svc.AddAttempt(f.ctx, f.human, tk.ID, nil)
		if assert.NoError(t, err) {
			numbers <- added.Number
		}
	}
	wg.Add(2)
	go add()
	<-inside
	go add()
	f.awaitLockWaiter(t)
	release()
	wg.Wait()
	close(numbers)

	var got []int
	for n := range numbers {
		got = append(got, n)
	}
	assert.ElementsMatch(t, []int{2, 3}, got)
}

func TestAddAttempt_NumbersAreNeverReused(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	tk := f.ticket(t, "Numbered", "a/")
	require.Equal(t, 2, f.add(t, tk.ID).Number)
	require.Equal(t, 3, f.add(t, tk.ID).Number)
	_, err := f.database.Exec(f.ctx, `DELETE FROM attempts WHERE ticket_id = $1 AND number = 3`, tk.ID)
	require.NoError(t, err)

	assert.Equal(t, 4, f.add(t, tk.ID).Number, "3 was deleted and is not handed out again")
}

func TestAddAttempt_FiveLiveAttemptsRefuseASixth(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	tk := f.ticket(t, "Capped", "a/")
	for i := 0; i < ticket.MaxLiveAttempts-1; i++ {
		f.add(t, tk.ID)
	}
	_, err := f.svc.AddAttempt(f.ctx, f.human, tk.ID, nil)
	require.ErrorIs(t, err, ticket.ErrAttemptCap)
	assert.Len(t, f.attemptRows(t, tk.ID), ticket.MaxLiveAttempts, "nothing was inserted")

	// Finished attempts do not count: review is awaiting a choice, closed and
	// done are over. in_progress and discussion do.
	f.setAttemptStatus(t, tk.ID, 2, "review")
	f.setAttemptStatus(t, tk.ID, 3, "closed")
	f.setAttemptStatus(t, tk.ID, 4, "done")
	f.add(t, tk.ID)
	f.add(t, tk.ID)
	f.add(t, tk.ID)
	_, err = f.svc.AddAttempt(f.ctx, f.human, tk.ID, nil)
	require.ErrorIs(t, err, ticket.ErrAttemptCap, "five live again")

	f.setAttemptStatus(t, tk.ID, 1, "in_progress")
	f.setAttemptStatus(t, tk.ID, 5, "discussion")
	_, err = f.svc.AddAttempt(f.ctx, f.human, tk.ID, nil)
	require.ErrorIs(t, err, ticket.ErrAttemptCap, "in_progress and discussion are live")
}

func TestAddAttempt_ByTicketStatus(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		status  ticket.Status
		allowed bool
		after   ticket.Status
	}{
		{ticket.StatusPreparing, true, ticket.StatusPreparing},
		{ticket.StatusParked, true, ticket.StatusParked},
		{ticket.StatusOpen, true, ticket.StatusOpen},
		{ticket.StatusBlocked, true, ticket.StatusBlocked},
		{ticket.StatusInProgress, true, ticket.StatusInProgress},
		{ticket.StatusDiscussion, true, ticket.StatusDiscussion},
		{ticket.StatusReview, true, ticket.StatusReview},
		{ticket.StatusDone, false, ticket.StatusDone},
		{ticket.StatusCancelled, false, ticket.StatusCancelled},
	} {
		t.Run(string(tc.status), func(t *testing.T) {
			f := newFixture(t)
			tk := f.ticket(t, "Status", "a/")
			f.forceStatus(t, tk.ID, tc.status)
			switch tc.status {
			case ticket.StatusBlocked:
				// Blocked is derived: an unfinished dependency is what holds it.
				dep := f.ticket(t, "Dependency", "b/")
				_, err := f.database.Exec(f.ctx,
					`INSERT INTO ticket_deps (ticket_id, depends_on_ticket_id) VALUES ($1, $2)`, tk.ID, dep.ID)
				require.NoError(t, err)
			// The ticket's status follows its attempts here, so attempt 1 is
			// where the status says it is.
			case ticket.StatusInProgress, ticket.StatusDiscussion, ticket.StatusReview:
				f.setAttemptStatus(t, tk.ID, 1, string(tc.status))
			}

			_, err := f.svc.AddAttempt(f.ctx, f.human, tk.ID, nil)

			if tc.allowed {
				require.NoError(t, err)
				assert.Len(t, f.attemptRows(t, tk.ID), 2)
			} else {
				require.ErrorIs(t, err, ticket.ErrNoAttemptsOnStatus)
				assert.Len(t, f.attemptRows(t, tk.ID), 1)
			}
			assert.Equal(t, tc.after, f.status(t, tk.ID))
		})
	}
}

func TestAddAttempt_ANewAttemptOnAnUnknownTicketIsNotFound(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	_, err := f.svc.AddAttempt(f.ctx, f.human, 999999, nil)
	require.ErrorIs(t, err, ticket.ErrNotFound)
}

func TestAddAttempt_ShadowRule(t *testing.T) {
	t.Parallel()
	t.Run("promoted and started: a shadow, and the promotion stays", func(t *testing.T) {
		f := newFixture(t)
		tk := f.ticket(t, "Started", "a/")
		f.pinBase(t, tk.ID)
		agent := f.agent(t, "worker")
		require.NotNil(t, f.claim(t, agent))

		added := f.add(t, tk.ID)

		assert.True(t, added.Shadow)
		assert.Zero(t, added.DemotedAttempt)
		assert.Equal(t, map[int]attemptRow{
			1: {Status: "in_progress", Promoted: true},
			2: {Status: "queued", Shadow: true},
		}, f.attemptRows(t, tk.ID))
		assert.Equal(t, ticket.StatusInProgress, f.status(t, tk.ID))
	})

	t.Run("started once, since released: still a shadow", func(t *testing.T) {
		f := newFixture(t)
		tk := f.ticket(t, "Started and released", "a/")
		f.pinBase(t, tk.ID)
		agent := f.agent(t, "worker")
		f.claim(t, agent)
		f.release(t, agent)

		assert.True(t, f.add(t, tk.ID).Shadow, "started_revision outlives the lease")
	})

	// Leases are pruned after LEASE_RETENTION, so whether an attempt started
	// is read from started_revision, never from lease history.
	t.Run("started, its leases pruned: still a shadow", func(t *testing.T) {
		f := newFixture(t)
		tk := f.ticket(t, "Backfilled", "a/")
		f.pinBase(t, tk.ID)
		agent := f.agent(t, "worker")
		f.claim(t, agent)
		f.release(t, agent)
		_, err := f.database.Exec(f.ctx, `DELETE FROM leases WHERE ticket_id = $1`, tk.ID)
		require.NoError(t, err)

		assert.True(t, f.add(t, tk.ID).Shadow)
	})

	t.Run("promoted and started, no pinned base: refused, nothing inserted", func(t *testing.T) {
		f := newFixture(t)
		tk := f.ticket(t, "Unpinned", "a/")
		require.NotNil(t, f.claim(t, f.agent(t, "worker")))

		_, err := f.svc.AddAttempt(f.ctx, f.human, tk.ID, nil)

		require.ErrorIs(t, err, ticket.ErrNoBase)
		assert.Len(t, f.attemptRows(t, tk.ID), 1)
		f.pinBase(t, tk.ID)
		assert.True(t, f.add(t, tk.ID).Shadow, "allowed once the base is pinned")
	})

	t.Run("promoted and not started, no pinned base: still demotes, no shadow is made", func(t *testing.T) {
		f := newFixture(t)
		tk := f.ticket(t, "Idle unpinned", "a/")

		added := f.add(t, tk.ID)

		assert.False(t, added.Shadow)
		assert.Equal(t, 1, added.DemotedAttempt)
	})

	t.Run("promoted and not started: it loses the promotion", func(t *testing.T) {
		f := newFixture(t)
		tk := f.ticket(t, "Idle", "a/")
		_, err := f.database.Exec(f.ctx, `UPDATE attempts SET promoted_by_user_id = $2 WHERE ticket_id = $1`, tk.ID, f.human)
		require.NoError(t, err)

		added := f.add(t, tk.ID)

		assert.False(t, added.Shadow)
		assert.Equal(t, 1, added.DemotedAttempt)
		assert.Equal(t, map[int]attemptRow{
			1: {Status: "queued"},
			2: {Status: "queued"},
		}, f.attemptRows(t, tk.ID))
		var by *uuid.UUID
		require.NoError(t, f.database.QueryRow(f.ctx,
			`SELECT promoted_by_user_id FROM attempts WHERE ticket_id = $1 AND number = 1`, tk.ID).Scan(&by))
		assert.Nil(t, by, "the promoting person goes with it")
		assert.Equal(t, ticket.StatusOpen, f.status(t, tk.ID))
	})

	t.Run("none promoted: a normal, unpromoted attempt", func(t *testing.T) {
		f := newFixture(t)
		tk := f.ticket(t, "Unpromoted", "a/")
		f.unpromote(t, tk.ID)

		added := f.add(t, tk.ID)

		assert.False(t, added.Shadow)
		assert.Zero(t, added.DemotedAttempt)
		assert.Equal(t, map[int]attemptRow{
			1: {Status: "queued"},
			2: {Status: "queued"},
		}, f.attemptRows(t, tk.ID))
	})
}

// A claim and an add of one ticket are serialised: the add waits for the
// claim, and so sees attempt 1 started and makes a shadow. Without the lock
// it would read attempt 1 as unstarted and take its promotion away.
func TestAddAttempt_RacingAClaimWaitsForIt(t *testing.T) {
	f := newFixture(t)
	tk := f.ticket(t, "Racing", "a/")
	f.pinBase(t, tk.ID)
	agent := f.agent(t, "worker")

	inside, proceed := make(chan struct{}), make(chan struct{})
	var once, unblock sync.Once
	release := func() { unblock.Do(func() { close(proceed) }) }
	defer release()
	defer ticket.SetClaimBarrier(func() {
		once.Do(func() { close(inside) })
		<-proceed
	})()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		result, err := f.svc.Claim(f.ctx, agent, nil)
		if assert.NoError(t, err) {
			assert.NotNil(t, result.Ticket)
		}
	}()
	<-inside
	var added *ticket.AttemptAdded
	go func() {
		defer wg.Done()
		var err error
		added, err = f.svc.AddAttempt(f.ctx, f.human, tk.ID, nil)
		assert.NoError(t, err)
	}()
	f.awaitLockWaiter(t)
	release()
	wg.Wait()

	require.NotNil(t, added)
	assert.True(t, added.Shadow, "the claim started attempt 1 first")
	assert.Equal(t, map[int]attemptRow{
		1: {Status: "in_progress", Promoted: true},
		2: {Status: "queued", Shadow: true},
	}, f.attemptRows(t, tk.ID))
}

// A queued shadow joins a ticket in review without taking it out of review,
// and can be claimed, while attempt 1's scope stays held.
func TestAddAttempt_AShadowOnATicketInReviewLeavesItInReview(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a1, a2, other := f.agent(t, "first"), f.agent(t, "second"), f.agent(t, "other")
	tk := f.gated(t, "Gated", "a/")
	f.pinBase(t, tk.ID)
	f.claim(t, a1)
	require.NoError(t, f.svc.AcquireIntegrationLock(f.ctx, a1))
	f.review(t, a1)
	rival := f.ticket(t, "Rival", "a/")

	added := f.add(t, tk.ID)

	require.True(t, added.Shadow)
	assert.Equal(t, ticket.StatusReview, f.status(t, tk.ID))
	claimed := f.claim(t, a2)
	require.NotNil(t, claimed, "the shadow can be claimed")
	assert.Equal(t, tk.ID, claimed.ID)
	assert.Equal(t, 2, f.leasedAttempt(t, a2))
	assert.Equal(t, ticket.StatusReview, f.status(t, tk.ID))
	assert.Nil(t, f.claim(t, other), "attempt 1's scope is held")
	assert.Equal(t, ticket.StatusOpen, f.status(t, rival.ID))
}

func TestAddAttempt_NamesAVariantVersion(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	tk := f.ticket(t, "Variant", "a/")
	v1 := f.namedVariantVersion(t, "fast", 1)
	archived := f.namedVariantVersion(t, "slow", 1)
	f.archiveVariant(t, archived)

	_, err := f.svc.AddAttempt(f.ctx, f.human, tk.ID, &archived)
	require.ErrorIs(t, err, ticket.ErrInvalidVariant)
	missing := uuid.New()
	_, err = f.svc.AddAttempt(f.ctx, f.human, tk.ID, &missing)
	require.ErrorIs(t, err, ticket.ErrVariantNotFound)
	assert.Len(t, f.attemptRows(t, tk.ID), 1, "a refused add inserts nothing")

	added, err := f.svc.AddAttempt(f.ctx, f.human, tk.ID, &v1)
	require.NoError(t, err)
	require.NotNil(t, added.Variant)
	var routed *uuid.UUID
	require.NoError(t, f.database.QueryRow(f.ctx,
		`SELECT variant_version_id FROM attempts WHERE ticket_id = $1 AND number = $2`, tk.ID, added.Number).Scan(&routed))
	require.NotNil(t, routed)
	assert.Equal(t, v1, *routed)
}

func TestAddAttempt_IsAudited(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	var logged bytes.Buffer
	counter := audittest.NewCounter()
	svc := ticket.NewService(newStore(f.database), slog.DiscardHandler,
		ticket.WithEvents(audit.New(slog.NewJSONHandler(&logged, nil), counter)))
	tk := f.ticket(t, "Audited", "a/")
	f.pinBase(t, tk.ID)
	require.NotNil(t, f.claim(t, f.agent(t, "worker")))
	v1 := f.namedVariantVersion(t, "fast", 1)

	_, err := svc.AddAttempt(f.ctx, f.human, tk.ID, &v1)
	require.NoError(t, err)

	assert.Equal(t, 1, counter.Count(audit.AttemptAdded))
	var event map[string]any
	require.NoError(t, json.Unmarshal(logged.Bytes(), &event))
	assert.Equal(t, f.human.String(), event["user_id"])
	assert.EqualValues(t, tk.ID, event["ticket"])
	assert.EqualValues(t, 2, event["attempt"])
	assert.Equal(t, true, event["shadow"], "attempt 1 had started")
	assert.Contains(t, event["variant"], "fast v1")
	assert.EqualValues(t, 0, event["demoted_attempt"])

	_, err = svc.AddAttempt(f.ctx, f.human, 999999, nil)
	require.Error(t, err)
	assert.Equal(t, 1, counter.Count(audit.AttemptAdded), "a refused add is not recorded as one")
}

func (f *fixture) attemptsMux(enabled bool, user uuid.UUID) http.Handler {
	mux := http.NewServeMux()
	api := humago.New(mux, huma.DefaultConfig("test", "0.0.0"))
	api.UseMiddleware(authn.UserMiddleware(api, func(_ context.Context, _ string) (*authn.User, error) {
		return &authn.User{ID: user, TenantID: f.tenant}, nil
	}, authntest.Guard(), slog.New(slog.DiscardHandler)))
	ticket.RegisterRoutes(api, ticket.NewHandler(f.svc, slog.DiscardHandler, ticket.WithAttemptsAdd(enabled)))
	return mux
}

func TestPostTicketAttempts_Handler(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	tk := f.ticket(t, "Handled", "a/")
	path := fmt.Sprintf("/api/tickets/%d/attempts", tk.ID)

	t.Run("off answers 404 and inserts nothing", func(t *testing.T) {
		rec := send(t, f.attemptsMux(false, f.human), http.MethodPost, path, `{}`)
		assert.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
		assert.Equal(t, ticket.FeatureDisabledCode, errorCode(t, rec.Body.Bytes()))
		assert.Len(t, f.attemptRows(t, tk.ID), 1)
	})

	mux := f.attemptsMux(true, f.human)
	t.Run("on adds the attempt and returns the ticket", func(t *testing.T) {
		rec := send(t, mux, http.MethodPost, path, `{}`)
		require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
		got := decodeTicket(t, rec.Body.Bytes())
		require.Len(t, got.Attempts, 2)
		assert.Equal(t, 2, got.Attempts[1].Number)
	})
	t.Run("a bad version id is 422, an unknown version 404, an archived one 422", func(t *testing.T) {
		assert.Equal(t, http.StatusUnprocessableEntity, send(t, mux, http.MethodPost, path, `{"version_id": "nope"}`).Code)
		assert.Equal(t, http.StatusNotFound,
			send(t, mux, http.MethodPost, path, fmt.Sprintf(`{"version_id": %q}`, uuid.New())).Code)
		archived := f.namedVariantVersion(t, "old", 1)
		f.archiveVariant(t, archived)
		assert.Equal(t, http.StatusUnprocessableEntity,
			send(t, mux, http.MethodPost, path, fmt.Sprintf(`{"version_id": %q}`, archived)).Code)
	})
	t.Run("an unknown ticket is 404", func(t *testing.T) {
		rec := send(t, mux, http.MethodPost, "/api/tickets/999999/attempts", `{}`)
		assert.Equal(t, http.StatusNotFound, rec.Code)
		assert.Empty(t, errorCode(t, rec.Body.Bytes()), "a missing ticket is not a switched-off feature")
	})
	t.Run("the cap and a finished ticket are 409", func(t *testing.T) {
		for i := 0; i < ticket.MaxLiveAttempts; i++ {
			send(t, mux, http.MethodPost, path, `{}`)
		}
		assert.Equal(t, http.StatusConflict, send(t, mux, http.MethodPost, path, `{}`).Code)
		done := f.ticket(t, "Done", "b/")
		f.forceStatus(t, done.ID, ticket.StatusDone)
		assert.Equal(t, http.StatusConflict,
			send(t, mux, http.MethodPost, fmt.Sprintf("/api/tickets/%d/attempts", done.ID), `{}`).Code)
	})
	t.Run("a shadow without a pinned base is 409 with the reason", func(t *testing.T) {
		started := f.ticket(t, "Started unpinned", "c/")
		claimed, err := f.svc.Claim(f.ctx, f.agent(t, "handler worker"), &started.ID)
		require.NoError(t, err)
		require.NotNil(t, claimed.Ticket)
		rec := send(t, mux, http.MethodPost, fmt.Sprintf("/api/tickets/%d/attempts", started.ID), `{}`)
		assert.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
		assert.Contains(t, rec.Body.String(), "no pinned base commit")
	})
}

// errorCode reads the machine-readable "code" of an error body; empty when it has none.
func errorCode(t *testing.T, body []byte) string {
	t.Helper()
	var e struct {
		Code string `json:"code"`
	}
	require.NoError(t, json.Unmarshal(body, &e))
	return e.Code
}
