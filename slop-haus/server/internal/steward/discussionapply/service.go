// Package discussionapply lets the steward post a routine discussion label's
// templated answer itself at level 3 and above, following the category's
// effective tier (auto at once, veto after the veto window). It decides no
// tier: EffectiveTier does, so below level 3 nothing is ever scheduled.
package discussionapply

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/jobs"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward/discussionanswer"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward/discussionlabel"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/tenantstate"
)

// Actor is who a steward-posted answer is recorded as.
const Actor = "steward"

// VetoReason is the reason recorded when the owner vetoes a scheduled answer.
const VetoReason = "vetoed within the steward's window"

// dueBatch bounds the answers one pass posts per tenant.
const dueBatch = 100

// missedBatch bounds the unscheduled labels one pass reads. It is larger than
// dueBatch because labels left to the owner stay in the list until they age
// out, and must not crowd out the ones that are actionable.
const missedBatch = 1000

// UnscheduledWindow is how far back the sweep looks for routine labels that
// were never scheduled. Past it the owner is left the discussion; a longer
// window would re-read labels the owner keeps for themselves for longer.
const UnscheduledWindow = 24 * time.Hour

// DefaultInterval is how often due veto-tier answers are looked for; it only
// bounds how late one is posted after its deadline.
const DefaultInterval = 30 * time.Second

var (
	// ErrNotScheduled is vetoing a label the steward is not about to apply.
	ErrNotScheduled = discussionanswer.ErrNotScheduled
	// ErrWindowClosed is vetoing after the window ran out or the answer was decided.
	ErrWindowClosed = discussionanswer.ErrWindowClosed
)

// Settings reads the tenant's steward settings.
type Settings interface {
	StewardConfig(ctx context.Context) (*steward.Config, error)
}

// Schedules is where plans are kept; *Store is one.
type Schedules interface {
	Create(ctx context.Context, labelID uuid.UUID, tier steward.Tier, applyAt, now time.Time) (*Schedule, error)
	Get(ctx context.Context, labelID uuid.UUID) (*Schedule, error)
	Void(ctx context.Context, labelID uuid.UUID, at time.Time, reason string) error
	ListDue(ctx context.Context, now time.Time, limit int) ([]Due, error)
	ListUnscheduled(ctx context.Context, since time.Time, limit int) ([]Unscheduled, error)
}

// Answers posts and refuses answers through the owner's path;
// *discussionanswer.Service is one. Its checks re-run on every Approve.
type Answers interface {
	Approve(ctx context.Context, r *discussionanswer.Round, actor string) (*discussionanswer.Decision, error)
	Reject(ctx context.Context, r *discussionanswer.Round, reason, actor string) (*discussionanswer.Decision, error)
}

// Decisions reads what was decided on a label.
type Decisions interface {
	ForLabel(ctx context.Context, labelID uuid.UUID) (*discussionanswer.Decision, error)
}

// Labels reads labels by round.
type Labels interface {
	Get(ctx context.Context, ticketID int64, attempt, round int) (*discussionlabel.Record, error)
}

// Deps are what the service is built from.
type Deps struct {
	Settings  Settings
	Schedules Schedules
	Answers   Answers
	Decisions Decisions
	Labels    Labels
	Roster    tenantstate.Roster
	// Expirer, when set, runs on every sweep: the pass that already visits each
	// tenant also times out the labels the steward never completed.
	Expirer interface {
		ExpireOnce(ctx context.Context) error
	}
	// Now is injectable for tests; nil means time.Now.
	Now func() time.Time
}

// Service schedules and posts the steward's own discussion answers.
type Service struct {
	Deps
	logger *slog.Logger
}

func NewService(logHandler slog.Handler, d *Deps) *Service {
	s := &Service{Deps: *d, logger: slog.New(logHandler)}
	if s.Now == nil {
		s.Now = time.Now
	}
	return s
}

func roundOf(rec *discussionlabel.Record) *discussionanswer.Round {
	return &discussionanswer.Round{TicketID: rec.TicketID, Attempt: rec.Attempt, Round: rec.Round}
}

// tierOf is the category's effective tier. Every decision here goes through
// steward.EffectiveTier; this feature has no rule of its own about levels.
func tierOf(category string, cfg *steward.Config) steward.Tier {
	cat, ok := steward.Lookup(category)
	if !ok {
		return steward.TierHuman
	}
	return steward.EffectiveTier(cat, cfg.Level, cfg.Tiers)
}

// Consider is called for a just-completed label. A routine one whose category
// is auto or veto is scheduled, and an auto one is posted now. Anything else
// (yours, not assessed, a human tier, a level that applies nothing) is left to
// the owner. It never fails the label; what goes wrong leaves the discussion
// pending for the owner.
func (s *Service) Consider(ctx context.Context, rec *discussionlabel.Record) {
	if err := s.consider(ctx, rec); err != nil {
		s.logger.WarnContext(ctx, "steward could not act on a discussion label", "ticket", rec.TicketID, "label", rec.ID, "error", err)
	}
}

func (s *Service) consider(ctx context.Context, rec *discussionlabel.Record) error {
	if rec.Label != discussionlabel.LabelRoutine {
		return nil
	}
	cfg, err := s.Settings.StewardConfig(ctx)
	if err != nil {
		return fmt.Errorf("read settings: %w", err)
	}
	now := s.Now()
	var applyAt time.Time
	tier := tierOf(rec.Category, cfg)
	switch tier {
	case steward.TierAuto:
		applyAt = now
	case steward.TierVeto:
		applyAt = now.Add(time.Duration(cfg.VetoMinutes) * time.Minute)
	default:
		return nil
	}
	sch, err := s.Schedules.Create(ctx, rec.ID, tier, applyAt, now)
	if err != nil {
		return err
	}
	if tier == steward.TierAuto && sch.VoidedAt == nil && !sch.ApplyAt.After(now) {
		return s.post(ctx, rec.ID, tier, roundOf(rec))
	}
	return nil
}

