package attachment_test

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-auth/audit/audittest"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/attachment"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
)

func TestUploadListOpenDelete(t *testing.T) {
	f := newFixture(t)
	r := f.newTenant()
	content := pngOf(100)
	a := f.mustUpload(r, "shot.png", content)

	assert.Equal(t, r.id, a.TenantID)
	assert.Equal(t, int64(100), a.SizeBytes)
	require.NotNil(t, a.UploaderID)
	assert.Equal(t, r.user.ID, *a.UploaderID)
	assert.Equal(t, attachment.UploaderUser, a.UploaderKind)
	require.NotNil(t, a.TicketID)
	assert.Equal(t, r.ticket, *a.TicketID)
	assert.Len(t, a.SHA256, 32)

	listed, err := f.svc.ListForTicket(r.ctx, r.user, r.ticket)
	require.NoError(t, err)
	require.Len(t, listed, 1)
	assert.Equal(t, a.ID, listed[0].ID)
	assert.Equal(t, content, f.read(r, &a))
	assert.Zero(t, f.count(r, `SELECT count(*) FROM attachment_reservations`), "the row clears its reservation")

	require.NoError(t, f.svc.Delete(r.ctx, r.user, a.ID))
	_, _, err = f.svc.Open(r.ctx, r.user, a.ID)
	assert.ErrorIs(t, err, attachment.ErrNotFound)
	assert.Equal(t, 1, f.count(r, `SELECT count(*) FROM attachment_delete_queue WHERE swept_at IS NULL`))
	assert.True(t, f.objectExists(r, a.StorageKey), "the object goes with the sweeper, not the request")
}

func TestUploadToAMissingTicketIs404(t *testing.T) {
	f := newFixture(t)
	r := f.newTenant()
	_, err := f.svc.UploadToTicket(r.ctx, r.user, r.ticket+1000, &attachment.Upload{Filename: "a.png", Body: bytes.NewReader(pngBytes)})
	assert.ErrorIs(t, err, attachment.ErrNotFound)
	_, err = f.svc.ListForTicket(r.ctx, r.user, r.ticket+1000)
	assert.ErrorIs(t, err, attachment.ErrNotFound)
}

// Tenant B reaches nothing of tenant A's, by id or by guessing its ticket.
func TestTenantsAreSeparate(t *testing.T) {
	f := newFixture(t)
	a, b := f.newTenant(), f.newTenant()
	mine := f.mustUpload(a, "a.png", pngBytes)

	_, _, err := f.svc.Open(b.ctx, b.user, mine.ID)
	assert.ErrorIs(t, err, attachment.ErrNotFound, "fetching another tenant's attachment")
	assert.ErrorIs(t, f.svc.Delete(b.ctx, b.user, mine.ID), attachment.ErrNotFound, "deleting it")
	assert.Equal(t, pngBytes, f.read(a, &mine), "and it is untouched")

	// B's ticket may carry the very number of A's: it lists B's own, none.
	listed, err := f.svc.ListForTicket(b.ctx, b.user, *mine.TicketID)
	require.NoError(t, err, "tenant B has a ticket of that number")
	assert.Empty(t, listed)

	// The object store is namespaced as well: the same key under B is nothing.
	_, err = f.store.Get(context.Background(), b.id, mine.StorageKey)
	assert.Error(t, err)

	// Even with A's tenant on the context, B's person is not A's.
	stranger := attachment.Caller{Kind: attachment.UploaderUser, ID: uuid.New()}
	conv := f.conversation(a, a.user.ID)
	_, err = f.svc.UploadStaged(a.ctx, stranger, conv, &attachment.Upload{Filename: "a.png", Body: bytes.NewReader(pngBytes)})
	assert.ErrorIs(t, err, attachment.ErrNotFound)
}

func TestQuotaIsPerTenant(t *testing.T) {
	f := newFixture(t)
	f.quota = 1000
	a, b := f.newTenant(), f.newTenant()

	first := f.mustUpload(a, "1.png", pngOf(600))
	_, err := f.upload(a, "2.png", pngOf(600))
	require.ErrorIs(t, err, attachment.ErrQuotaExceeded)
	assert.Equal(t, 1, f.count(a, `SELECT count(*) FROM attachments`))
	assert.Zero(t, f.count(a, `SELECT count(*) FROM attachment_reservations`))

	// Tenant A being full leaves B alone.
	f.mustUpload(b, "1.png", pngOf(600))

	// Deleting frees the room.
	require.NoError(t, f.svc.Delete(a.ctx, a.user, first.ID))
	f.mustUpload(a, "3.png", pngOf(900))
}

