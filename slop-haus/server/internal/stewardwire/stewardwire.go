// Package stewardwire adapts the board's services to the steward's own narrow
// interfaces, so the steward package does not import them.
package stewardwire

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/agent"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/change"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/fleetinstructions"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/harnessconfig"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/manifest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/pendingaction"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/reviewreport"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward/assist"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward/discussionpick"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward/ticketready"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/tenantstate"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// Deps are the services the steward's checks read.
type Deps struct {
	Database *db.DB
	Logger   slog.Handler
	Pending  *pendingaction.Service
	Tickets  *ticket.Service
	Agents   *agent.Service
	Hosts    *codehost.Registry
	Settings steward.Settings
	Store    *steward.Store
	// Instructions reads the pinned steward instructions; optional.
	Instructions InstructionsReader
	// HarnessConfig resolves the steward agent's effective model; optional.
	HarnessConfig EffectiveConfigReader
	// Tenants are the tenants the apply-due job visits.
	Tenants tenantstate.Roster
}

// NewService builds the steward service over the board's own services, with
// every check a category names registered, so a category passes or fails on
// facts the server reads and never on the steward's word.
func NewService(d *Deps) *steward.Service {
	engine := newEngine(d)
	proposals := &stewardProposals{pending: d.Pending}
	applier := steward.NewApplier(d.Logger, d.Store, proposals, d.Settings, engine, &stewardApprover{pending: d.Pending}, d.Tenants, nil)
	svc := steward.NewService(d.Logger, d.Agents, proposals, d.Settings, d.Store, engine, nil).WithApplier(applier)
	if d.Instructions != nil {
		svc = svc.WithInstructions(NewPinnedInstructions(d.Instructions))
	}
	if d.HarnessConfig != nil {
		svc = svc.WithModels(NewConfigModels(d.HarnessConfig))
	}
	return svc
}

// EffectiveConfigReader is the part of the harness config service the steward needs.
type EffectiveConfigReader interface {
	Effective(ctx context.Context, agentID uuid.UUID) (harnessconfig.Effective, error)
}

// stewardHarness is the harness the steward runs on; the factory refuses any other.
const stewardHarness = "claude"

// NewConfigModels is the steward service's view of the model its agent's harness config gives it.
func NewConfigModels(r EffectiveConfigReader) steward.Models { return &configModels{config: r} }

type configModels struct{ config EffectiveConfigReader }

func (c *configModels) StewardRunConfig(ctx context.Context, agentID uuid.UUID) (steward.RunConfig, error) {
	eff, err := c.config.Effective(ctx, agentID)
	if err != nil {
		return steward.RunConfig{}, err
	}
	settings := eff.Values[stewardHarness]
	return steward.RunConfig{Model: settings["model"], Effort: settings["effort"]}, nil
}

// InstructionsReader is the part of the fleet instructions store the steward needs.
type InstructionsReader interface {
	DefaultOf(ctx context.Context, kind fleetinstructions.Kind) (*fleetinstructions.Version, error)
}

// NewPinnedInstructions is the steward service's view of the pinned steward instructions.
func NewPinnedInstructions(r InstructionsReader) steward.Instructions {
	return &pinnedInstructions{fleet: r}
}

type pinnedInstructions struct{ fleet InstructionsReader }

func (p *pinnedInstructions) PinnedStewardInstructions(ctx context.Context) (*uuid.UUID, error) {
	v, err := p.fleet.DefaultOf(ctx, fleetinstructions.KindSteward)
	if err != nil || v == nil {
		return nil, err
	}
	return &v.ID, nil
}

// NewAssist builds the assist-mode service over the same checks and the same
// approval path as NewService, so a draft is held to what a live decision is.
func NewAssist(d *Deps, store *assist.Store) *assist.Service {
	return assist.NewService(d.Logger, d.Agents, &stewardProposals{pending: d.Pending}, d.Settings, store, newEngine(d),
		&stewardApprover{pending: d.Pending}, nil)
}

func newEngine(d *Deps) *steward.Engine {
	t := &stewardTickets{tickets: d.Tickets}
	changes := NewChangeReader(d.Hosts, change.NewPostgresStore(d.Database), d.Tickets)
	comments := NewCommentReader(d.Tickets)
	protected := NewProtectedPaths(manifest.NewEditor(manifest.NewPostgresStore(d.Database)))
	picks := NewPickDiscussions(d.Tickets)
	return steward.NewEngine(d.Logger,
		steward.TicketRevUnchanged(t), steward.NoNewPaths(t), steward.TicketPreparing(t),
		steward.CIGreenOnHead(changes), steward.HeadUnchanged(changes), steward.DiffWithinScope(changes, t),
		steward.NoHumanOnlyMarker(comments), steward.EditFieldsAllowed(), steward.AnswerFromTemplate(),
		steward.ApprovedPlan(NewPlanReader(d.Pending)),
		steward.DiffSizeWithinLimit(NewDiffLines(changes), reviewLimit(d.Settings)),
		steward.SlopReviewComplete(NewReviewRunReader(reviewreport.NewPostgresStore(d.Database), d.Tickets)), steward.NoReviewRequestReason(comments),
		categoryChecks(discussionpick.CategoryID,
			categoryChecks(ticketready.CategoryID, steward.NoProtectedPath(protected, changes, t), ticketready.NoProtectedPath(t, protected)),
			discussionpick.NoProtectedPath(t, protected)),
		ticketready.TicketHasBrief(t), ticketready.BlockersRecorded(t),
		discussionpick.OptionRecommendedByWorker(picks), discussionpick.TopicNotSensitive(picks, pinnedWords(d.Instructions)))
}

