package landing

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
)

// State is where a ticket stands in server-side landing.
type State string

const (
	// Landing: queued, or being worked by the runner.
	Landing State = "landing"
	// BackWithAgent: handed back to the author to fix and finish again.
	BackWithAgent State = "back_with_agent"
)

// Step is the side effect the runner is about to make or is waiting on. It is
// written before the effect so a restart reconciles from the code host.
type Step string

const (
	MergingMain   Step = "merging_main"
	WaitingChecks Step = "waiting_checks"
	Merging       Step = "merging"
)

func (s Step) valid() bool {
	return s == MergingMain || s == WaitingChecks || s == Merging
}

var (
	// ErrNoLanding is returned when the ticket has no landing row.
	ErrNoLanding = errors.New("outcome: ticket is not in landing")
	// ErrNotLanding is returned for a step change on a row handed back.
	ErrNotLanding = errors.New("outcome: landing is not in state landing")
	// ErrConflict is matched by a ConflictError: the row was not in the state
	// the write expected, because another writer moved it first.
	ErrConflict = errors.New("outcome: landing is not in the expected state")
	// ErrIllegalTransition is returned for a move the state table does not have.
	ErrIllegalTransition = errors.New("outcome: illegal landing transition")
)

// ConflictError is a guarded write that matched no row.
type ConflictError struct {
	TicketID int64
	From, To Phase
	// Actual is the row's phase now; empty when it has no row.
	Actual Phase
}

func (e *ConflictError) Error() string {
	if e.Actual == "" {
		return fmt.Sprintf("outcome: #%d has no landing to move %s -> %s", e.TicketID, e.From, e.To)
	}
	return fmt.Sprintf("outcome: #%d is %s, not %s, so it cannot move to %s", e.TicketID, e.Actual, e.From, e.To)
}

func (e *ConflictError) Is(target error) bool { return target == ErrConflict }

// LandingRow is one ticket's place in the landing queue.
type LandingRow struct {
	TicketID      int64
	AttemptNumber int
	ChangeURL     string
	HeadSHA       string
	State         State
	// Phase is the entry's stored state; State and Step are derived from it.
	Phase Phase
	// Step is empty while queued and once handed back. A member of a testing
	// batch reads as waiting_checks, which is what the batch is doing.
	Step Step
	// BackReason is the outcome that handed the ticket back; empty while landing.
	BackReason Outcome
	KeepsPlace bool
	// Position is 1 for the head of the landing queue; 0 for a row handed back.
	Position int
	// QueueDepth is set by the queue's Head: the other landings waiting at the
	// moment the row was taken. Not stored on the row.
	QueueDepth int
	// EnqueuedAt is when the row first joined the queue; set by the queue's Head.
	EnqueuedAt time.Time
	// BatchID is the batch the row is a member of; nil outside one, which is
	// every row until batches are written.
	BatchID *int64
	// BatchPosition is the row's place in its batch's merge order, from 1; 0
	// outside a batch.
	BatchPosition int
	// SinceUpdate is how long the row has been as it is (the database clock):
	// for a begun landing the time in its step, for a queued one the wait since
	// its last enqueue. Set by the queue's Ordered.
	SinceUpdate time.Duration
}

// LandingStore keeps the landing queue for the tenant in the context.
type LandingStore struct {
	db *db.DB
}

func NewLandingStore(database *db.DB) *LandingStore { return &LandingStore{db: database} }

const landingCols = `ticket_id, attempt_number, change_url, head_sha, queue_state, coalesce(back_reason, ''), keeps_place`

// Enqueue puts the ticket's change in the queue, and is idempotent: a finish
// call repeated while the ticket is landing changes nothing. A ticket handed
// back joins at the back, unless the handback was the server's fault
// (KeepsPlace), when it keeps its seq. The head and attempt are refreshed
// either way, because the author's fix moved them.
func (s *LandingStore) Enqueue(ctx context.Context, r *LandingRow) error {
	return s.db.InTx(ctx, func(q db.Querier) error { return EnqueueIn(ctx, q, r) })
}

