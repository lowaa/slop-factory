package attachment_test

import (
	"bytes"
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/attachment"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
)

func TestSweepDeletesTheObjectOfADeletedAttachmentOnce(t *testing.T) {
	f := newFixture(t)
	r := f.newTenant()
	keep := f.mustUpload(r, "keep.png", pngBytes)
	gone := f.mustUpload(r, "gone.png", pngBytes)
	require.NoError(t, f.svc.Delete(r.ctx, r.user, gone.ID))

	res, err := f.sweep.Sweep(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, res.Deleted)
	assert.False(t, f.objectExists(r, gone.StorageKey))
	assert.True(t, f.objectExists(r, keep.StorageKey))

	// Repeating changes nothing, and a swept row is not swept again.
	res, err = f.sweep.Sweep(context.Background())
	require.NoError(t, err)
	assert.Equal(t, attachment.SweepResult{}, res)
	assert.Len(t, f.store.deletions(), 1)
}

// The sweeper works one tenant at a time and cannot name another's object.
func TestSweepStaysWithinEachTenant(t *testing.T) {
	f := newFixture(t)
	a, b := f.newTenant(), f.newTenant()
	inA := f.mustUpload(a, "a.png", pngBytes)
	inB := f.mustUpload(b, "b.png", pngBytes)
	require.NoError(t, f.svc.Delete(a.ctx, a.user, inA.ID))

	_, err := f.sweep.Sweep(context.Background())
	require.NoError(t, err)

	assert.Equal(t, []deletion{{a.id, inA.StorageKey}}, f.store.deletions())
	assert.True(t, f.objectExists(b, inB.StorageKey), "tenant B's object is untouched")
	assert.Equal(t, 1, f.count(b, `SELECT count(*) FROM attachments`))

	// Even a queue row naming B's key, made in A's tenant, deletes under A's folder.
	require.NoError(t, f.db.InTx(a.ctx, func(q db.Querier) error {
		_, err := q.Exec(a.ctx, `INSERT INTO attachment_delete_queue (storage_key, reason) VALUES ($1, 'deleted')`, inB.StorageKey)
		return err
	}))
	_, err = f.sweep.Sweep(context.Background())
	require.NoError(t, err)
	assert.True(t, f.objectExists(b, inB.StorageKey))
}

func TestSweepRetriesAFailedDelete(t *testing.T) {
	f := newFixture(t)
	r := f.newTenant()
	a := f.mustUpload(r, "a.png", pngBytes)
	require.NoError(t, f.svc.Delete(r.ctx, r.user, a.ID))

	f.store.failing = true
	res, err := f.sweep.Sweep(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, res.Failed)
	assert.True(t, f.objectExists(r, a.StorageKey))
	var attempts int
	var lastErr *string
	require.NoError(t, f.db.QueryRow(r.ctx, `SELECT attempts, last_error FROM attachment_delete_queue`).Scan(&attempts, &lastErr))
	assert.Equal(t, 1, attempts)
	assert.NotNil(t, lastErr)

	f.store.failing = false
	res, err = f.sweep.Sweep(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, res.Deleted)
	assert.False(t, f.objectExists(r, a.StorageKey))
}

// An upload that died between its Put and its row leaves a reservation, and
// the object it names goes once the grace period has passed.
func TestSweepDeletesAnAbandonedUploadAfterTheGrace(t *testing.T) {
	f := newFixture(t)
	r := f.newTenant()
	key := uuid.NewString() + ".png"
	require.NoError(t, f.db.InTx(r.ctx, func(q db.Querier) error { return f.repo.Reserve(r.ctx, q, key, 10, 1000) }))
	require.NoError(t, f.store.Put(context.Background(), r.id, key, bytes.NewReader(pngBytes), int64(len(pngBytes)), "image/png"))
	// A reservation of an upload still arriving is aged by the database's clock.
	_, err := f.db.Exec(r.ctx, `UPDATE attachment_reservations SET reserved_at = $1`, f.clock.Now().Add(-time.Hour))
	require.NoError(t, err)

	res, err := f.sweep.Sweep(context.Background())
	require.NoError(t, err)
	assert.Zero(t, res.Abandoned, "within the grace period")
	assert.True(t, f.objectExists(r, key))

	f.clock.Advance(attachment.DefaultUploadGrace)
	res, err = f.sweep.Sweep(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, res.Abandoned)
	assert.Equal(t, 1, res.Deleted)
	assert.False(t, f.objectExists(r, key))
	assert.Zero(t, f.count(r, `SELECT count(*) FROM attachment_reservations`))
	assert.Zero(t, f.count(r, `SELECT count(*) FROM attachment_usage WHERE bytes <> 0`))
}

