package steward

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
)

// reportScanLimit bounds the assessments one digest reads.
const reportScanLimit = 10_000

// ErrWindowTooLarge is a window holding more assessments than one report
// reads; a silently cut-off count would misstate the agreement.
var ErrWindowTooLarge = errors.New("steward: too many assessments in the window; narrow it")

// Agreement says how the owner's decision on an assessment compares with the
// steward's verdict. The agreement package owns the rules, and imports this
// one, so the digest takes them injected.
type Agreement func(*Record) (agreed, disagreed bool)

// Outcome is what became of one assessment, for the digest. The first that
// holds wins, so an applied decision the owner then undid counts as undone.
type Outcome string

const (
	OutcomeUndone       Outcome = "undone"
	OutcomeVetoed       Outcome = "vetoed"
	OutcomeApplied      Outcome = "applied"
	OutcomeNeedsHuman   Outcome = "needs_human"
	OutcomeVoided       Outcome = "voided"
	OutcomeWaiting      Outcome = "waiting"
	OutcomeOwnerDecided Outcome = "owner_decided"
	// OutcomeShadow is an assessment made without acting on it.
	OutcomeShadow Outcome = "shadow"
)

func (r *Record) outcome() Outcome {
	switch {
	case r.UndoneAt != nil:
		return OutcomeUndone
	case r.VetoedAt != nil:
		return OutcomeVetoed
	case r.AppliedAt != nil:
		return OutcomeApplied
	case r.Verdict == VerdictNeedsHuman:
		return OutcomeNeedsHuman
	case r.VoidedAt != nil:
		return OutcomeVoided
	case r.HumanDecision != nil:
		return OutcomeOwnerDecided
	case r.Mode != ModeLive:
		return OutcomeShadow
	}
	return OutcomeWaiting
}

// DigestWindows are the spans the Steward view offers.
var DigestWindows = map[string]time.Duration{
	"24h": 24 * time.Hour,
	"7d":  7 * 24 * time.Hour,
}

// ErrUnknownWindow is a digest window not in DigestWindows.
var ErrUnknownWindow = errors.New("steward: unknown digest window")

// MaxDigestDecisions bounds the decisions a digest lists, newest first; the
// counts cover every assessment in the window.
const MaxDigestDecisions = 100

// CategoryDigest is one category's outcomes in the window.
type CategoryDigest struct {
	Category   string
	Assessed   int
	Applied    int
	Vetoed     int
	Undone     int
	NeedsHuman int
	// Awaiting are the assessments whose verdict claims something (would
	// auto-apply or would veto) and that the owner has not yet decided or the
	// steward has not yet applied.
	Awaiting int
	// Unclassified are the assessments whose category is not on the allowlist,
	// nor a retired one: the steward could not place the proposal.
	Unclassified int
	// Agreed and Disagreed are the agreement report's: how the owner's
	// decisions compared with the steward's verdicts.
	Agreed    int
	Disagreed int
}

// AgreementRate is the share of decided assessments the owner agreed with,
// false when none was decided.
func (c *CategoryDigest) AgreementRate() (float64, bool) {
	return rate(c.Agreed, c.Disagreed)
}

func rate(agreed, disagreed int) (float64, bool) {
	if agreed+disagreed == 0 {
		return 0, false
	}
	return float64(agreed) / float64(agreed+disagreed), true
}

// DecisionDigest is one assessment as the digest lists it.
type DecisionDigest struct {
	PendingActionID uuid.UUID
	TicketID        *int64
	Category        string
	Outcome         Outcome
	At              time.Time
}

// Digest is what the steward did over a window.
type Digest struct {
	Since      time.Time
	Until      time.Time
	Categories []CategoryDigest
	Total      CategoryDigest
	Decisions  []DecisionDigest
	// DecisionsCut says Decisions holds fewer than the window's assessments.
	DecisionsCut bool
	Discussions  DiscussionDigest
}

// Notice is a decision the owner can still act on: one waiting out its veto
// window (veto it), or one applied lately (undo it).
type Notice struct {
	PendingActionID uuid.UUID
	TicketID        *int64
	Category        string
	Outcome         Outcome
	// At is when it was, or will be, applied; empty for a decision whose
	// window has not been opened yet.
	At *time.Time
}

// Notices is what the Approvals header says about the steward.
type Notices struct {
	Mode    Mode
	Pending []Notice
	Applied []Notice
	// CapReached says the daily cap on applications is used up, so further
	// decisions wait for the owner.
	CapReached bool
	DailyCap   int
}

// MaxNotices bounds each list of notices.
const MaxNotices = 50

