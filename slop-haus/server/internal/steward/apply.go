package steward

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/jobs"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/tenantstate"
)

// ErrApprovalRefused is the approval refusing a proposal the steward asked to
// apply (the ticket moved on, the grant is gone, the ticket's rules say no).
// The proposal stays the owner's.
var ErrApprovalRefused = errors.New("steward: the approval was refused")

// Approver applies a pending proposal as the owner would have, through the
// board's normal approval path. It reports ErrNotPending for a proposal that
// is already decided and ErrApprovalRefused for one the approval refused.
type Approver interface {
	ApproveForSteward(ctx context.Context, proposalID uuid.UUID) error
}

// dueBatch bounds the decisions one pass applies per tenant.
const dueBatch = 100

// DefaultApplyInterval is how often due Veto-tier decisions are looked for;
// it only bounds how late one is applied after its deadline.
const DefaultApplyInterval = 30 * time.Second

// unscheduledGrace is how long a fresh assessment is left to the request that
// made it before the job picks it up.
const unscheduledGrace = time.Minute

// stuckClaimAfter is how long an application may take, from its claim to the
// proposal being approved, before it is taken to have been interrupted.
const stuckClaimAfter = 5 * time.Minute

// capWindow is the span the daily cap counts applications over.
const capWindow = 24 * time.Hour

// Applier carries out live-mode assessments: Auto ones at once, Veto ones when
// their window has run out. It decides nothing new. Before every application
// it runs the category's checks again against what the server reads now, and
// anything that no longer holds voids the assessment and leaves the proposal
// to the owner.
type Applier struct {
	store     *Store
	proposals Proposals
	settings  Settings
	engine    *Engine
	approver  Approver
	roster    tenantstate.Roster
	comments  Commenter
	links     Links
	now       func() time.Time
	logger    *slog.Logger
}

// NewApplier wires the applier. roster says which tenants the due-job visits;
// now is injectable for tests.
func NewApplier(logHandler slog.Handler, store *Store, proposals Proposals, settings Settings, engine *Engine, approver Approver, roster tenantstate.Roster, now func() time.Time) *Applier {
	if now == nil {
		now = time.Now
	}
	return &Applier{store: store, proposals: proposals, settings: settings, engine: engine, approver: approver,
		roster: roster, now: now, logger: slog.New(logHandler)}
}

// WithComments has the applier say what it did on the ticket, with the board
// links links builds. Without it decisions are applied silently.
func (a *Applier) WithComments(c Commenter, links Links) *Applier {
	a.comments, a.links = c, links
	return a
}

func (a *Applier) link(ctx context.Context, kind LinkKind, rec *Record) string {
	if a.links == nil {
		return ""
	}
	return a.links(ctx, kind, rec)
}

// openWindow gives a Veto-tier decision its deadline and says so on the
// ticket, once: only the call that set the deadline comments.
func (a *Applier) openWindow(ctx context.Context, rec *Record, deadline time.Time) error {
	if err := a.store.SetVetoDeadline(ctx, rec.ID, deadline); err != nil {
		return err
	}
	postComment(ctx, a.comments, a.logger, rec, pendingComment(rec, deadline, a.link(ctx, LinkVeto, rec)))
	return nil
}

// Consider is called for a just-stored assessment of the tenant in ctx. Only a
// live-mode one is acted on: an Auto-tier one is applied now, a Veto-tier one
// gets its window. It never fails the assessment; whatever goes wrong leaves
// the proposal pending for the owner.
func (a *Applier) Consider(ctx context.Context, rec *Record, cfg *Config) {
	if cfg.Mode != ModeLive || rec.Mode != ModeLive || rec.Verdict == VerdictNeedsHuman {
		return
	}
	var err error
	switch rec.Tier {
	case TierAuto:
		err = a.apply(ctx, rec, cfg, true)
	case TierVeto:
		err = a.openWindow(ctx, rec, a.now().Add(time.Duration(cfg.VetoMinutes)*time.Minute))
	}
	if err != nil {
		a.logger.WarnContext(ctx, "steward could not act on an assessment", "assessment", rec.ID, "proposal", rec.PendingActionID, "error", err)
	}
}

// ApplyDue applies the tenant's Veto-tier decisions whose window has run out,
// and finishes what a stopped server left half done: an assessment never
// scheduled is scheduled (or, for Auto, applied), and an application marked
// but never approved is voided. One failing does not stop the others; the
// first error is returned.
func (a *Applier) ApplyDue(ctx context.Context) error {
	cfg, err := a.settings.StewardConfig(ctx)
	if err != nil {
		return fmt.Errorf("steward: read settings: %w", err)
	}
	if cfg.Mode != ModeLive {
		return nil
	}
	var first error
	note := func(rec *Record, err error) {
		if err == nil {
			return
		}
		a.logger.WarnContext(ctx, "steward could not apply a due decision", "assessment", rec.ID, "error", err)
		if first == nil {
			first = err
		}
	}
	stuck, err := a.store.ListStuckClaims(ctx, a.now().Add(-stuckClaimAfter), dueBatch)
	if err != nil {
		return err
	}
	for i := range stuck {
		note(&stuck[i], a.unclaim(ctx, stuck[i].ID, VoidNotApplicable, "the application did not complete"))
	}
	due, err := a.store.ListDue(ctx, a.now(), unscheduledGrace, dueBatch)
	if err != nil {
		return err
	}
	for i := range due {
		rec := &due[i]
		switch {
		case rec.VetoDeadline != nil:
			note(rec, a.apply(ctx, rec, cfg, false))
		case rec.Tier == TierAuto:
			note(rec, a.apply(ctx, rec, cfg, true))
		case rec.Tier == TierVeto:
			deadline := rec.CreatedAt.Add(time.Duration(cfg.VetoMinutes) * time.Minute)
			if err := a.openWindow(ctx, rec, deadline); err != nil && !errors.Is(err, ErrNotApplicable) {
				note(rec, err)
			}
		}
	}
	return first
}

