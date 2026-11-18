package dsr

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/lowaa/slop-factory/go-db/engine"
)

// PostgresStore is the Store over the tables in package schema.
type PostgresStore struct {
	eng engine.Engine
}

// NewPostgresStore reads and writes through eng, which must connect as the
// app's role. The store does not close eng.
func NewPostgresStore(eng engine.Engine) *PostgresStore { return &PostgresStore{eng: eng} }

const requestColumns = `id, requester_name, requester_email, request_type, source, authorised_agent, details,
	verified, COALESCE(verification_method, ''), verified_at, status, received_at, due_at, extended, extension_reason,
	closed_at, alerted_soon_at, alerted_overdue_at`

func scanRequest(row pgx.Row) (*Request, error) {
	r := &Request{}
	var typ, source, method, status string
	err := row.Scan(&r.ID, &r.RequesterName, &r.RequesterEmail, &typ, &source, &r.AuthorisedAgent, &r.Details,
		&r.Verified, &method, &r.VerifiedAt, &status, &r.ReceivedAt, &r.DueAt, &r.Extended, &r.ExtensionReason,
		&r.ClosedAt, &r.AlertedSoonAt, &r.AlertedOverdueAt)
	r.Type, r.Source, r.VerificationMethod, r.Status = Type(typ), Source(source), VerificationMethod(method), Status(status)
	return r, err
}

const eventColumns = `id, request_id, at, actor, kind, COALESCE(from_status, ''), COALESCE(to_status, ''), detail`

