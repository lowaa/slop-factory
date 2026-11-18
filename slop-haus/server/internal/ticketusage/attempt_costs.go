package ticketusage

import (
	"context"
	"fmt"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/reports"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// The cost rule is queryUsage's, over the attempts named: one aggregate for
// a whole page of board cards, grouped by attempt, in place of a query per
// card. unnest turns the two arrays into the (ticket, attempt) pairs wanted,
// which llm_usage_by_ticket then finds rows for.
var queryAttemptCosts = `
SELECT ticket_id, attempt_number, billing_mode, ` + reports.UsageSumsSelect + `
  FROM llm_usage
 WHERE (ticket_id, attempt_number) IN (SELECT * FROM unnest($1::bigint[], $2::int[]))
 GROUP BY ticket_id, attempt_number, billing_mode
 ORDER BY ticket_id, attempt_number, billing_mode`

var _ ticket.AttemptCosts = (*PostgresStore)(nil)

// AttemptCosts is what the workers of the attempts spent, for the board's
// cards. It reads the caller's tenant only: llm_usage is row-level scoped, so
// a ref to another tenant's ticket has no rows.
func (s *PostgresStore) AttemptCosts(ctx context.Context, refs []ticket.AttemptRef) (map[ticket.AttemptRef][]ticket.CostFigure, error) {
	tickets := make([]int64, len(refs))
	numbers := make([]int32, len(refs))
	for i, r := range refs {
		tickets[i], numbers[i] = r.TicketID, int32(r.Number)
	}
	out := map[ticket.AttemptRef][]ticket.CostFigure{}
	err := s.db.InReadTx(ctx, func(q db.Querier) error {
		rows, err := q.Query(ctx, queryAttemptCosts, tickets, numbers)
		if err != nil {
			return fmt.Errorf("ticketusage: attempt costs: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var ref ticket.AttemptRef
			var sums reports.UsageSums
			var number int32
			if err := reports.ScanUsageSums(rows, &sums, &ref.TicketID, &number); err != nil {
				return fmt.Errorf("ticketusage: attempt costs: %w", err)
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
		return nil, err
	}
	return out, nil
}
