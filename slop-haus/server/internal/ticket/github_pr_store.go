package ticket

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

// PRAttempt is an attempt whose reported PR a link may mean.
type PRAttempt struct {
	AttemptRef
	Promoted bool
	PRURL    string
}

// AttemptsForPR is the attempts, of any ticket and promoted or not, whose
// pr_url starts with link (case-insensitively): a narrowing by the
// attempts_by_pr_url index, which the caller makes exact.
func (s *PostgresStore) AttemptsForPR(ctx context.Context, link string) ([]PRAttempt, error) {
	rows, err := s.db.Query(ctx, `
		SELECT ticket_id, number, promoted_at IS NOT NULL, pr_url FROM attempts
		 WHERE lower(pr_url) LIKE $1 ESCAPE '\'
		 ORDER BY ticket_id, number`, likePrefix(link))
	if err != nil {
		return nil, fmt.Errorf("find attempts by pr: %w", err)
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (PRAttempt, error) {
		var a PRAttempt
		err := row.Scan(&a.TicketID, &a.Number, &a.Promoted, &a.PRURL)
		return a, err
	})
}

// likePrefix is s as a LIKE pattern matching anything starting with it.
func likePrefix(s string) string {
	return likeEscape(strings.ToLower(s)) + "%"
}
