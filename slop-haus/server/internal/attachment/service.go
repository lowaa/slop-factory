package attachment

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
)

// Options are what a Service is built from.
type Options struct {
	Repo  *Repository
	Store Store
	Quota QuotaSource
	// Events and Observer may be nil.
	Events   *audit.Recorder
	Observer Observer
	// Holder says which ticket a worker holds; it decides where a worker may
	// attach (AgentUploadToTicket). Nil refuses every worker.
	Holder Holder
	// TempDir is where an upload is spooled while it is checked; the OS's
	// when empty.
	TempDir string
}

// Service holds the attachment rules and the policy over who may reach what.
// The tenant is always the context's, which authentication set from the
// caller's session or agent token; no method takes one from a request.
type Service struct {
	repo     *Repository
	store    Store
	quota    QuotaSource
	events   *audit.Recorder
	observer Observer
	holder   Holder
	tempDir  string
}

func NewService(o *Options) *Service {
	s := &Service{repo: o.Repo, store: o.Store, quota: o.Quota, events: o.Events, observer: o.Observer, holder: o.Holder, tempDir: o.TempDir}
	if s.observer == nil {
		s.observer = nopObserver{}
	}
	return s
}

// Upload is a file as the client sent it.
type Upload struct {
	// Filename is kept for display only.
	Filename string
	// Body is read once, as a stream.
	Body io.Reader
}

// target is what an upload is for: a ticket, or a conversation's staging.
type target struct {
	ticketID       *int64
	conversationID *uuid.UUID
}

// UploadToTicket attaches a file to a ticket of the caller's tenant.
func (s *Service) UploadToTicket(ctx context.Context, who Caller, ticketID int64, in *Upload) (Attachment, error) {
	return s.upload(ctx, who, target{ticketID: &ticketID}, in)
}

// UploadStaged stores a file for a Remote Control conversation, visible only
// to its owner until it is attached to a ticket.
func (s *Service) UploadStaged(ctx context.Context, who Caller, conversationID uuid.UUID, in *Upload) (Attachment, error) {
	return s.upload(ctx, who, target{conversationID: &conversationID}, in)
}

// authorizeTarget answers ErrNotFound for a ticket or conversation the caller
// cannot reach, as for one that does not exist.
func (s *Service) authorizeTarget(ctx context.Context, q db.Querier, who Caller, t target) error {
	if t.ticketID != nil {
		ok, err := s.repo.TicketExists(ctx, q, *t.ticketID)
		if err != nil {
			return err
		}
		if !ok {
			return ErrNotFound
		}
		return nil
	}
	return s.requireConversationOwner(ctx, q, who, *t.conversationID)
}

func (s *Service) requireConversationOwner(ctx context.Context, q db.Querier, who Caller, conversationID uuid.UUID) error {
	owner, err := s.repo.ConversationOwner(ctx, q, conversationID)
	if err != nil {
		return err
	}
	if who.Kind != UploaderUser || owner != who.ID {
		return ErrNotFound
	}
	return nil
}

// requireStagedReader allows a staged file's owner, and the conductor the
// conversation runs on: its host downloads the file into the session's working
// copy. No other agent may read it.
func (s *Service) requireStagedReader(ctx context.Context, q db.Querier, who Caller, conversationID uuid.UUID) error {
	if who.Kind == UploaderAgent {
		conductor, err := s.repo.ConversationConductor(ctx, q, conversationID)
		if err != nil {
			return err
		}
		if conductor != who.ID {
			return ErrNotFound
		}
		return nil
	}
	return s.requireConversationOwner(ctx, q, who, conversationID)
}

func (s *Service) upload(ctx context.Context, who Caller, t target, in *Upload) (Attachment, error) {
	if !who.valid() {
		return Attachment{}, ErrNotFound
	}
	tenantID, err := db.TenantFrom(ctx)
	if err != nil {
		return Attachment{}, err
	}
	quota, err := s.quota.Quota(ctx)
	if err != nil {
		return Attachment{}, err
	}
	var available int64
	err = s.repo.db.InTx(ctx, func(q db.Querier) error {
		if err := s.authorizeTarget(ctx, q, who, t); err != nil {
			return err
		}
		available, err = s.repo.Available(ctx, q, quota)
		return err
	})
	if err != nil {
		return Attachment{}, err
	}

	sp, err := s.spool(in.Body, available)
	if err != nil {
		return Attachment{}, err
	}
	defer sp.Close()

	id := uuid.New()
	key := id.String() + sp.ft.extension
	// Reserved and committed before the object is stored, so an upload that
	// dies after the Put leaves a reservation the sweeper finds.
	if err := s.repo.db.InTx(ctx, func(q db.Querier) error {
		return s.repo.Reserve(ctx, q, key, sp.size, quota)
	}); err != nil {
		return Attachment{}, err
	}
	if err := s.store.Put(ctx, tenantID, key, sp.file, sp.size, sp.ft.contentType); err != nil {
		return Attachment{}, fmt.Errorf("attachment: store: %w", err)
	}
	var stored Attachment
	err = s.repo.db.InTx(ctx, func(q db.Querier) error {
		// Checked again in the transaction that inserts: the ticket or the
		// conversation may have gone while the file was arriving.
		if err := s.authorizeTarget(ctx, q, who, t); err != nil {
			return err
		}
		stored, err = s.repo.Insert(ctx, q, &Attachment{
			ID: id, TicketID: t.ticketID, ConversationID: t.conversationID,
			UploaderKind: who.Kind, UploaderID: &who.ID, Filename: in.Filename,
			ContentType: sp.ft.contentType, SizeBytes: sp.size, SHA256: sp.sum, StorageKey: key,
		})
		return err
	})
	if err != nil {
		return Attachment{}, err
	}
	s.observer.Stored(tenantID, stored.SizeBytes)
	s.record(ctx, audit.AttachmentUploaded, who, &stored)
	return stored, nil
}

