package guidance

import (
	"context"
	"errors"
	"fmt"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/change"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
)

// maxCandidates bounds the merged changes looked at, and so the host calls a
// handback makes: two per change that landed since the ticket's base.
const maxCandidates = 20

// PostgresSource is Source over the change store and the code host: the
// changes the server merged, and the host's diff of each.
type PostgresSource struct {
	db      *db.DB
	changes change.Store
	hosts   *codehost.Registry
}

func NewPostgresSource(database *db.DB, store change.Store, hosts *codehost.Registry) *PostgresSource {
	return &PostgresSource{db: database, changes: store, hosts: hosts}
}

func (s *PostgresSource) host(ctx context.Context, ticketID int64, attempt int) (*change.Record, codehost.Changes, error) {
	rec, err := s.changes.Current(ctx, ticketID, attempt)
	if err != nil {
		return nil, nil, err
	}
	c, ok := s.hosts.Changes(rec.Change.Host)
	if !ok {
		return nil, nil, fmt.Errorf("%w: %s", change.ErrNoHost, rec.Change.Host)
	}
	return rec, c, nil
}

func (s *PostgresSource) HeadFiles(ctx context.Context, ticketID int64, attempt int, head string) ([]string, error) {
	rec, c, err := s.host(ctx, ticketID, attempt)
	if err != nil {
		return nil, err
	}
	tenant, err := db.TenantFrom(ctx)
	if err != nil {
		return nil, err
	}
	d, err := c.Diff(ctx, tenant, rec.Change, head)
	if err != nil {
		return nil, err
	}
	return paths(d), nil
}

// LandedSince lists the merged changes of the repository whose merge commit
// descends from the ticket's base commit. Merges are serialised by the lock, so
// merge time is the order on the base, and each change's files are read as the
// commit before it against its own merge commit: that includes anything a
// person pushed straight to the base in between (a deploy commit), which is
// named too rather than left out.
func (s *PostgresSource) LandedSince(ctx context.Context, ticketID int64, attempt int) ([]Landed, error) {
	rec, c, err := s.host(ctx, ticketID, attempt)
	if err != nil {
		return nil, err
	}
	tenant, err := db.TenantFrom(ctx)
	if err != nil {
		return nil, err
	}
	var base *string
	if err := s.db.QueryRow(ctx, `SELECT base_commit FROM tickets WHERE id = $1`, ticketID).Scan(&base); err != nil {
		return nil, fmt.Errorf("guidance: read the base commit: %w", err)
	}
	if base == nil || *base == "" {
		return nil, errors.New("guidance: the ticket has no base commit")
	}
	rows, err := s.db.Query(ctx, `
SELECT ticket_id, number, merge_sha FROM ticket_changes
 WHERE merged_at IS NOT NULL AND COALESCE(merge_sha, '') <> '' AND ticket_id <> $1
   AND host = $2 AND owner = $3 AND repo = $4
 ORDER BY merged_at DESC, id DESC LIMIT $5`, ticketID, rec.Change.Host, rec.Change.Owner, rec.Change.Repo, maxCandidates)
	if err != nil {
		return nil, fmt.Errorf("guidance: list merged changes: %w", err)
	}
	var newestFirst []Landed
	for rows.Next() {
		var l Landed
		var number int64
		if err := rows.Scan(&l.TicketID, &number, &l.MergeSHA); err != nil {
			rows.Close()
			return nil, fmt.Errorf("guidance: scan merged change: %w", err)
		}
		ref := rec.Change
		ref.Number = number
		l.ChangeURL = s.hosts.ChangeURL(ref)
		newestFirst = append(newestFirst, l)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("guidance: list merged changes: %w", err)
	}
	// Release the connection before the host calls below, which are slow and many.
	rows.Close()

	// Newest first, stopping at the first that is not after the base.
	var kept []Landed
	for _, l := range newestFirst {
		if l.MergeSHA == *base {
			// The base itself descends from itself; its work was there at the start.
			break
		}
		d, err := c.CompareCommits(ctx, tenant, rec.Change, *base, l.MergeSHA)
		if err != nil {
			return nil, err
		}
		if d.MergeBaseSHA != d.BaseSHA {
			break
		}
		kept = append(kept, l)
	}
	out := make([]Landed, len(kept))
	for i := range kept {
		out[len(kept)-1-i] = kept[i]
	}
	from := *base
	for i := range out {
		d, err := c.CompareCommits(ctx, tenant, rec.Change, from, out[i].MergeSHA)
		if err != nil {
			return nil, err
		}
		out[i].Files = paths(d)
		from = out[i].MergeSHA
	}
	return out, nil
}

func paths(d *codehost.Diff) []string {
	out := make([]string, len(d.Files))
	for i, f := range d.Files {
		out[i] = f.Path
	}
	return out
}

// PostgresHistory is History over the outcomes the runner records.
type PostgresHistory struct{ db *db.DB }

func NewPostgresHistory(database *db.DB) *PostgresHistory { return &PostgresHistory{db: database} }

// Collisions counts the ticket's handbacks recorded as collisions since the
// handback that reached the bound, which is the point a person looked at it.
func (h *PostgresHistory) Collisions(ctx context.Context, ticketID int64) (int, error) {
	var n int
	err := h.db.QueryRow(ctx, `
SELECT count(*) FROM server_merge_outcomes
 WHERE ticket_id = $1 AND starts_with(detail, $2) AND NOT starts_with(detail, $3)
   AND created_at > COALESCE((SELECT max(created_at) FROM server_merge_outcomes WHERE ticket_id = $1 AND starts_with(detail, $3)), '-infinity')`,
		ticketID, DetailPrefix, BoundPrefix).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("guidance: count collisions: %w", err)
	}
	return n, nil
}
