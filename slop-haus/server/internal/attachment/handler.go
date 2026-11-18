package attachment

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"mime"
	"mime/multipart"
	"net/http"
	"strconv"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/httperr"
)

// maxFormOverhead is what a multipart body may carry beyond the largest file.
const maxFormOverhead = 1 << 20

type Handler struct {
	svc    *Service
	logger *slog.Logger
}

// NewHandler's service may be nil, which answers every route 503: the server
// runs without an object store.
func NewHandler(svc *Service, logHandler slog.Handler) *Handler {
	return &Handler{svc: svc, logger: slog.New(logHandler)}
}

// Service is what the routes serve; nil when there is no object store.
func (h *Handler) Service() *Service { return h.svc }

var errTable = httperr.Table{
	Rules: []httperr.Rule{
		httperr.SignIn,
		{Err: ErrNotFound, Kind: httperr.NotFound, Message: "not found"},
		{Err: ErrNotPermitted, Kind: httperr.Forbidden},
		{Err: ErrSecretInFilename, Kind: httperr.Invalid},
		{Err: ErrUnsupportedType, Kind: httperr.Invalid, Message: "only PNG, JPEG, WebP, GIF, MP4, WebM and MOV files can be attached"},
		{Err: ErrEmpty, Kind: httperr.Invalid},
		{Err: ErrNoFile, Kind: httperr.Invalid},
		{Err: ErrNotMultipart, Kind: httperr.Invalid},
		{Err: ErrTooLarge, Kind: httperr.TooLarge, Message: "file too large: images may be 10 MB and videos 100 MB"},
		{Err: ErrQuotaExceeded, Kind: httperr.TooLarge, Message: "the attachment quota is used up"},
		{Err: ErrUnavailable, Kind: httperr.Unavailable, Message: "attachments are not available"},
	},
	Unexpected: "unexpected attachment error",
}

func (h *Handler) mapError(err error) error { return httperr.Map(h.logger, err, &errTable) }

// caller is the signed-in person; the id comes from the session.
func (h *Handler) caller(ctx context.Context) (Caller, error) {
	if h.svc == nil {
		return Caller{}, ErrUnavailable
	}
	u, err := authn.UserFromContext(ctx)
	if err != nil {
		return Caller{}, err
	}
	return Caller{Kind: UploaderUser, ID: u.ID}, nil
}

type AttachmentBody struct {
	ID             uuid.UUID  `json:"id"`
	TicketID       *int64     `json:"ticket_id"`
	ConversationID *uuid.UUID `json:"conversation_id"`
	Filename       string     `json:"filename"`
	ContentType    string     `json:"content_type"`
	SizeBytes      int64      `json:"size_bytes"`
	UploaderKind   string     `json:"uploader_kind" enum:"user,agent"`
	// UploaderID is null for a person whose account was deleted.
	UploaderID *uuid.UUID `json:"uploader_id"`
	CreatedAt  time.Time  `json:"created_at"`
}

func toBody(a *Attachment) AttachmentBody {
	return AttachmentBody{ID: a.ID, TicketID: a.TicketID, ConversationID: a.ConversationID, Filename: a.Filename,
		ContentType: a.ContentType, SizeBytes: a.SizeBytes, UploaderKind: string(a.UploaderKind), UploaderID: a.UploaderID, CreatedAt: a.CreatedAt}
}

type AttachmentResponse struct {
	Body AttachmentBody
}

type ListResponse struct {
	Body struct {
		Attachments []AttachmentBody `json:"attachments"`
	}
}

type TicketRequest struct {
	ID int64 `path:"id"`
}

type AttachmentRequest struct {
	AttachmentID uuid.UUID `path:"aid"`
	// Download asks for Content-Disposition: attachment rather than inline.
	Download bool `query:"download"`
}

// The upload is read as a stream from the request, not through huma's
// multipart decoding, which writes the whole body out before a handler runs
// and so cannot stop an oversized file as it arrives.
type requestKey struct{}

type rawRequest struct {
	r *http.Request
	w http.ResponseWriter
}

// WithRequest puts the raw request where the upload handlers can reach it,
// and caps its body.
func WithRequest(ctx huma.Context, next func(huma.Context)) {
	r, w := humago.Unwrap(ctx)
	r.Body = http.MaxBytesReader(w, r.Body, MaxVideoBytes+maxFormOverhead)
	next(huma.WithValue(ctx, requestKey{}, &rawRequest{r: r, w: w}))
}

// UploadRequest has no body field, so huma leaves the body unread.
type UploadRequest struct {
	ID int64 `path:"id"`
}

type StagedUploadRequest struct {
	ConversationID uuid.UUID `path:"conversation_id"`
}

// firstFile is the multipart body's "file" part, positioned for reading.
func firstFile(ctx context.Context) (*multipart.Part, error) {
	raw, _ := ctx.Value(requestKey{}).(*rawRequest)
	if raw == nil {
		return nil, errors.New("attachment: the upload route is mounted without WithRequest")
	}
	mr, err := raw.r.MultipartReader()
	if err != nil {
		return nil, ErrNotMultipart
	}
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			return nil, ErrNoFile
		}
		if err != nil {
			return nil, readError(err)
		}
		if part.FormName() == "file" && part.FileName() != "" {
			return part, nil
		}
	}
}

