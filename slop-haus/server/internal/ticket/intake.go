package ticket

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

// An intake source's tickets are filed, commented on and reopened here, in
// one transaction under the claim lock: the ticket, the correlation to the
// sender's key and what the sender said must commit together or a redelivery
// files a second ticket. Which of those to do, and with what status, is the
// intake package's decision (it passes it as an IntakePlanner); this package
// only carries it out.
//
// What a sender says is never written to the ticket's body or comments. It
// is an ExternalEvent, which every reader renders as external content, so no
// text a stranger controls can pass for slop-haus's own words.

// ExternalEvent is what a source sent about a ticket.
type ExternalEvent struct {
	ID         int64
	TicketID   int64
	SourceID   uuid.UUID
	SourceName string
	// Key is the sender's id for the issue.
	Key        string
	DeliveryID string
	// Event is open, update or resolve.
	Event   string
	Title   string
	Body    string
	Link    string
	Context json.RawMessage
	At      time.Time
}

// IntakeAction is what a delivery does to the tickets.
type IntakeAction string

const (
	IntakeCreate  IntakeAction = "create"
	IntakeComment IntakeAction = "comment"
	// IntakeReopen moves the source's closed ticket back into the queue.
	IntakeReopen IntakeAction = "reopen"
	// IntakeCancel cancels the source's ticket, which nobody has claimed, because
	// the sender reported its issue resolved.
	IntakeCancel IntakeAction = "cancel"
	// IntakeSkip does nothing, for the reason in the plan.
	IntakeSkip IntakeAction = "skip"
)

// IntakeState is what a planner decides on, read under the claim lock.
type IntakeState struct {
	// Latest is the newest ticket the source filed for the key, or nil.
	Latest *Ticket
	// Events is how many events Latest has.
	Events int
	// Unclaimed is whether Latest is untouched by any agent or person: open
	// or preparing, with no lease ever taken, no attempt past its first
	// queued one, and no comment. It is the one condition under which a
	// sender may close a ticket (IntakeCancel).
	Unclaimed bool
	// LiveTickets is how many unfinished tickets the source has filed.
	LiveTickets int
}

// IntakePlan is a planner's decision.
type IntakePlan struct {
	Action IntakeAction
	// Why is the planner's reason for a skip.
	Why string
	// Create is the ticket to file, for IntakeCreate.
	Create *CreateParams
	// Reopen is the status Latest returns to, for IntakeReopen.
	Reopen Status
	// Event is recorded against the ticket acted on, for every action but a
	// skip.
	Event *ExternalEvent
	// Cause is the status change's recorded reason.
	Cause string
	// SenderTitle is kept on a created ticket as IntakeRef.Title.
	SenderTitle string
}

// IntakePlanner decides a delivery from the state under the claim lock. An
// error aborts, writing nothing.
type IntakePlanner func(state *IntakeState) (*IntakePlan, error)

// IntakeResult is what ApplyIntake did.
type IntakeResult struct {
	Action IntakeAction
	// Ticket is the ticket acted on, as the transaction left it; nil for a
	// skip with no ticket.
	Ticket *Ticket
	// Skipped is the plan's reason, for a skip.
	Skipped string
	// AlreadyApplied is set when the delivery had already been applied, by an
	// earlier attempt cut off before it was recorded: nothing was done now,
	// and Ticket is the one it was applied to.
	AlreadyApplied bool
}