// EnqueueIn is Enqueue in the transaction q belongs to, for a caller whose own
// write must land with the row.
func EnqueueIn(ctx context.Context, q db.Querier, r *LandingRow) error {
	// Insert first: SELECT ... FOR UPDATE cannot lock a row that does not
	// exist yet, so two first-time calls would race to a key violation.
	tag, err := q.Exec(ctx, `
INSERT INTO landings (ticket_id, attempt_number, change_url, head_sha, queue_state)
VALUES ($1, $2, $3, $4, 'queued')
ON CONFLICT (tenant_id, ticket_id) DO NOTHING`, r.TicketID, r.AttemptNumber, r.ChangeURL, r.HeadSHA)
	if err != nil {
		return fmt.Errorf("outcome: enqueue: %w", err)
	}
	if tag.RowsAffected() == 1 {
		return nil
	}
	// Only a handed-back row changes; a row in any other state is left alone.
	// Taking a new seq moves it to the back.
	err = transition(ctx, q, &move{
		TicketID: r.TicketID, From: PhaseBackWithAgent, To: PhaseQueued, Cause: CauseRefinished,
		Set: `attempt_number = $4, change_url = $5, head_sha = $6,
       batch_id = NULL, batch_position = NULL, back_reason = NULL,
       seq = CASE WHEN keeps_place THEN seq ELSE nextval(pg_get_serial_sequence('landings', 'seq')) END,
       keeps_place = false`,
		Args: []any{r.AttemptNumber, r.ChangeURL, r.HeadSHA},
	})
	var conflict *ConflictError
	if errors.As(err, &conflict) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("outcome: enqueue: %w", err)
	}
	return nil
}

// phaseAtStep is the phase an entry in from takes when the runner begins step:
// a batch member's holder, which waits on the batch's checks, is testing.
func phaseAtStep(from EntryState, step Step) (Phase, bool) {
	if from.BatchID != 0 {
		switch step {
		case Merging:
			return PhaseBatchMerging, true
		case MergingMain, WaitingChecks:
			return PhaseBatchTesting, true
		}
		return "", false
	}
	switch step {
	case MergingMain:
		return PhaseSerialMergingMain, true
	case WaitingChecks:
		return PhaseSerialWaitingChecks, true
	case Merging:
		return PhaseSerialMerging, true
	}
	return "", false
}

// stepMove is where a step puts an entry and why; a step the entry is already
// at stays put and needs no cause.
func stepMove(from EntryState, step Step) (to Phase, cause Cause, ok bool) {
	next, ok := phaseAtStep(from, step)
	if !ok {
		return "", "", false
	}
	if next == from.Phase {
		return next, "", true
	}
	t, ok := Lookup(from.Phase, next)
	return next, t.Cause, ok
}

// SetStep records the step the runner is about to take.
func (s *LandingStore) SetStep(ctx context.Context, ticketID int64, step Step) error {
	if !step.valid() {
		return fmt.Errorf("%w: unknown step %q", ErrInvalid, step)
	}
	from, err := currentState(ctx, s.db, ticketID)
	if err != nil {
		return err
	}
	if from.Phase == PhaseBackWithAgent {
		return ErrNotLanding
	}
	to, cause, ok := stepMove(from, step)
	if !ok {
		return fmt.Errorf("%w: %s cannot take step %s", ErrIllegalTransition, from.Phase, step)
	}
	return mapNotLanding(transition(ctx, s.db, &move{
		TicketID: ticketID, From: from.Phase, To: to, Cause: cause,
	}))
}

