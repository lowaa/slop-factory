package state

import (
	"context"
	"fmt"
	"time"

	"github.com/uptrace/bun"

	clock "github.com/lowaa/slop-factory/go-clock"
	"github.com/lowaa/slop-factory/slop-bot/internal/ident"
	"github.com/lowaa/slop-factory/slop-bot/internal/sqlitex"
)

// Transcript is one retained transcript file.
type Transcript struct {
	Path        string
	Agent       ident.AgentID
	Ticket      ident.TicketID
	RetainUntil time.Time
	CreatedAt   time.Time
}

// NewTranscript is what Transcripts.Add needs.
type NewTranscript struct {
	Path        string
	Agent       ident.AgentID
	Ticket      ident.TicketID
	RetainUntil time.Time
}

type transcriptRow struct {
	bun.BaseModel `bun:"table:transcripts"`

	Path        string `bun:"path,pk"`
	AgentID     string `bun:"agent_id"`
	TicketID    string `bun:"ticket_id"`
	RetainUntil int64  `bun:"retain_until"`
	CreatedAt   int64  `bun:"created_at"`
}

func (r *transcriptRow) transcript() Transcript {
	return Transcript{
		Path: r.Path, Agent: ident.AgentID(r.AgentID), Ticket: ident.TicketID(r.TicketID),
		RetainUntil: fromMillis(r.RetainUntil), CreatedAt: fromMillis(r.CreatedAt),
	}
}

// Transcripts indexes the transcript files slop-bot keeps after a ticket
// ends, for the retention sweep. The files are the content; this is only
// where they are and how long they stay.
type Transcripts struct {
	db    *sqlitex.DB
	clock clock.Clock
}

// Add indexes a retained transcript. Adding a path again replaces its
// entry, e.g. to extend its retention.
func (t *Transcripts) Add(ctx context.Context, n *NewTranscript) error {
	row := &transcriptRow{
		Path: n.Path, AgentID: string(n.Agent), TicketID: string(n.Ticket),
		RetainUntil: toMillis(n.RetainUntil), CreatedAt: toMillis(t.clock.Now()),
	}
	_, err := t.db.NewInsert().Model(row).
		On("CONFLICT (path) DO UPDATE").
		Set("agent_id = EXCLUDED.agent_id").Set("ticket_id = EXCLUDED.ticket_id").
		Set("retain_until = EXCLUDED.retain_until").
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("state: indexing transcript %s: %w", n.Path, err)
	}
	return nil
}

// ForAgent returns agent's retained transcripts, oldest first.
func (t *Transcripts) ForAgent(ctx context.Context, agent ident.AgentID) ([]Transcript, error) {
	return t.list(ctx, func(q *bun.SelectQuery) *bun.SelectQuery {
		return q.Where("agent_id = ?", string(agent)).Order("created_at", "path")
	})
}

// Expired returns the transcripts whose retention ended at or before at:
// what the sweep deletes, file first, then Remove.
func (t *Transcripts) Expired(ctx context.Context, at time.Time) ([]Transcript, error) {
	return t.list(ctx, func(q *bun.SelectQuery) *bun.SelectQuery {
		return q.Where("retain_until <= ?", toMillis(at)).Order("retain_until", "path")
	})
}

// Remove drops path from the index once its file is gone.
func (t *Transcripts) Remove(ctx context.Context, path string) error {
	res, err := t.db.NewDelete().Model((*transcriptRow)(nil)).Where("path = ?", path).Exec(ctx)
	if err != nil {
		return fmt.Errorf("state: removing transcript %s: %w", path, err)
	}
	return requireRow(res, "transcript %s", path)
}

func (t *Transcripts) list(ctx context.Context, where func(*bun.SelectQuery) *bun.SelectQuery) ([]Transcript, error) {
	var rows []transcriptRow
	if err := t.db.NewSelect().Model(&rows).Apply(where).Scan(ctx); err != nil {
		return nil, fmt.Errorf("state: listing transcripts: %w", err)
	}
	out := make([]Transcript, len(rows))
	for i := range rows {
		out[i] = rows[i].transcript()
	}
	return out, nil
}
