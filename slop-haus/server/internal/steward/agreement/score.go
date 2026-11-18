package agreement

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward"
)

// MaxDisagreements bounds the disagreements quoted per category.
const MaxDisagreements = 5

// DefaultScanLimit bounds the assessments one score reads.
const DefaultScanLimit = 10_000

// ErrWindowTooLarge is steward's: the digest reads the same limit, and one
// sentinel lets the API map either to the same answer.
var ErrWindowTooLarge = steward.ErrWindowTooLarge

// Disagreement is one assessment the owner went against, with what the UI
// needs to link to it.
type Disagreement struct {
	AssessmentID    uuid.UUID
	PendingActionID uuid.UUID
	TicketID        *int64
	Verdict         steward.Verdict
	// StewardReason is the steward's own reason for the proposal.
	StewardReason string
	// How is what the owner did: the recorded decision, or the veto or undo.
	How string
	// OwnerReason is the reason the owner gave, if any. It never changes the count.
	OwnerReason string
	At          time.Time
	// Dangerous disagreements (see Dangerous) are listed before the rest.
	Dangerous bool
}

// CategoryScore is the agreement record of one category.
type CategoryScore struct {
	Category string
	// Decisions is Agreed + Disagreed.
	Decisions int
	Agreed    int
	// AgreementPct is 0 when there are no decisions; check Decisions first.
	AgreementPct float64
	// Latest are the most recent disagreements, newest first.
	Latest []Disagreement
}

// Window bounds assessments by creation time: [Since, Until). Zero is open.
type Window struct {
	Since, Until time.Time
}

// Scorer computes agreement for the tenant in the context, on read.
type Scorer struct {
	store     *steward.Store
	db        *db.DB
	scanLimit int
}

func NewScorer(store *steward.Store, database *db.DB) *Scorer {
	return &Scorer{store: store, db: database, scanLimit: DefaultScanLimit}
}

// WithScanLimit lowers the assessments one score reads, so a test can reach
// the limit without thousands of rows.
func (s *Scorer) WithScanLimit(n int) *Scorer {
	s.scanLimit = n
	return s
}

// Score returns a row per category that has assessments in the window,
// sorted by category. Shadow, assist, live, vetoed and undone all count.
func (s *Scorer) Score(ctx context.Context, w Window) ([]CategoryScore, error) {
	records, err := s.store.List(ctx, &steward.ListFilter{Since: w.Since, Until: w.Until, Limit: s.scanLimit + 1})
	if err != nil {
		return nil, err
	}
	if len(records) > s.scanLimit {
		return nil, ErrWindowTooLarge
	}
	scores := map[string]*CategoryScore{}
	all := map[string][]Disagreement{}
	for i := range records {
		rec := &records[i]
		c := scores[rec.Category]
		if c == nil {
			c = &CategoryScore{Category: rec.Category}
			scores[rec.Category] = c
		}
		switch ComparatorFor(rec.Category)(rec) {
		case Agreed:
			c.Decisions++
			c.Agreed++
		case Disagreed:
			c.Decisions++
			all[rec.Category] = append(all[rec.Category], newDisagreement(rec))
		}
	}
	out := make([]CategoryScore, 0, len(scores))
	for _, c := range scores {
		if c.Decisions > 0 {
			c.AgreementPct = 100 * float64(c.Agreed) / float64(c.Decisions)
		}
		// Latest by when the owner went against it: an undo comes long after
		// the assessment was created.
		d := all[c.Category]
		sort.SliceStable(d, func(i, j int) bool {
			if d[i].Dangerous != d[j].Dangerous {
				return d[i].Dangerous
			}
			return d[i].At.After(d[j].At)
		})
		c.Latest = d[:min(len(d), MaxDisagreements)]
		out = append(out, *c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Category < out[j].Category })
	var quoted []*Disagreement
	for i := range out {
		for j := range out[i].Latest {
			quoted = append(quoted, &out[i].Latest[j])
		}
	}
	if err := s.fillOwnerReasons(ctx, quoted); err != nil {
		return nil, err
	}
	return out, nil
}

func newDisagreement(r *steward.Record) Disagreement {
	d := Disagreement{AssessmentID: r.ID, PendingActionID: r.PendingActionID, TicketID: r.TicketID,
		Verdict: r.Verdict, StewardReason: r.Reason, Dangerous: Dangerous(r.Category)}
	switch {
	case r.UndoneAt != nil:
		d.How, d.At = "undone", *r.UndoneAt
	case r.VetoedAt != nil:
		d.How, d.At = "vetoed", *r.VetoedAt
	case r.HumanDecision == nil:
		// Left undecided past the category's window (SameActionOrExpired).
		d.How, d.At = "expired", r.CreatedAt
	default:
		d.How = string(*r.HumanDecision)
		if r.DecidedAt != nil {
			d.At = *r.DecidedAt
		}
	}
	return d
}

// fillOwnerReasons reads the owner's stated reason for a rejection, which
// the pending action keeps as its outcome. A veto or undo records no reason
// yet, so those keep an empty one.
func (s *Scorer) fillOwnerReasons(ctx context.Context, ds []*Disagreement) error {
	if len(ds) == 0 {
		return nil
	}
	ids := make([]uuid.UUID, len(ds))
	for i, d := range ds {
		ids[i] = d.PendingActionID
	}
	rows, err := s.db.Query(ctx, `SELECT id, outcome FROM pending_actions WHERE id = ANY($1) AND status = 'rejected'`, ids)
	if err != nil {
		return fmt.Errorf("agreement: read owner reasons: %w", err)
	}
	defer rows.Close()
	reasons := map[uuid.UUID]string{}
	for rows.Next() {
		var id uuid.UUID
		var outcome string
		if err := rows.Scan(&id, &outcome); err != nil {
			return fmt.Errorf("agreement: read owner reasons: %w", err)
		}
		reasons[id] = outcome
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("agreement: read owner reasons: %w", err)
	}
	for _, d := range ds {
		if d.How == "rejected" {
			// pendingaction stores a rejection as "rejected: <reason>", or bare "rejected".
			d.OwnerReason = strings.TrimPrefix(strings.TrimPrefix(reasons[d.PendingActionID], "rejected"), ": ")
		}
	}
	return nil
}
