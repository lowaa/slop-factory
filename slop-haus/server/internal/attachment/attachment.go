// Package attachment stores the images and videos attached to tickets (and
// staged in a Remote Control conversation until they are), tenant-separated in
// layers: the object store's per-tenant folder, row-level security on every
// table, the tenant always taken from the caller's credential, and a quota.
package attachment

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

// UploaderKind says what made an upload.
type UploaderKind string

const (
	UploaderUser  UploaderKind = "user"
	UploaderAgent UploaderKind = "agent"
)

// Caller is who is acting. Its id comes from the session or agent token,
// never from a request.
type Caller struct {
	Kind UploaderKind
	ID   uuid.UUID
}

func (c Caller) valid() bool {
	return (c.Kind == UploaderUser || c.Kind == UploaderAgent) && c.ID != uuid.Nil
}

// Attachment is a stored file's metadata.
type Attachment struct {
	ID       uuid.UUID
	TenantID uuid.UUID
	// TicketID is nil while the attachment is staged in a conversation.
	TicketID       *int64
	ConversationID *uuid.UUID
	UploaderKind   UploaderKind
	// UploaderID is nil for a person whose account was deleted.
	UploaderID *uuid.UUID
	// Filename is the client's, for display only.
	Filename    string
	ContentType string
	SizeBytes   int64
	SHA256      []byte
	StorageKey  string
	CreatedAt   time.Time
}

// Per-file limits.
const (
	MaxImageBytes int64 = 10 << 20
	MaxVideoBytes int64 = 100 << 20
	// DefaultQuotaBytes is a tenant's quota until its setting says otherwise.
	DefaultQuotaBytes int64 = 5 << 30
)

var (
	// ErrNotFound answers an attachment, ticket or conversation that does not
	// exist or belongs to another tenant (or, for a staged upload, another
	// person): the two are not told apart.
	ErrNotFound = errors.New("attachment: not found")
	// ErrUnsupportedType is a file whose bytes are not an allowed image or video.
	ErrUnsupportedType = errors.New("attachment: unsupported file type")
	ErrEmpty           = errors.New("attachment: the file is empty")
	ErrTooLarge        = errors.New("attachment: file too large")
	ErrQuotaExceeded   = errors.New("attachment: the tenant's attachment quota is used up")
	// ErrUnavailable means no object store is configured.
	ErrUnavailable  = errors.New("attachment: attachments are not configured")
	ErrNoFile       = errors.New("attachment: a multipart \"file\" field is required")
	ErrNotMultipart = errors.New("attachment: a multipart/form-data body is required")
)
