// Package dsr is the operator's log of data subject requests: ones that
// arrive by email or a form and need a person to handle them. It records who
// asked for what, how their identity was checked, the deadline that follows
// from when the request arrived, and every change of state as an audit trail.
//
// Nothing in it is legal advice. The deadlines are configuration whose
// defaults are placeholders for a qualified person to confirm.
package dsr

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/mail"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

var (
	// ErrInvalid is input a request or change cannot take. Its text names the
	// field, never the value.
	ErrInvalid = errors.New("dsr: invalid input")
	// ErrClosed is a change to a request that is already closed.
	ErrClosed = errors.New("dsr: request is closed")
	// ErrAlreadyExtended is a second extension: the regimes allow one.
	ErrAlreadyExtended = errors.New("dsr: deadline already extended")
	// ErrNotVerified is completing a request that discloses, changes or
	// deletes data before the requester's identity is verified.
	ErrNotVerified = errors.New("dsr: requester not verified")
)

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

const (
	maxNameBytes   = 200
	maxEmailBytes  = 254
	maxDetailBytes = 4000
	maxActorBytes  = 200

	// DefaultDueSoon is how long before its due date a request counts as near it.
	DefaultDueSoon = 7 * 24 * time.Hour
	// DefaultAlertLease is long enough for a send to finish and be recorded,
	// and short enough that an alert lost to a crash goes out at the next check.
	DefaultAlertLease = 30 * time.Minute
)

// Config is what NewService needs.
type Config struct {
	Store Store
	// Deadlines is nil for DefaultDeadlines.
	Deadlines *Deadlines
	// DueSoon is how far ahead of its due date an alert is owed; zero is DefaultDueSoon.
	DueSoon time.Duration
	// AlertLease is how long a claimed alert is left to go out before another
	// check may take it over; zero is DefaultAlertLease.
	AlertLease time.Duration
	// Clock is nil for time.Now.
	Clock  func() time.Time
	Logger slog.Handler
}

// Service holds the rules over a Store: deadlines, which changes are allowed.
type Service struct {
	store     Store
	deadlines *Deadlines
	dueSoon   time.Duration
	lease     time.Duration
	clock     func() time.Time
	logger    *slog.Logger
}

func NewService(cfg *Config) (*Service, error) {
	if cfg.Store == nil {
		return nil, errors.New("dsr: no store")
	}
	s := &Service{store: cfg.Store, deadlines: cfg.Deadlines, dueSoon: cfg.DueSoon, lease: cfg.AlertLease, clock: cfg.Clock}
	if s.deadlines == nil {
		s.deadlines = DefaultDeadlines()
	}
	if s.dueSoon <= 0 {
		s.dueSoon = DefaultDueSoon
	}
	if s.lease <= 0 {
		s.lease = DefaultAlertLease
	}
	if s.clock == nil {
		s.clock = time.Now
	}
	handler := cfg.Logger
	if handler == nil {
		handler = slog.DiscardHandler
	}
	// Ids, types and statuses only: a request's email and text never reach a log.
	s.logger = slog.New(handler)
	return s, nil
}

func tooLong(s string, max int) bool { return len(s) > max }

func checkText(field, s string, max int) error {
	if tooLong(s, max) {
		return invalid("%s is longer than %d bytes", field, max)
	}
	if !utf8.ValidString(s) {
		return invalid("%s is not valid UTF-8", field)
	}
	// NUL is refused by Postgres text, and nothing here wants control characters.
	if strings.ContainsRune(s, 0) {
		return invalid("%s has a NUL character", field)
	}
	return nil
}

func checkActor(actor string) error {
	if actor == "" {
		return invalid("no actor")
	}
	return checkText("actor", actor, maxActorBytes)
}

// checked is a NewRequest's fields as Create stores them.
type checked struct {
	name, addr string
	received   time.Time
	now        time.Time
}