// HandBack marks the ticket as back with its agent for reason. keepsPlace is
// for a handback that was the server's own fault.
func (s *LandingStore) HandBack(ctx context.Context, ticketID int64, reason Outcome, keepsPlace bool) error {
	if !reason.Valid() || reason == Merged || reason == AlreadyMerged {
		return fmt.Errorf("%w: %q is not a handback reason", ErrInvalid, reason)
	}
	from, err := currentState(ctx, s.db, ticketID)
	if err != nil {
		return err
	}
	if from.Phase == PhaseBackWithAgent {
		return ErrNotLanding
	}
	return mapNotLanding(transition(ctx, s.db, &move{
		TicketID: ticketID, From: from.Phase, To: PhaseBackWithAgent, Cause: CauseHandedBack,
		Set:  `batch_id = NULL, batch_position = NULL, back_reason = $4, keeps_place = $5`,
		Args: []any{string(reason), keepsPlace},
	}))
}

// Remove drops the ticket's row once it is merged or cancelled, as a move to
// to (PhaseMerged or PhaseDequeued) the table allows from the row's state.
// Removing a ticket that has none is not an error.
func (s *LandingStore) Remove(ctx context.Context, ticketID int64, to Phase) error {
	return removeRow(ctx, s.db, ticketID, to)
}

// Get returns the ticket's row with its queue position.
func (s *LandingStore) Get(ctx context.Context, ticketID int64) (*LandingRow, error) {
	rows, err := s.Queue(ctx)
	if err != nil {
		return nil, err
	}
	for i := range rows {
		if rows[i].TicketID == ticketID {
			return &rows[i], nil
		}
	}
	return nil, ErrNoLanding
}

// Queue lists every row: the landing ones in FIFO order with positions from 1,
// then those handed back.
func (s *LandingStore) Queue(ctx context.Context) ([]LandingRow, error) {
	rows, err := s.db.Query(ctx, `SELECT `+landingCols+`,
       CASE WHEN queue_state <> 'back_with_agent' THEN row_number() OVER (PARTITION BY queue_state = 'back_with_agent' ORDER BY seq) ELSE 0 END
  FROM landings ORDER BY (queue_state <> 'back_with_agent') DESC, seq`)
	if err != nil {
		return nil, fmt.Errorf("outcome: queue: %w", err)
	}
	defer rows.Close()
	var out []LandingRow
	for rows.Next() {
		var r LandingRow
		var phase, reason string
		if err := rows.Scan(&r.TicketID, &r.AttemptNumber, &r.ChangeURL, &r.HeadSHA, &phase, &reason, &r.KeepsPlace, &r.Position); err != nil {
			return nil, fmt.Errorf("outcome: scan queue: %w", err)
		}
		r.setPhase(Phase(phase))
		r.BackReason = Outcome(reason)
		out = append(out, r)
	}
	return out, rows.Err()
}

// move is one guarded change to a stored row.
type move struct {
	TicketID int64
	From, To Phase
	// Cause is the table's cause for From -> To; empty when staying put.
	Cause Cause
	// Set is extra assignments, whose parameters start at $4.
	Set  string
	Args []any
}

// currentState reads the ticket's phase, ErrNoLanding if it has no row.
func currentState(ctx context.Context, q db.Querier, ticketID int64) (EntryState, error) {
	var phase string
	var batch *int64
	err := q.QueryRow(ctx, `SELECT queue_state, batch_id FROM landings WHERE ticket_id = $1`, ticketID).Scan(&phase, &batch)
	if errors.Is(err, pgx.ErrNoRows) {
		return EntryState{}, ErrNoLanding
	}
	if err != nil {
		return EntryState{}, fmt.Errorf("outcome: read landing: %w", err)
	}
	s := EntryState{Phase: Phase(phase)}
	if batch != nil {
		s.BatchID = *batch
	}
	return s, nil
}

// guardedPhase is the column a write compares-and-sets on, and every reader
// reads: the stored phase.
const guardedPhase = `queue_state`

// checkMove refuses a move the state table does not have.
func checkMove(from, to Phase, cause Cause) error {
	if from == to {
		return nil
	}
	t, ok := Lookup(from, to)
	if !ok || t.Cause != cause {
		return fmt.Errorf("%w: %s -> %s (%s)", ErrIllegalTransition, from, to, cause)
	}
	return nil
}

