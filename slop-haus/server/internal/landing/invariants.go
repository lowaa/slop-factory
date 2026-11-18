package landing

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
)

// The rules that span the landings, batches and attempts tables. Each was
// broken by a bug that nothing reported: a repair sweep fixed the
// symptom quietly.
const (
	RuleBatchMembersAgree    = "batch_members_agree"
	RuleOneEnginePerApp      = "one_engine_per_app"
	RuleQueueStatusMatches   = "queue_status_matches_rows"
	RulePositionsOrdered     = "positions_ordered"
	RuleFinishedAccountedFor = "finished_batches_accounted_for"
)

// InvariantRules is every rule CheckInvariants reports, for a metric to start
// each at zero.
var InvariantRules = []string{
	RuleBatchMembersAgree, RuleOneEnginePerApp, RuleQueueStatusMatches, RulePositionsOrdered, RuleFinishedAccountedFor,
}

// Violation is one broken rule. TicketID and BatchID are zero where the rule is
// not about one.
type Violation struct {
	Rule     string
	TicketID int64
	BatchID  int64
	Detail   string
}

// CheckInvariants reads the tenant's queue and reports every rule it breaks. It
// only reads: what to do about a break is for a person, or for the state
// machine that will make the break impossible.
//
// The rules are written against the stored states (landings.queue_state and
// batch_id/batch_position, batches.state, attempt status). policy decides which app a
// row's paths are in, as batch selection and the lane check do: paths in no
// single app are shared, and count in every app.
//
// The tables are read one after the other, so a write between two reads can show
// as a break that is not one; a caller that runs this on a live system confirms
// a violation by seeing it again.
func CheckInvariants(ctx context.Context, q db.Querier, tenant uuid.UUID, policy BatchPolicy) ([]Violation, error) {
	s, err := readQueueSnapshot(ctx, q, tenant)
	if err != nil {
		return nil, err
	}
	var out []Violation
	out = append(out, s.batchMembersAgree()...)
	out = append(out, s.oneEnginePerApp(policy)...)
	out = append(out, s.queueStatusMatches()...)
	out = append(out, s.positionsOrdered()...)
	out = append(out, s.finishedAccountedFor()...)
	return out, nil
}

type snapshotLanding struct {
	ticketID int64
	attempt  int
	phase    Phase
	batchID  *int64
	position *int
}

type snapshotBatch struct {
	id      int64
	state   string
	members []int64
}

type snapshotOutcome struct {
	batchID  int64
	ticketID int64
	outcome  string
}

// mergedEarly is a member that merged while its batch was still being built or
// tested: the batch no longer tests what lands.
type mergedEarly struct {
	batchID  int64
	ticketID int64
	state    string
}

type queueSnapshot struct {
	mergedEarly []mergedEarly
	landings    []snapshotLanding
	batches     map[int64]*snapshotBatch
	// inQueue are the attempts whose status is in_merge_queue, as ticket id and attempt number.
	inQueue  [][2]int64
	outcomes []snapshotOutcome
	paths    map[int64][]string
}