// Pending reservations count against the quota.
func TestReservationsHoldQuota(t *testing.T) {
	f := newFixture(t)
	r := f.newTenant()
	require.NoError(t, f.db.InTx(r.ctx, func(q db.Querier) error {
		return f.repo.Reserve(r.ctx, q, uuid.NewString()+".png", 800, 1000)
	}))
	f.quota = 1000
	_, err := f.upload(r, "a.png", pngOf(300))
	assert.ErrorIs(t, err, attachment.ErrQuotaExceeded)

	err = f.db.InTx(r.ctx, func(q db.Querier) error { return f.repo.Reserve(r.ctx, q, uuid.NewString()+".png", 300, 1000) })
	assert.ErrorIs(t, err, attachment.ErrQuotaExceeded)
}

// A row is only inserted under a live reservation, so the sweeper's expiry
// cannot leave a row pointing at an object it has queued.
func TestInsertNeedsAReservation(t *testing.T) {
	f := newFixture(t)
	r := f.newTenant()
	err := f.db.InTx(r.ctx, func(q db.Querier) error {
		_, err := f.repo.Insert(r.ctx, q, &attachment.Attachment{
			ID: uuid.New(), TicketID: &r.ticket, UploaderKind: attachment.UploaderUser, UploaderID: &r.user.ID,
			Filename: "a.png", ContentType: "image/png", SizeBytes: 1, SHA256: make([]byte, 32), StorageKey: "k.png"})
		return err
	})
	assert.Error(t, err)
}

func TestStagedUploadsAreTheConversationOwners(t *testing.T) {
	f := newFixture(t)
	r := f.newTenant()
	colleague := attachment.Caller{Kind: attachment.UploaderUser, ID: dbtestUser(t, f)}
	agent := attachment.Caller{Kind: attachment.UploaderAgent, ID: uuid.New()}
	conv := f.conversation(r, r.user.ID)

	a, err := f.svc.UploadStaged(r.ctx, r.user, conv, &attachment.Upload{Filename: "s.png", Body: bytes.NewReader(pngBytes)})
	require.NoError(t, err)
	assert.Nil(t, a.TicketID)
	require.NotNil(t, a.ConversationID)
	assert.Equal(t, conv, *a.ConversationID)

	for _, who := range []attachment.Caller{colleague, agent} {
		_, _, err := f.svc.Open(r.ctx, who, a.ID)
		assert.ErrorIs(t, err, attachment.ErrNotFound, "%v reads a staged upload", who.Kind)
		assert.ErrorIs(t, f.svc.Delete(r.ctx, who, a.ID), attachment.ErrNotFound)
		_, err = f.svc.UploadStaged(r.ctx, who, conv, &attachment.Upload{Filename: "x.png", Body: bytes.NewReader(pngBytes)})
		assert.ErrorIs(t, err, attachment.ErrNotFound)
	}
	assert.Equal(t, pngBytes, f.read(r, &a))
	require.NoError(t, f.svc.Delete(r.ctx, r.user, a.ID))

	_, err = f.svc.UploadStaged(r.ctx, r.user, uuid.New(), &attachment.Upload{Filename: "x.png", Body: bytes.NewReader(pngBytes)})
	assert.ErrorIs(t, err, attachment.ErrNotFound, "a conversation that does not exist")
}

func TestAnUnidentifiedCallerReachesNothing(t *testing.T) {
	f := newFixture(t)
	r := f.newTenant()
	a := f.mustUpload(r, "a.png", pngBytes)
	nobody := attachment.Caller{}
	_, _, err := f.svc.Open(r.ctx, nobody, a.ID)
	assert.ErrorIs(t, err, attachment.ErrNotFound)
	_, err = f.svc.ListForTicket(r.ctx, nobody, r.ticket)
	assert.ErrorIs(t, err, attachment.ErrNotFound)
	_, err = f.svc.UploadToTicket(r.ctx, nobody, r.ticket, &attachment.Upload{Filename: "a.png", Body: bytes.NewReader(pngBytes)})
	assert.ErrorIs(t, err, attachment.ErrNotFound)
}

func TestNoTenantOnTheContextFails(t *testing.T) {
	f := newFixture(t)
	r := f.newTenant()
	_, err := f.svc.UploadToTicket(context.Background(), r.user, r.ticket, &attachment.Upload{Filename: "a.png", Body: bytes.NewReader(pngBytes)})
	assert.ErrorIs(t, err, db.ErrNoTenant)
}