// SweepOnce is ApplyDue for every tenant. A tenant that fails does not stop
// the others.
func (a *Applier) SweepOnce(ctx context.Context) error {
	return tenantstate.Each(ctx, a.roster, func(ctx context.Context, _ uuid.UUID) error { return a.ApplyDue(ctx) })
}

// Job is the sweep as a background job, nil when interval is zero.
func (a *Applier) Job(interval time.Duration) *jobs.Job {
	if interval <= 0 {
		return nil
	}
	return &jobs.Job{Name: "steward_apply_due", Interval: interval, Run: a.SweepOnce}
}

// apply re-checks and applies one assessment. immediate is the Auto path; the
// deadline path is the Veto one. A returned error is one that left the
// assessment waiting (it may be tried again); every refusal is recorded as a
// void instead.
func (a *Applier) apply(ctx context.Context, rec *Record, cfg *Config, immediate bool) error {
	cat, known := Lookup(rec.Category)
	if rec.Verdict == VerdictNeedsHuman || !known || cat.NeverAuto || rec.TicketID == nil || rec.TicketRevision == nil {
		return a.void(ctx, rec.ID, VoidNotApplicable, "this decision is never taken by the steward")
	}
	facts, err := a.proposals.Proposal(ctx, rec.PendingActionID)
	if errors.Is(err, ErrNotYours) || (err == nil && !facts.Pending) {
		return a.void(ctx, rec.ID, VoidNotApplicable, "the proposal is no longer pending")
	}
	if err != nil {
		return fmt.Errorf("steward: read proposal: %w", err)
	}
	head := ""
	if rec.HeadSHA != nil {
		head = *rec.HeadSHA
	}
	fresh := a.engine.Assess(ctx, &Proposal{
		Category: rec.Category, Kind: facts.Kind, Reason: rec.Reason, Decision: facts.Decision,
		Subject: Subject{TicketID: *rec.TicketID, Revision: *rec.TicketRevision, HeadSHA: head, ProposedPaths: facts.Paths, EditedFields: facts.EditedFields, Body: facts.Body},
	}, cfg.Level, cfg.Tiers)
	switch {
	case fresh.Verdict == VerdictNeedsHuman:
		return a.void(ctx, rec.ID, VoidChecks, failedChecks(fresh))
	case immediate && fresh.Tier != TierAuto:
		return a.void(ctx, rec.ID, VoidNotApplicable, "the category is no longer automatic")
	}

	now := a.now()
	switch err := a.store.Claim(ctx, rec.ID, now, now.Add(-capWindow), cfg.DailyCap); {
	case errors.Is(err, ErrNotApplicable):
		return nil
	case errors.Is(err, ErrDailyCap):
		return a.void(ctx, rec.ID, VoidDailyCap, fmt.Sprintf("the daily cap of %d applications is reached", cfg.DailyCap))
	case err != nil:
		return err
	}
	switch err := a.approver.ApproveForSteward(ctx, rec.PendingActionID); {
	case err == nil:
		a.logger.InfoContext(ctx, "steward applied a decision", "assessment", rec.ID, "proposal", rec.PendingActionID,
			"category", rec.Category, "tier", string(rec.Tier))
		postComment(ctx, a.comments, a.logger, rec, appliedComment(rec, a.link(ctx, LinkUndo, rec)))
		return nil
	case errors.Is(err, ErrNotPending):
		return a.unclaim(ctx, rec.ID, VoidNotApplicable, "the proposal was decided before the steward applied it")
	default:
		// A failure that is not a refusal may be transient, but the claim
		// cannot be left standing without the approval, and the owner can
		// still decide the proposal.
		kind := VoidRefused
		return errors.Join(a.unclaim(ctx, rec.ID, kind, "the approval did not go through: "+err.Error()),
			ignore(err, ErrApprovalRefused))
	}
}

// ignore drops err when it is one the void already records.
func ignore(err, known error) error {
	if errors.Is(err, known) {
		return nil
	}
	return err
}

func (a *Applier) void(ctx context.Context, id uuid.UUID, kind VoidKind, reason string) error {
	a.logger.InfoContext(ctx, "steward voided a decision", "assessment", id, "kind", string(kind), "reason", reason)
	return ignore(a.store.Void(ctx, id, kind, reason, a.now()), ErrNotApplicable)
}

func (a *Applier) unclaim(ctx context.Context, id uuid.UUID, kind VoidKind, reason string) error {
	a.logger.InfoContext(ctx, "steward voided a decision", "assessment", id, "kind", string(kind), "reason", reason)
	return ignore(a.store.Unclaim(ctx, id, kind, reason, a.now()), ErrNotApplicable)
}

// failedChecks says what no longer holds, for the owner reading the void.
func failedChecks(a *Assessment) string {
	var parts []string
	for _, ev := range a.Evidence {
		if !ev.OK {
			parts = append(parts, strings.TrimSpace(ev.Check+": "+ev.Detail))
		}
	}
	if len(parts) == 0 {
		return "the category's checks no longer hold"
	}
	return "checks no longer hold: " + strings.Join(parts, "; ")
}