// post carries out one due schedule. The checks re-run in Approve; one that no
// longer holds, or a ticket that moved, voids the schedule and leaves the
// discussion to the owner. The tier is read again, so an owner who lowered
// the level or the category's tier since the label stops it.
// A schedule made at auto is not posted once the category has moved to veto:
// the window the owner now wants was never given.
func (s *Service) post(ctx context.Context, labelID uuid.UUID, scheduled steward.Tier, r *discussionanswer.Round) error {
	// Read per post, not once per sweep, so a setting changed while the sweep
	// runs is honoured by the next answer.
	cfg, err := s.Settings.StewardConfig(ctx)
	if err != nil {
		return fmt.Errorf("discussionapply: read settings: %w", err)
	}
	rec, err := s.Labels.Get(ctx, r.TicketID, r.Attempt, r.Round)
	if err != nil {
		return err
	}
	switch t := tierOf(rec.Category, cfg); {
	case t != steward.TierAuto && t != steward.TierVeto:
		return s.Schedules.Void(ctx, labelID, s.Now(), "the category is no longer applied by the steward")
	case scheduled == steward.TierAuto && t == steward.TierVeto:
		return s.Schedules.Void(ctx, labelID, s.Now(), "the category now has a veto window the schedule did not give")
	}
	// The post has no signed-in person behind it (the sweep, or a label
	// completing in a worker's request): it is the server's own act.
	_, err = s.Answers.Approve(authn.AsServer(ctx), r, Actor)
	switch {
	case err == nil, errors.Is(err, discussionanswer.ErrAlreadyDecided):
		return nil
	case errors.Is(err, discussionanswer.ErrStale), errors.Is(err, discussionanswer.ErrChecksFailed),
		errors.Is(err, discussionanswer.ErrNotRoutine), errors.Is(err, discussionanswer.ErrOff):
		return s.Schedules.Void(ctx, labelID, s.Now(), err.Error())
	}
	// Possibly transient; the sweep tries again.
	return err
}

// Veto is the owner stopping a scheduled answer inside its window. It is
// recorded as a rejection of the draft, so it counts as disagreement, and it
// claims the label's decision so the steward cannot post it afterwards.
func (s *Service) Veto(ctx context.Context, r *discussionanswer.Round, actor string) (*discussionanswer.Decision, error) {
	rec, err := s.Labels.Get(ctx, r.TicketID, r.Attempt, r.Round)
	if err != nil {
		return nil, err
	}
	sch, err := s.Schedules.Get(ctx, rec.ID)
	if errors.Is(err, ErrNoSchedule) {
		return nil, ErrNotScheduled
	}
	if err != nil {
		return nil, err
	}
	if sch.VoidedAt != nil {
		return nil, ErrNotScheduled
	}
	if sch.Tier != steward.TierVeto || !s.Now().Before(sch.ApplyAt) {
		return nil, ErrWindowClosed
	}
	dec, err := s.Answers.Reject(ctx, r, VetoReason, actor)
	if errors.Is(err, discussionanswer.ErrAlreadyDecided) {
		return nil, ErrWindowClosed
	}
	return dec, err
}

// ApplyDue first schedules the routine labels the label-time call missed, then
// posts the tenant's due schedules. One failing does not stop the others; the
// errors are joined.
func (s *Service) ApplyDue(ctx context.Context) error {
	errs := []error{s.scheduleMissed(ctx)}
	due, err := s.Schedules.ListDue(ctx, s.Now(), dueBatch)
	if err != nil {
		return errors.Join(append(errs, err)...)
	}
	for i := range due {
		d := &due[i]
		r := &discussionanswer.Round{TicketID: d.TicketID, Attempt: d.Attempt, Round: d.Round}
		if err := s.post(ctx, d.LabelID, d.Tier, r); err != nil {
			s.logger.WarnContext(ctx, "steward could not apply a due discussion answer", "ticket", d.TicketID, "error", err)
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// scheduleMissed gives the labels Consider never scheduled the schedule they
// would have had, as if they had just completed: a veto window starts now, not
// when the label was made, so the owner still gets all of it.
func (s *Service) scheduleMissed(ctx context.Context) error {
	missed, err := s.Schedules.ListUnscheduled(ctx, s.Now().Add(-UnscheduledWindow), missedBatch)
	if err != nil {
		return err
	}
	var errs []error
	for i := range missed {
		m := &missed[i]
		rec, err := s.Labels.Get(ctx, m.TicketID, m.Attempt, m.Round)
		if err == nil {
			err = s.consider(ctx, rec)
		}
		if err != nil {
			s.logger.WarnContext(ctx, "steward could not schedule a discussion label", "ticket", m.TicketID, "error", err)
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// SweepOnce is ApplyDue for every tenant, then the label timeout.
func (s *Service) SweepOnce(ctx context.Context) error {
	err := tenantstate.Each(ctx, s.Roster, func(ctx context.Context, _ uuid.UUID) error { return s.ApplyDue(ctx) })
	if s.Expirer != nil {
		err = errors.Join(err, s.Expirer.ExpireOnce(ctx))
	}
	return err
}

// Job is the periodic sweep of due answers. A non-positive interval gives no
// job.
func (s *Service) Job(interval time.Duration) *jobs.Job {
	if interval <= 0 {
		return nil
	}
	return &jobs.Job{Name: "steward_discussion_apply_due", Interval: interval, Run: s.SweepOnce}
}