// ApplyIntake decides with plan on the state of source's tickets for key,
// and carries the decision out, atomically. The planner runs in the
// transaction, so two deliveries for one key are decided one after the
// other, never on the same state. A delivery already applied -- an attempt
// cut off before its outcome was recorded, handled again -- is recognised by
// its id and applied no second time.
func (s *Service) ApplyIntake(ctx context.Context, ref model.IntakeRef, deliveryID string, plan IntakePlanner) (*IntakeResult, error) {
	var result *IntakeResult
	err := s.store.InTransition(ctx, func(tx TransitionTx) error {
		q := tx.Querier()
		if prior, err := appliedDelivery(ctx, tx, ref.SourceID, deliveryID); err != nil || prior != nil {
			if err == nil {
				result = &IntakeResult{Action: IntakeSkip, Ticket: prior, AlreadyApplied: true}
			}
			return err
		}
		state, err := readIntakeState(ctx, tx, &ref)
		if err != nil {
			return err
		}
		p, err := plan(state)
		if err != nil {
			return err
		}
		if p.Cause != "" {
			ctx = WithCause(ctx, p.Cause)
		}
		result, err = carryOut(ctx, tx, q, &ref, state, p)
		return err
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func carryOut(ctx context.Context, tx TransitionTx, q db.Querier, ref *model.IntakeRef, state *IntakeState, p *IntakePlan) (*IntakeResult, error) {
	result := &IntakeResult{Action: p.Action}
	var ticketID int64
	switch p.Action {
	case IntakeSkip:
		result.Skipped = p.Why
		if state.Latest != nil {
			result.Ticket = state.Latest
		}
		return result, nil
	case IntakeCreate:
		// Not checked against the manifest's one-repo rule (see checkOneRepo): a
		// source's paths are a person's config, and refusing a delivery for them
		// would lose the alert.
		create := *p.Create
		create.Intake = &model.IntakeRef{SourceID: ref.SourceID, Key: ref.Key, Title: p.SenderTitle}
		if err := checkCreate(&create); err != nil {
			return nil, err
		}
		id, err := insertTicket(ctx, q, &create)
		if err != nil {
			return nil, err
		}
		ticketID = id
	case IntakeComment:
		ticketID = state.Latest.ID
	case IntakeCancel:
		// Checked again here, not left to the planner: this is the one path
		// by which a sender's word ends a ticket.
		if state.Latest == nil || !state.Unclaimed {
			return nil, errors.New("ticket: intake cancel of a ticket that is not unclaimed")
		}
		ticketID = state.Latest.ID
		if _, err := applyMove(ctx, tx, []int64{ticketID}, MoveTo(StatusCancelled)); err != nil {
			return nil, single(err)
		}
	case IntakeReopen:
		ticketID = state.Latest.ID
		if _, err := applyMove(ctx, tx, []int64{ticketID}, MoveTo(p.Reopen)); err != nil {
			return nil, single(err)
		}
	default:
		return nil, fmt.Errorf("ticket: unknown intake action %q", p.Action)
	}
	if err := insertExternalEvent(ctx, q, ticketID, p.Event); err != nil {
		return nil, err
	}
	t, err := tx.Ticket(ctx, ticketID)
	if err != nil {
		return nil, err
	}
	result.Ticket = t
	return result, nil
}

// appliedDelivery is the ticket an earlier attempt at the delivery applied
// an event to, or nil.
func appliedDelivery(ctx context.Context, tx TransitionTx, source uuid.UUID, deliveryID string) (*Ticket, error) {
	var id int64
	err := tx.Querier().QueryRow(ctx, `
		SELECT ticket_id FROM intake_events WHERE source_id = $1 AND delivery_id = $2`, source, deliveryID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find applied intake delivery: %w", err)
	}
	return tx.Ticket(ctx, id)
}

// readIntakeState is run under the claim lock, which every write to a
// source's tickets takes, so it cannot go stale before the plan is carried
// out.
func readIntakeState(ctx context.Context, tx TransitionTx, ref *model.IntakeRef) (*IntakeState, error) {
	q := tx.Querier()
	state := &IntakeState{}
	var latest int64
	err := q.QueryRow(ctx, `
		SELECT id FROM tickets
		 WHERE intake_source_id = $1 AND intake_key = $2
		 ORDER BY id DESC LIMIT 1`, ref.SourceID, ref.Key).Scan(&latest)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return nil, fmt.Errorf("find intake ticket: %w", err)
	default:
		if state.Latest, err = tx.Ticket(ctx, latest); err != nil {
			return nil, err
		}
		if err := q.QueryRow(ctx, `SELECT count(*) FROM intake_events WHERE ticket_id = $1`, latest).Scan(&state.Events); err != nil {
			return nil, fmt.Errorf("count intake events: %w", err)
		}
		if state.Unclaimed, err = isUnclaimed(ctx, q, state.Latest); err != nil {
			return nil, err
		}
	}
	if err := q.QueryRow(ctx, `
		SELECT count(*) FROM tickets
		 WHERE intake_source_id = $1 AND status NOT IN `+TerminalStatusesSQL, ref.SourceID).Scan(&state.LiveTickets); err != nil {
		return nil, fmt.Errorf("count intake tickets: %w", err)
	}
	return state, nil
}

// isUnclaimed is IntakeState.Unclaimed. A lease row, even a released one, a
// second attempt or one past queued, any comment, or a status change by a
// person or an agent means someone has worked or spoken on the ticket, so it
// counts as claimed for good.
//
// Claiming holds the claim lock this runs under, but adding a comment does
// not, so the ticket's row is locked first: a comment insert in flight holds
// a key-share lock on it through its foreign key, which FOR UPDATE waits out,
// and the check after it then sees that comment. One that starts later
// waits for the cancel, and lands on a cancelled ticket.
func isUnclaimed(ctx context.Context, q db.Querier, t *Ticket) (bool, error) {
	if t.Status != StatusOpen && t.Status != StatusPreparing {
		return false, nil
	}
	if _, err := q.Exec(ctx, `SELECT 1 FROM tickets WHERE id = $1 FOR UPDATE`, t.ID); err != nil {
		return false, fmt.Errorf("lock intake ticket: %w", err)
	}
	var touched bool
	if err := q.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM leases WHERE ticket_id = $1)
		    OR EXISTS (SELECT 1 FROM attempts WHERE ticket_id = $1 AND (status <> 'queued' OR number > 1))
		    OR EXISTS (SELECT 1 FROM ticket_comments WHERE ticket_id = $1)
		    OR EXISTS (SELECT 1 FROM ticket_status_changes WHERE ticket_id = $1 AND actor_kind <> 'system')`, t.ID).Scan(&touched); err != nil {
		return false, fmt.Errorf("check intake ticket is unclaimed: %w", err)
	}
	return !touched, nil
}

func insertExternalEvent(ctx context.Context, q db.Querier, ticketID int64, e *ExternalEvent) error {
	if e.Context == nil {
		e.Context = json.RawMessage(`{}`)
	}
	if _, err := q.Exec(ctx, `
		INSERT INTO intake_events (ticket_id, source_id, delivery_id, event, title, body, link, context)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		ticketID, e.SourceID, e.DeliveryID, e.Event, e.Title, e.Body, e.Link, []byte(e.Context)); err != nil {
		return fmt.Errorf("record intake event: %w", err)
	}
	return nil
}

