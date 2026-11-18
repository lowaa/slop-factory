package consent

import (
	"time"

	"github.com/google/uuid"
)

// Source is the surface a decision came through.
type Source string

const (
	SourceBanner   Source = "banner"
	SourceSettings Source = "settings"
	SourceSignUp   Source = "sign_up"
	// SourcePrompt is a processing-consent prompt, such as before a first
	// extraction.
	SourcePrompt Source = "prompt"
	// SourceGPC and SourceMigration are written by the server only: a
	// client can't claim them.
	SourceGPC       Source = "gpc"
	SourceMigration Source = "migration"
)

func (s Source) clientSettable() bool {
	switch s {
	case SourceBanner, SourceSettings, SourceSignUp, SourcePrompt:
		return true
	}
	return false
}

// NewEvent is a decision about to be appended to a user's record.
//
// It carries what proof of consent needs and nothing more: not an IP
// address, not a user agent.
type NewEvent struct {
	Purpose         string
	Granted         bool
	DocumentKind    string
	DocumentVersion string
	Source          Source
	DecidedAt       time.Time
	// SessionID is the sign-in session the decision was made in, as go-auth
	// reports it. Empty when there is none.
	SessionID string
	// MergedFromAnonymous marks a decision first made while signed out.
	MergedFromAnonymous bool
	// MergeToken is the anonymous cookie's token, for merged events. A
	// stored event holds its SHA-256 instead: the token is a one-time
	// credential for a cookie, and the record needs only to recognise it.
	MergeToken string
}

// Event is a stored decision. Events are never changed or removed.
type Event struct {
	ID     int64
	UserID uuid.UUID
	NewEvent
	RecordedAt time.Time
}