func (s *Service) check(in *NewRequest) (*checked, error) {
	if !in.Type.Valid() {
		return nil, invalid("unknown request type")
	}
	if !in.Source.Valid() {
		return nil, invalid("unknown source")
	}
	if err := checkActor(in.Actor); err != nil {
		return nil, err
	}
	name := strings.TrimSpace(in.RequesterName)
	if err := checkText("name", name, maxNameBytes); err != nil {
		return nil, err
	}
	if err := checkText("details", in.Details, maxDetailBytes); err != nil {
		return nil, err
	}
	addr, err := normaliseEmail(in.RequesterEmail)
	if err != nil {
		return nil, err
	}
	now := s.clock()
	received := in.ReceivedAt
	if received.IsZero() {
		received = now
	}
	if received.After(now) {
		return nil, invalid("received date is in the future")
	}
	return &checked{name: name, addr: addr, received: received, now: now}, nil
}

// Validate says whether Create would take the request, without touching the
// store, so a caller can rate limit only requests that are well formed.
func (s *Service) Validate(in *NewRequest) error {
	_, err := s.check(in)
	return err
}

// Create logs a request and starts its clock.
func (s *Service) Create(ctx context.Context, in *NewRequest) (*Request, error) {
	c, err := s.check(in)
	if err != nil {
		return nil, err
	}
	name, addr, received, now := c.name, c.addr, c.received, c.now
	r := &Request{
		ID: uuid.New(), RequesterName: name, RequesterEmail: addr, Type: in.Type, Source: in.Source,
		AuthorisedAgent: in.AuthorisedAgent, Details: in.Details, Status: StatusReceived,
		ReceivedAt: received.UTC(), DueAt: s.deadlines.Due(in.Type, received, false),
	}
	first := &Event{RequestID: r.ID, At: now, Actor: in.Actor, Kind: EventReceived, ToStatus: StatusReceived}
	if err := s.store.Insert(ctx, r, first); err != nil {
		return nil, err
	}
	s.logger.InfoContext(ctx, "dsr: request logged", "request", r.ID, "type", r.Type, "source", r.Source)
	return r, nil
}

// normaliseEmail accepts a bare address only: a display name or a list is
// not one person's mailbox.
func normaliseEmail(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || tooLong(raw, maxEmailBytes) {
		return "", invalid("email is empty or too long")
	}
	a, err := mail.ParseAddress(raw)
	if err != nil || a.Address != raw || a.Name != "" {
		return "", invalid("email is not a plain address")
	}
	return strings.ToLower(raw), nil
}

