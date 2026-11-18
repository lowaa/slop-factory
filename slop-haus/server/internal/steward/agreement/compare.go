// Package agreement scores, per category, how often the owner decided as the
// steward would have. The score is what the owner reads before raising a
// category's autonomy.
package agreement

import (
	"time"

	"github.com/lowaa/slop-factory/go-clock"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward"
)

// Outcome is how one assessment compares with what the owner did.
type Outcome int

const (
	// Undecided assessments claim nothing to agree with (needs_human), or the
	// owner has not acted, or withdrew it: they are not decisions.
	Undecided Outcome = iota
	Agreed
	Disagreed
)

// Comparator turns an assessment and the owner's recorded decision into an
// Outcome. There is one per category: what "the same action" means differs
// as categories gain judgment.
type Comparator func(*steward.Record) Outcome

// sameAction is the comparison for categories where the steward proposes the
// owner's own action: agreed when the owner approved it.
//
// A veto or an undo is a disagreement whatever else is recorded, and whatever
// reason the owner gave: the reason is shown beside it, never weighed.
func sameAction(r *steward.Record) Outcome {
	if r.Verdict == steward.VerdictNeedsHuman {
		return Undecided
	}
	if r.VetoedAt != nil || r.UndoneAt != nil {
		return Disagreed
	}
	if r.HumanDecision == nil {
		return Undecided
	}
	switch *r.HumanDecision {
	case steward.DecisionApproved:
		return Agreed
	case steward.DecisionRejected, steward.DecisionEdited:
		return Disagreed
	}
	return Undecided
}

// SameActionOrExpired is sameAction for a category whose proposal the owner
// is expected to act on: an assessment still undecided after window counts as
// a disagreement, unless it was voided. It lives here, not in the category's
// package, because the comparators table below must name it.
func SameActionOrExpired(c clock.Clock, window time.Duration) Comparator {
	c = clock.Or(c)
	return func(r *steward.Record) Outcome {
		out := sameAction(r)
		if out == Undecided && r.Verdict != steward.VerdictNeedsHuman && r.HumanDecision == nil &&
			r.VoidedAt == nil && clock.Since(c, r.CreatedAt) > window {
			return Disagreed
		}
		return out
	}
}

// ticketReadyWindow is how long an unopened ticket_ready proposal may sit
// before the owner's silence counts against the steward.
const ticketReadyWindow = 7 * 24 * time.Hour

// OwnerAnswer reads the option the steward proposed and the answer the owner
// finally gave, for an assessment. ok is false when the owner's answer is not
// recorded.
type OwnerAnswer func(r *steward.Record) (option, answer string, ok bool)

// PickComparator is the comparison for discussion_pick: the same option agrees;
// a different option, free text that names no option, a rejection, a veto or an
// undo disagree. An owner who edited the answer agrees only when owner shows
// the answer is still exactly the option (byte for byte, as the category's check
// compares). owner may be nil, when every edit counts as different.
func PickComparator(owner OwnerAnswer) Comparator {
	return func(r *steward.Record) Outcome {
		if r.Verdict != steward.VerdictNeedsHuman && r.HumanDecision != nil &&
			*r.HumanDecision == steward.DecisionEdited && r.VetoedAt == nil && r.UndoneAt == nil &&
			owner != nil {
			if option, answer, ok := owner(r); ok && option == answer {
				return Agreed
			}
		}
		return sameAction(r)
	}
}

// comparators holds an entry per category in the allowlist; a test fails when
// a category is added without one, so its part must register its own.
var comparators = map[string]Comparator{
	"merge_continue": sameAction,
	"infra_wait":     sameAction,
	"gate_confirmed": sameAction,
	"bookkeeping":    sameAction,
	"narrow_scope":   sameAction,
	"priority":       sameAction,
	"review_approve": sameAction,
	"ticket_ready":   SameActionOrExpired(nil, ticketReadyWindow),
	// No record keeps the answer the owner edited to, so every edit counts as a
	// different answer until one is wired in.
	"discussion_pick": PickComparator(nil),
}

// dangerous marks the disagreements that are worse than a different answer.
// For review_approve the steward proposes approval, so the owner going
// against it (rejecting, editing, vetoing or undoing) means the steward would
// have let through a change the owner would not: those are listed first.
var dangerous = map[string]bool{"review_approve": true}

// Dangerous reports whether a disagreement on the category is one the owner
// should read first.
func Dangerous(category string) bool { return dangerous[category] }

// ComparatorFor returns the category's comparator. A retired category's
// stored assessments stay scorable, by the plain same-action rule.
func ComparatorFor(category string) Comparator {
	if c, ok := comparators[category]; ok {
		return c
	}
	return sameAction
}

// Registered reports whether the category has its own comparator.
func Registered(category string) bool {
	_, ok := comparators[category]
	return ok
}

// ForDigest adapts the category comparators to the steward digest's agreement
// counts.
func ForDigest(r *steward.Record) (agreed, disagreed bool) {
	switch ComparatorFor(r.Category)(r) {
	case Agreed:
		return true, false
	case Disagreed:
		return false, true
	}
	return false, false
}
