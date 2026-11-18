package ticket

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

// The work columns of the board (in progress, discussion, review) list one
// card per live attempt, each in its own attempt's status: attempts of one
// ticket reach discussion and review at different times. The other columns
// list tickets. A ticket with one attempt has one card either way, and shows
// as it always did (see Ticket.Card).

var workStatuses = model.WorkStatuses()

// cardStatuses are the statuses an attempt-card list reads.
func (f *ListFilter) cardStatuses() []Status {
	if len(f.Statuses) == 0 {
		return workStatuses
	}
	return f.Statuses
}

// cardFrom is the FROM of a list of attempt cards: the attempt a card is of
// stands where TicketFrom has the promoted one (pa), so every field read from
// pa is the card's attempt's.
func cardFrom(f *ListFilter) string {
	quoted := make([]string, 0, len(f.cardStatuses()))
	for _, st := range f.cardStatuses() {
		quoted = append(quoted, "'"+string(st)+"'")
	}
	return `tickets t JOIN attempts pa ON pa.tenant_id = t.tenant_id AND pa.ticket_id = t.id AND pa.status IN (` +
		strings.Join(quoted, ", ") + `)
	LEFT JOIN variant_versions vv ON vv.tenant_id = pa.tenant_id AND vv.id = pa.variant_version_id
	LEFT JOIN variant_archives va ON va.tenant_id = vv.tenant_id AND va.version_id = vv.id`
}

// cardColumns follow the ticket's columns in a row of attempt cards. The
// aliases keep the columns of a union of branches apart (see listQuery).
const cardColumns = `, pa.number AS attempt_number, pa.status AS attempt_status,
	(pa.status = 'review' AND pa.promoted_at IS NULL) AS attempt_awaiting_choice,
	vv.id AS attempt_variant_id, vv.name AS attempt_variant_name, vv.version AS attempt_variant_version,
	(va.version_id IS NOT NULL) AS attempt_variant_archived,
	(SELECT count(*) FROM attempts x WHERE x.tenant_id = t.tenant_id AND x.ticket_id = t.id) AS attempt_total`

// cardDest is where a row's cardColumns are read to, and finish turns what
// was read into the Card.
type cardDest struct {
	card           model.AttemptCard
	variantID      *uuid.UUID
	variantName    *string
	variantVersion *int
	archived       bool
}

func (d *cardDest) dest() []any {
	return []any{&d.card.Number, &d.card.Status, &d.card.AwaitingChoice,
		&d.variantID, &d.variantName, &d.variantVersion, &d.archived, &d.card.Total}
}

func (d *cardDest) finish() *model.AttemptCard {
	card := d.card
	if d.variantID != nil && d.variantName != nil && d.variantVersion != nil {
		card.Variant = &model.AttemptVariant{VersionID: *d.variantID, Name: *d.variantName, Version: *d.variantVersion, Archived: d.archived}
	}
	return &card
}

// countCards counts the attempt cards the filter matches in each status it
// names (every work status when none).
func (s *PostgresStore) countCards(ctx context.Context, filter *ListFilter) (map[Status]int, error) {
	where, args := ticketPredicate(filter, 0)
	rows, err := s.db.Query(ctx, `SELECT pa.status, count(*) FROM `+cardFrom(filter)+` WHERE `+where+` GROUP BY pa.status`, args...)
	if err != nil {
		return nil, fmt.Errorf("count attempt cards: %w", err)
	}
	defer rows.Close()
	counts := map[Status]int{}
	for rows.Next() {
		var status string
		var n int
		if err := rows.Scan(&status, &n); err != nil {
			return nil, fmt.Errorf("scan attempt card count: %w", err)
		}
		counts[Status(status)] = n
	}
	return counts, rows.Err()
}

// CostFigure is what an attempt's worker spent in one billing mode: the
// figures the board card shows, a few of the Reports tab's.
type CostFigure struct {
	BillingMode  string
	Requests     int64
	InputTokens  int64
	OutputTokens int64
	// CostMicros is micro-dollars over the priced records, nil when none is.
	CostMicros      *int64
	UnpricedRecords int64
}