func scanEvent(row pgx.Row) (*Event, error) {
	e := &Event{}
	var kind, from, to string
	err := row.Scan(&e.ID, &e.RequestID, &e.At, &e.Actor, &kind, &from, &to, &e.Detail)
	e.Kind, e.FromStatus, e.ToStatus = EventKind(kind), Status(from), Status(to)
	return e, err
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func insertEvent(ctx context.Context, q engine.Querier, e *Event) error {
	at := e.At
	if at.IsZero() {
		at = time.Now()
	}
	_, err := q.ExecContext(ctx, `INSERT INTO global_privacy_request_events
		(request_id, at, actor, kind, from_status, to_status, detail) VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		e.RequestID, at, e.Actor, string(e.Kind), nullable(string(e.FromStatus)), nullable(string(e.ToStatus)), e.Detail)
	return err
}

// begin starts a transaction and returns the rollback to defer: it does
// nothing once the transaction has committed.
func (s *PostgresStore) begin(ctx context.Context) (engine.Tx, func(), error) {
	tx, err := s.eng.BeginTx(ctx)
	if err != nil {
		return nil, nil, storeErr("begin", err)
	}
	return tx, func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }, nil
}

func (s *PostgresStore) Insert(ctx context.Context, r *Request, first *Event) error {
	tx, cleanup, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer cleanup()
	_, err = tx.ExecContext(ctx, `INSERT INTO global_privacy_requests
		(id, requester_name, requester_email, request_type, source, authorised_agent, details, status, received_at, due_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		r.ID, r.RequesterName, r.RequesterEmail, string(r.Type), string(r.Source), r.AuthorisedAgent, r.Details,
		string(r.Status), r.ReceivedAt, r.DueAt)
	if err != nil {
		return storeErr("insert request", err)
	}
	if err := insertEvent(ctx, tx, first); err != nil {
		return storeErr("insert event", err)
	}
	return storeErr("commit", tx.Commit(context.WithoutCancel(ctx)))
}

func (s *PostgresStore) Get(ctx context.Context, id uuid.UUID) (*Request, error) {
	r, err := scanRequest(s.eng.QueryRowContext(ctx, `SELECT `+requestColumns+` FROM global_privacy_requests WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, storeErr("get request", err)
	}
	return r, nil
}

func (s *PostgresStore) Events(ctx context.Context, id uuid.UUID) ([]*Event, error) {
	rows, err := s.eng.QueryContext(ctx, `SELECT `+eventColumns+` FROM global_privacy_request_events WHERE request_id = $1 ORDER BY id`, id)
	if err != nil {
		return nil, storeErr("read events", err)
	}
	defer rows.Close()
	var out []*Event
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return nil, storeErr("read events", err)
		}
		out = append(out, e)
	}
	return out, storeErr("read events", rows.Err())
}

func (s *PostgresStore) List(ctx context.Context, f *Filter) ([]*Request, error) {
	limit := DefaultListLimit
	where := ""
	if f != nil {
		if f.Limit > 0 {
			limit = f.Limit
		}
		if f.OpenOnly {
			where = "WHERE closed_at IS NULL"
		}
	}
	rows, err := s.eng.QueryContext(ctx, fmt.Sprintf(`SELECT %s FROM global_privacy_requests %s
		ORDER BY (closed_at IS NOT NULL), CASE WHEN closed_at IS NULL THEN due_at END, received_at DESC, id
		LIMIT $1`, requestColumns, where), limit)
	if err != nil {
		return nil, storeErr("list requests", err)
	}
	defer rows.Close()
	var out []*Request
	for rows.Next() {
		r, err := scanRequest(rows)
		if err != nil {
			return nil, storeErr("list requests", err)
		}
		out = append(out, r)
	}
	return out, storeErr("list requests", rows.Err())
}

func (s *PostgresStore) Update(ctx context.Context, id uuid.UUID, change func(*Request) (*Event, error)) (*Request, error) {
	tx, cleanup, err := s.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	r, err := scanRequest(tx.QueryRowContext(ctx, `SELECT `+requestColumns+` FROM global_privacy_requests WHERE id = $1 FOR UPDATE`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, storeErr("lock request", err)
	}
	ev, err := change(r)
	if err != nil {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE global_privacy_requests SET
		verified = $2, verification_method = $3, verified_at = $4, status = $5, due_at = $6, extended = $7,
		extension_reason = $8, closed_at = $9, alerted_soon_at = $10, alerted_overdue_at = $11
		WHERE id = $1`,
		id, r.Verified, nullable(string(r.VerificationMethod)), r.VerifiedAt, string(r.Status), r.DueAt, r.Extended,
		r.ExtensionReason, r.ClosedAt, r.AlertedSoonAt, r.AlertedOverdueAt)
	if err != nil {
		return nil, storeErr("update request", err)
	}
	if ev != nil {
		ev.RequestID = id
		if err := insertEvent(ctx, tx, ev); err != nil {
			return nil, storeErr("insert event", err)
		}
	}
	if err := tx.Commit(context.WithoutCancel(ctx)); err != nil {
		return nil, storeErr("commit", err)
	}
	return r, nil
}

// The two thresholds are claimed in one transaction. A claimed row stays
// locked until it commits, so an extension that moves a request's due date
// waits for it and cannot make one request yield two alerts. SKIP LOCKED
// leaves a request another claim or a status change holds to the next check,
// which is a day away at the slowest, rather than waiting on it.
func (s *PostgresStore) ClaimAlerts(ctx context.Context, now time.Time, soon, lease time.Duration) ([]*Alert, error) {
	tx, rollback, err := s.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer rollback()
	var out []*Alert
	// $1 is now, $2 the lease, and $3 (due soon only) the window. A mark is
	// stale when it is older than the lease and no event of its kind was
	// recorded at or after it.
	claim := func(kind AlertKind, column string, event EventKind, cond string, args ...any) error {
		rows, err := tx.QueryContext(ctx, fmt.Sprintf(`
			UPDATE global_privacy_requests SET %[1]s = $1 WHERE id IN (
				SELECT r.id FROM global_privacy_requests r
				WHERE r.closed_at IS NULL AND %[2]s
				  AND (r.%[1]s IS NULL OR (r.%[1]s < $1 - $2::interval AND NOT EXISTS (
				        SELECT 1 FROM global_privacy_request_events e
				        WHERE e.request_id = r.id AND e.kind = '%[3]s' AND e.at >= r.%[1]s)))
				FOR UPDATE OF r SKIP LOCKED)
			RETURNING `+requestColumns, column, cond, event), append([]any{now, lease}, args...)...)
		if err != nil {
			return storeErr("claim alerts", err)
		}
		defer rows.Close()
		for rows.Next() {
			r, err := scanRequest(rows)
			if err != nil {
				return storeErr("claim alerts", err)
			}
			out = append(out, &Alert{Kind: kind, Request: r, ClaimedAt: now})
		}
		return storeErr("claim alerts", rows.Err())
	}
	// Overdue is claimed first, and due-soon only takes requests not yet
	// due, so a request logged already late yields one alert, not two.
	if err := claim(AlertOverdue, "alerted_overdue_at", EventAlertLate, "r.due_at < $1"); err != nil {
		return nil, err
	}
	if err := claim(AlertDueSoon, "alerted_soon_at", EventAlertSoon, "r.due_at >= $1 AND r.due_at <= $1 + $3::interval", soon); err != nil {
		return nil, err
	}
	if err := tx.Commit(context.WithoutCancel(ctx)); err != nil {
		return nil, storeErr("commit", err)
	}
	return out, nil
}

// ReleaseAlerts clears a mark only if it is still the one the claim left: an
// extension that cleared it, and a later check that claimed the new date, own
// the mark now.
func (s *PostgresStore) ReleaseAlerts(ctx context.Context, alerts []*Alert) error {
	for _, a := range alerts {
		column := "alerted_soon_at"
		if a.Kind == AlertOverdue {
			column = "alerted_overdue_at"
		}
		if _, err := s.eng.ExecContext(ctx, fmt.Sprintf(`UPDATE global_privacy_requests SET %[1]s = NULL WHERE id = $1 AND %[1]s = $2`, column),
			a.Request.ID, a.ClaimedAt); err != nil {
			return storeErr("release alert", err)
		}
	}
	return nil
}

func (s *PostgresStore) AppendEvents(ctx context.Context, events []*Event) error {
	tx, cleanup, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer cleanup()
	for _, e := range events {
		if err := insertEvent(ctx, tx, e); err != nil {
			return storeErr("insert event", err)
		}
	}
	return storeErr("commit", tx.Commit(context.WithoutCancel(ctx)))
}

var _ Store = (*PostgresStore)(nil)