// transition is the one way a landings row's state changes: a compare-and-set
// on the phase it was read in, so a writer that lost a race is told instead of
// overwriting the winner.
func transition(ctx context.Context, q db.Querier, m *move) error {
	if err := checkMove(m.From, m.To, m.Cause); err != nil {
		return err
	}
	if m.To.Terminal() || m.To == PhaseReleased {
		return fmt.Errorf("%w: %s is not a stored phase", ErrIllegalTransition, m.To)
	}
	set := ""
	if m.Set != "" {
		set = m.Set + ", "
	}
	args := append([]any{m.TicketID, string(m.To), string(m.From)}, m.Args...)
	tag, err := q.Exec(ctx, `UPDATE landings SET `+set+`queue_state = $2, updated_at = now() WHERE ticket_id = $1 AND `+guardedPhase+` = $3`, args...)
	if err != nil {
		return fmt.Errorf("outcome: update landing: %w", err)
	}
	if tag.RowsAffected() == 1 {
		return nil
	}
	return conflictOf(ctx, q, m)
}

// removeRow deletes the ticket's row as a move to the terminal phase to.
func removeRow(ctx context.Context, q db.Querier, ticketID int64, to Phase) error {
	if !to.Terminal() {
		return fmt.Errorf("%w: %s is not a terminal phase", ErrIllegalTransition, to)
	}
	from, err := currentState(ctx, q, ticketID)
	if errors.Is(err, ErrNoLanding) {
		return nil
	}
	if err != nil {
		return err
	}
	t, ok := Lookup(from.Phase, to)
	if !ok {
		return fmt.Errorf("%w: %s -> %s", ErrIllegalTransition, from.Phase, to)
	}
	tag, err := q.Exec(ctx, `DELETE FROM landings WHERE ticket_id = $1 AND `+guardedPhase+` = $2`, ticketID, string(from.Phase))
	if err != nil {
		return fmt.Errorf("outcome: remove landing: %w", err)
	}
	if tag.RowsAffected() == 1 {
		return nil
	}
	err = conflictOf(ctx, q, &move{TicketID: ticketID, From: from.Phase, To: to, Cause: t.Cause})
	var c *ConflictError
	if errors.As(err, &c) && c.Actual == "" {
		return nil // gone already
	}
	return err
}

func conflictOf(ctx context.Context, q db.Querier, m *move) error {
	c := &ConflictError{TicketID: m.TicketID, From: m.From, To: m.To}
	now, err := currentState(ctx, q, m.TicketID)
	if err != nil && !errors.Is(err, ErrNoLanding) {
		return err
	}
	c.Actual = now.Phase
	return c
}

// mapNotLanding keeps the errors callers match on: no row at all, and a row
// that is back with its agent.
func mapNotLanding(err error) error {
	var c *ConflictError
	switch {
	case !errors.As(err, &c):
		return err
	case c.Actual == "":
		return ErrNoLanding
	case c.Actual == PhaseBackWithAgent:
		return ErrNotLanding
	}
	return err
}

// stepOf is the step an entry in p reads as. Only the serial engine's phases
// and a merging batch member have a step of their own; a testing member waits on
// its batch's checks.
func stepOf(p Phase) Step {
	switch p {
	case PhaseSerialMergingMain:
		return MergingMain
	case PhaseSerialWaitingChecks, PhaseBatchTesting:
		return WaitingChecks
	case PhaseSerialMerging, PhaseBatchMerging:
		return Merging
	}
	return ""
}

// setPhase fills in the row's phase and what is derived from it.
func (r *LandingRow) setPhase(p Phase) {
	r.Phase, r.Step = p, stepOf(p)
	r.State = Landing
	if p == PhaseBackWithAgent {
		r.State = BackWithAgent
	}
}
