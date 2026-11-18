package ticket

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

// Attempt is one attempt at a ticket, as the ticket page shows it. Before C5
// every ticket has one.
type Attempt struct {
	Number             int
	Status             string
	PromotedAt         *time.Time
	PromotedByUserID   *uuid.UUID
	PromotedByDeleted  bool
	PRURL              *string
	ApprovedAt         *time.Time
	ApprovedByUserID   *uuid.UUID
	ApprovedByDeleted  bool
	ChangesRequestedAt *time.Time
	ReviewStartedAt    *time.Time
	// StartedRevision is the ticket revision the attempt started from, nil
	// until it starts.
	StartedRevision *int
	// Shadow is an attempt added after work had started.
	Shadow bool
	// Replay is an attempt added to a done ticket to run it again from its
	// original base, on the ticket as its snapshot froze it.
	Replay bool
	// Holder is the agent with a live lease on the attempt, if any.
	Holder *AttemptHolder
	// Variant is the version the attempt is routed to, nil for none.
	Variant *model.AttemptVariant
	// Leased is whether the attempt has ever been leased, which ends the
	// time its variant can be set.
	Leased bool
	// Runs is what each of the attempt's leases ran with, oldest first.
	Runs []AttemptRun
}

// AttemptHolder is the agent holding an attempt's lease.
type AttemptHolder struct {
	AgentID uuid.UUID
	Name    string
}

// AttemptBody is the wire form of an Attempt. It appears in the ticket detail
// response only, not in board or list rows, which stay lean.
type AttemptBody struct {
	Number             int        `json:"number"`
	Status             string     `json:"status" enum:"queued,in_progress,in_merge_queue,discussion,review,closed,done"`
	Promoted           bool       `json:"promoted"`
	PromotedAt         *time.Time `json:"promoted_at"`
	PromotedByUserID   *string    `json:"promoted_by_user_id"`
	PromotedByDeleted  bool       `json:"promoted_by_user_id_deleted,omitempty" doc:"true when the person's account was deleted; the id is then null"`
	PRURL              *string    `json:"pr_url"`
	ApprovedAt         *time.Time `json:"approved_at"`
	ApprovedByUserID   *string    `json:"approved_by_user_id"`
	ApprovedByDeleted  bool       `json:"approved_by_user_id_deleted,omitempty" doc:"true when the person's account was deleted; the id is then null"`
	ChangesRequestedAt *time.Time `json:"changes_requested_at"`
	ReviewStartedAt    *time.Time `json:"review_started_at"`
	// StartedRevision is the ticket revision the attempt started from. Null
	// until the attempt starts.
	StartedRevision *int               `json:"started_revision"`
	Shadow          bool               `json:"shadow"`
	Replay          bool               `json:"replay,omitempty" doc:"An attempt added to a done ticket to run it again; always a shadow."`
	Holder          *AttemptHolderBody `json:"holder"`
	// Variant is the variant version the attempt is routed to: only agents
	// on exactly that version claim it. Null for none, which only agents in
	// no variant claim.
	Variant *AttemptVariantBody `json:"variant"`
	// VariantSettable is whether the variant can still be set: until the
	// attempt's first lease.
	VariantSettable bool `json:"variant_settable"`
	// WorkerCost is what the attempt's agents spent, a figure per billing
	// mode; empty when they spent nothing. It is the figure the attempt's
	// board card shows.
	WorkerCost []CostFigureBody `json:"worker_cost"`
	// Runs is what each lease of the attempt ran with beside its variant,
	// oldest first; empty before its first lease.
	Runs []AttemptRunBody `json:"runs"`
	// Signals is what the attempt's change and run show about the work. Null
	// when none have been computed, which says nothing about the work.
	Signals *AttemptSignalsBody `json:"signals"`
	// Verdict is a person's verdict on the finished attempt. Null when nobody
	// has given one, and for anyone but a person.
	Verdict *AttemptVerdictBody `json:"verdict"`
	// Discussion is the attempt's open discussion round and its steward label.
	// Null when the attempt is not in discussion, and for anyone but a person:
	// a worker never sees the label.
	Discussion *AttemptDiscussionBody `json:"discussion"`
}

// AttemptVariantBody names a variant version.
type AttemptVariantBody struct {
	VersionID string `json:"version_id"`
	Name      string `json:"name"`
	Version   int    `json:"version"`
	Archived  bool   `json:"archived"`
}

type AttemptHolderBody struct {
	AgentID string `json:"agent_id"`
	Name    string `json:"name"`
}