// An agent attaches to the tickets of its own tenant the same way a person does.
func TestAnAgentCanAttachToATicket(t *testing.T) {
	f := newFixture(t)
	r := f.newTenant()
	agent := attachment.Caller{Kind: attachment.UploaderAgent, ID: uuid.New()}
	_, err := f.db.Exec(r.ctx, `INSERT INTO agents (id, name) VALUES ($1, 'worker')`, agent.ID)
	require.NoError(t, err)

	a, err := f.svc.UploadToTicket(r.ctx, agent, r.ticket, &attachment.Upload{Filename: "a.png", Body: bytes.NewReader(pngBytes)})
	require.NoError(t, err)
	assert.Equal(t, attachment.UploaderAgent, a.UploaderKind)
	require.NotNil(t, a.UploaderID)
	assert.Equal(t, agent.ID, *a.UploaderID)
	assert.Equal(t, pngBytes, f.read(r, &a), "and the tenant's people read it")
}

// Reservations racing for the last of the quota are serialised by the usage
// row's lock: only as many as fit succeed.
func TestConcurrentReservationsCannotOvershootTheQuota(t *testing.T) {
	f := newFixture(t)
	r := f.newTenant()
	const attempts, size, quota = 8, 300, 1000
	var wg sync.WaitGroup
	results := make(chan error, attempts)
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- f.db.InTx(r.ctx, func(q db.Querier) error {
				return f.repo.Reserve(r.ctx, q, uuid.NewString()+".png", size, quota)
			})
		}()
	}
	wg.Wait()
	close(results)
	won := 0
	for err := range results {
		if err == nil {
			won++
			continue
		}
		assert.ErrorIs(t, err, attachment.ErrQuotaExceeded)
	}
	assert.Equal(t, quota/size, won)
}

// endless is a body that never ends, and counts what was read from it.
type endless struct {
	head []byte
	read int64
}

func (e *endless) Read(p []byte) (int, error) {
	n := 0
	for n < len(p) {
		if int(e.read) < len(e.head) {
			p[n] = e.head[e.read]
		} else {
			p[n] = 0
		}
		n++
		e.read++
	}
	return n, nil
}

// An upload is cut off at its limit: a body that never ends is not read past
// it, so nothing is buffered or spooled beyond what the limit allows.
func TestAnUploadIsCutOffAtItsLimit(t *testing.T) {
	f := newFixture(t)
	r := f.newTenant()
	for name, tc := range map[string]struct {
		head  []byte
		limit int64
	}{"image": {pngBytes, attachment.MaxImageBytes}, "video": {movBytes(64), attachment.MaxVideoBytes}} {
		body := &endless{head: tc.head}
		_, err := f.svc.UploadToTicket(r.ctx, r.user, r.ticket, &attachment.Upload{Filename: "x", Body: body})
		require.ErrorIs(t, err, attachment.ErrTooLarge, name)
		assert.LessOrEqual(t, body.read, tc.limit+4096, "%s: read well past the limit", name)
	}
	f.quota = 5000
	body := &endless{head: pngBytes}
	_, err := f.svc.UploadToTicket(r.ctx, r.user, r.ticket, &attachment.Upload{Filename: "x", Body: body})
	require.ErrorIs(t, err, attachment.ErrQuotaExceeded)
	assert.LessOrEqual(t, body.read, int64(5000+4096), "cut off at the quota's room")
}

// failingPut is a store whose writes fail.
type failingPut struct{ attachment.Store }

func (failingPut) Put(context.Context, uuid.UUID, string, io.Reader, int64, string) error {
	return io.ErrClosedPipe
}

// A failed Put leaves no row, keeps the reservation (the sweeper's record that
// something may have been stored), and the sweeper clears it after the grace.
func TestAFailedPutLeavesAReservationTheSweeperClears(t *testing.T) {
	f := newFixture(t)
	r := f.newTenant()
	svc := attachment.NewService(&attachment.Options{Repo: f.repo, Store: failingPut{f.store}, Quota: quotaFunc(func() int64 { return f.quota }), TempDir: t.TempDir()})
	_, err := svc.UploadToTicket(r.ctx, r.user, r.ticket, &attachment.Upload{Filename: "a.png", Body: bytes.NewReader(pngBytes)})
	require.Error(t, err)
	assert.Zero(t, f.count(r, `SELECT count(*) FROM attachments`))
	assert.Equal(t, 1, f.count(r, `SELECT count(*) FROM attachment_reservations`), "reserved before the Put")

	f.clock.Advance(attachment.DefaultUploadGrace + time.Hour)
	_, err = f.db.Exec(r.ctx, `UPDATE attachment_reservations SET reserved_at = $1`, f.clock.Now().Add(-attachment.DefaultUploadGrace-time.Minute))
	require.NoError(t, err)
	res, err := f.sweep.Sweep(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, res.Abandoned)
	assert.Zero(t, f.count(r, `SELECT count(*) FROM attachment_reservations`))
}

