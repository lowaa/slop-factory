// Package discussionlabel classifies each discussion round as routine (a
// template category covers it) or the owner's, and stores the label.
package discussionlabel

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/jobs"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/tenantstate"
)

// ErrOff is classifying while the steward is off for the tenant.
var ErrOff = steward.ErrOff

// Question is the worker's discussion as the classifier reads it.
type Question struct {
	Target
	Question string
	Handoff  string
}

// Classification is the classifier's claim. Label is routine or yours; the
// server never takes it on trust.
type Classification struct {
	Label     Label
	Category  string
	Reason    string
	Reasoning string
}

// Classifier asks the steward to classify a discussion in the same call. It is
// a seam for tests: the steward agent is asynchronous, so in production the
// service has none, queues each round as pending and the agent completes it
// through Submit.
type Classifier interface {
	Classify(ctx context.Context, q *Question) (*Classification, error)
}

// Settings reads the tenant's steward settings.
type Settings interface {
	StewardConfig(ctx context.Context) (*steward.Config, error)
}

// Labels is where labels are kept.
type Labels interface {
	Claim(ctx context.Context, t *Target, now, since time.Time, dailyCap int) (*Record, bool, error)
	Complete(ctx context.Context, id uuid.UUID, o *Outcome) (*Record, error)
	CompleteAgainst(ctx context.Context, id uuid.UUID, t *Target, o *Outcome) (*Record, error)
	ExpirePending(ctx context.Context, cutoff time.Time, reason string) (int, error)
	ClaimNotAssessed(ctx context.Context, id uuid.UUID, t *Target, now, since time.Time, dailyCap int) (*Record, error)
	Get(ctx context.Context, ticketID int64, attempt, round int) (*Record, error)
	Skip(ctx context.Context, t *Target, now time.Time, reason string) (*Record, error)
	Unlabelled(ctx context.Context, limit int) ([]Round, error)
}

// RoundQueuer queues a round for labelling as the discussion stands now: it
// reads the question itself. It is the hook that labels a round as it opens.
type RoundQueuer interface {
	QueueRound(ctx context.Context, r Round) error
}

// Assessor runs a proposal's checks; *steward.Engine is one.
type Assessor interface {
	Assess(ctx context.Context, p *steward.Proposal, level steward.Level, overrides steward.TierOverrides) *steward.Assessment
}

// Observer is told of each label as it completes, so the steward can act on a
// routine one. It must not fail the label: what goes wrong is its to handle.
type Observer interface {
	Consider(ctx context.Context, rec *Record)
}

// Service labels discussions.
type Service struct {
	observer   Observer
	classifier Classifier
	assessor   Assessor
	settings   Settings
	labels     Labels
	roster     tenantstate.Roster
	rounds     RoundQueuer
	now        func() time.Time
	logger     *slog.Logger
}

// NewService wires the service. now is injectable for tests.
func NewService(logHandler slog.Handler, c Classifier, a Assessor, s Settings, l Labels, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{classifier: c, assessor: a, settings: s, labels: l, now: now, logger: slog.New(logHandler)}
}

// WithObserver has o told of every label this service completes.
func (s *Service) WithObserver(o Observer) *Service {
	s.observer = o
	return s
}

// WithRoster has ExpireOnce sweep every tenant on it.
func (s *Service) WithRoster(r tenantstate.Roster) *Service {
	s.roster = r
	return s
}

// WithRounds has SweepUnlabelled queue rounds through q.
func (s *Service) WithRounds(q RoundQueuer) *Service {
	s.rounds = q
	return s
}

// complete stores the outcome and tells the observer of the finished label.
func (s *Service) complete(ctx context.Context, id uuid.UUID, o *Outcome) (*Record, error) {
	rec, err := s.labels.Complete(ctx, id, o)
	if err == nil && s.observer != nil {
		s.observer.Consider(ctx, rec)
	}
	return rec, err
}

// Reasons a round is queued without a classification run.
const (
	OffReason          = "the steward is off"
	NoClassifierReason = "no steward classifier is available"
)

// LabelTimeout is how long the steward agent has to label a pending round: a
// little over one steward sweep. Past it the round is not_assessed.
const LabelTimeout = 30 * time.Minute

// TimedOutReason is the reason of a round the steward did not label in time.
const TimedOutReason = "steward did not label it in time"

