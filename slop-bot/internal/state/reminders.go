package state

import (
	"context"
	"fmt"
	"time"

	"github.com/uptrace/bun"
)

// AsksToRemind returns the open asks put before before that the user
// hasn't been reminded of, oldest first.
func (m *Messages) AsksToRemind(ctx context.Context, before time.Time) ([]*Ask, error) {
	var rows []askJoinRow
	err := m.db.NewSelect().TableExpr("messages AS m").Join("JOIN asks AS a ON a.message_id = m.id").
		ColumnExpr("m.*").ColumnExpr("a.options, a.recommendation, a.closed_at, a.answer_message_id").
		Where("a.closed_at IS NULL").Where("a.reminded_at IS NULL").Where("m.created_at <= ?", toMillis(before)).
		Order("m.id").Scan(ctx, &rows)
	if err != nil {
		return nil, fmt.Errorf("state: listing asks to remind: %w", err)
	}
	out := make([]*Ask, len(rows))
	for i := range rows {
		if out[i], err = rows[i].ask(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Remind queues reminder to the user for the open ask askID and marks the
// ask reminded, in one transaction, so a reminder goes once per ask
// whoever sweeps. It reports false, queueing nothing, when the ask was
// already reminded of, or has closed.
func (m *Messages) Remind(ctx context.Context, askID int64, reminder *NewMessage) (bool, error) {
	reminded := false
	err := m.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		res, err := tx.NewUpdate().Table("asks").Set("reminded_at = ?", toMillis(m.clock.Now())).
			Where("message_id = ?", askID).Where("closed_at IS NULL").Where("reminded_at IS NULL").Exec(ctx)
		if err != nil {
			return fmt.Errorf("state: marking ask %d reminded: %w", askID, err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return nil
		}
		if _, err := m.insert(ctx, tx, reminder); err != nil {
			return err
		}
		reminded = true
		return nil
	})
	return reminded, err
}
