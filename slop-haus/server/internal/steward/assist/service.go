// Package assist is the steward's level 2 (Assist) mode: it pre-drafts the
// decision on a pending approval and leaves it for the owner to apply with one
// click. Nothing here applies a draft by itself.
package assist

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward"
)

var (
	// ErrNotAssist is drafting or applying while the tenant's level is not
	// Assist.
	ErrNotAssist = errors.New("assist: the tenant is not at the assist level")
	// ErrStale is applying a draft whose ticket revision or head moved, or
	// whose checks no longer hold. The proposal stays the owner's.
	ErrStale = errors.New("assist: the draft is out of date")
	// ErrNotApplicable is applying a draft that is notes only.
	ErrNotApplicable = errors.New("assist: the draft is notes only and cannot be applied")
)

// stuckClaimAfter is how long a claim may stand with its proposal still
// pending before a click may take it over: the server stopped between the
// claim and the approval.
const stuckClaimAfter = 5 * time.Minute

// Request is the steward's draft for a proposal it made.
type Request struct {
	ProposalID uuid.UUID
	Category   string
	Reason     string
	// HeadSHA is the head of the ticket's change the steward read.
	HeadSHA string
	// Body is the answer text, the review notes or a summary of the edit.
	Body string
	// WouldSendBack is a review the steward would request changes on.
	WouldSendBack bool
}

// Service drafts and applies. Applying goes through the same Approver as a
// Veto-tier decision after its window, so assist adds no apply path of its
// own.
type Service struct {
	stewards  steward.Stewards
	proposals steward.Proposals
	settings  steward.Settings
	store     *Store
	engine    *steward.Engine
	approver  steward.Approver
	now       func() time.Time
	logger    *slog.Logger
}

// NewService wires the service. now is injectable for tests.
func NewService(logHandler slog.Handler, stewards steward.Stewards, proposals steward.Proposals, settings steward.Settings,
	store *Store, engine *steward.Engine, approver steward.Approver, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{stewards: stewards, proposals: proposals, settings: settings, store: store, engine: engine,
		approver: approver, now: now, logger: slog.New(logHandler)}
}

func (s *Service) level(ctx context.Context) (*steward.Config, error) {
	cfg, err := s.settings.StewardConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("assist: read settings: %w", err)
	}
	if cfg.Level != steward.LevelAssist {
		return nil, ErrNotAssist
	}
	return cfg, nil
}

// Draft stores the steward's draft for one of its pending proposals. The
// checks run now and their outcomes, pass or fail, are kept as the evidence;
// the revision and head are the ones the server reads for the proposal, never
// the steward's word.
func (s *Service) Draft(ctx context.Context, agentID uuid.UUID, req *Request) (*Draft, error) {
	isSteward, err := s.stewards.IsSteward(ctx, agentID)
	if err != nil {
		return nil, fmt.Errorf("assist: read caller: %w", err)
	}
	if !isSteward {
		return nil, steward.ErrNotSteward
	}
	cfg, err := s.level(ctx)
	if err != nil {
		return nil, err
	}
	facts, err := s.proposals.Proposal(ctx, req.ProposalID)
	if err != nil {
		return nil, err
	}
	switch {
	case facts.AgentID != agentID:
		return nil, steward.ErrNotYours
	case facts.TicketID == nil:
		return nil, steward.ErrNoTicket
	case !facts.Pending:
		return nil, steward.ErrNotPending
	}
	assessment := s.engine.Assess(ctx, &steward.Proposal{
		Category: req.Category, Kind: facts.Kind, Reason: req.Reason, Decision: facts.Decision,
		Subject: steward.Subject{TicketID: *facts.TicketID, Revision: facts.Revision, HeadSHA: req.HeadSHA,
			ProposedPaths: facts.Paths, EditedFields: facts.EditedFields, Body: facts.Body},
	}, cfg.Level, cfg.Tiers)
	if facts.Kind != steward.KindReviewDecision && req.WouldSendBack {
		return nil, fmt.Errorf("assist: only a review can be marked would-send-back")
	}
	// An answer is shown as the proposal states it: approving posts the
	// proposal's own text, so a different text from the steward would show the
	// owner something other than what the click does.
	body := req.Body
	if facts.Kind == steward.KindDiscussionAnswer {
		body = facts.Body
	}
	d, err := s.store.Create(ctx, &Draft{
		TicketID: *facts.TicketID, PendingActionID: facts.ID, Category: req.Category, Kind: facts.Kind, Body: body,
		WouldSendBack: req.WouldSendBack, Reason: req.Reason, Evidence: assessment.Evidence,
		TicketRevision: facts.Revision, HeadSHA: req.HeadSHA, AgentID: agentID, CreatedAt: s.now(),
	})
	if err != nil {
		return nil, err
	}
	s.logger.InfoContext(ctx, "steward drafted", "proposal", facts.ID, "ticket", d.TicketID, "category", d.Category)
	return d, nil
}

// Apply is the owner's click on a draft: it approves the draft's proposal as
// the owner, through the approver, after re-running the category's checks
// against what the server reads now. A draft whose revision or head moved is
// refused with ErrStale.
func (s *Service) Apply(ctx context.Context, owner, proposalID uuid.UUID) error {
	cfg, err := s.level(ctx)
	if err != nil {
		return err
	}
	d, err := s.store.Get(ctx, proposalID)
	if err != nil {
		return err
	}
	if d.WouldSendBack {
		return ErrNotApplicable
	}
	facts, err := s.proposals.Proposal(ctx, proposalID)
	switch {
	case errors.Is(err, steward.ErrNotYours) || (err == nil && !facts.Pending):
		return steward.ErrNotPending
	case err != nil:
		return fmt.Errorf("assist: read proposal: %w", err)
	}
	fresh := s.engine.Assess(ctx, &steward.Proposal{
		Category: d.Category, Kind: facts.Kind, Reason: d.Reason, Decision: facts.Decision,
		Subject: steward.Subject{TicketID: d.TicketID, Revision: d.TicketRevision, HeadSHA: d.HeadSHA,
			ProposedPaths: facts.Paths, EditedFields: facts.EditedFields, Body: facts.Body},
	}, cfg.Level, cfg.Tiers)
	// The evidence decides, not the verdict: at this level every tier is the
	// owner's, so a judgment category's verdict is needs_human whatever it read.
	if failed := failedChecks(fresh); failed != "" {
		return fmt.Errorf("%w: %s", ErrStale, failed)
	}
	now := s.now()
	if err := s.store.Claim(ctx, d.ID, owner, now, now.Add(-stuckClaimAfter)); err != nil {
		return err
	}
	if err := s.approver.ApproveForSteward(ctx, proposalID); err != nil {
		// The owner can still decide the proposal, and click again. The
		// release must outlive a cancelled request, or the claim would stand
		// without an approval; a stopped server is covered by Claim taking an
		// old claim over.
		return errors.Join(s.store.Unclaim(context.WithoutCancel(ctx), d.ID), err)
	}
	s.logger.InfoContext(ctx, "steward draft applied by the owner", "proposal", proposalID, "ticket", d.TicketID, "owner", owner)
	return nil
}

// failedChecks says what no longer holds; empty when everything does.
func failedChecks(a *steward.Assessment) string {
	var parts []string
	for _, ev := range a.Evidence {
		if !ev.OK {
			parts = append(parts, strings.TrimSpace(ev.Check+": "+ev.Detail))
		}
	}
	return strings.Join(parts, "; ")
}