// Queue is how a discussion enters labelling. With a classifier it is Label.
// Without one the round is claimed as pending, counted against the cap, for
// the steward agent to complete through Submit. Either way a round that
// cannot be labelled (the steward is off) gets a not_assessed label saying
// why rather than none, and is never guessed as routine. Idempotent on the
// round.
func (s *Service) Queue(ctx context.Context, q *Question) (*Record, error) {
	if s.classifier == nil {
		return s.request(ctx, q)
	}
	rec, err := s.Label(ctx, q)
	if errors.Is(err, ErrOff) {
		return s.labels.Skip(ctx, &q.Target, s.now(), OffReason)
	}
	return rec, err
}

func (s *Service) request(ctx context.Context, q *Question) (*Record, error) {
	cfg, err := s.config(ctx)
	if errors.Is(err, ErrOff) {
		return s.labels.Skip(ctx, &q.Target, s.now(), OffReason)
	}
	if err != nil {
		return nil, err
	}
	now := s.now()
	rec, _, err := s.labels.Claim(ctx, &q.Target, now, now.Add(-24*time.Hour), cfg.DailyCap)
	return rec, err
}

// Submit completes a pending round with the steward agent's claim,
// as the discussion stands now in q. The server decides the label from the
// claim exactly as it does for a classifier: only a category whose every
// check passes is routine. Idempotent: a round already labelled is returned
// as it is. A round whose revision or head moved since it was queued is
// refused with ErrStale, and one with no label with ErrNotFound.
func (s *Service) Submit(ctx context.Context, q *Question, c *Classification) (*Record, error) {
	cfg, err := s.config(ctx)
	if err != nil {
		return nil, err
	}
	cur, err := s.labels.Get(ctx, q.TicketID, q.Attempt, q.Round)
	if err != nil {
		return nil, err
	}
	if cur.Label != LabelPending {
		return cur, nil
	}
	if cur.TicketRevision == nil || *cur.TicketRevision != q.Revision || !sameHead(cur.HeadSHA, q.HeadSHA) {
		return nil, ErrStale
	}
	rec, err := s.labels.CompleteAgainst(ctx, cur.ID, &q.Target, s.decideFrom(ctx, q, cfg, c))
	if errors.Is(err, ErrNotPending) {
		// Lost to a racing delivery, or the label timed out and was queued
		// again meanwhile: the label as it stands is the answer, and the
		// winner has already told the observer.
		return s.labels.Get(ctx, q.TicketID, q.Attempt, q.Round)
	}
	if err == nil && s.observer != nil {
		s.observer.Consider(ctx, rec)
	}
	return rec, err
}

func sameHead(label *string, head string) bool {
	if label == nil {
		return head == ""
	}
	return *label == head
}

// ExpireOnce turns the labels the steward left pending past LabelTimeout into
// not_assessed, for every tenant.
func (s *Service) ExpireOnce(ctx context.Context) error {
	return tenantstate.Each(ctx, s.roster, func(ctx context.Context, _ uuid.UUID) error {
		_, err := s.labels.ExpirePending(ctx, s.now().Add(-LabelTimeout), TimedOutReason)
		return err
	})
}

// SweepBatch bounds the rounds one tenant's sweep queues, so a backlog of old
// rounds is worked through over several passes rather than one long one.
const SweepBatch = 50

// SweepUnlabelled queues every open round with no label, for every tenant
// with the steward on: it repairs a hook call that was lost or failed, and
// the rounds opened before labels existed. Queueing is idempotent on the
// round, so it never doubles the hook, and it counts against the daily cap
// like any other classification.
func (s *Service) SweepUnlabelled(ctx context.Context) error {
	if s.rounds == nil {
		return nil
	}
	return tenantstate.Each(ctx, s.roster, func(ctx context.Context, _ uuid.UUID) error {
		if _, err := s.config(ctx); errors.Is(err, ErrOff) {
			return nil
		} else if err != nil {
			return err
		}
		missing, err := s.labels.Unlabelled(ctx, SweepBatch)
		if err != nil {
			return err
		}
		var errs []error
		for _, r := range missing {
			if err := s.rounds.QueueRound(ctx, r); err != nil {
				s.logger.WarnContext(ctx, "steward could not label a discussion round", "ticket", r.TicketID, "attempt", r.Attempt, "round", r.Round, "error", err)
				errs = append(errs, err)
			}
		}
		return errors.Join(errs...)
	})
}

// ExpiryInterval is how often pending labels are looked at for the timeout.
const ExpiryInterval = time.Minute

// Job is the periodic timeout of unlabelled rounds. A non-positive interval
// or no roster gives no job.
func (s *Service) Job(interval time.Duration) *jobs.Job {
	if interval <= 0 || s.roster == nil {
		return nil
	}
	return &jobs.Job{Name: "steward_discussion_label_expire", Interval: interval, Run: s.ExpireOnce}
}

