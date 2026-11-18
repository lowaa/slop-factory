package steward

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
)

var (
	// ErrOff is assessing while the tenant has the steward switched off.
	ErrOff = errors.New("steward: the steward is switched off for this tenant")
	// ErrDailyCap is assessing past the tenant's daily cap.
	ErrDailyCap = errors.New("steward: the tenant's daily cap is reached")
	// ErrNotSteward is assessing as an agent that is not an enabled steward.
	ErrNotSteward = errors.New("steward: only an enabled steward may assess")
	// ErrNotYours is a proposal that does not exist or that another agent made.
	// The two read the same, so an id does not tell what exists.
	ErrNotYours = errors.New("steward: no such proposal of yours")
	// ErrNotPending is assessing a proposal that has been decided or has
	// lapsed: there is nothing left to label.
	ErrNotPending = errors.New("steward: the proposal is no longer pending")
	// ErrAlreadyAssessed is a second assessment of one proposal.
	ErrAlreadyAssessed = errors.New("steward: the proposal is already assessed")
	// ErrNoTicket is a proposal that is about the tenant rather than a ticket.
	ErrNoTicket = errors.New("steward: only a proposal about a ticket can be assessed")
)

// ProposalFacts is a pending proposal as the server holds it.
type ProposalFacts struct {
	ID       uuid.UUID
	AgentID  uuid.UUID
	TicketID *int64
	// Kind is the pending-action kind, in the words of the Kind* constants.
	Kind    string
	Pending bool
	// Revision is the ticket's revision when the proposal was made.
	Revision int
	// Paths are the filed paths an edit proposal would leave the ticket with.
	Paths []string
	// EditedFields are the fields an edit proposal changes.
	EditedFields []string
	// Body is the text the proposal carries (an answer, review notes): what
	// approving it posts.
	Body string
	// Decision is a review proposal's verdict ("approve", "request_changes");
	// empty for other kinds.
	Decision string
}

// Proposals reads proposals; ErrNotYours when there is none.
type Proposals interface {
	Proposal(ctx context.Context, id uuid.UUID) (*ProposalFacts, error)
}

// Stewards says whether an agent is an enabled steward, read at call time.
type Stewards interface {
	IsSteward(ctx context.Context, agentID uuid.UUID) (bool, error)
}

// Config is the tenant's steward settings.
type Config struct {
	// Level is the tenant's autonomy level; Mode is derived from it.
	Level    Level
	Mode     Mode
	Tiers    TierOverrides
	DailyCap int
	// VetoMinutes is how long a Veto-tier decision waits for the owner.
	VetoMinutes int
	// WarnMinDecisions and WarnMinAgreementPct are the owner's thresholds for
	// warning on a category whose record is too thin or too low to rely on.
	WarnMinDecisions    int
	WarnMinAgreementPct int
}

// Settings reads the tenant's steward settings for the tenant in ctx.
type Settings interface {
	StewardConfig(ctx context.Context) (*Config, error)
}

// Instructions reads the owner's pinned steward instructions.
type Instructions interface {
	// PinnedStewardInstructions is the pinned version's id, nil while none is pinned.
	PinnedStewardInstructions(ctx context.Context) (*uuid.UUID, error)
}

// RunConfig is the model and effort a steward run is configured with; empty
// where its harness config sets none.
type RunConfig struct {
	Model  string
	Effort string
}

// Models reads what the steward agent's harness config gives its runs.
type Models interface {
	// StewardRunConfig is the effective config of the steward agent: its own
	// override laid over the tenant's defaults, as for a worker.
	StewardRunConfig(ctx context.Context, agentID uuid.UUID) (RunConfig, error)
}

// Assessments is where assessments are kept.
type Assessments interface {
	CreateWithinCap(ctx context.Context, in *NewRecord, since time.Time, dailyCap int) (*Record, error)
}

// Request is the steward's claim about a proposal it made.
type Request struct {
	ProposalID uuid.UUID
	Category   string
	Reason     string
	// HeadSHA is the head of the ticket's change the steward read, for the
	// categories that concern one.
	HeadSHA string
}

// Service records the server's verdict on the steward's proposals.
type Service struct {
	stewards  Stewards
	proposals Proposals
	settings  Settings
	records   Assessments
	// instructions is optional: without it assessments record no version.
	instructions Instructions
	// models is optional: without it assessments record no model.
	models  Models
	engine  *Engine
	applier *Applier
	now     func() time.Time
	logger  *slog.Logger
}