func (h *Handler) upload(ctx context.Context, who Caller, store func(*Upload) (Attachment, error)) (*AttachmentResponse, error) {
	part, err := firstFile(ctx)
	if err != nil {
		return nil, h.mapError(err)
	}
	name := part.FileName()
	if len(name) > 255 {
		name = name[:255]
	}
	a, err := store(&Upload{Filename: name, Body: part})
	if err != nil {
		return nil, h.mapError(err)
	}
	return &AttachmentResponse{Body: toBody(&a)}, nil
}

func (h *Handler) UploadToTicket(ctx context.Context, in *UploadRequest) (*AttachmentResponse, error) {
	who, err := h.caller(ctx)
	if err != nil {
		return nil, h.mapError(err)
	}
	return h.upload(ctx, who, func(up *Upload) (Attachment, error) {
		return h.svc.UploadToTicket(ctx, who, in.ID, up)
	})
}

func (h *Handler) UploadStaged(ctx context.Context, in *StagedUploadRequest) (*AttachmentResponse, error) {
	who, err := h.caller(ctx)
	if err != nil {
		return nil, h.mapError(err)
	}
	return h.upload(ctx, who, func(up *Upload) (Attachment, error) {
		return h.svc.UploadStaged(ctx, who, in.ConversationID, up)
	})
}

// AgentUploadToTicket is the agent-token route, for a session uploading a
// file from its working copy. The uploader recorded is the agent.
func (h *Handler) AgentUploadToTicket(ctx context.Context, in *UploadRequest) (*AttachmentResponse, error) {
	if h.svc == nil {
		return nil, h.mapError(ErrUnavailable)
	}
	who, err := authn.FromContext(ctx)
	if err != nil {
		return nil, h.mapError(err)
	}
	return h.upload(ctx, AgentCaller(who), func(up *Upload) (Attachment, error) {
		return h.svc.AgentUploadToTicket(ctx, who, in.ID, up)
	})
}

func (h *Handler) List(ctx context.Context, in *TicketRequest) (*ListResponse, error) {
	who, err := h.caller(ctx)
	if err != nil {
		return nil, h.mapError(err)
	}
	found, err := h.svc.ListForTicket(ctx, who, in.ID)
	if err != nil {
		return nil, h.mapError(err)
	}
	resp := &ListResponse{}
	resp.Body.Attachments = make([]AttachmentBody, len(found))
	for i := range found {
		resp.Body.Attachments[i] = toBody(&found[i])
	}
	return resp, nil
}

// Content streams the file from the store. The type was fixed from the bytes
// at upload and is only ever an allowlisted image or video, never SVG or HTML;
// nosniff and a sandbox keep a browser from treating it as anything else.
func (h *Handler) Content(ctx context.Context, in *AttachmentRequest) (*huma.StreamResponse, error) {
	who, err := h.caller(ctx)
	if err != nil {
		return nil, h.mapError(err)
	}
	return h.content(ctx, who, in)
}

// AgentContent is Content for an agent token: how a session downloads a video
// to pull frames from it in its own working copy.
func (h *Handler) AgentContent(ctx context.Context, in *AttachmentRequest) (*huma.StreamResponse, error) {
	if h.svc == nil {
		return nil, h.mapError(ErrUnavailable)
	}
	who, err := authn.FromContext(ctx)
	if err != nil {
		return nil, h.mapError(err)
	}
	return h.content(ctx, AgentCaller(who), in)
}

func (h *Handler) content(ctx context.Context, who Caller, in *AttachmentRequest) (*huma.StreamResponse, error) {
	a, body, err := h.svc.Open(ctx, who, in.AttachmentID)
	if err != nil {
		return nil, h.mapError(err)
	}
	disposition := "inline"
	if in.Download {
		disposition = "attachment"
	}
	return &huma.StreamResponse{Body: func(hctx huma.Context) {
		defer func() { _ = body.Close() }()
		hctx.SetHeader("Content-Type", a.ContentType)
		hctx.SetHeader("Content-Length", strconv.FormatInt(a.SizeBytes, 10))
		hctx.SetHeader("Content-Disposition", contentDisposition(disposition, a.Filename))
		hctx.SetHeader("X-Content-Type-Options", "nosniff")
		hctx.SetHeader("Content-Security-Policy", "sandbox")
		hctx.SetHeader("Cache-Control", "private, no-store")
		hctx.SetStatus(http.StatusOK)
		if _, err := io.Copy(hctx.BodyWriter(), body); err != nil {
			h.logger.Error("attachment: stream content", "err", err)
		}
	}}, nil
}

// contentDisposition always names its disposition, with the client's filename
// quoted or encoded by mime rather than pasted in.
func contentDisposition(disposition, filename string) string {
	if v := mime.FormatMediaType(disposition, map[string]string{"filename": filename}); v != "" {
		return v
	}
	return disposition
}

func (h *Handler) Delete(ctx context.Context, in *AttachmentRequest) (*struct{}, error) {
	who, err := h.caller(ctx)
	if err != nil {
		return nil, h.mapError(err)
	}
	if err := h.svc.Delete(ctx, who, in.AttachmentID); err != nil {
		return nil, h.mapError(err)
	}
	return nil, nil
}
