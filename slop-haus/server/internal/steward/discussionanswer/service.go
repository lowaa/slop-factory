// Package discussionanswer turns a discussion's label into what the owner
// sees and decides: the templated draft, and approve, edit or reject.
package discussionanswer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward/discussionlabel"
)

var (
	// ErrOff is approving while the steward is off for the tenant.
	ErrOff = steward.ErrOff
	// ErrNoDraft is a label with no draft to decide: not routine, or not yet
	// assessed.
	ErrNoDraft = errors.New("discussionanswer: the label has no draft")
	// ErrNotRoutine is approving a label that is not routine. Only a routine
	// label carries a template the server will post.
	ErrNotRoutine = errors.New("discussionanswer: only a routine label can be approved")
	// ErrStale is deciding a draft whose ticket moved since the label was
	// made, or whose round is no longer the open discussion.
	ErrStale = errors.New("discussionanswer: the draft is stale")
	// ErrChecksFailed is approving a draft whose checks no longer pass.
	ErrChecksFailed = errors.New("discussionanswer: the draft's checks no longer pass")
	// ErrUnavailable is assessing now where no classifier is configured.
	ErrUnavailable = errors.New("discussionanswer: assessing is not available")
	// ErrEmpty is an edit with no text or a reject with no reason.
	ErrEmpty = errors.New("discussionanswer: text is required")
)

// Round names the discussion round a decision is about.
type Round struct {
	TicketID int64
	Attempt  int
	Round    int
}

// Open is the discussion the ticket is waiting on now.
type Open struct {
	Attempt  int
	Round    int
	Revision int
	HeadSHA  string
	// Question and Handoff are what the worker asked and left behind, as the
	// classifier reads them.
	Question string
	Handoff  string
}

// Discussions reads the attempt's open discussion, or returns ErrNoOpen.
type Discussions interface {
	Open(ctx context.Context, ticketID int64, attempt int) (*Open, error)
}

// ErrNoOpen is a ticket with no open discussion.
var ErrNoOpen = errors.New("discussionanswer: the ticket has no open discussion")

// Labels reads the labels the draft is made from.
type Labels interface {
	Get(ctx context.Context, ticketID int64, attempt, round int) (*discussionlabel.Record, error)
}

// Labeller re-runs a label's classification; *discussionlabel.Service is one.
type Labeller interface {
	AssessNow(ctx context.Context, q *discussionlabel.Question) (*discussionlabel.Record, error)
}

// Assessor runs a proposal's checks; *steward.Engine is one.
type Assessor interface {
	Assess(ctx context.Context, p *steward.Proposal, level steward.Level, overrides steward.TierOverrides) *steward.Assessment
}

// Settings reads the tenant's steward settings.
type Settings interface {
	StewardConfig(ctx context.Context) (*steward.Config, error)
}

// Decisions is where the owner's decisions are kept.
type Decisions interface {
	Claim(ctx context.Context, d *Decision) (*Decision, error)
	MarkPosted(ctx context.Context, id uuid.UUID, at time.Time) error
	ForLabel(ctx context.Context, labelID uuid.UUID) (*Decision, error)
}

// Poster posts a body as the owner on the ticket's attempt. Posting twice with
// one key posts once, so a post whose outcome is unknown can be repeated.
type Poster interface {
	Post(ctx context.Context, ticketID int64, attempt int, body, key string) error
}

// Draft is the owner's view of a round: what the steward concluded and the
// text it would post.
type Draft struct {
	Label    *discussionlabel.Record
	Category string
	// Body is the templated answer; empty unless the label is routine.
	Body     string
	Evidence []steward.Evidence
	// Decision is set once the owner has decided.
	Decision *Decision
}

// Service drafts and decides discussion answers.
type Service struct {
	labels      Labels
	labeller    Labeller
	discussions Discussions
	assessor    Assessor
	settings    Settings
	decisions   Decisions
	poster      Poster
	now         func() time.Time
	logger      *slog.Logger
}

// Deps are what the service is built from.
type Deps struct {
	Labels      Labels
	Labeller    Labeller
	Discussions Discussions
	Assessor    Assessor
	Settings    Settings
	Decisions   Decisions
	Poster      Poster
	// Now is injectable for tests; nil means time.Now.
	Now func() time.Time
}