func noticeOf(rec *Record, outcome Outcome, at *time.Time) Notice {
	return Notice{PendingActionID: rec.PendingActionID, TicketID: rec.TicketID, Category: rec.Category, Outcome: outcome, At: at}
}

// noticeAppliedWithin is how far back applied decisions are announced.
const noticeAppliedWithin = time.Hour

// Digester reads the steward's activity for the owner's views.
type Digester struct {
	store     *Store
	settings  Settings
	agreement Agreement
	// discussions reads the discussion record; nil leaves it out of the digest.
	discussions DiscussionSource
	now         func() time.Time
}

// WithDiscussions adds the discussion label and answer agreement to digests.
func (d *Digester) WithDiscussions(src DiscussionSource) *Digester {
	d.discussions = src
	return d
}

// NewDigester builds the reader; now is injectable for tests.
func NewDigester(store *Store, settings Settings, agreement Agreement, now func() time.Time) *Digester {
	if now == nil {
		now = time.Now
	}
	return &Digester{store: store, settings: settings, agreement: agreement, now: now}
}

// Digest summarises the assessments created in the named window.
func (d *Digester) Digest(ctx context.Context, window string) (*Digest, error) {
	span, ok := DigestWindows[window]
	if !ok {
		return nil, ErrUnknownWindow
	}
	until := d.now()
	since := until.Add(-span)
	records, err := d.store.List(ctx, &ListFilter{Since: since, Until: until, Limit: reportScanLimit + 1})
	if err != nil {
		return nil, err
	}
	if len(records) > reportScanLimit {
		return nil, ErrWindowTooLarge
	}
	out := &Digest{Since: since, Until: until, Total: CategoryDigest{Category: "all"}}
	byCategory := map[string]*CategoryDigest{}
	for i := range records {
		rec := &records[i]
		c := byCategory[rec.Category]
		if c == nil {
			c = &CategoryDigest{Category: rec.Category}
			byCategory[rec.Category] = c
		}
		outcome := rec.outcome()
		isAgreed, isDisagreed := d.agreement(rec)
		_, listed := Lookup(rec.Category)
		unclassified := !listed && !Retired(rec.Category)
		for _, t := range []*CategoryDigest{c, &out.Total} {
			t.Assessed++
			switch outcome {
			case OutcomeApplied:
				t.Applied++
			case OutcomeVetoed:
				t.Vetoed++
			case OutcomeUndone:
				t.Undone++
			case OutcomeNeedsHuman:
				t.NeedsHuman++
			case OutcomeWaiting, OutcomeShadow:
				t.Awaiting++
			}
			if unclassified {
				t.Unclassified++
			}
			if isAgreed {
				t.Agreed++
			}
			if isDisagreed {
				t.Disagreed++
			}
		}
		// List is newest first.
		if len(out.Decisions) < MaxDigestDecisions {
			out.Decisions = append(out.Decisions, DecisionDigest{
				PendingActionID: rec.PendingActionID, TicketID: rec.TicketID, Category: rec.Category,
				Outcome: outcome, At: rec.CreatedAt,
			})
		} else {
			out.DecisionsCut = true
		}
	}
	for _, c := range byCategory {
		out.Categories = append(out.Categories, *c)
	}
	sort.Slice(out.Categories, func(i, j int) bool { return out.Categories[i].Category < out.Categories[j].Category })
	if d.discussions != nil {
		disc, err := d.discussionDigest(ctx, since, until)
		if err != nil {
			return nil, err
		}
		out.Discussions = *disc
	}
	return out, nil
}

// Notices reads what the owner may still veto or undo, and whether the daily
// cap is used up.
func (d *Digester) Notices(ctx context.Context) (*Notices, error) {
	cfg, err := d.settings.StewardConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("steward: read config: %w", err)
	}
	out := &Notices{Mode: cfg.Mode, DailyCap: cfg.DailyCap}
	if cfg.Mode != ModeLive {
		return out, nil
	}
	now := d.now()
	waiting, err := d.store.ListWaiting(ctx, MaxNotices)
	if err != nil {
		return nil, err
	}
	applied, err := d.store.ListRecentlyApplied(ctx, now.Add(-noticeAppliedWithin), MaxNotices)
	if err != nil {
		return nil, err
	}
	for i := range waiting {
		out.Pending = append(out.Pending, noticeOf(&waiting[i], OutcomeWaiting, waiting[i].VetoDeadline))
	}
	for i := range applied {
		out.Applied = append(out.Applied, noticeOf(&applied[i], OutcomeApplied, applied[i].AppliedAt))
	}
	used, err := d.store.Applied(ctx, now.Add(-capWindow))
	if err != nil {
		return nil, err
	}
	out.CapReached = used >= cfg.DailyCap
	return out, nil
}