// spooled is an upload written to a temporary file, checked and hashed.
type spooled struct {
	file *os.File
	ft   fileType
	size int64
	sum  []byte
}

func (sp *spooled) Close() {
	_ = sp.file.Close()
	_ = os.Remove(sp.file.Name())
}

// spool writes body to a temporary file, never holding it in memory. The type
// comes from the first bytes, and the size and the quota are enforced as bytes
// arrive, so an oversized upload is cut off at its limit rather than received.
func (s *Service) spool(body io.Reader, available int64) (*spooled, error) {
	head := make([]byte, sniffLen)
	n, err := io.ReadFull(body, head)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return nil, readError(err)
	}
	head = head[:n]
	if n == 0 {
		return nil, ErrEmpty
	}
	ft, err := detect(head)
	if err != nil {
		return nil, err
	}

	over := ErrTooLarge
	limit := ft.maxBytes
	if available < limit {
		limit, over = available, ErrQuotaExceeded
	}
	if int64(n) > limit {
		return nil, over
	}
	f, err := os.CreateTemp(s.tempDir, "attachment-*")
	if err != nil {
		return nil, fmt.Errorf("attachment: spool: %w", err)
	}
	sp := &spooled{file: f, ft: ft}
	ok := false
	defer func() {
		if !ok {
			sp.Close()
		}
	}()
	digest := sha256.New()
	copied, err := io.Copy(io.MultiWriter(f, digest), io.MultiReader(bytes.NewReader(head), io.LimitReader(body, limit+1-int64(n))))
	if err != nil {
		return nil, readError(err)
	}
	if copied > limit {
		return nil, over
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("attachment: spool: %w", err)
	}
	sp.size, sp.sum = copied, digest.Sum(nil)
	ok = true
	return sp, nil
}

func readError(err error) error {
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) {
		return ErrTooLarge
	}
	return fmt.Errorf("attachment: read the upload: %w", err)
}

// ListForTicket is a ticket's attachments, oldest first.
func (s *Service) ListForTicket(ctx context.Context, who Caller, ticketID int64) ([]Attachment, error) {
	if !who.valid() {
		return nil, ErrNotFound
	}
	var out []Attachment
	err := s.repo.db.InReadTx(ctx, func(q db.Querier) error {
		if err := s.authorizeTarget(ctx, q, who, target{ticketID: &ticketID}); err != nil {
			return err
		}
		var err error
		out, err = s.repo.ListForTicket(ctx, q, ticketID)
		return err
	})
	return out, err
}

// visible loads an attachment the caller may reach. One in another tenant is
// invisible to the query (row-level security); a staged one is the owner's
// alone.
func (s *Service) visible(ctx context.Context, q db.Querier, who Caller, id uuid.UUID) (Attachment, error) {
	if !who.valid() {
		return Attachment{}, ErrNotFound
	}
	a, err := s.repo.Get(ctx, q, id)
	if err != nil {
		return Attachment{}, err
	}
	if a.TicketID == nil {
		if err := s.requireStagedReader(ctx, q, who, *a.ConversationID); err != nil {
			return Attachment{}, err
		}
	}
	return a, nil
}

// Meta is an attachment's metadata, for a caller that may reach it.
func (s *Service) Meta(ctx context.Context, who Caller, id uuid.UUID) (Attachment, error) {
	var a Attachment
	err := s.repo.db.InReadTx(ctx, func(q db.Querier) (err error) {
		a, err = s.visible(ctx, q, who, id)
		return err
	})
	return a, err
}

// Open returns an attachment's metadata and a reader for its content, which
// the caller must close.
func (s *Service) Open(ctx context.Context, who Caller, id uuid.UUID) (Attachment, io.ReadCloser, error) {
	var a Attachment
	err := s.repo.db.InReadTx(ctx, func(q db.Querier) (err error) {
		a, err = s.visible(ctx, q, who, id)
		return err
	})
	if err != nil {
		return Attachment{}, nil, err
	}
	body, err := s.store.Get(ctx, a.TenantID, a.StorageKey)
	if err != nil {
		return Attachment{}, nil, fmt.Errorf("attachment: read content: %w", err)
	}
	return a, body, nil
}

// Delete removes an attachment. Its object is deleted by the sweeper.
func (s *Service) Delete(ctx context.Context, who Caller, id uuid.UUID) error {
	var a Attachment
	err := s.repo.db.InTx(ctx, func(q db.Querier) (err error) {
		if a, err = s.visible(ctx, q, who, id); err != nil {
			return err
		}
		// A conductor may read its conversation's staged files, not remove them.
		if a.TicketID == nil && who.Kind != UploaderUser {
			return ErrNotFound
		}
		return s.repo.Delete(ctx, q, id)
	})
	if err != nil {
		return err
	}
	s.observer.Removed(a.TenantID, a.SizeBytes)
	s.record(ctx, audit.AttachmentDeleted, who, &a)
	return nil
}

func (s *Service) record(ctx context.Context, action audit.Action, who Caller, a *Attachment) {
	if s.events == nil {
		return
	}
	attrs := []any{"tenant", a.TenantID, "actor_kind", string(who.Kind), "actor_id", who.ID,
		"attachment_id", a.ID, "content_type", a.ContentType, "size_bytes", a.SizeBytes}
	if a.TicketID != nil {
		attrs = append(attrs, "ticket_id", *a.TicketID)
	}
	if a.ConversationID != nil {
		attrs = append(attrs, "conversation_id", *a.ConversationID)
	}
	s.events.Record(ctx, action, attrs...)
}
