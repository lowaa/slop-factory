package steward

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
)

// Verdict is what the server concludes about a steward's proposal. The
// steward's own opinion of its category never enters it.
type Verdict string

const (
	VerdictWouldAuto  Verdict = "would_auto"
	VerdictWouldVeto  Verdict = "would_veto"
	VerdictNeedsHuman Verdict = "needs_human"
)

// Proposal is what the steward claims: a category and why.
type Proposal struct {
	Category string
	// Kind is the pending-action kind being decided.
	Kind   string
	Reason string
	// Decision is the review decision a KindReviewDecision proposal takes
	// (the pendingaction words); empty for other kinds.
	Decision string
	Subject  Subject
}

// Assessment is the server's verdict with the evidence behind it.
type Assessment struct {
	Category string
	Tier     Tier
	Verdict  Verdict
	Reason   string
	Evidence []Evidence
}

// Engine runs a category's checks and decides the verdict.
type Engine struct {
	checks map[string]Check
	logger *slog.Logger
}

// NewEngine builds an engine over the checks it may run. A category whose
// check is not registered can never pass.
func NewEngine(logHandler slog.Handler, checks ...Check) *Engine {
	e := &Engine{checks: make(map[string]Check, len(checks)), logger: slog.New(logHandler)}
	for _, c := range checks {
		e.checks[c.Name()] = c
	}
	return e
}

// Assess decides p under the tenant's level and tier overrides. Every check runs, so the
// owner sees all that failed; any failure, an unknown category, a kind the
// category does not cover or an unreadable fact makes it the owner's.
func (e *Engine) Assess(ctx context.Context, p *Proposal, level Level, overrides TierOverrides) *Assessment {
	a := &Assessment{Category: p.Category, Tier: TierHuman, Verdict: VerdictNeedsHuman, Reason: p.Reason}
	cat, ok := Lookup(p.Category)
	if !ok {
		a.Evidence = []Evidence{{Check: "category", Detail: fmt.Sprintf("%q is not a steward category", p.Category)}}
		return a
	}
	a.Tier = EffectiveTier(cat, level.assessedAs(), overrides)
	if !slices.Contains(cat.Kinds, p.Kind) {
		a.Evidence = []Evidence{{Check: "kind", Detail: fmt.Sprintf("%s does not cover %s proposals", cat.ID, p.Kind)}}
		return a
	}
	// The steward approves, never sends a review back: where it would, the
	// owner decides.
	if p.Kind == KindReviewDecision && p.Decision != ReviewDecisionApprove {
		a.Evidence = []Evidence{{Check: "decision", Detail: fmt.Sprintf("the steward may only approve a review, not %q", p.Decision)}}
		return a
	}
	p.Subject.Category = cat.ID
	passed := true
	for _, name := range cat.Checks {
		ev := e.run(ctx, name, &p.Subject)
		passed = passed && ev.OK
		a.Evidence = append(a.Evidence, ev)
	}
	if !passed {
		return a
	}
	switch a.Tier {
	case TierAuto:
		a.Verdict = VerdictWouldAuto
	case TierVeto:
		a.Verdict = VerdictWouldVeto
	}
	return a
}

func (e *Engine) run(ctx context.Context, name string, s *Subject) Evidence {
	c, ok := e.checks[name]
	if !ok {
		return Evidence{Check: name, Detail: "check is not available"}
	}
	ev, err := c.Run(ctx, s)
	if err != nil {
		e.logger.WarnContext(ctx, "steward check could not run", "check", name, "ticket", s.TicketID, "error", err)
		return Evidence{Check: name, Detail: "the fact could not be read"}
	}
	return ev
}
