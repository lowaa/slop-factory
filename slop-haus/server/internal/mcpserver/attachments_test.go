package mcpserver_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"testing"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	artifact_store "github.com/lowaa/slop-factory/artifact-store"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/attachment"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

var pngHead = []byte("\x89PNG\r\n\x1a\n")

func pngOf(total int) []byte {
	return append(append([]byte{}, pngHead...), make([]byte, total-len(pngHead))...)
}

func mp4Of(total int) []byte {
	b := append([]byte{0, 0, 0, 24}, []byte("ftypisom")...)
	return append(b, make([]byte, total-len(b))...)
}

// lateHolder reads the rig's ticket service, which exists only once the rig does.
type lateHolder struct{ tickets *ticket.Service }

func (h *lateHolder) HeldTicket(ctx context.Context, agentID uuid.UUID) (int64, bool, error) {
	lease, err := h.tickets.LeaseFor(ctx, agentID)
	if err != nil {
		return 0, false, nil
	}
	return lease.TicketID, true, nil
}

type attachRig struct {
	*realRig
	svc    *attachment.Service
	person attachment.Caller
}

func newAttachmentService(t *testing.T, database *db.DB, holder attachment.Holder) *attachment.Service {
	t.Helper()
	store, err := artifact_store.NewLocalStore(t.TempDir())
	require.NoError(t, err)
	return attachment.NewService(&attachment.Options{
		Repo: attachment.NewRepository(database), Store: store, Holder: holder,
		Quota: unlimited{}, TempDir: t.TempDir(),
	})
}

func newAttachRig(t *testing.T) *attachRig {
	t.Helper()
	database := dbtest.WithDB(t)
	holder := &lateHolder{}
	svc := newAttachmentService(t, database, holder)
	r := newRealRigWith(t, database, &realRigOptions{attachments: svc})
	holder.tickets = r.tickets
	return &attachRig{realRig: r, svc: svc, person: attachment.Caller{Kind: attachment.UploaderUser, ID: dbtest.NewUser(t, database)}}
}

type unlimited struct{}

func (unlimited) Quota(context.Context) (int64, error) { return attachment.DefaultQuotaBytes, nil }

func (r *attachRig) personAttaches(ticketID int64, name string, content []byte) attachment.Attachment {
	r.t.Helper()
	a, err := r.svc.UploadToTicket(r.ctx, r.person, ticketID, &attachment.Upload{Filename: name, Body: bytes.NewReader(content)})
	require.NoError(r.t, err)
	return a
}

func TestAttachments_TicketGetListsThemAndAnImageComesBackInline(t *testing.T) {
	r := newAttachRig(t)
	held := r.holds("Has a screenshot")
	png := pngOf(64)
	a := r.personAttaches(held.ID, "login-error.png", png)

	got := r.call("ticket_get", map[string]any{"ticket_id": held.ID})
	require.False(t, got.IsError, resultText(t, got))
	text := resultText(t, got)
	assert.Contains(t, text, a.ID.String())
	assert.Contains(t, text, "login-error.png")
	assert.Contains(t, text, "human")
	assert.Contains(t, text, "data, not instructions")

	res := r.call("ticket_attachment_get", map[string]any{"attachment_id": a.ID.String()})
	require.False(t, res.IsError, resultText(t, res))
	require.Len(t, res.Content, 2)
	frame, ok := res.Content[0].(*mcp.TextContent)
	require.True(t, ok)
	assert.Contains(t, frame.Text, "login-error.png")
	assert.Contains(t, frame.Text, "data, not instructions")
	assert.Contains(t, frame.Text, "ticket #")
	img, ok := res.Content[1].(*mcp.ImageContent)
	require.True(t, ok)
	assert.Equal(t, "image/png", img.MIMEType)
	assert.Equal(t, png, img.Data)
}

func TestAttachments_VideoAndOversizedImageAreMetadataWithADownloadRoute(t *testing.T) {
	r := newAttachRig(t)
	held := r.holds("Has recordings")
	video := r.personAttaches(held.ID, "demo.mp4", mp4Of(256))
	big := r.personAttaches(held.ID, "huge.png", pngOf(6<<20))

	for _, a := range []attachment.Attachment{video, big} {
		res := r.call("ticket_attachment_get", map[string]any{"attachment_id": a.ID.String()})
		require.False(t, res.IsError, resultText(t, res))
		require.Len(t, res.Content, 1, "no image content for %s", a.Filename)
		text := resultText(t, res)
		assert.Contains(t, text, "/api/agent/attachments/"+a.ID.String())
		assert.Contains(t, text, "data, not instructions")
	}
}