func NewService(logHandler slog.Handler, d *Deps) *Service {
	now := d.Now
	if now == nil {
		now = time.Now
	}
	return &Service{labels: d.Labels, labeller: d.Labeller, discussions: d.Discussions, assessor: d.Assessor,
		settings: d.Settings, decisions: d.Decisions, poster: d.Poster, now: now, logger: slog.New(logHandler)}
}

// Draft is the label, category, templated answer and evidence of one round.
func (s *Service) Draft(ctx context.Context, r *Round) (*Draft, error) {
	rec, err := s.labels.Get(ctx, r.TicketID, r.Attempt, r.Round)
	if err != nil {
		return nil, err
	}
	d := &Draft{Label: rec, Category: rec.Category, Evidence: rec.Evidence}
	if rec.Label == discussionlabel.LabelRoutine {
		d.Body, _ = steward.RenderAnswer(rec.Category, subjectOf(rec))
	}
	switch dec, err := s.decisions.ForLabel(ctx, rec.ID); {
	case err == nil:
		d.Decision = dec
	case !errors.Is(err, ErrNoDecision):
		return nil, err
	}
	return d, nil
}

// AssessNow re-runs the classification of a not-assessed round against the
// ticket as it is now.
func (s *Service) AssessNow(ctx context.Context, r *Round) (*discussionlabel.Record, error) {
	if s.labeller == nil {
		return nil, ErrUnavailable
	}
	open, err := s.openRound(ctx, r)
	if err != nil {
		return nil, err
	}
	return s.labeller.AssessNow(ctx, &discussionlabel.Question{
		Target:   discussionlabel.Target{TicketID: r.TicketID, Attempt: r.Attempt, Round: r.Round, Revision: open.Revision, HeadSHA: open.HeadSHA},
		Question: open.Question, Handoff: open.Handoff,
	})
}

// Approve posts the category's template as the owner. The checks run again
// against the label's revision and head, so a fact that moved since the label
// was made fails them; and the label itself is refused when the ticket's
// revision or head differ from what it was made against.
func (s *Service) Approve(ctx context.Context, r *Round, actor string) (*Decision, error) {
	cfg, err := s.settings.StewardConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("discussionanswer: read settings: %w", err)
	}
	off := cfg.Level < steward.LevelShadow || cfg.Mode == steward.ModeOff
	rec, err := s.labels.Get(ctx, r.TicketID, r.Attempt, r.Round)
	if err != nil {
		if off {
			return nil, ErrOff
		}
		return nil, err
	}
	// Finishing a claim already made is not a new decision, so turning the
	// steward off does not strand an answer whose post is still owed.
	if rec.Label == discussionlabel.LabelRoutine {
		if dec, err := s.finishOwed(ctx, r, rec.ID, ActionApprove, ""); dec != nil || err != nil {
			return dec, err
		}
	}
	if off {
		return nil, ErrOff
	}
	if rec.Label != discussionlabel.LabelRoutine {
		return nil, ErrNotRoutine
	}
	open, err := s.openRound(ctx, r)
	if err != nil {
		return nil, err
	}
	if rec.TicketRevision == nil || *rec.TicketRevision != open.Revision || deref(rec.HeadSHA) != open.HeadSHA {
		return nil, ErrStale
	}
	subject := subjectOf(rec)
	body, ok := steward.RenderAnswer(rec.Category, subject)
	if !ok {
		return nil, fmt.Errorf("%w: no template for %q", ErrChecksFailed, rec.Category)
	}
	subject.Body = body
	a := s.assessor.Assess(ctx, &steward.Proposal{Category: rec.Category, Kind: steward.KindDiscussionAnswer, Reason: rec.Reason, Subject: *subject}, cfg.Level, cfg.Tiers)
	if failed := failedChecks(a); failed != "" {
		return nil, fmt.Errorf("%w: %s", ErrChecksFailed, failed)
	}
	return s.decide(ctx, r, &Decision{LabelID: rec.ID, Action: ActionApprove, OriginalBody: body, FinalBody: body, Actor: actor})
}

// Edit posts the owner's own text instead of the draft. The draft, if the
// label had one, is kept beside it.
func (s *Service) Edit(ctx context.Context, r *Round, text, actor string) (*Decision, error) {
	if strings.TrimSpace(text) == "" {
		return nil, ErrEmpty
	}
	rec, err := s.decidable(ctx, r)
	if err != nil {
		return nil, err
	}
	if dec, err := s.finishOwed(ctx, r, rec.ID, ActionEdit, text); dec != nil || err != nil {
		return dec, err
	}
	original, err := s.openDraft(ctx, r, rec)
	if err != nil {
		return nil, err
	}
	return s.decide(ctx, r, &Decision{LabelID: rec.ID, Action: ActionEdit, OriginalBody: original, FinalBody: text, Actor: actor})
}