// AttemptCosts reads the worker cost of attempts, for the board to show on
// their cards. It is one read for any number of attempts, so a page of cards
// costs one query and not one per card. It lives behind an interface because
// the usage tables are read by a package that itself reads tickets.
type AttemptCosts interface {
	// AttemptCosts returns each attempt's figures, a figure per billing mode;
	// an attempt that spent nothing has no entry.
	AttemptCosts(ctx context.Context, refs []AttemptRef) (map[AttemptRef][]CostFigure, error)
}

// CostFigureBody is the wire form of a CostFigure.
type CostFigureBody struct {
	// BillingMode is "metered" (money spent) or "subscription" (an
	// API-equivalent estimate, not money spent). Figures of the two are
	// never added together.
	BillingMode  string `json:"billing_mode"`
	Requests     int64  `json:"requests"`
	InputTokens  int64  `json:"input_tokens"`
	OutputTokens int64  `json:"output_tokens"`
	// CostMicros is micro-dollars over the priced records; absent when none
	// is priced, which is not zero.
	CostMicros *int64 `json:"cost_micros"`
	// UnpricedRecords is the usage the cost leaves out.
	UnpricedRecords int64 `json:"unpriced_records"`
}

// AttemptCardBody is what a card of one of several attempts adds to its
// ticket's body. It is left out for a ticket with one attempt, whose card is
// the ticket's as it always was.
type AttemptCardBody struct {
	Number int `json:"number"`
	// Total is how many attempts the ticket has, closed ones too: "Number of
	// Total attempts".
	Total int `json:"total"`
	// Status is the attempt's, and the column the card is in; the card's own
	// status is the same, and TicketStatus is the ticket's.
	Status       string `json:"status" enum:"in_progress,in_merge_queue,discussion,review"`
	TicketStatus Status `json:"ticket_status"`
	// AwaitingChoice is a finished attempt nobody has promoted: it waits in
	// review for a person to choose it or a sibling.
	AwaitingChoice bool                `json:"awaiting_choice"`
	Variant        *AttemptVariantBody `json:"variant"`
	// WorkerCost is what the attempt's agents spent, a figure per billing
	// mode; empty when they spent nothing. Guidance is not in it.
	WorkerCost []CostFigureBody `json:"worker_cost"`
}

func attemptCardToBody(t *Ticket) *AttemptCardBody {
	c := t.Card
	body := &AttemptCardBody{Number: c.Number, Total: c.Total, Status: string(c.Status), TicketStatus: t.Status,
		AwaitingChoice: c.AwaitingChoice, WorkerCost: []CostFigureBody{}}
	if c.Variant != nil {
		body.Variant = &AttemptVariantBody{VersionID: c.Variant.VersionID.String(), Name: c.Variant.Name,
			Version: c.Variant.Version, Archived: c.Variant.Archived}
	}
	return body
}

// fillCosts sets the worker cost of every attempt card among bodies with one
// read.
func fillCosts(ctx context.Context, costs AttemptCosts, bodies ...[]TicketBody) error {
	if costs == nil {
		return nil
	}
	var refs []AttemptRef
	for _, list := range bodies {
		for i := range list {
			if a := list[i].Attempt; a != nil {
				refs = append(refs, AttemptRef{TicketID: list[i].ID, Number: a.Number})
			}
		}
	}
	if len(refs) == 0 {
		return nil
	}
	got, err := costs.AttemptCosts(ctx, refs)
	if err != nil {
		return err
	}
	for _, list := range bodies {
		for i := range list {
			a := list[i].Attempt
			if a == nil {
				continue
			}
			for _, f := range got[AttemptRef{TicketID: list[i].ID, Number: a.Number}] {
				a.WorkerCost = append(a.WorkerCost, CostFigureBody(f))
			}
		}
	}
	return nil
}

// fillAttemptCosts sets the worker cost of a ticket's attempts, with the same
// read the board cards use so the two cannot disagree.
func fillAttemptCosts(ctx context.Context, costs AttemptCosts, ticketID int64, attempts []AttemptBody) error {
	if costs == nil || len(attempts) == 0 {
		return nil
	}
	refs := make([]AttemptRef, len(attempts))
	for i := range attempts {
		refs[i] = AttemptRef{TicketID: ticketID, Number: attempts[i].Number}
	}
	got, err := costs.AttemptCosts(ctx, refs)
	if err != nil {
		return err
	}
	for i := range attempts {
		for _, f := range got[refs[i]] {
			attempts[i].WorkerCost = append(attempts[i].WorkerCost, CostFigureBody(f))
		}
	}
	return nil
}