func (s *Service) Get(ctx context.Context, id uuid.UUID) (*Request, []*Event, error) {
	r, err := s.store.Get(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	events, err := s.store.Events(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	return r, events, nil
}

func (s *Service) List(ctx context.Context, f *Filter) ([]*Request, error) {
	return s.store.List(ctx, f)
}

// DueState is how a request stands against its due date at a moment.
type DueState string

const (
	DueNone    DueState = ""
	DueSoon    DueState = "due_soon"
	DuePastDue DueState = "overdue"
)

// DueState is r's standing now. A closed request is never due.
func (s *Service) DueState(r *Request) DueState {
	if r.Status.Closed() {
		return DueNone
	}
	now := s.clock()
	switch {
	case r.DueAt.Before(now):
		return DuePastDue
	case r.DueAt.Sub(now) <= s.dueSoon:
		return DueSoon
	}
	return DueNone
}

func (s *Service) event(actor string, kind EventKind, detail string) *Event {
	return &Event{At: s.clock(), Actor: actor, Kind: kind, Detail: detail}
}

// update runs change on a request that is still open.
func (s *Service) update(ctx context.Context, id uuid.UUID, actor string, change func(r *Request) (*Event, error)) (*Request, error) {
	if err := checkActor(actor); err != nil {
		return nil, err
	}
	return s.store.Update(ctx, id, func(r *Request) (*Event, error) {
		if r.Status.Closed() {
			return nil, ErrClosed
		}
		return change(r)
	})
}

// SetStatus moves a request to another open status, or closes it. Closing as
// completed a request that discloses, changes or deletes data needs its
// requester verified first.
func (s *Service) SetStatus(ctx context.Context, id uuid.UUID, actor string, to Status, detail string) (*Request, error) {
	if !to.Valid() || to == StatusReceived {
		return nil, invalid("a request can only move to verifying, in_progress, completed, refused or withdrawn")
	}
	if err := checkText("detail", detail, maxDetailBytes); err != nil {
		return nil, err
	}
	r, err := s.update(ctx, id, actor, func(r *Request) (*Event, error) {
		if r.Status == to {
			return nil, invalid("the request is already %s", to)
		}
		if to == StatusCompleted && r.Type.NeedsVerification() && !r.Verified {
			return nil, ErrNotVerified
		}
		ev := s.event(actor, EventStatus, detail)
		ev.FromStatus, ev.ToStatus = r.Status, to
		r.Status = to
		if to.Closed() {
			t := s.clock().UTC()
			r.ClosedAt = &t
		}
		return ev, nil
	})
	if err == nil {
		s.logger.InfoContext(ctx, "dsr: status changed", "request", id, "status", to)
	}
	return r, err
}

// Verify records that the requester's identity was established, and how.
func (s *Service) Verify(ctx context.Context, id uuid.UUID, actor string, method VerificationMethod, detail string) (*Request, error) {
	if !method.Valid() {
		return nil, invalid("unknown verification method")
	}
	if err := checkText("detail", detail, maxDetailBytes); err != nil {
		return nil, err
	}
	return s.update(ctx, id, actor, func(r *Request) (*Event, error) {
		t := s.clock().UTC()
		r.Verified, r.VerificationMethod, r.VerifiedAt = true, method, &t
		return s.event(actor, EventVerified, strings.TrimSpace(string(method)+" "+detail)), nil
	})
}

// Extend moves the due date out by the regime's extension, once, for a
// reason the audit trail keeps. The alerts are owed afresh for the new date.
func (s *Service) Extend(ctx context.Context, id uuid.UUID, actor, reason string) (*Request, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return nil, invalid("an extension needs a reason")
	}
	if err := checkText("reason", reason, maxDetailBytes); err != nil {
		return nil, err
	}
	return s.update(ctx, id, actor, func(r *Request) (*Event, error) {
		if r.Extended {
			return nil, ErrAlreadyExtended
		}
		r.Extended, r.ExtensionReason = true, reason
		r.DueAt = s.deadlines.Due(r.Type, r.ReceivedAt, true)
		r.AlertedSoonAt, r.AlertedOverdueAt = nil, nil
		return s.event(actor, EventExtended, reason), nil
	})
}

// AddNote adds to a request's audit trail without changing it. Unlike the
// other changes it is allowed on a closed request: a note after the fact
// (a complaint, an outcome) belongs on the record.
func (s *Service) AddNote(ctx context.Context, id uuid.UUID, actor, note string) error {
	note = strings.TrimSpace(note)
	if note == "" {
		return invalid("a note is empty")
	}
	if err := checkActor(actor); err != nil {
		return err
	}
	if err := checkText("note", note, maxDetailBytes); err != nil {
		return err
	}
	if _, err := s.store.Get(ctx, id); err != nil {
		return err
	}
	ev := s.event(actor, EventNote, note)
	ev.RequestID = id
	return s.store.AppendEvents(ctx, []*Event{ev})
}

// SystemActor is the actor of what the software does by itself.
const SystemActor = "system"

// CheckAlerts finds the requests whose alert is owed, hands them to notify
// and records that it did. When notify fails the alerts are put back, so the
// next check offers them again. It returns how many alerts were sent.
func (s *Service) CheckAlerts(ctx context.Context, notify func(ctx context.Context, alerts []*Alert) error) (int, error) {
	alerts, err := s.store.ClaimAlerts(ctx, s.clock(), s.dueSoon, s.lease)
	if err != nil || len(alerts) == 0 {
		return 0, err
	}
	if err := notify(ctx, alerts); err != nil {
		// The notifier's context may be what ended; the release must not be.
		if relErr := s.store.ReleaseAlerts(context.WithoutCancel(ctx), alerts); relErr != nil {
			return 0, errors.Join(err, relErr)
		}
		return 0, err
	}
	events := make([]*Event, len(alerts))
	for i, a := range alerts {
		kind := EventAlertSoon
		if a.Kind == AlertOverdue {
			kind = EventAlertLate
		}
		events[i] = &Event{RequestID: a.Request.ID, At: s.clock(), Actor: SystemActor, Kind: kind}
	}
	// The alerts are out, so their claims stay: releasing them for a failed
	// audit write would send them twice. The error says the trail is short.
	if err := s.store.AppendEvents(ctx, events); err != nil {
		return len(alerts), err
	}
	s.logger.InfoContext(ctx, "dsr: alerts sent", "count", len(alerts))
	return len(alerts), nil
}
