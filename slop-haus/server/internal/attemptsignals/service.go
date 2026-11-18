package attemptsignals

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/attemptsignals/usage"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// ErrNoAttempt is a Source answer for an attempt the tenant does not have.
var ErrNoAttempt = errors.New("attemptsignals: no such attempt")

// Source gathers the inputs the signals are computed from: the attempt's
// change and what the code host shows of it. It returns ErrNoAttempt for an
// attempt that does not exist, and any other error when the evidence could
// not be read, in which case the attempt keeps the signals it had.
type Source interface {
	Inputs(ctx context.Context, ref ticket.AttemptRef) (*Inputs, error)
}

// DefaultStaleAfter is how old the signals of a running attempt may be before
// a read recomputes them.
const DefaultStaleAfter = time.Minute

// Service serves the signals of attempts. It recomputes those still moving
// when they are read and stale, and freezes them once the attempt is over.
type Service struct {
	store      *Store
	usage      *usage.Store
	source     Source
	log        *slog.Logger
	now        func() time.Time
	staleAfter time.Duration

	// tried is when each attempt was last refreshed, successfully or not. A
	// read refreshes at most once per staleAfter per attempt, so polling the
	// ticket (or a host that is down) cannot multiply code host calls.
	mu    sync.Mutex
	tried map[ticket.AttemptRef]time.Time
}

type Options struct {
	// StaleAfter defaults to DefaultStaleAfter.
	StaleAfter time.Duration
	// Now defaults to time.Now.
	Now func() time.Time
}

func NewService(store *Store, usageStore *usage.Store, source Source, logHandler slog.Handler, opts *Options) *Service {
	s := &Service{store: store, usage: usageStore, source: source, log: slog.New(logHandler), now: time.Now, staleAfter: DefaultStaleAfter, tried: map[ticket.AttemptRef]time.Time{}}
	if opts != nil {
		if opts.StaleAfter != 0 {
			s.staleAfter = opts.StaleAfter
		}
		if opts.Now != nil {
			s.now = opts.Now
		}
	}
	return s
}

var _ ticket.AttemptSignals = (*Service)(nil)

// AttemptSignals returns the stored signals, refreshing the running ones that
// are missing or stale first. A refresh that fails leaves the stored signals
// as they were: the ticket page does not depend on the code host being up.
func (s *Service) AttemptSignals(ctx context.Context, refs []ticket.AttemptRef) (map[ticket.AttemptRef]*ticket.AttemptSignalsBody, error) {
	stored, err := s.store.Get(ctx, refs)
	if err != nil {
		return nil, err
	}
	for _, ref := range refs {
		if b := stored[ref]; b != nil && (b.Final || s.now().Sub(b.ComputedAt) < s.staleAfter) {
			continue
		}
		if !s.claim(ref) {
			continue
		}
		body, err := s.Refresh(ctx, ref)
		if err != nil {
			if !errors.Is(err, ErrNoAttempt) {
				s.log.WarnContext(ctx, "attempt signals refresh failed", "ticket", ref.TicketID, "attempt", ref.Number, "err", err)
			}
			continue
		}
		stored[ref] = body
	}
	return stored, nil
}

// claim is whether this read may refresh the attempt: not when one started
// within staleAfter.
func (s *Service) claim(ref ticket.AttemptRef) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if at, ok := s.tried[ref]; ok && now.Sub(at) < s.staleAfter {
		return false
	}
	for r, at := range s.tried {
		if now.Sub(at) >= s.staleAfter {
			delete(s.tried, r)
		}
	}
	s.tried[ref] = now
	return true
}

// Refresh recomputes and stores one attempt's signals. It returns what is
// stored afterwards, which is the earlier row when that one was already final.
func (s *Service) Refresh(ctx context.Context, ref ticket.AttemptRef) (*ticket.AttemptSignalsBody, error) {
	// Stamped before the evidence is read, so a slower refresh that read older
	// evidence is the older write (see Store.Put).
	started := s.now()
	in, err := s.source.Inputs(ctx, ref)
	if err != nil {
		return nil, err
	}
	used, err := s.usage.Signals(ctx, []ticket.AttemptRef{ref})
	if err != nil {
		return nil, err
	}
	c := Compute(in, used[ref], started)
	if err := s.store.Put(ctx, ref, c); err != nil {
		return nil, err
	}
	got, err := s.store.Get(ctx, []ticket.AttemptRef{ref})
	if err != nil {
		return nil, err
	}
	return got[ref], nil
}