func attemptsToBody(attempts []Attempt) []AttemptBody {
	out := make([]AttemptBody, len(attempts))
	for i := range attempts {
		a := &attempts[i]
		out[i] = AttemptBody{
			Number:             a.Number,
			Status:             a.Status,
			Promoted:           a.PromotedAt != nil,
			PromotedAt:         a.PromotedAt,
			PromotedByUserID:   uuidPtrString(a.PromotedByUserID),
			PromotedByDeleted:  a.PromotedByDeleted,
			PRURL:              a.PRURL,
			ApprovedAt:         a.ApprovedAt,
			ApprovedByUserID:   uuidPtrString(a.ApprovedByUserID),
			ApprovedByDeleted:  a.ApprovedByDeleted,
			ChangesRequestedAt: a.ChangesRequestedAt,
			ReviewStartedAt:    a.ReviewStartedAt,
			StartedRevision:    a.StartedRevision,
			Shadow:             a.Shadow,
			Replay:             a.Replay,
			VariantSettable:    !a.Leased,
			WorkerCost:         []CostFigureBody{},
			Runs:               attemptRunBodies(a.Runs),
		}
		if a.Variant != nil {
			out[i].Variant = &AttemptVariantBody{VersionID: a.Variant.VersionID.String(),
				Name: a.Variant.Name, Version: a.Variant.Version, Archived: a.Variant.Archived}
		}
		if a.Holder != nil {
			out[i].Holder = &AttemptHolderBody{AgentID: a.Holder.AgentID.String(), Name: a.Holder.Name}
		}
	}
	return out
}

func uuidPtrString(id *uuid.UUID) *string {
	if id == nil {
		return nil
	}
	s := id.String()
	return &s
}

// Attempts is a ticket's attempts, by number.
func (s *Service) Attempts(ctx context.Context, ticketID int64) ([]Attempt, error) {
	return s.store.ListAttempts(ctx, ticketID)
}

// ListAttempts reads a ticket's attempts, by number, with the agent holding
// each one's live lease. A ticket that does not exist has none.
func (s *PostgresStore) ListAttempts(ctx context.Context, ticketID int64) ([]Attempt, error) {
	return listAttempts(ctx, s.db, ticketID)
}

func listAttempts(ctx context.Context, q db.Querier, ticketID int64) ([]Attempt, error) {
	rows, err := q.Query(ctx, `
		SELECT a.number, a.status, a.promoted_at, a.promoted_by_user_id, a.promoted_by_user_id_deleted, a.pr_url,
		       a.approved_at, a.approved_by_user_id, a.approved_by_user_id_deleted, a.changes_requested_at, a.review_started_at,
		       a.started_revision, a.shadow, a.replay,
		       h.agent_id, h.name, vv.id, vv.name, vv.version, va.version_id IS NOT NULL,
		       `+attemptEverLeased("a")+`
		  FROM attempts a
		  LEFT JOIN variant_versions vv ON vv.tenant_id = a.tenant_id AND vv.id = a.variant_version_id
		  LEFT JOIN variant_archives va ON va.tenant_id = vv.tenant_id AND va.version_id = vv.id
		  LEFT JOIN LATERAL (
		        SELECT l.agent_id, ag.name
		          FROM leases l JOIN agents ag ON ag.tenant_id = l.tenant_id AND ag.id = l.agent_id
		         WHERE l.tenant_id = a.tenant_id AND l.ticket_id = a.ticket_id
		           AND l.attempt_number = a.number AND `+activeLeaseClause+`
		         LIMIT 1) h ON true
		 WHERE a.ticket_id = $1
		 ORDER BY a.number`, ticketID)
	if err != nil {
		return nil, fmt.Errorf("list attempts of ticket #%d: %w", ticketID, err)
	}
	attempts, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Attempt, error) {
		var a Attempt
		var holderID *uuid.UUID
		var holderName, variantName *string
		var variantID *uuid.UUID
		var variantVersion *int
		var variantArchived bool
		if err := row.Scan(&a.Number, &a.Status, &a.PromotedAt, &a.PromotedByUserID, &a.PromotedByDeleted, &a.PRURL,
			&a.ApprovedAt, &a.ApprovedByUserID, &a.ApprovedByDeleted, &a.ChangesRequestedAt, &a.ReviewStartedAt,
			&a.StartedRevision, &a.Shadow, &a.Replay,
			&holderID, &holderName, &variantID, &variantName, &variantVersion, &variantArchived, &a.Leased); err != nil {
			return a, err
		}
		if variantID != nil && variantName != nil && variantVersion != nil {
			a.Variant = &model.AttemptVariant{VersionID: *variantID, Name: *variantName, Version: *variantVersion, Archived: variantArchived}
		}
		if holderID != nil && holderName != nil {
			a.Holder = &AttemptHolder{AgentID: *holderID, Name: *holderName}
		}
		return a, nil
	})
	if err != nil {
		return nil, err
	}
	runs, err := attemptRuns(ctx, q, ticketID)
	if err != nil {
		return nil, err
	}
	for i := range attempts {
		attempts[i].Runs = runs[attempts[i].Number]
	}
	return attempts, nil
}