func readQueueSnapshot(ctx context.Context, q db.Querier, tenant uuid.UUID) (*queueSnapshot, error) {
	s := &queueSnapshot{batches: map[int64]*snapshotBatch{}, paths: map[int64][]string{}}
	rows, err := q.Query(ctx, `
SELECT ticket_id, attempt_number, queue_state, batch_id, batch_position
  FROM landings WHERE tenant_id = $1 ORDER BY ticket_id`, tenant)
	if err != nil {
		return nil, fmt.Errorf("landing: invariants: read landings: %w", err)
	}
	for rows.Next() {
		var l snapshotLanding
		var phase string
		if err := rows.Scan(&l.ticketID, &l.attempt, &phase, &l.batchID, &l.position); err != nil {
			rows.Close()
			return nil, fmt.Errorf("landing: invariants: scan landing: %w", err)
		}
		l.phase = Phase(phase)
		s.landings = append(s.landings, l)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("landing: invariants: read landings: %w", err)
	}

	rows, err = q.Query(ctx, `SELECT id, state, member_ticket_ids FROM batches WHERE tenant_id = $1 ORDER BY id`, tenant)
	if err != nil {
		return nil, fmt.Errorf("landing: invariants: read batches: %w", err)
	}
	for rows.Next() {
		var b snapshotBatch
		if err := rows.Scan(&b.id, &b.state, &b.members); err != nil {
			rows.Close()
			return nil, fmt.Errorf("landing: invariants: scan batch: %w", err)
		}
		s.batches[b.id] = &b
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("landing: invariants: read batches: %w", err)
	}

	rows, err = q.Query(ctx, `SELECT ticket_id, number FROM attempts WHERE tenant_id = $1 AND status = 'in_merge_queue' ORDER BY ticket_id, number`, tenant)
	if err != nil {
		return nil, fmt.Errorf("landing: invariants: read attempts: %w", err)
	}
	for rows.Next() {
		var id, n int64
		if err := rows.Scan(&id, &n); err != nil {
			rows.Close()
			return nil, fmt.Errorf("landing: invariants: scan attempt: %w", err)
		}
		s.inQueue = append(s.inQueue, [2]int64{id, n})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("landing: invariants: read attempts: %w", err)
	}

	rows, err = q.Query(ctx, `
SELECT o.batch_id, o.ticket_id, o.outcome FROM server_merge_outcomes o
  JOIN batches b ON b.tenant_id = o.tenant_id AND b.id = o.batch_id
 WHERE o.tenant_id = $1 AND b.state = 'done'`, tenant)
	if err != nil {
		return nil, fmt.Errorf("landing: invariants: read outcomes: %w", err)
	}
	for rows.Next() {
		var o snapshotOutcome
		if err := rows.Scan(&o.batchID, &o.ticketID, &o.outcome); err != nil {
			rows.Close()
			return nil, fmt.Errorf("landing: invariants: scan outcome: %w", err)
		}
		s.outcomes = append(s.outcomes, o)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("landing: invariants: read outcomes: %w", err)
	}

	rows, err = q.Query(ctx, `
SELECT DISTINCT b.id, o.ticket_id, b.state FROM batches b
  JOIN server_merge_outcomes o ON o.tenant_id = b.tenant_id AND o.ticket_id = ANY(b.member_ticket_ids)
 WHERE b.tenant_id = $1 AND b.state IN ('building', 'testing') AND o.outcome IN ('merged', 'already_merged') AND o.batch_id IS NULL AND o.created_at >= b.created_at
 ORDER BY b.id, o.ticket_id`, tenant)
	if err != nil {
		return nil, fmt.Errorf("landing: invariants: read early merges: %w", err)
	}
	for rows.Next() {
		var m mergedEarly
		if err := rows.Scan(&m.batchID, &m.ticketID, &m.state); err != nil {
			rows.Close()
			return nil, fmt.Errorf("landing: invariants: scan early merge: %w", err)
		}
		s.mergedEarly = append(s.mergedEarly, m)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("landing: invariants: read early merges: %w", err)
	}

	// Paths only of the tickets an engine acts on: serial rows with a step, and
	// the members of live batches.
	var ids []int64
	for i := range s.landings {
		if l := &s.landings[i]; l.phase.Serial() {
			ids = append(ids, l.ticketID)
		}
	}
	for _, b := range s.batches {
		if b.state != string(BatchDone) {
			ids = append(ids, b.members...)
		}
	}
	rows, err = q.Query(ctx, `SELECT ticket_id, path FROM ticket_paths WHERE tenant_id = $1 AND ticket_id = ANY($2) ORDER BY ticket_id, path`, tenant, ids)
	if err != nil {
		return nil, fmt.Errorf("landing: invariants: read paths: %w", err)
	}
	for rows.Next() {
		var id int64
		var p string
		if err := rows.Scan(&id, &p); err != nil {
			rows.Close()
			return nil, fmt.Errorf("landing: invariants: scan path: %w", err)
		}
		s.paths[id] = append(s.paths[id], p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("landing: invariants: read paths: %w", err)
	}
	return s, nil
}

func (s *queueSnapshot) sortedBatches() []*snapshotBatch {
	out := make([]*snapshotBatch, 0, len(s.batches))
	for _, b := range s.batches {
		out = append(out, b)
	}
	slices.SortFunc(out, func(a, b *snapshotBatch) int { return int(a.id - b.id) })
	return out
}

// reserved are the landing rows a batch holds, in position order.
func (s *queueSnapshot) reserved(batch int64) []snapshotLanding {
	var out []snapshotLanding
	for _, l := range s.landings {
		if l.batchID != nil && *l.batchID == batch {
			out = append(out, l)
		}
	}
	return out
}

// batchMembersAgree: a row names a batch only while the batch is live and lists
// the ticket; and a live batch some members of which were reserved keeps one.
// A batch every member of which has left it is finished and must say so. A
// member that merges before its batch reaches merging was landed by another
// engine.
func (s *queueSnapshot) batchMembersAgree() []Violation {
	var out []Violation
	for _, l := range s.landings {
		if l.batchID == nil {
			continue
		}
		b, ok := s.batches[*l.batchID]
		switch {
		case !ok:
			out = append(out, Violation{Rule: RuleBatchMembersAgree, TicketID: l.ticketID, BatchID: *l.batchID, Detail: "the row names a batch that does not exist"})
		case b.state == string(BatchDone):
			out = append(out, Violation{Rule: RuleBatchMembersAgree, TicketID: l.ticketID, BatchID: b.id, Detail: fmt.Sprintf("the row is still reserved by a done batch (row state %s)", l.phase)})
		case !slices.Contains(b.members, l.ticketID):
			out = append(out, Violation{Rule: RuleBatchMembersAgree, TicketID: l.ticketID, BatchID: b.id, Detail: "the row is reserved by a batch that does not list the ticket"})
		case !l.phase.Batch():
			out = append(out, Violation{Rule: RuleBatchMembersAgree, TicketID: l.ticketID, BatchID: b.id, Detail: fmt.Sprintf("a row in state %s is still reserved by a live batch", l.phase)})
		}
	}
	for _, l := range s.landings {
		if l.phase.Batch() && l.batchID == nil {
			out = append(out, Violation{Rule: RuleBatchMembersAgree, TicketID: l.ticketID, Detail: fmt.Sprintf("a row in state %s names no batch", l.phase)})
		}
	}
	for _, m := range s.mergedEarly {
		out = append(out, Violation{Rule: RuleBatchMembersAgree, TicketID: m.ticketID, BatchID: m.batchID, Detail: fmt.Sprintf("the member merged while its batch was still %s", m.state)})
	}
	for _, b := range s.sortedBatches() {
		// A building batch reserves its members only once the branch is built.
		if b.state != string(BatchDone) && b.state != string(BatchBuilding) && len(s.reserved(b.id)) == 0 {
			out = append(out, Violation{Rule: RuleBatchMembersAgree, BatchID: b.id, Detail: fmt.Sprintf("the batch is %s but none of its members is reserved by it: every member has finished", b.state)})
		}
	}
	return out
}

// engine is something acting on the queue: a serial landing in hand, or a live
// batch with all its members.
type engine struct {
	name     string
	ticketID int64
	batchID  int64
	paths    []string
}

// oneEnginePerApp: at most one engine is active in an app. A row touching a
// shared path (or several apps) counts in every app. A batch is listed first, so
// the serial landing that started beside it is the one reported.
func (s *queueSnapshot) oneEnginePerApp(policy BatchPolicy) []Violation {
	var engines []engine
	for _, b := range s.sortedBatches() {
		if b.state == string(BatchDone) {
			continue
		}
		e := engine{name: fmt.Sprintf("batch %d", b.id), batchID: b.id}
		for _, id := range b.members {
			e.paths = append(e.paths, s.paths[id]...)
		}
		engines = append(engines, e)
	}
	for _, l := range s.landings {
		if l.phase.Serial() {
			engines = append(engines, engine{name: fmt.Sprintf("serial landing of ticket %d", l.ticketID), ticketID: l.ticketID, paths: s.paths[l.ticketID]})
		}
	}
	var out []Violation
	for i := 1; i < len(engines); i++ {
		for j := range i {
			if enginesShareApp(policy, &engines[i], &engines[j]) {
				out = append(out, Violation{Rule: RuleOneEnginePerApp, TicketID: engines[i].ticketID, BatchID: engines[i].batchID,
					Detail: fmt.Sprintf("%s is active in the same app as %s", engines[i].name, engines[j].name)})
				break
			}
		}
	}
	return out
}

func enginesShareApp(policy BatchPolicy, a, b *engine) bool {
	appA, okA := policy.App(a.paths)
	appB, okB := policy.App(b.paths)
	return !okA || !okB || appA == appB
}

// queueStatusMatches: an attempt is in_merge_queue if and only if it has a live
// landings row.
func (s *queueSnapshot) queueStatusMatches() []Violation {
	live := map[[2]int64]bool{}
	for _, l := range s.landings {
		if l.phase != PhaseBackWithAgent {
			live[[2]int64{l.ticketID, int64(l.attempt)}] = true
		}
	}
	var out []Violation
	queued := map[[2]int64]bool{}
	for _, a := range s.inQueue {
		queued[a] = true
		if !live[a] {
			out = append(out, Violation{Rule: RuleQueueStatusMatches, TicketID: a[0], Detail: fmt.Sprintf("attempt %d is in_merge_queue and has no live landings row", a[1])})
		}
	}
	for _, l := range s.landings {
		if k := [2]int64{l.ticketID, int64(l.attempt)}; l.phase != PhaseBackWithAgent && !queued[k] {
			out = append(out, Violation{Rule: RuleQueueStatusMatches, TicketID: l.ticketID, Detail: fmt.Sprintf("attempt %d has a live landings row and is not in_merge_queue", l.attempt)})
		}
	}
	return out
}

// positionsOrdered: the positions of a live batch's reserved rows are unique
// and strictly increasing in the members' tested order. Gaps are fine: a merged
// member's row goes and the ones behind it keep their place, as the position is
// only there to give the merge order.
func (s *queueSnapshot) positionsOrdered() []Violation {
	var out []Violation
	for _, b := range s.sortedBatches() {
		if b.state == string(BatchDone) {
			continue
		}
		rows := s.reserved(b.id)
		if len(rows) == 0 {
			continue
		}
		rank := make(map[int64]int, len(b.members))
		for i, id := range b.members {
			rank[id] = i
		}
		slices.SortStableFunc(rows, func(x, y snapshotLanding) int { return rank[x.ticketID] - rank[y.ticketID] })
		var got []string
		for _, r := range rows {
			if r.position == nil {
				got = append(got, fmt.Sprintf("#%d:none", r.ticketID))
				continue
			}
			got = append(got, fmt.Sprintf("#%d:%d", r.ticketID, *r.position))
		}
		for i, r := range rows {
			if r.position == nil || (i > 0 && rows[i-1].position != nil && *rows[i-1].position >= *r.position) {
				out = append(out, Violation{Rule: RulePositionsOrdered, BatchID: b.id, Detail: "positions are not unique and increasing in tested order: " + strings.Join(got, " ")})
				break
			}
		}
	}
	return out
}

// accountedOutcomes end a member's part in a batch: it merged, or the batch
// was voided under it.
var accountedOutcomes = []string{"merged", "already_merged", "batch_voided"}

// finishedAccountedFor: every member of a done batch ended in a recorded way:
// an outcome of the batch that merged it or voided it, or, if the batch handed
// it back, the row waiting for its author with an outcome of the batch to say why.
func (s *queueSnapshot) finishedAccountedFor() []Violation {
	byMember := map[[2]int64][]string{}
	for _, o := range s.outcomes {
		k := [2]int64{o.batchID, o.ticketID}
		byMember[k] = append(byMember[k], o.outcome)
	}
	back := map[int64]bool{}
	for _, l := range s.landings {
		if l.phase == PhaseBackWithAgent {
			back[l.ticketID] = true
		}
	}
	var out []Violation
	for _, b := range s.sortedBatches() {
		if b.state != string(BatchDone) {
			continue
		}
		for _, id := range b.members {
			got := byMember[[2]int64{b.id, id}]
			if slices.ContainsFunc(got, func(o string) bool { return slices.Contains(accountedOutcomes, o) }) {
				continue
			}
			if len(got) > 0 && back[id] {
				continue
			}
			out = append(out, Violation{Rule: RuleFinishedAccountedFor, TicketID: id, BatchID: b.id, Detail: fmt.Sprintf("the batch is done and the member has no merged, batch_voided or handed-back outcome of it (outcomes %v)", got)})
		}
	}
	return out
}
