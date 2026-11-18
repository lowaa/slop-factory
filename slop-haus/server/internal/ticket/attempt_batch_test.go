package ticket_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit/audittest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

func (f *fixture) addBatch(t *testing.T, id int64, versions ...*uuid.UUID) []*ticket.AttemptAdded {
	t.Helper()
	added, err := f.svc.AddAttempts(f.ctx, f.human, id, versions)
	require.NoError(t, err)
	return added
}

func TestAddAttempts_ThreeRowsGetConsecutiveNumbersInOneTransaction(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	tk := f.ticket(t, "Batch", "a/")
	v1 := f.namedVariantVersion(t, "fast", 1)

	added := f.addBatch(t, tk.ID, nil, &v1, nil)

	require.Len(t, added, 3)
	assert.Equal(t, []int{2, 3, 4}, []int{added[0].Number, added[1].Number, added[2].Number})
	require.NotNil(t, added[1].Variant)
	assert.Len(t, f.attemptRows(t, tk.ID), 4)
	assert.Equal(t, 5, f.add(t, tk.ID).Number, "the next number follows the batch")

	// One transaction means one xmin for every row the batch inserted.
	var distinct int
	require.NoError(t, f.database.QueryRow(f.ctx,
		`SELECT count(DISTINCT xmin::text) FROM attempts WHERE ticket_id = $1 AND number IN (2, 3, 4)`, tk.ID).Scan(&distinct))
	assert.Equal(t, 1, distinct, "the rows were inserted by one transaction")
}

func TestAddAttempts_AllOrNone(t *testing.T) {
	t.Parallel()
	archived := func(f *fixture) uuid.UUID {
		v := f.namedVariantVersion(t, "old", 1)
		f.archiveVariant(t, v)
		return v
	}
	t.Run("a batch past five live is refused, counting the batch", func(t *testing.T) {
		f := newFixture(t)
		tk := f.ticket(t, "Capped", "a/")
		f.add(t, tk.ID)
		f.add(t, tk.ID) // three live
		_, err := f.svc.AddAttempts(f.ctx, f.human, tk.ID, []*uuid.UUID{nil, nil, nil})
		require.ErrorIs(t, err, ticket.ErrAttemptCap)
		assert.Len(t, f.attemptRows(t, tk.ID), 3, "nothing was added")
		assert.Len(t, f.addBatch(t, tk.ID, nil, nil), 2, "two more fit exactly")
	})
	t.Run("one archived version refuses every row", func(t *testing.T) {
		f := newFixture(t)
		tk := f.ticket(t, "Bad row", "a/")
		bad := archived(f)
		// The bad row is last: the earlier rows must not survive it.
		_, err := f.svc.AddAttempts(f.ctx, f.human, tk.ID, []*uuid.UUID{nil, nil, &bad})
		require.ErrorIs(t, err, ticket.ErrInvalidVariant)
		assert.Len(t, f.attemptRows(t, tk.ID), 1)
		assert.Equal(t, 2, f.add(t, tk.ID).Number, "no number was taken")
	})
	t.Run("an unknown version refuses every row", func(t *testing.T) {
		f := newFixture(t)
		tk := f.ticket(t, "Unknown row", "a/")
		missing := uuid.New()
		_, err := f.svc.AddAttempts(f.ctx, f.human, tk.ID, []*uuid.UUID{nil, &missing})
		require.ErrorIs(t, err, ticket.ErrVariantNotFound)
		assert.Len(t, f.attemptRows(t, tk.ID), 1)
	})
	t.Run("empty and oversize batches are refused", func(t *testing.T) {
		f := newFixture(t)
		tk := f.ticket(t, "Sizes", "a/")
		_, err := f.svc.AddAttempts(f.ctx, f.human, tk.ID, nil)
		require.ErrorIs(t, err, ticket.ErrEmptyBatch)
		_, err = f.svc.AddAttempts(f.ctx, f.human, tk.ID, make([]*uuid.UUID, ticket.MaxLiveAttempts+1))
		require.ErrorIs(t, err, ticket.ErrBatchTooLarge)
		assert.Len(t, f.attemptRows(t, tk.ID), 1)
	})
	t.Run("an unknown or done ticket", func(t *testing.T) {
		f := newFixture(t)
		_, err := f.svc.AddAttempts(f.ctx, f.human, 999999, []*uuid.UUID{nil})
		require.ErrorIs(t, err, ticket.ErrNotFound)
		done := f.ticket(t, "Done", "a/")
		f.forceStatus(t, done.ID, ticket.StatusDone)
		_, err = f.svc.AddAttempts(f.ctx, f.human, done.ID, []*uuid.UUID{nil, nil})
		require.ErrorIs(t, err, ticket.ErrNoAttemptsOnStatus)
	})
}

