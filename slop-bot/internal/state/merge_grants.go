package state

import (
	"context"
	"fmt"

	"github.com/uptrace/bun"

	clock "github.com/lowaa/slop-factory/go-clock"
	"github.com/lowaa/slop-factory/slop-bot/internal/ident"
	"github.com/lowaa/slop-factory/slop-bot/internal/sqlitex"
)

type mergeGrantRow struct {
	bun.BaseModel `bun:"table:merge_grants"`

	Ticket    string `bun:"ticket_id,pk"`
	GrantedBy string `bun:"granted_by"`
	GrantedAt int64  `bun:"granted_at"`
}

// MergeGrants records which tickets the user has authorised a merge for.
type MergeGrants struct {
	db    *sqlitex.DB
	clock clock.Clock
}

// Grant records the user's go-ahead to merge ticket; granting again
// re-stamps it.
func (g *MergeGrants) Grant(ctx context.Context, ticket ident.TicketID, by Party) error {
	row := &mergeGrantRow{Ticket: string(ticket), GrantedBy: string(by), GrantedAt: toMillis(g.clock.Now())}
	_, err := g.db.NewInsert().Model(row).
		On("CONFLICT (ticket_id) DO UPDATE").Set("granted_by = EXCLUDED.granted_by").Set("granted_at = EXCLUDED.granted_at").
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("state: granting a merge of ticket %s: %w", ticket, err)
	}
	return nil
}

// Granted reports whether ticket has the user's go-ahead to merge.
func (g *MergeGrants) Granted(ctx context.Context, ticket ident.TicketID) (bool, error) {
	n, err := g.db.NewSelect().Model((*mergeGrantRow)(nil)).Where("ticket_id = ?", string(ticket)).Count(ctx)
	if err != nil {
		return false, fmt.Errorf("state: reading ticket %s's merge grant: %w", ticket, err)
	}
	return n > 0, nil
}

// Revoke clears ticket's go-ahead, if it has one.
func (g *MergeGrants) Revoke(ctx context.Context, ticket ident.TicketID) error {
	if _, err := g.db.NewDelete().Model((*mergeGrantRow)(nil)).Where("ticket_id = ?", string(ticket)).Exec(ctx); err != nil {
		return fmt.Errorf("state: revoking ticket %s's merge grant: %w", ticket, err)
	}
	return nil
}