// The object is stored under a live reservation, which the insert clears.
func TestTheObjectIsStoredUnderAReservation(t *testing.T) {
	f := newFixture(t)
	r := f.newTenant()
	var reservedAtPut int
	svc := attachment.NewService(&attachment.Options{Repo: f.repo, Store: putHook{f.store, func() { reservedAtPut = f.count(r, `SELECT count(*) FROM attachment_reservations`) }},
		Quota: quotaFunc(func() int64 { return f.quota }), TempDir: t.TempDir()})
	_, err := svc.UploadToTicket(r.ctx, r.user, r.ticket, &attachment.Upload{Filename: "a.png", Body: bytes.NewReader(pngBytes)})
	require.NoError(t, err)
	assert.Equal(t, 1, reservedAtPut, "reserved (and committed) before the Put")
	assert.Zero(t, f.count(r, `SELECT count(*) FROM attachment_reservations`), "cleared by the row")
}

type putHook struct {
	attachment.Store
	hook func()
}

func (h putHook) Put(ctx context.Context, t uuid.UUID, key string, body io.Reader, size int64, ct string) error {
	h.hook()
	return h.Store.Put(ctx, t, key, body, size, ct)
}

func TestMetricsCountBytesPerTenant(t *testing.T) {
	m := attachment.NewMetrics()
	a, b := uuid.New(), uuid.New()
	m.Stored(a, 100)
	m.Stored(b, 7)
	m.Removed(a, 40)
	reg := prometheus.NewRegistry()
	require.NoError(t, reg.Register(m.Collectors()[0]))
	assert.Equal(t, 100.0, counter(t, reg, a, "stored"))
	assert.Equal(t, 40.0, counter(t, reg, a, "removed"))
	assert.Equal(t, 7.0, counter(t, reg, b, "stored"))
	assert.Zero(t, counter(t, reg, b, "removed"))
}

func counter(t *testing.T, reg *prometheus.Registry, tenant uuid.UUID, direction string) float64 {
	t.Helper()
	families, err := reg.Gather()
	require.NoError(t, err)
	for _, fam := range families {
		for _, m := range fam.GetMetric() {
			labels := map[string]string{}
			for _, l := range m.GetLabel() {
				labels[l.GetName()] = l.GetValue()
			}
			if labels["tenant"] == tenant.String() && labels["direction"] == direction {
				return m.GetCounter().GetValue()
			}
		}
	}
	return 0
}

func TestUploadAndDeleteAreAudited(t *testing.T) {
	f := newFixture(t)
	r := f.newTenant()
	counter := audittest.NewCounter()
	var logs bytes.Buffer
	svc := attachment.NewService(&attachment.Options{Repo: f.repo, Store: f.store, Quota: quotaFunc(func() int64 { return f.quota }), TempDir: t.TempDir(),
		Events: audit.New(slog.NewJSONHandler(&logs, nil), counter)})
	a, err := svc.UploadToTicket(r.ctx, r.user, r.ticket, &attachment.Upload{Filename: "secret-name.png", Body: bytes.NewReader(pngBytes)})
	require.NoError(t, err)
	assert.Equal(t, 1, counter.Count(audit.AttachmentUploaded))
	require.NoError(t, svc.Delete(r.ctx, r.user, a.ID))
	assert.Equal(t, 1, counter.Count(audit.AttachmentDeleted))
	assert.NotContains(t, logs.String(), "secret-name", "the filename is not recorded")
	assert.Contains(t, logs.String(), a.ID.String())
}

// The conductor a conversation runs on may read its staged files, for its host
// to put in the session's working copy; no other agent may, and it may not
// remove them.
func TestTheConversationsConductorMayReadItsStagedUploads(t *testing.T) {
	f := newFixture(t)
	r := f.newTenant()
	conv := f.conversation(r, r.user.ID)
	var conductorID uuid.UUID
	require.NoError(t, f.db.QueryRow(r.ctx, `SELECT conductor_id FROM conductor_conversations WHERE id = $1`, conv).Scan(&conductorID))
	a, err := f.svc.UploadStaged(r.ctx, r.user, conv, &attachment.Upload{Filename: "s.png", Body: bytes.NewReader(pngBytes)})
	require.NoError(t, err)

	host := attachment.Caller{Kind: attachment.UploaderAgent, ID: conductorID}
	_, body, err := f.svc.Open(r.ctx, host, a.ID)
	require.NoError(t, err)
	_ = body.Close()
	assert.ErrorIs(t, f.svc.Delete(r.ctx, host, a.ID), attachment.ErrNotFound)

	stranger := attachment.Caller{Kind: attachment.UploaderAgent, ID: uuid.New()}
	_, _, err = f.svc.Open(r.ctx, stranger, a.ID)
	assert.ErrorIs(t, err, attachment.ErrNotFound)
}
