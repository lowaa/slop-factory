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
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-clock"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/attachment"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
)

// Smallest byte strings the server's detection accepts.
var (
	pngBytes  = append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0}, 32)...)
	webmBytes = append([]byte("\x1a\x45\xdf\xa3"), bytes.Repeat([]byte{0}, 32)...)
	svgBytes  = []byte(`<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg" onload="alert(1)"></svg>`)
	htmlBytes = []byte(`<!DOCTYPE html><html><script>alert(1)</script></html>`)
)

func ftyp(brand string, total int) []byte {
	b := append([]byte{0, 0, 0, 24}, []byte("ftyp"+brand)...)
	b = append(b, make([]byte, 12)...)
	return append(b, bytes.Repeat([]byte{0}, total-len(b))...)
}

func movBytes(total int) []byte { return ftyp("qt  ", total) }
func mp4Bytes(total int) []byte { return ftyp("isom", total) }

// pngOf is a PNG of the given size.
func pngOf(total int) []byte {
	return append(append([]byte{}, pngBytes...), make([]byte, total-len(pngBytes))...)
}

// spyStore records what the sweeper deletes, and can be made to fail.
type spyStore struct {
	attachment.Store
	mu      sync.Mutex
	deleted []deletion
	failing bool
}

type deletion struct {
	tenant uuid.UUID
	key    string
}

func (s *spyStore) Delete(ctx context.Context, tenantID uuid.UUID, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failing {
		return io.ErrUnexpectedEOF
	}
	s.deleted = append(s.deleted, deletion{tenantID, key})
	return s.Store.Delete(ctx, tenantID, key)
}

func (s *spyStore) deletions() []deletion {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]deletion(nil), s.deleted...)
}

type fixture struct {
	t     *testing.T
	db    *db.DB
	store *spyStore
	repo  *attachment.Repository
	clock *clock.Fake
	quota int64
	svc   *attachment.Service
	sweep *attachment.Sweeper
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	database := dbtest.WithDB(t)
	local, err := attachment.NewLocalStore(t.TempDir())
	require.NoError(t, err)
	f := &fixture{t: t, db: database, store: &spyStore{Store: local}, repo: attachment.NewRepository(database),
		clock: clock.NewFake(time.Now()), quota: attachment.DefaultQuotaBytes}
	f.svc = attachment.NewService(&attachment.Options{Repo: f.repo, Store: f.store, Quota: quotaFunc(func() int64 { return f.quota }), TempDir: t.TempDir()})
	f.sweep, err = attachment.NewSweeper(f.repo, f.store, &attachment.SweeperConfig{Clock: f.clock}, slog.DiscardHandler)
	require.NoError(t, err)
	return f
}

type quotaFunc func() int64

func (q quotaFunc) Quota(context.Context) (int64, error) { return q(), nil }

// tenantRig is one tenant with a person and a ticket.
type tenantRig struct {
	id     uuid.UUID
	ctx    context.Context
	user   attachment.Caller
	ticket int64
}

func (f *fixture) newTenant() *tenantRig {
	f.t.Helper()
	id := dbtest.NewTenant(f.t, f.db)
	r := &tenantRig{id: id, ctx: db.WithTenant(context.Background(), id), user: attachment.Caller{Kind: attachment.UploaderUser, ID: dbtest.NewUser(f.t, f.db)}}
	require.NoError(f.t, f.db.QueryRow(r.ctx, `INSERT INTO tickets (title) VALUES ('t') RETURNING id`).Scan(&r.ticket))
	return r
}

// conversation makes a Remote Control conversation owned by owner.
func (f *fixture) conversation(r *tenantRig, owner uuid.UUID) uuid.UUID {
	f.t.Helper()
	agent, conv := uuid.New(), uuid.New()
	_, err := f.db.Exec(r.ctx, `INSERT INTO agents (id, name) VALUES ($1, $2)`, agent, "conductor-"+agent.String())
	require.NoError(f.t, err)
	_, err = f.db.Exec(r.ctx, `
		INSERT INTO conductor_conversations (id, conductor_id, owner_user_id, state)
		VALUES ($1, $2, $3, 'ready')`, conv, agent, owner)
	require.NoError(f.t, err)
	return conv
}

func (f *fixture) upload(r *tenantRig, name string, content []byte) (attachment.Attachment, error) {
	return f.svc.UploadToTicket(r.ctx, r.user, r.ticket, &attachment.Upload{Filename: name, Body: bytes.NewReader(content)})
}

func (f *fixture) mustUpload(r *tenantRig, name string, content []byte) attachment.Attachment {
	f.t.Helper()
	a, err := f.upload(r, name, content)
	require.NoError(f.t, err)
	return a
}

func (f *fixture) count(r *tenantRig, query string) (n int) {
	f.t.Helper()
	require.NoError(f.t, f.db.QueryRow(r.ctx, query).Scan(&n))
	return n
}

func (f *fixture) read(r *tenantRig, a *attachment.Attachment) []byte {
	f.t.Helper()
	got, body, err := f.svc.Open(r.ctx, r.user, a.ID)
	require.NoError(f.t, err)
	defer func() { _ = body.Close() }()
	require.Equal(f.t, a.ID, got.ID)
	b, err := io.ReadAll(body)
	require.NoError(f.t, err)
	return b
}

func (f *fixture) objectExists(r *tenantRig, key string) bool {
	body, err := f.store.Get(context.Background(), r.id, key)
	if err != nil {
		return false
	}
	_ = body.Close()
	return true
}

func dbtestUser(t *testing.T, f *fixture) uuid.UUID { t.Helper(); return dbtest.NewUser(t, f.db) }