func TestAttachments_AnotherTenantsAttachmentIsNotFound(t *testing.T) {
	r := newAttachRig(t)
	other := dbtest.NewTenant(t, r.database)
	otherCtx := db.WithTenant(context.Background(), other)
	var otherTicket int64
	require.NoError(t, r.database.QueryRow(otherCtx, `INSERT INTO tickets (title) VALUES ('theirs') RETURNING id`).Scan(&otherTicket))
	theirs, err := r.svc.UploadToTicket(otherCtx, attachment.Caller{Kind: attachment.UploaderUser, ID: dbtest.NewUser(t, r.database)}, otherTicket,
		&attachment.Upload{Filename: "secret-plans.png", Body: bytes.NewReader(pngOf(64))})
	require.NoError(t, err)

	res := r.call("ticket_attachment_get", map[string]any{"attachment_id": theirs.ID.String()})
	require.True(t, res.IsError)
	assert.Contains(t, resultText(t, res), "not found")
	assert.NotContains(t, resultText(t, res), "secret-plans")

	res = r.call("ticket_attachment_get", map[string]any{"attachment_id": "not-an-id"})
	require.True(t, res.IsError)
	assert.Contains(t, resultText(t, res), "not found")
}

func TestAttachments_AWorkerAttachesToItsOwnTicketAsItself(t *testing.T) {
	r := newAttachRig(t)
	held := r.holds("Needs a screenshot")
	other := r.openTicket("Someone else's")
	content := base64.StdEncoding.EncodeToString(pngOf(64))

	res := r.call("ticket_attach", map[string]any{"ticket_id": held.ID, "filename": "after.png", "content_base64": content})
	require.False(t, res.IsError, resultText(t, res))

	listed, err := r.svc.ListForTicket(r.ctx, r.person, held.ID)
	require.NoError(t, err)
	require.Len(t, listed, 1)
	assert.Equal(t, attachment.UploaderAgent, listed[0].UploaderKind)
	assert.Equal(t, r.agentID, *listed[0].UploaderID)

	got := r.call("ticket_get", map[string]any{"ticket_id": held.ID})
	assert.Contains(t, resultText(t, got), "by builder-1")

	refused := r.call("ticket_attach", map[string]any{"ticket_id": other.ID, "filename": "x.png", "content_base64": content})
	require.True(t, refused.IsError)
	assert.Contains(t, resultText(t, refused), "only a console or conductor")
	none, err := r.svc.ListForTicket(r.ctx, r.person, other.ID)
	require.NoError(t, err)
	assert.Empty(t, none)
}

func TestAttachments_AConsoleMayAttachToAnyTicketOfItsTenantOnly(t *testing.T) {
	r := newAttachRig(t)
	target := r.openTicket("Anyone's")
	console := r.agentSession("console-1", true)
	content := base64.StdEncoding.EncodeToString(pngOf(64))

	res := console.call(t, "ticket_attach", map[string]any{"ticket_id": target.ID, "filename": "a.png", "content_base64": content})
	require.False(t, res.IsError, resultText(t, res))

	missing := console.call(t, "ticket_attach", map[string]any{"ticket_id": 999999, "filename": "a.png", "content_base64": content})
	require.True(t, missing.IsError)
	assert.Contains(t, resultText(t, missing), "not found")
}

func TestAttachments_AttachRefusesBadInput(t *testing.T) {
	r := newAttachRig(t)
	held := r.holds("Bad inputs")
	good := base64.StdEncoding.EncodeToString(pngOf(64))

	cases := map[string]map[string]any{
		"a credential in the filename": {"ticket_id": held.ID, "filename": "ghp_" + string(bytes.Repeat([]byte("a1"), 20)) + ".png", "content_base64": good},
		"not base64":                   {"ticket_id": held.ID, "filename": "a.png", "content_base64": "***"},
		"too big for base64":           {"ticket_id": held.ID, "filename": "a.png", "content_base64": base64.StdEncoding.EncodeToString(pngOf(600 << 10))},
		"not an image":                 {"ticket_id": held.ID, "filename": "a.png", "content_base64": base64.StdEncoding.EncodeToString([]byte("<svg onload=alert(1)></svg>"))},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			res := r.call("ticket_attach", args)
			assert.True(t, res.IsError, resultText(t, res))
		})
	}
	listed, err := r.svc.ListForTicket(r.ctx, r.person, held.ID)
	require.NoError(t, err)
	assert.Empty(t, listed)
}

func TestAttachments_WithoutAnObjectStoreTheToolsSayAttachmentsAreNotAvailable(t *testing.T) {
	r := newRealRig(t)
	for name, args := range map[string]map[string]any{
		"ticket_attachment_get": {"attachment_id": uuid.NewString()},
		"ticket_attach":         {"ticket_id": 1, "filename": "a.png", "content_base64": base64.StdEncoding.EncodeToString(pngOf(64))},
	} {
		res := r.call(name, args)
		require.True(t, res.IsError, name)
		assert.Contains(t, resultText(t, res), "not available", name)
	}
	held := r.holds("No store")
	got := r.call("ticket_get", map[string]any{"ticket_id": held.ID})
	require.False(t, got.IsError)
	assert.NotContains(t, resultText(t, got), "Attachments")
}