// ListExternal is what sources sent about a ticket, oldest first. Every
// consumer renders it as external content; see RenderExternal.
func (s *Service) ListExternal(ctx context.Context, ticketID int64) ([]ExternalEvent, error) {
	return s.store.ListExternal(ctx, ticketID)
}

func listExternal(ctx context.Context, q db.Querier, ticketID int64) ([]ExternalEvent, error) {
	rows, err := q.Query(ctx, `
		SELECT e.id, e.ticket_id, e.source_id, s.name, t.intake_key, e.delivery_id, e.event, e.title, e.body, e.link, e.context, e.created_at
		  FROM intake_events e
		  JOIN intake_sources s ON s.tenant_id = e.tenant_id AND s.id = e.source_id
		  JOIN tickets t ON t.tenant_id = e.tenant_id AND t.id = e.ticket_id
		 WHERE e.ticket_id = $1
		 ORDER BY e.id`, ticketID)
	if err != nil {
		return nil, fmt.Errorf("list intake events: %w", err)
	}
	defer rows.Close()
	out := []ExternalEvent{}
	for rows.Next() {
		var e ExternalEvent
		var ctxJSON []byte
		if err := rows.Scan(&e.ID, &e.TicketID, &e.SourceID, &e.SourceName, &e.Key, &e.DeliveryID, &e.Event,
			&e.Title, &e.Body, &e.Link, &ctxJSON, &e.At); err != nil {
			return nil, fmt.Errorf("scan intake event: %w", err)
		}
		e.Context = ctxJSON
		out = append(out, e)
	}
	return out, rows.Err()
}
