package steward

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
)

// LeaseTTL is how long a run's hold on the steward slot lasts without being
// renewed, so a supervisor that died mid-run frees it.
const LeaseTTL = 30 * time.Minute

// MaxEvents bounds the board events one poll returns.
const MaxEvents = 200

// BoardEventKind is why a board event wakes the steward.
type BoardEventKind string

const (
	EventDiscussion BoardEventKind = "discussion"
	EventAgentFiled BoardEventKind = "agent_filed"
)

// BoardEvent is a change on the board the steward should look at.
type BoardEvent struct {
	Kind     BoardEventKind
	TicketID int64
	// Cursor is the position after this event.
	Cursor int64
}

// Supervision is what a supervisor's steward runner reads and takes from
// the server: board events, the run lease and the agreement report.
type Supervision struct {
	db       *db.DB
	reporter AgreementReporter
	settings Settings
}

// AgreementReporter renders the agreement report as prose. The agreement
// package implements it; it imports this one, so it is injected.
type AgreementReporter interface {
	Text(ctx context.Context, since, until time.Time) (string, error)
}

func NewSupervision(database *db.DB, reporter AgreementReporter, settings Settings) *Supervision {
	return &Supervision{db: database, reporter: reporter, settings: settings}
}

// Mode is the tenant's steward_mode, for a supervisor deciding whether to run a steward at all.
func (s *Supervision) Mode(ctx context.Context) (Mode, error) {
	cfg, err := s.settings.StewardConfig(ctx)
	if err != nil {
		return "", fmt.Errorf("steward: mode: %w", err)
	}
	return cfg.Mode, nil
}

// Head is the cursor past everything on the board now, for a first poll that
// should see only what happens from here.
func (s *Supervision) Head(ctx context.Context) (int64, error) {
	var head int64
	if err := s.db.QueryRow(ctx, `SELECT COALESCE(max(id), 0) FROM ticket_status_changes`).Scan(&head); err != nil {
		return 0, fmt.Errorf("steward: board head: %w", err)
	}
	return head, nil
}

// Events are the wake events after cursor, oldest first, up to limit: a ticket
// entering discussion, and a ticket an agent filed in preparing. The second
// returned cursor is where to poll from next; it advances past status changes
// that wake nobody, so they are not read again.
func (s *Supervision) Events(ctx context.Context, after int64, limit int) ([]BoardEvent, int64, error) {
	if limit <= 0 || limit > MaxEvents {
		limit = MaxEvents
	}
	rows, err := s.db.Query(ctx, `
		SELECT c.id, c.ticket_id, c.to_status, c.from_status IS NULL AND COALESCE(t.created_by_agent_id IS NOT NULL, false)
		  FROM ticket_status_changes c LEFT JOIN tickets t ON t.tenant_id = c.tenant_id AND t.id = c.ticket_id
		 WHERE c.id > $1 ORDER BY c.id LIMIT $2`, after, limit)
	if err != nil {
		return nil, 0, fmt.Errorf("steward: board events: %w", err)
	}
	defer rows.Close()
	var out []BoardEvent
	next := after
	for rows.Next() {
		var id, ticketID int64
		var to string
		var agentCreated bool
		if err := rows.Scan(&id, &ticketID, &to, &agentCreated); err != nil {
			return nil, 0, fmt.Errorf("steward: board events: %w", err)
		}
		next = id
		switch {
		case to == "discussion":
			out = append(out, BoardEvent{Kind: EventDiscussion, TicketID: ticketID, Cursor: id})
		case to == "preparing" && agentCreated:
			out = append(out, BoardEvent{Kind: EventAgentFiled, TicketID: ticketID, Cursor: id})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("steward: board events: %w", err)
	}
	return out, next, nil
}

// AcquireLease takes the tenant's steward slot for holder, or renews it when
// holder already has it. False when another holder's lease has not expired.
// Expiry is judged by the database clock so supervisors served by different
// API instances agree on it.
func (s *Supervision) AcquireLease(ctx context.Context, holder uuid.UUID) (bool, time.Time, error) {
	var expires time.Time
	err := s.db.QueryRow(ctx, `
		INSERT INTO steward_leases (holder_id, expires_at) VALUES ($1, now() + $2 * interval '1 second')
		ON CONFLICT (tenant_id) DO UPDATE SET holder_id = EXCLUDED.holder_id, expires_at = EXCLUDED.expires_at
		 WHERE steward_leases.holder_id = EXCLUDED.holder_id OR steward_leases.expires_at <= now()
		RETURNING expires_at`, holder, LeaseTTL.Seconds()).Scan(&expires)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, time.Time{}, nil
		}
		return false, time.Time{}, fmt.Errorf("steward: acquire lease: %w", err)
	}
	return true, expires, nil
}

// ReleaseLease gives the slot back; a holder that does not hold it changes nothing.
func (s *Supervision) ReleaseLease(ctx context.Context, holder uuid.UUID) error {
	if _, err := s.db.Exec(ctx, `DELETE FROM steward_leases WHERE holder_id = $1`, holder); err != nil {
		return fmt.Errorf("steward: release lease: %w", err)
	}
	return nil
}

// AgreementText is the report the steward starts a run from, as prose.
func (s *Supervision) AgreementText(ctx context.Context, since, until time.Time) (string, error) {
	return s.reporter.Text(ctx, since, until)
}
