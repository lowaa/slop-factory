package conductor_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/attachment"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/conductor"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
)

// memoryStore keeps attachment content in memory: these tests are about who
// may name a staged file, not about where its bytes live.
type memoryStore struct {
	mu      sync.Mutex
	objects map[string][]byte
}

func (m *memoryStore) Put(_ context.Context, tenantID uuid.UUID, key string, body io.Reader, _ int64, _ string) error {
	b, err := io.ReadAll(body)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.objects[tenantID.String()+key] = b
	return err
}

func (m *memoryStore) Get(_ context.Context, tenantID uuid.UUID, key string) (io.ReadCloser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return io.NopCloser(bytes.NewReader(m.objects[tenantID.String()+key])), nil
}

func (m *memoryStore) Delete(context.Context, uuid.UUID, string) error { return nil }

type unlimited struct{}

func (unlimited) Quota(context.Context) (int64, error) { return attachment.DefaultQuotaBytes, nil }

var pngBytes = append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 32)...)

func (f *fixture) attachments(t *testing.T) *attachment.Service {
	t.Helper()
	return attachment.NewService(&attachment.Options{
		Repo: attachment.NewRepository(f.database), Store: &memoryStore{objects: map[string][]byte{}},
		Quota: unlimited{}, TempDir: t.TempDir(),
	})
}

// stage is a file the person staged in the conversation, in ctx's tenant.
func stage(t *testing.T, ctx context.Context, svc *attachment.Service, who *authn.User, c *conductor.Conversation, name string) attachment.Attachment {
	t.Helper()
	a, err := svc.UploadStaged(ctx, attachment.Caller{Kind: attachment.UploaderUser, ID: who.ID}, c.ID,
		&attachment.Upload{Filename: name, Body: bytes.NewReader(pngBytes)})
	require.NoError(t, err)
	return a
}

// nextMessage is the message item the host is handed.
func (f *fixture) nextMessage(t *testing.T, h *host) conductor.WorkItem {
	t.Helper()
	res, err := f.svc.Poll(f.ctx, h.proof, &conductor.PollRequest{HostID: h.id})
	require.NoError(t, err)
	for _, it := range res.Items {
		if it.Kind == conductor.WorkMessage {
			return it
		}
	}
	require.FailNow(t, "no message in the queue")
	return conductor.WorkItem{}
}

func TestAMessageCarriesItsStagedAttachmentsToTheHost(t *testing.T) {
	f := newFixture(t)
	svc := f.attachments(t)
	owned := f.owned(t, f.owner, "c1")
	c := f.start(t, owned)
	first, second := stage(t, f.ctx, svc, f.owner, c, "one.png"), stage(t, f.ctx, svc, f.owner, c, "two.png")

	_, err := f.svc.Send(f.ctx, owned, c.ID, &conductor.SendRequest{Text: "look", AttachmentIDs: []uuid.UUID{second.ID, first.ID}})
	require.NoError(t, err)

	item := f.nextMessage(t, f.host(t, owned))
	assert.Equal(t, "look", item.Text)
	require.Len(t, item.Attachments, 2)
	assert.Equal(t, conductor.AttachmentRef{ID: second.ID, Filename: "two.png", ContentType: "image/png", SizeBytes: int64(len(pngBytes))}, item.Attachments[0])
	assert.Equal(t, first.ID, item.Attachments[1].ID)
}

func TestAMessageMayBeJustAttachments(t *testing.T) {
	f := newFixture(t)
	svc := f.attachments(t)
	owned := f.owned(t, f.owner, "c1")
	c := f.start(t, owned)
	a := stage(t, f.ctx, svc, f.owner, c, "shot.png")

	_, err := f.svc.Send(f.ctx, owned, c.ID, &conductor.SendRequest{AttachmentIDs: []uuid.UUID{a.ID}})
	require.NoError(t, err)
	_, err = f.svc.Send(f.ctx, owned, c.ID, &conductor.SendRequest{Text: " "})
	assert.ErrorIs(t, err, conductor.ErrInvalid)
}