// Label classifies the round once and stores the label. At level 0 it runs
// nothing (ErrOff). The round is claimed before the classifier runs: a repeat
// or racing call gets the existing label and runs nothing. When the daily cap
// is spent no classifier runs and the label is not_assessed.
func (s *Service) Label(ctx context.Context, q *Question) (*Record, error) {
	cfg, err := s.config(ctx)
	if err != nil {
		return nil, err
	}
	now := s.now()
	rec, claimed, err := s.labels.Claim(ctx, &q.Target, now, now.Add(-24*time.Hour), cfg.DailyCap)
	if err != nil || !claimed {
		return rec, err
	}
	return s.complete(ctx, rec.ID, s.decide(ctx, q, cfg))
}

// AssessNow runs the classification for a round whose label is not_assessed
// (or whose earlier run never finished),
// as the discussion stands now: the label's revision and head move with it.
func (s *Service) AssessNow(ctx context.Context, q *Question) (*Record, error) {
	cfg, err := s.config(ctx)
	if err != nil {
		return nil, err
	}
	cur, err := s.labels.Get(ctx, q.TicketID, q.Attempt, q.Round)
	if err != nil {
		return nil, err
	}
	now := s.now()
	if !cur.Assessable(now) {
		return nil, ErrNotAssessable
	}
	rec, err := s.labels.ClaimNotAssessed(ctx, cur.ID, &q.Target, now, now.Add(-24*time.Hour), cfg.DailyCap)
	if err != nil || s.classifier == nil {
		return rec, err
	}
	return s.complete(ctx, rec.ID, s.decide(ctx, q, cfg))
}

func (s *Service) config(ctx context.Context) (*steward.Config, error) {
	cfg, err := s.settings.StewardConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("discussionlabel: read settings: %w", err)
	}
	if cfg.Level < steward.LevelShadow || cfg.Mode == steward.ModeOff {
		return nil, ErrOff
	}
	return cfg, nil
}

// decide turns the classifier's claim into the label the server stands
// behind. Anything short of a classifier that says routine, for a category
// whose every check passes on a rendered template, is yours.
func (s *Service) decide(ctx context.Context, q *Question, cfg *steward.Config) *Outcome {
	c, err := s.classifier.Classify(ctx, q)
	if err != nil {
		s.logger.WarnContext(ctx, "steward discussion classification failed", "ticket", q.TicketID, "error", err)
		return yours("the classification could not be read", "")
	}
	return s.decideFrom(ctx, q, cfg, c)
}

// decideFrom is decide for a claim already in hand.
func (s *Service) decideFrom(ctx context.Context, q *Question, cfg *steward.Config, c *Classification) *Outcome {
	if c == nil {
		return yours("the classification could not be read", "")
	}
	switch c.Label {
	case LabelYours:
		return yours(c.Reason, c.Reasoning)
	case LabelRoutine:
	default:
		return yours(fmt.Sprintf("the classifier gave %q, which is not a label", c.Label), c.Reasoning)
	}
	body, ok := steward.RenderAnswer(c.Category, &steward.Subject{TicketID: q.TicketID, HeadSHA: q.HeadSHA})
	if !ok {
		return yours(fmt.Sprintf("no answer template applies to %q on this discussion", c.Category), c.Reasoning)
	}
	a := s.assessor.Assess(ctx, &steward.Proposal{
		Category: c.Category, Kind: steward.KindDiscussionAnswer, Reason: c.Reason,
		Subject: steward.Subject{TicketID: q.TicketID, Revision: q.Revision, HeadSHA: q.HeadSHA, Body: body},
	}, cfg.Level, cfg.Tiers)
	// The verdict is not read: below level 3 it is needs_human even when
	// every check passes. Only the checks decide routine.
	if failed := failedChecks(a); failed != "" {
		o := yours("checks failed: "+failed, c.Reasoning)
		if a != nil {
			o.Evidence = a.Evidence
		}
		return o
	}
	return &Outcome{Label: LabelRoutine, Category: c.Category, Reason: c.Reason, Reasoning: c.Reasoning, Evidence: a.Evidence}
}

func yours(reason, reasoning string) *Outcome {
	return &Outcome{Label: LabelYours, Reason: reason, Reasoning: reasoning}
}

func failedChecks(a *steward.Assessment) string {
	if a == nil || len(a.Evidence) == 0 {
		return "no check ran"
	}
	var parts []string
	for _, ev := range a.Evidence {
		if !ev.OK {
			parts = append(parts, strings.TrimSpace(ev.Check+": "+ev.Detail))
		}
	}
	return strings.Join(parts, "; ")
}