func TestSweepRefusesAGraceShorterThanAnUploadTakes(t *testing.T) {
	f := newFixture(t)
	_, err := attachment.NewSweeper(f.repo, f.store, &attachment.SweeperConfig{UploadGrace: time.Minute}, slog.DiscardHandler)
	assert.Error(t, err)
}

func TestDeletingATicketQueuesItsAttachments(t *testing.T) {
	f := newFixture(t)
	r := f.newTenant()
	a := f.mustUpload(r, "a.png", pngBytes)
	_, err := f.db.Exec(r.ctx, `DELETE FROM tickets WHERE id = $1`, r.ticket)
	require.NoError(t, err)
	assert.Zero(t, f.count(r, `SELECT count(*) FROM attachments`))
	assert.Zero(t, f.count(r, `SELECT bytes FROM attachment_usage`))

	_, err = f.sweep.Sweep(context.Background())
	require.NoError(t, err)
	assert.False(t, f.objectExists(r, a.StorageKey))
}

func TestSweepRemovesAnArchivedTenantsAttachments(t *testing.T) {
	f := newFixture(t)
	archived, live := f.newTenant(), f.newTenant()
	a := f.mustUpload(archived, "a.png", pngBytes)
	b := f.mustUpload(live, "b.png", pngBytes)
	dbtest.ArchiveTenant(t, f.db, archived.id)

	res, err := f.sweep.Sweep(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, res.Removed)
	assert.False(t, f.objectExists(archived, a.StorageKey))
	assert.Zero(t, f.count(archived, `SELECT count(*) FROM attachments`))
	assert.True(t, f.objectExists(live, b.StorageKey))
	assert.Equal(t, 1, f.count(live, `SELECT count(*) FROM attachments`))
}

func TestSweepRemovesTheStagedUploadsOfAPrunedConversation(t *testing.T) {
	f := newFixture(t)
	r := f.newTenant()
	kept, pruned := f.conversation(r, r.user.ID), f.conversation(r, r.user.ID)
	up := func(c uuid.UUID) attachment.Attachment {
		a, err := f.svc.UploadStaged(r.ctx, r.user, c, &attachment.Upload{Filename: "s.png", Body: bytes.NewReader(pngBytes)})
		require.NoError(t, err)
		return a
	}
	k, p := up(kept), up(pruned)
	attached := f.mustUpload(r, "t.png", pngBytes)
	_, err := f.db.Exec(r.ctx, `DELETE FROM conductor_conversations WHERE id = $1`, pruned)
	require.NoError(t, err)

	_, err = f.sweep.Sweep(context.Background())
	require.NoError(t, err)
	assert.False(t, f.objectExists(r, p.StorageKey))
	assert.True(t, f.objectExists(r, k.StorageKey))
	assert.True(t, f.objectExists(r, attached.StorageKey), "an attachment of a ticket does not depend on a conversation")
}

func TestSweepPrunesOldRecords(t *testing.T) {
	f := newFixture(t)
	r := f.newTenant()
	a := f.mustUpload(r, "a.png", pngBytes)
	require.NoError(t, f.svc.Delete(r.ctx, r.user, a.ID))
	_, err := f.sweep.Sweep(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, f.count(r, `SELECT count(*) FROM attachment_delete_queue`), "kept as the record of the erasure")

	f.clock.Advance(attachment.DefaultSweptRetention + time.Hour)
	res, err := f.sweep.Sweep(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, res.Pruned)
	assert.Zero(t, f.count(r, `SELECT count(*) FROM attachment_delete_queue`))
}