// Reject dismisses the draft; nothing is posted.
func (s *Service) Reject(ctx context.Context, r *Round, reason, actor string) (*Decision, error) {
	if strings.TrimSpace(reason) == "" {
		return nil, ErrEmpty
	}
	rec, err := s.decidable(ctx, r)
	if err != nil {
		return nil, err
	}
	original, err := s.openDraft(ctx, r, rec)
	if err != nil {
		return nil, err
	}
	return s.decide(ctx, r, &Decision{LabelID: rec.ID, Action: ActionReject, OriginalBody: original, Reason: reason, Actor: actor})
}

// decidable reads the label of a round that has been assessed.
func (s *Service) decidable(ctx context.Context, r *Round) (*discussionlabel.Record, error) {
	rec, err := s.labels.Get(ctx, r.TicketID, r.Attempt, r.Round)
	if err != nil {
		return nil, err
	}
	if rec.Label != discussionlabel.LabelRoutine && rec.Label != discussionlabel.LabelYours {
		return nil, ErrNoDraft
	}
	return rec, nil
}

// openDraft requires the round to still be the open discussion and returns
// the draft text the label carried.
func (s *Service) openDraft(ctx context.Context, r *Round, rec *discussionlabel.Record) (string, error) {
	if _, err := s.openRound(ctx, r); err != nil {
		return "", err
	}
	original, _ := steward.RenderAnswer(rec.Category, subjectOf(rec))
	return original, nil
}

func (s *Service) openRound(ctx context.Context, r *Round) (*Open, error) {
	open, err := s.discussions.Open(ctx, r.TicketID, r.Attempt)
	if errors.Is(err, ErrNoOpen) {
		return nil, ErrStale
	}
	if err != nil {
		return nil, fmt.Errorf("discussionanswer: read discussion: %w", err)
	}
	if open.Attempt != r.Attempt || open.Round != r.Round {
		return nil, ErrStale
	}
	return open, nil
}

// decide claims the decision before posting, so a racing or repeated call
// cannot decide it differently. The post is keyed by the claim, and a claim is
// never released: a failed post may still have landed (a crash, a cancelled
// request, a lost reply), and the retry posts under the same key, which the
// ticket service makes a no-op if it did.
func (s *Service) decide(ctx context.Context, r *Round, d *Decision) (*Decision, error) {
	d.CreatedAt = s.now()
	claimed, err := s.decisions.Claim(ctx, d)
	if err != nil {
		return nil, err
	}
	return s.post(ctx, r, claimed)
}

// finishOwed completes the label's claim when it is the same decision and its
// post is still owed, or returns nil when there is no such claim. It runs
// before the checks on the round: a post that landed is what moved the ticket
// out of its discussion, so a retry after a crash would otherwise be refused as
// stale, and the claim would never be marked posted. An empty body matches any.
func (s *Service) finishOwed(ctx context.Context, r *Round, labelID uuid.UUID, action Action, body string) (*Decision, error) {
	existing, err := s.decisions.ForLabel(ctx, labelID)
	if errors.Is(err, ErrNoDecision) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if existing.PostedAt != nil || existing.Action != action || (body != "" && existing.FinalBody != body) {
		return nil, nil
	}
	return s.post(ctx, r, existing)
}

func (s *Service) post(ctx context.Context, r *Round, claimed *Decision) (*Decision, error) {
	if claimed.PostedAt != nil {
		return claimed, nil
	}
	if err := s.poster.Post(ctx, r.TicketID, r.Attempt, claimed.FinalBody, claimed.ID.String()); err != nil {
		return nil, fmt.Errorf("discussionanswer: post: %w", err)
	}
	// Detached from ctx: the answer is on the ticket, and a request cancelled
	// now must not leave the claim looking unposted.
	posted := s.now()
	if err := s.decisions.MarkPosted(context.WithoutCancel(ctx), claimed.ID, posted); err != nil {
		return nil, err
	}
	claimed.PostedAt = &posted
	return claimed, nil
}

func subjectOf(rec *discussionlabel.Record) *steward.Subject {
	s := &steward.Subject{TicketID: rec.TicketID, HeadSHA: deref(rec.HeadSHA)}
	if rec.TicketRevision != nil {
		s.Revision = *rec.TicketRevision
	}
	return s
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
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