// NewService wires the service. now is injectable for tests.
func NewService(logHandler slog.Handler, stewards Stewards, proposals Proposals, settings Settings, records Assessments, engine *Engine, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{stewards: stewards, proposals: proposals, settings: settings, records: records, engine: engine, now: now, logger: slog.New(logHandler)}
}

// WithInstructions has each assessment record the steward instructions
// version pinned when it is made. The run was briefed with the version
// pinned when it started; a repin during a run is the only way the two differ.
func (s *Service) WithInstructions(i Instructions) *Service {
	s.instructions = i
	return s
}

// WithModels has each assessment record the model and effort the steward's
// harness config gives it when the assessment is made. The run started under
// the config read then; a change during a run is the only way they differ.
func (s *Service) WithModels(m Models) *Service {
	s.models = m
	return s
}

// WithApplier has live-mode assessments carried out as they are made (Auto)
// or given their veto window (Veto). Without one nothing is ever applied.
func (s *Service) WithApplier(a *Applier) *Service {
	s.applier = a
	return s
}

// Applier is the applier the service was given, for the job that applies due
// decisions; nil when there is none.
func (s *Service) Applier() *Applier { return s.applier }

// Assess runs the category's checks against the proposal and stores what the
// server concludes. The steward's own opinion of the category is an input to
// the checks and never to the verdict.
func (s *Service) Assess(ctx context.Context, agentID uuid.UUID, req *Request) (*Record, error) {
	// The caller is checked here and not only by its transport, so a second
	// one cannot let a revoked or non-steward agent record verdicts.
	isSteward, err := s.stewards.IsSteward(ctx, agentID)
	if err != nil {
		return nil, fmt.Errorf("steward: read caller: %w", err)
	}
	if !isSteward {
		return nil, ErrNotSteward
	}
	cfg, err := s.settings.StewardConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("steward: read settings: %w", err)
	}
	if cfg.Mode == ModeOff {
		return nil, ErrOff
	}
	facts, err := s.proposals.Proposal(ctx, req.ProposalID)
	if err != nil {
		return nil, err
	}
	switch {
	case facts.AgentID != agentID:
		return nil, ErrNotYours
	case facts.TicketID == nil:
		return nil, ErrNoTicket
	case !facts.Pending:
		return nil, ErrNotPending
	}
	now := s.now()

	assessment := s.engine.Assess(ctx, &Proposal{
		Category: req.Category, Kind: facts.Kind, Reason: req.Reason, Decision: facts.Decision,
		Subject: Subject{TicketID: *facts.TicketID, Revision: facts.Revision, HeadSHA: req.HeadSHA, ProposedPaths: facts.Paths, EditedFields: facts.EditedFields, Body: facts.Body},
	}, cfg.Level, cfg.Tiers)
	revision := facts.Revision
	var instructionsVersion *uuid.UUID
	if s.instructions != nil {
		if instructionsVersion, err = s.instructions.PinnedStewardInstructions(ctx); err != nil {
			return nil, fmt.Errorf("steward: read steward instructions: %w", err)
		}
	}
	var model, effort *string
	if s.models != nil {
		run, err := s.models.StewardRunConfig(ctx, agentID)
		if err != nil {
			return nil, fmt.Errorf("steward: read steward model: %w", err)
		}
		model, effort = nonEmpty(run.Model), nonEmpty(run.Effort)
	}
	rec, err := s.records.CreateWithinCap(ctx, &NewRecord{
		TicketID: facts.TicketID, PendingActionID: facts.ID, AgentID: agentID, Mode: cfg.Mode,
		Assessment: assessment, TicketRevision: &revision, HeadSHA: req.HeadSHA, CreatedAt: now, StewardInstructionsVersionID: instructionsVersion,
		Model: model, Effort: effort,
	}, now.Add(-24*time.Hour), cfg.DailyCap)
	if err != nil {
		return nil, err
	}
	s.logger.InfoContext(ctx, "steward assessed", "proposal", facts.ID, "ticket", *facts.TicketID,
		"category", rec.Category, "verdict", string(rec.Verdict), "mode", string(rec.Mode))
	if s.applier != nil {
		s.applier.Consider(ctx, rec, cfg)
	}
	return rec, nil
}

func nonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
