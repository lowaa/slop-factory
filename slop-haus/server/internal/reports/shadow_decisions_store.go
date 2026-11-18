package reports

import (
	"context"
	"fmt"
	"time"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
)

const queryShadowTotals = `
SELECT count(*) FILTER (WHERE decision = 'would_accept'),
       count(*) FILTER (WHERE decision = 'would_refuse'),
       count(*) FILTER (WHERE decision = 'would_accept' AND ci_passed),
       count(*) FILTER (WHERE decision = 'would_accept' AND NOT ci_passed),
       count(*) FILTER (WHERE decision = 'would_accept' AND ci_passed IS NULL)
  FROM merge_drift_decisions
 WHERE decided_at >= $1 AND decided_at < $2`

const queryShadowReasons = `
SELECT decision, reason, count(*) AS n
  FROM merge_drift_decisions
 WHERE decided_at >= $1 AND decided_at < $2
 GROUP BY decision, reason
 ORDER BY n DESC, decision, reason
 LIMIT $3`

const queryShadowFailures = `
SELECT d.ticket_id, t.title, d.change_id, d.head_sha, d.ci_head_sha, d.drift_dirs, d.decided_at, d.ci_recorded_at
  FROM merge_drift_decisions d
  JOIN tickets t ON t.tenant_id = d.tenant_id AND t.id = d.ticket_id
 WHERE d.decided_at >= $1 AND d.decided_at < $2
   AND d.decision = 'would_accept' AND NOT d.ci_passed
 ORDER BY d.decided_at DESC, d.id DESC
 LIMIT $3`

// ShadowDecisions reads the independent-apps decisions made in [from, to).
// Row level security keeps it to the context's tenant.
func (s *PostgresStore) ShadowDecisions(ctx context.Context, from, to time.Time) (*ShadowDecisions, error) {
	out := &ShadowDecisions{From: from, To: to, Reasons: []ShadowReason{}, Failures: []ShadowFailure{}}
	err := s.readTx(ctx, func(q db.Querier) error {
		if err := q.QueryRow(ctx, queryShadowTotals, from, to).Scan(&out.WouldAccept, &out.WouldRefuse,
			&out.AcceptCIPassed, &out.AcceptCIFailed, &out.AcceptAwaitingCI); err != nil {
			return fmt.Errorf("reports: shadow totals: %w", err)
		}
		rows, err := q.Query(ctx, queryShadowReasons, from, to, MaxShadowReasons)
		if err != nil {
			return fmt.Errorf("reports: shadow reasons: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var r ShadowReason
			if err := rows.Scan(&r.Decision, &r.Reason, &r.Count); err != nil {
				return fmt.Errorf("reports: scan shadow reason: %w", err)
			}
			out.Reasons = append(out.Reasons, r)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		failed, err := q.Query(ctx, queryShadowFailures, from, to, MaxShadowFailures)
		if err != nil {
			return fmt.Errorf("reports: shadow failures: %w", err)
		}
		defer failed.Close()
		for failed.Next() {
			var f ShadowFailure
			if err := failed.Scan(&f.TicketID, &f.Title, &f.ChangeID, &f.HeadSHA, &f.CIHeadSHA, &f.DriftDirs, &f.DecidedAt, &f.CIRecordedAt); err != nil {
				return fmt.Errorf("reports: scan shadow failure: %w", err)
			}
			out.Failures = append(out.Failures, f)
		}
		return failed.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
