package variantcompare

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/reports"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// PostgresSource reads the tables the attempts live in. Row-level security
// scopes every read to the caller's tenant.
type PostgresSource struct {
	db *db.DB
}

func NewPostgresSource(database *db.DB) *PostgresSource { return &PostgresSource{db: database} }

var _ Source = (*PostgresSource)(nil)

func (s *PostgresSource) BatchTickets(ctx context.Context, batch uuid.UUID) ([]int64, error) {
	var ids []int64
	err := s.db.InReadTx(ctx, func(q db.Querier) error {
		rows, err := q.Query(ctx, `SELECT DISTINCT ticket_id FROM attempts WHERE replay_batch_id = $1 ORDER BY ticket_id`, batch)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				return err
			}
			ids = append(ids, id)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("variantcompare: batch tickets: %w", err)
	}
	return ids, nil
}

// Person comments addressed to an attempt, and those on the ticket as a
// whole, which go to the promoted attempt (as ticket-level writes do). Only
// comments a person wrote: one an agent wrote on a person's behalf comes from
// Remote Control, which is not attributed to attempts.
var queryFinished = `
SELECT a.ticket_id, a.number, vv.name, vv.version, a.promoted_at IS NOT NULL, a.changes_requested_at IS NOT NULL,
       hc.n, hc.words, w.secs
  FROM attempts a
  LEFT JOIN variant_versions vv ON vv.tenant_id = a.tenant_id AND vv.id = a.variant_version_id
  CROSS JOIN LATERAL (
        SELECT count(*) AS n,
               COALESCE(sum(cardinality(regexp_split_to_array(btrim(c.body), '\s+'))), 0)::bigint AS words
          FROM ticket_comments c
         WHERE c.ticket_id = a.ticket_id AND c.author_user_id IS NOT NULL
           AND COALESCE(c.attempt_number,
                        (SELECT p.number FROM attempts p WHERE p.ticket_id = a.ticket_id AND p.promoted_at IS NOT NULL)) = a.number
  ) hc
  CROSS JOIN LATERAL (
        SELECT COALESCE(sum(EXTRACT(EPOCH FROM (COALESCE(t.next_at, now()) - t.changed_at))), 0)::bigint AS secs
          FROM (SELECT to_status, changed_at, lead(changed_at) OVER (ORDER BY id) AS next_at
                  FROM attempt_status_changes s
                 WHERE s.ticket_id = a.ticket_id AND s.attempt_number = a.number) t
         WHERE t.to_status = 'discussion'
  ) w
 WHERE a.ticket_id = ANY($1) AND a.status IN ` + ticket.AttemptFinishedStatusesSQL + `
 ORDER BY a.ticket_id, a.number`

const queryRuns = `
SELECT l.ticket_id, l.attempt_number, COALESCE(c.harness_version, ''), COALESCE(c.model, ''), COALESCE(c.bundle_hash, ''),
       COALESCE(fi.name || ' v' || fi.version, '')
  FROM leases l
  JOIN lease_configs c ON c.tenant_id = l.tenant_id AND c.lease_id = l.id
  LEFT JOIN fleet_instruction_versions fi ON fi.tenant_id = c.tenant_id AND fi.id = c.fleet_instructions_version_id
 WHERE l.ticket_id = ANY($1)
 ORDER BY l.acquired_at, l.id`

func (s *PostgresSource) FinishedAttempts(ctx context.Context, ticketIDs []int64) ([]AttemptRow, int, error) {
	var out []AttemptRow
	var unfinished int
	err := s.db.InReadTx(ctx, func(q db.Querier) error {
		if err := q.QueryRow(ctx, `
			SELECT count(*) FROM attempts WHERE ticket_id = ANY($1) AND status NOT IN `+ticket.AttemptFinishedStatusesSQL,
			ticketIDs).Scan(&unfinished); err != nil {
			return err
		}
		rows, err := q.Query(ctx, queryFinished, ticketIDs)
		if err != nil {
			return err
		}
		defer rows.Close()
		index := map[ticket.AttemptRef]int{}
		for rows.Next() {
			var r AttemptRow
			var number int32
			var name *string
			var version *int32
			if err := rows.Scan(&r.Ref.TicketID, &number, &name, &version, &r.Promoted, &r.ChangesRequested,
				&r.HumanComments, &r.HumanWords, &r.HumanWaitSeconds); err != nil {
				return err
			}
			r.Ref.Number = int(number)
			if name != nil && version != nil {
				r.Variant = &VariantRef{Name: *name, Version: int(*version)}
			}
			index[r.Ref] = len(out)
			out = append(out, r)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		runs, err := q.Query(ctx, queryRuns, ticketIDs)
		if err != nil {
			return err
		}
		defer runs.Close()
		for runs.Next() {
			var ref ticket.AttemptRef
			var number int32
			var rec RunRecord
			if err := runs.Scan(&ref.TicketID, &number, &rec.HarnessVersion, &rec.Model, &rec.BundleHash, &rec.FleetInstructions); err != nil {
				return err
			}
			ref.Number = int(number)
			if i, ok := index[ref]; ok {
				out[i].Runs = append(out[i].Runs, rec)
			}
		}
		return runs.Err()
	})
	if err != nil {
		return nil, 0, fmt.Errorf("variantcompare: attempts: %w", err)
	}
	return out, unfinished, nil
}

// The guidance ledger has llm_usage's shape, so the cost rule is the one the
// board's cards use.
const queryGuidance = `
SELECT ticket_id, attempt_number, billing_mode, ` + reports.UsageSumsSelect + `
  FROM llm_guidance_usage
 WHERE (ticket_id, attempt_number) IN (SELECT * FROM unnest($1::bigint[], $2::int[]))
 GROUP BY ticket_id, attempt_number, billing_mode`

func (s *PostgresSource) GuidanceCosts(ctx context.Context, refs []ticket.AttemptRef) (map[ticket.AttemptRef][]ticket.CostFigure, error) {
	tickets := make([]int64, len(refs))
	numbers := make([]int32, len(refs))
	for i, r := range refs {
		tickets[i], numbers[i] = r.TicketID, int32(r.Number)
	}
	out := map[ticket.AttemptRef][]ticket.CostFigure{}
	err := s.db.InReadTx(ctx, func(q db.Querier) error {
		rows, err := q.Query(ctx, queryGuidance, tickets, numbers)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var ref ticket.AttemptRef
			var sums reports.UsageSums
			var number int32
			if err := reports.ScanUsageSums(rows, &sums, &ref.TicketID, &number); err != nil {
				return err
			}
			ref.Number = int(number)
			out[ref] = append(out[ref], ticket.CostFigure{
				BillingMode: sums.BillingMode, Requests: sums.Requests,
				InputTokens: sums.InputTokens, OutputTokens: sums.OutputTokens,
				CostMicros: sums.CostMicros, UnpricedRecords: sums.UnpricedRecords,
			})
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("variantcompare: guidance costs: %w", err)
	}
	return out, nil
}
