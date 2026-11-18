package ticket

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
)

// MergeFactsStore is what Close as merged reads and writes beside the ticket
// itself. A separate interface from Store for the reason PRAttemptStore is:
// PostgresStore is the one implementation.
type MergeFactsStore interface {
	// RecordIgnoredMerge keeps m once per ticket, attempt and change.
	RecordIgnoredMerge(ctx context.Context, m *IgnoredMerge) error
	// LatestIgnoredMerge is the newest ignored merge of pr recorded for the
	// ticket, or nil.
	LatestIgnoredMerge(ctx context.Context, ticketID int64, pr codehost.ChangeRef) (*IgnoredMerge, error)
	// ReviewedHead is the head commit the promoted attempt's approval covers
	// on pr: empty when it has no approval or none was recorded for pr.
	ReviewedHead(ctx context.Context, ticketID int64, pr codehost.ChangeRef) (string, error)
}

func (s *PostgresStore) RecordIgnoredMerge(ctx context.Context, m *IgnoredMerge) error {
	_, err := s.db.Exec(ctx, `
		INSERT INTO ignored_merges (ticket_id, attempt_number, host, owner, repo, number, merge_sha, merger_login, merger_github_id, reason)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (tenant_id, ticket_id, attempt_number, host, owner, repo, number) DO NOTHING`,
		m.TicketID, m.Attempt, m.PR.Host, m.PR.Owner, m.PR.Repo, m.PR.Number, m.MergeSHA, m.MergerLogin, m.MergerGitHubID, m.Reason)
	if err != nil {
		return fmt.Errorf("record ignored merge: %w", err)
	}
	return nil
}

func (s *PostgresStore) LatestIgnoredMerge(ctx context.Context, ticketID int64, pr codehost.ChangeRef) (*IgnoredMerge, error) {
	m := &IgnoredMerge{TicketID: ticketID, PR: pr}
	err := s.db.QueryRow(ctx, `
		SELECT attempt_number, merge_sha, merger_login, merger_github_id, reason, created_at FROM ignored_merges
		 WHERE ticket_id = $1 AND host = $2 AND owner = $3 AND repo = $4 AND number = $5
		 ORDER BY created_at DESC, id DESC LIMIT 1`,
		ticketID, pr.Host, pr.Owner, pr.Repo, pr.Number).
		Scan(&m.Attempt, &m.MergeSHA, &m.MergerLogin, &m.MergerGitHubID, &m.Reason, &m.At)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read ignored merge: %w", err)
	}
	return m, nil
}

func (s *PostgresStore) ReviewedHead(ctx context.Context, ticketID int64, pr codehost.ChangeRef) (string, error) {
	var sha *string
	err := s.db.QueryRow(ctx, `
		SELECT c.reviewed_head_sha FROM attempts a
		  JOIN ticket_changes c ON c.ticket_id = a.ticket_id AND c.attempt_number = a.number
		 WHERE a.ticket_id = $1 AND a.promoted_at IS NOT NULL AND a.approved_at IS NOT NULL
		   AND c.host = $2 AND c.owner = $3 AND c.repo = $4 AND c.number = $5`,
		ticketID, pr.Host, pr.Owner, pr.Repo, pr.Number).Scan(&sha)
	if errors.Is(err, pgx.ErrNoRows) || sha == nil {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read reviewed head: %w", err)
	}
	return *sha, nil
}