func TestAddAttempts_ShadowAndDemotionRules(t *testing.T) {
	t.Parallel()
	t.Run("promoted and started: every row is a shadow", func(t *testing.T) {
		f := newFixture(t)
		tk := f.ticket(t, "Started", "a/")
		f.pinBase(t, tk.ID)
		require.NotNil(t, f.claim(t, f.agent(t, "worker")))

		added := f.addBatch(t, tk.ID, nil, nil)

		assert.True(t, added[0].Shadow)
		assert.True(t, added[1].Shadow)
		assert.Equal(t, map[int]attemptRow{
			1: {Status: "in_progress", Promoted: true},
			2: {Status: "queued", Shadow: true},
			3: {Status: "queued", Shadow: true},
		}, f.attemptRows(t, tk.ID))
	})
	t.Run("promoted and started, no pinned base: the whole batch is refused", func(t *testing.T) {
		f := newFixture(t)
		tk := f.ticket(t, "Unpinned", "a/")
		require.NotNil(t, f.claim(t, f.agent(t, "worker")))

		_, err := f.svc.AddAttempts(f.ctx, f.human, tk.ID, []*uuid.UUID{nil, nil})
		require.ErrorIs(t, err, ticket.ErrNoBase)
		assert.Len(t, f.attemptRows(t, tk.ID), 1)
	})
	t.Run("promoted and not started: demoted once, the rows are plain", func(t *testing.T) {
		f := newFixture(t)
		tk := f.ticket(t, "Unstarted", "a/")

		added := f.addBatch(t, tk.ID, nil, nil, nil)

		assert.Equal(t, 1, added[0].DemotedAttempt)
		assert.Zero(t, added[1].DemotedAttempt)
		assert.Zero(t, added[2].DemotedAttempt)
		for _, a := range added {
			assert.False(t, a.Shadow)
		}
		rows := f.attemptRows(t, tk.ID)
		assert.Len(t, rows, 4)
		for n, r := range rows {
			assert.False(t, r.Promoted, "attempt %d", n)
		}
	})
}

func TestAddAttempts_IsAuditedPerAttemptAfterCommit(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	var logged bytes.Buffer
	counter := audittest.NewCounter()
	svc := ticket.NewService(newStore(f.database), slog.DiscardHandler,
		ticket.WithEvents(audit.New(slog.NewJSONHandler(&logged, nil), counter)))
	tk := f.ticket(t, "Audited", "a/")

	_, err := svc.AddAttempts(f.ctx, f.human, tk.ID, []*uuid.UUID{nil, nil})
	require.NoError(t, err)
	assert.Equal(t, 2, counter.Count(audit.AttemptAdded))
	var numbers []int
	dec := json.NewDecoder(&logged)
	for dec.More() {
		var event map[string]any
		require.NoError(t, dec.Decode(&event))
		numbers = append(numbers, int(event["attempt"].(float64)))
	}
	assert.Equal(t, []int{2, 3}, numbers)

	missing := uuid.New()
	_, err = svc.AddAttempts(f.ctx, f.human, tk.ID, []*uuid.UUID{nil, &missing})
	require.Error(t, err)
	assert.Equal(t, 2, counter.Count(audit.AttemptAdded), "a refused batch records nothing")
}

func TestPostTicketAttempts_BatchHandler(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	tk := f.ticket(t, "Handled batch", "a/")
	path := fmt.Sprintf("/api/tickets/%d/attempts", tk.ID)
	v1 := f.namedVariantVersion(t, "fast", 1)

	t.Run("off answers 404 and inserts nothing", func(t *testing.T) {
		rec := send(t, f.attemptsMux(false, f.human), http.MethodPost, path, `{"attempts": [{}, {}]}`)
		assert.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
		assert.Len(t, f.attemptRows(t, tk.ID), 1)
	})

	mux := f.attemptsMux(true, f.human)
	t.Run("on adds every row and returns the ticket", func(t *testing.T) {
		rec := send(t, mux, http.MethodPost, path, fmt.Sprintf(`{"attempts": [{}, {"version_id": %q}]}`, v1))
		require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
		got := decodeTicket(t, rec.Body.Bytes())
		require.Len(t, got.Attempts, 3)
		require.NotNil(t, got.Attempts[2].Variant)
		assert.Equal(t, v1.String(), got.Attempts[2].Variant.VersionID)
	})
	t.Run("empty, oversize, mixed and malformed bodies are 422 and add nothing", func(t *testing.T) {
		before := len(f.attemptRows(t, tk.ID))
		for name, body := range map[string]string{
			"empty":    `{"attempts": []}`,
			"oversize": `{"attempts": [{},{},{},{},{},{}]}`,
			"mixed":    fmt.Sprintf(`{"version_id": %q, "attempts": [{}]}`, v1),
			"bad id":   `{"attempts": [{}, {"version_id": "nope"}]}`,
		} {
			rec := send(t, mux, http.MethodPost, path, body)
			assert.Equal(t, http.StatusUnprocessableEntity, rec.Code, "%s: %s", name, rec.Body.String())
		}
		assert.Len(t, f.attemptRows(t, tk.ID), before)
	})
	t.Run("past the cap is 409 and adds nothing", func(t *testing.T) {
		before := len(f.attemptRows(t, tk.ID))
		rec := send(t, mux, http.MethodPost, path, `{"attempts": [{}, {}, {}]}`)
		assert.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
		assert.Len(t, f.attemptRows(t, tk.ID), before)
	})
	t.Run("an archived row is 422 and an unknown one 404", func(t *testing.T) {
		archived := f.namedVariantVersion(t, "old", 1)
		f.archiveVariant(t, archived)
		assert.Equal(t, http.StatusUnprocessableEntity,
			send(t, mux, http.MethodPost, path, fmt.Sprintf(`{"attempts": [{"version_id": %q}]}`, archived)).Code)
		assert.Equal(t, http.StatusNotFound,
			send(t, mux, http.MethodPost, path, fmt.Sprintf(`{"attempts": [{"version_id": %q}]}`, uuid.New())).Code)
	})
}