func TestAMessageRefusesAttachmentsThatAreNotTheOwnersStagedFilesHere(t *testing.T) {
	f := newFixture(t)
	svc := f.attachments(t)
	owned := f.owned(t, f.owner, "c1")
	c := f.start(t, owned)
	// Another person's conversation, and the owner's own other one (starting it
	// ends the first, which a later send brings back).
	theirs := f.start(t, f.owned(t, f.other, "c2"))
	second := f.start(t, owned)

	// Another tenant's, staged in its own tenant.
	elsewhere := dbtest.NewTenant(t, f.database)
	otherCtx := db.WithTenant(context.Background(), elsewhere)
	stranger := f.member(t, elsewhere, "member")
	registered, err := f.conductors.Register(otherCtx, stranger, "c3")
	require.NoError(t, err)
	strangerOwned, err := f.conductors.AuthorizeOwner(otherCtx, stranger, registered.ID)
	require.NoError(t, err)
	strangersConversation, err := f.svc.Start(otherCtx, strangerOwned, &conductor.StartRequest{})
	require.NoError(t, err)

	cases := map[string]uuid.UUID{
		"another person's conversation": stage(t, f.ctx, svc, f.other, theirs, "theirs.png").ID,
		"another conversation":          stage(t, f.ctx, svc, f.owner, second, "elsewhere.png").ID,
		"another tenant":                stage(t, otherCtx, svc, stranger, strangersConversation, "foreign.png").ID,
		"unknown":                       uuid.New(),
	}
	for name, id := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := f.svc.Send(f.ctx, owned, c.ID, &conductor.SendRequest{Text: "x", AttachmentIDs: []uuid.UUID{id}})
			assert.ErrorIs(t, err, conductor.ErrInvalid)
		})
	}
	t.Run("listed twice", func(t *testing.T) {
		a := stage(t, f.ctx, svc, f.owner, c, "dup.png")
		_, err := f.svc.Send(f.ctx, owned, c.ID, &conductor.SendRequest{Text: "x", AttachmentIDs: []uuid.UUID{a.ID, a.ID}})
		assert.ErrorIs(t, err, conductor.ErrInvalid)
	})
	t.Run("too many", func(t *testing.T) {
		ids := make([]uuid.UUID, conductor.MaxMessageAttachments+1)
		for i := range ids {
			ids[i] = uuid.New()
		}
		_, err := f.svc.Send(f.ctx, owned, c.ID, &conductor.SendRequest{Text: "x", AttachmentIDs: ids})
		assert.ErrorIs(t, err, conductor.ErrInvalid)
	})
}

func TestAttachmentsTravelOverHTTPFromTheSendToThePoll(t *testing.T) {
	r := newRest(t)
	svc := r.attachments(t)
	started := r.start(t, map[string]any{})
	conv, err := uuid.Parse(started.ID)
	require.NoError(t, err)
	a := stage(t, r.ctx, svc, r.owner, &conductor.Conversation{ID: conv}, "shot.png")

	// A message of only a file; a blank one is still refused.
	sent := r.api.do("owner", http.MethodPost, r.base+"/"+started.ID+"/messages", map[string]any{"text": "", "attachment_ids": []string{a.ID.String()}})
	require.Equal(t, http.StatusCreated, sent.Code, sent.Body.String())
	assert.Equal(t, http.StatusUnprocessableEntity, r.api.do("owner", http.MethodPost, r.base+"/"+started.ID+"/messages",
		map[string]any{"text": "  ", "attachment_ids": []string{a.ID.String()}}).Code)
	assert.Equal(t, http.StatusUnprocessableEntity, r.api.do("owner", http.MethodPost, r.base+"/"+started.ID+"/messages",
		map[string]any{"text": "x", "attachment_ids": []string{uuid.NewString()}}).Code)

	poll := decode[pollJSON](t, r.api.do(r.hostToken, http.MethodPost, "/api/conductor/poll", map[string]any{"host_id": uuid.NewString()}))
	require.Len(t, poll.Items, 2)
	require.Equal(t, "message", poll.Items[1].Type)
	require.Len(t, poll.Items[1].Attachments, 1)
	got := poll.Items[1].Attachments[0]
	assert.Equal(t, a.ID.String(), got.ID)
	assert.Equal(t, "shot.png", got.Filename)
	assert.Equal(t, "image/png", got.ContentType)
	assert.Equal(t, int64(len(pngBytes)), got.SizeBytes)
}
