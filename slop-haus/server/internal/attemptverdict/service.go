package attemptverdict

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// Service is what a person's verdict goes through, so every transport is
// refused alike when the caller is not a person.
type Service struct {
	store  *Store
	events *audit.Recorder
}

// NewService records changes to events; nil records nothing.
func NewService(store *Store, events *audit.Recorder) *Service {
	return &Service{store: store, events: events}
}

// Set gives the finished attempt the person's verdict, replacing their
// current one (which stays in its history). Any member may; an agent may not.
func (s *Service) Set(ctx context.Context, who *authn.User, ref ticket.AttemptRef, verdict, note string) (*ticket.AttemptVerdictBody, error) {
	if err := authn.RequirePerson(who); err != nil {
		return nil, err
	}
	note = strings.TrimSpace(note)
	switch {
	case !validVerdict(verdict):
		return nil, ErrInvalidVerdict
	case utf8.RuneCountInString(note) > MaxNoteRunes:
		return nil, ErrNoteTooLong
	}
	previous, err := s.store.Set(ctx, ref, verdict, note, who.ID)
	if err != nil {
		return nil, err
	}
	if s.events != nil {
		tenant, _ := db.TenantFrom(ctx)
		s.events.Record(ctx, audit.AttemptVerdictSet,
			"user_id", who.ID, "tenant", tenant, "ticket", ref.TicketID, "attempt", ref.Number,
			"verdict", verdict, "previous", previous, "has_note", note != "")
	}
	return s.Get(ctx, who, ref)
}

// Get is the attempt's current verdict and history, for a person.
func (s *Service) Get(ctx context.Context, who *authn.User, ref ticket.AttemptRef) (*ticket.AttemptVerdictBody, error) {
	if err := authn.RequirePerson(who); err != nil {
		return nil, err
	}
	got, err := s.store.AttemptVerdicts(ctx, []ticket.AttemptRef{ref})
	if err != nil {
		return nil, err
	}
	if v := got[ref]; v != nil {
		return v, nil
	}
	return nil, ErrNoVerdict
}
