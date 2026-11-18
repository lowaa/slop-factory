package discussionapply

import (
	"context"
	"errors"
	"time"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward/discussionanswer"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward/discussionlabel"
)

// State is what the discussion's badge says about the steward applying it.
type State string

const (
	// StateNone: the steward is not applying this one; the owner decides.
	StateNone State = ""
	// StateApplied: "Applied by steward".
	StateApplied State = "applied"
	// StateScheduled: "Applies at <time> unless you veto".
	StateScheduled State = "scheduled"
	// StateVetoed: the owner stopped it.
	StateVetoed State = "vetoed"
)

// Badge is the steward-apply part of a discussion's badge.
type Badge struct {
	State State
	// At is when a scheduled answer applies, or when an applied one was posted.
	At *time.Time
	// CanVeto is set while a veto-tier answer is inside its window.
	CanVeto bool
	// CanUndo is whether the answer can be taken back. A posted discussion
	// answer cannot be recalled from a worker who already read it.
	CanUndo bool
	// Reason is why the steward gave a schedule up, when it did.
	Reason string
}

// CanUndo says whether a category's applied answer can be undone: not for
// discussion answers, which a worker may already have read.
func CanUndo(category string) bool {
	cat, ok := steward.Lookup(category)
	if !ok {
		return false
	}
	for _, k := range cat.Kinds {
		if k == steward.KindDiscussionAnswer {
			return false
		}
	}
	return true
}

// Badge reads the apply state of a label.
func (s *Service) Badge(ctx context.Context, rec *discussionlabel.Record) (*Badge, error) {
	b := &Badge{CanUndo: false}
	if rec.Label != discussionlabel.LabelRoutine {
		return b, nil
	}
	sch, err := s.Schedules.Get(ctx, rec.ID)
	if errors.Is(err, ErrNoSchedule) {
		return b, nil
	}
	if err != nil {
		return nil, err
	}
	dec, err := s.Decisions.ForLabel(ctx, rec.ID)
	switch {
	case err == nil:
		switch {
		case dec.Actor == Actor && dec.Action == discussionanswer.ActionApprove:
			b.State, b.At, b.CanUndo = StateApplied, &dec.CreatedAt, CanUndo(rec.Category)
		case sch.Tier == steward.TierVeto && dec.Action == discussionanswer.ActionReject && dec.Reason == VetoReason:
			b.State, b.At = StateVetoed, &dec.CreatedAt
		}
		return b, nil
	case !errors.Is(err, discussionanswer.ErrNoDecision):
		return nil, err
	}
	if sch.VoidedAt != nil {
		b.Reason = sch.VoidedReason
		return b, nil
	}
	b.State, b.At = StateScheduled, &sch.ApplyAt
	b.CanVeto = sch.Tier == steward.TierVeto && s.Now().Before(sch.ApplyAt)
	return b, nil
}

// DraftBadge is Badge in the shape the draft API serves.
func (s *Service) DraftBadge(ctx context.Context, rec *discussionlabel.Record) (*discussionanswer.ApplyBadge, error) {
	b, err := s.Badge(ctx, rec)
	if err != nil {
		return nil, err
	}
	return &discussionanswer.ApplyBadge{State: string(b.State), At: b.At, CanVeto: b.CanVeto, CanUndo: b.CanUndo, Reason: b.Reason}, nil
}