// reviewLimit reads the size limit from the steward's settings; settings that
// cannot give one fail the check rather than skipping it.
func reviewLimit(s steward.Settings) steward.ReviewLimitReader {
	if l, ok := s.(steward.ReviewLimitReader); ok {
		return l
	}
	return noReviewLimit{}
}

type noReviewLimit struct{}

func (noReviewLimit) ReviewMaxLines(context.Context) (int, error) {
	return 0, errors.New("the steward's settings give no review size limit")
}

// stewardApprover approves through the board's own approval path, naming the
// outcomes the applier acts on.
type stewardApprover struct{ pending *pendingaction.Service }

func (a *stewardApprover) ApproveForSteward(ctx context.Context, id uuid.UUID) error {
	_, err := a.pending.ApproveForSteward(ctx, id)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, pendingaction.ErrNotPending):
		return fmt.Errorf("%w: %w", steward.ErrNotPending, err)
	case errors.Is(err, pendingaction.ErrStale), errors.Is(err, pendingaction.ErrVoided), errors.Is(err, pendingaction.ErrRefused):
		return fmt.Errorf("%w: %w", steward.ErrApprovalRefused, err)
	}
	return err
}

type stewardProposals struct{ pending *pendingaction.Service }

func (p *stewardProposals) Proposal(ctx context.Context, id uuid.UUID) (*steward.ProposalFacts, error) {
	a, err := p.pending.Get(ctx, id)
	if errors.Is(err, pendingaction.ErrNotFound) {
		return nil, steward.ErrNotYours
	}
	if err != nil {
		return nil, err
	}
	facts := &steward.ProposalFacts{ID: a.ID, AgentID: a.AgentID, TicketID: a.TicketID, Kind: string(a.Kind),
		Pending: a.State == pendingaction.StatePending, Revision: a.Basis.Revision, Decision: string(a.Payload.Decision), Body: a.Payload.Body}
	if a.Payload.Edit != nil {
		facts.Paths = a.Payload.Edit.Paths
		facts.EditedFields = a.Payload.Edit.Fields()
	}
	return facts, nil
}

type stewardTickets struct{ tickets *ticket.Service }

func (t *stewardTickets) Revision(ctx context.Context, id int64) (int, error) {
	found, err := t.tickets.Get(ctx, id)
	if err != nil {
		return 0, fmt.Errorf("read ticket %d: %w", id, err)
	}
	return found.Revision, nil
}

func (t *stewardTickets) Paths(ctx context.Context, id int64) ([]string, error) {
	found, err := t.tickets.Get(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("read ticket %d: %w", id, err)
	}
	paths := make([]string, len(found.Paths))
	for i, p := range found.Paths {
		paths[i] = p.Path
	}
	return paths, nil
}

func (t *stewardTickets) Status(ctx context.Context, id int64) (string, error) {
	found, err := t.tickets.Get(ctx, id)
	if err != nil {
		return "", fmt.Errorf("read ticket %d: %w", id, err)
	}
	return string(found.Status), nil
}

func (t *stewardTickets) Body(ctx context.Context, id int64) (string, error) {
	found, err := t.tickets.Get(ctx, id)
	if err != nil {
		return "", fmt.Errorf("read ticket %d: %w", id, err)
	}
	return found.Body, nil
}

func (t *stewardTickets) Blockers(ctx context.Context, id int64) ([]int64, error) {
	found, err := t.tickets.Get(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("read ticket %d: %w", id, err)
	}
	return found.DependsOn, nil
}

var _ ticketready.Tickets = (*stewardTickets)(nil)

// categoryChecks lets a category's own checks share a name with the engine's
// default (the engine keys checks by name): the check named for category runs
// for it, and base for every other.
func categoryChecks(category string, base steward.Check, own steward.Check) steward.Check {
	return categoryCheck{category: category, base: base, own: own}
}

type categoryCheck struct {
	category string
	base     steward.Check
	own      steward.Check
}

func (c categoryCheck) Name() string { return c.base.Name() }

func (c categoryCheck) Run(ctx context.Context, s *steward.Subject) (steward.Evidence, error) {
	if s.Category == c.category {
		return c.own.Run(ctx, s)
	}
	return c.base.Run(ctx, s)
}
