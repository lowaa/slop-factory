package reports

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// DefaultTimeout bounds each report query: a slow report fails clearly
// rather than holding a pooled connection.
const DefaultTimeout = 5 * time.Second

// postgres's query_canceled, which a statement timeout raises.
const queryCanceled = "57014"

type PostgresStore struct {
	db      *db.DB
	timeout time.Duration
}

// NewPostgresStore reads through database. A timeout of zero is
// DefaultTimeout.
func NewPostgresStore(database *db.DB, timeout time.Duration) *PostgresStore {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	return &PostgresStore{db: database, timeout: timeout}
}

var _ Store = (*PostgresStore)(nil)

// The queries are exported to the package's tests, which EXPLAIN each one.
// $1 and $2 are the period's start and end; $3 is the priority, empty for
// all. Each is bounded by that period: see the package doc.
var (
	// A stay is bounded by the change that ends it, found by an index range
	// scan over the period, and the one that began it, an index lookup per
	// row. A stay still going is the latest change of a ticket not yet
	// finished, counted up to $2.
	queryStates = `
WITH stays AS (
    SELECT p.to_status AS state,
           extract(epoch FROM GREATEST(r.changed_at - p.changed_at, interval '0')) AS secs,
           false AS ongoing
      FROM ticket_status_changes r
      CROSS JOIN LATERAL (
          SELECT p.to_status, p.priority, p.changed_at
            FROM ticket_status_changes p
           WHERE p.tenant_id = r.tenant_id AND p.ticket_id = r.ticket_id AND p.id < r.id
           ORDER BY p.id DESC LIMIT 1) p
     WHERE r.changed_at >= $1 AND r.changed_at < $2
       AND p.to_status NOT IN ` + ticket.TerminalStatusesSQL + `
       AND ($3 = '' OR p.priority = $3)
    UNION ALL
    SELECT l.to_status,
           extract(epoch FROM GREATEST($2::timestamptz - l.changed_at, interval '0')),
           true
      FROM tickets t
      CROSS JOIN LATERAL (
          SELECT l.to_status, l.priority, l.changed_at
            FROM ticket_status_changes l
           WHERE l.tenant_id = t.tenant_id AND l.ticket_id = t.id
           ORDER BY l.id DESC LIMIT 1) l
     WHERE t.status NOT IN ` + ticket.TerminalStatusesSQL + `
       AND ($3 = '' OR l.priority = $3)
)
SELECT state, count(*), count(*) FILTER (WHERE ongoing),
       percentile_cont(ARRAY[0.5, 0.9]) WITHIN GROUP (ORDER BY secs), sum(secs)
  FROM stays GROUP BY state ORDER BY state`

	// The ticket's creation is a subquery rather than a join so that it is a
	// primary key lookup per done change: a join let the planner hash the
	// tenant's whole tickets table instead.
	//
	// Lead time and first-claimed-to-done, overall (the empty grouping set)
	// and per week, over the changes into done in the period. $4 is whether
	// parked counts; when it does not, the ticket's parked stays before the
	// done are taken off (a stay ends at the ticket's next change, found by
	// an index lookup, like the claim). Only stays after the claim come off
	// first-claimed-to-done.
	queryLead = `
WITH done AS (
    SELECT (date_trunc('week', d.changed_at AT TIME ZONE 'UTC') AT TIME ZONE 'UTC') AS week,
           extract(epoch FROM GREATEST(d.changed_at - (
               SELECT t.created_at FROM tickets t
                WHERE t.tenant_id = d.tenant_id AND t.id = d.ticket_id), interval '0')) AS lead,
           extract(epoch FROM GREATEST(d.changed_at - c.at, interval '0')) AS claimed,
           COALESCE(pk.total, 0) AS parked, COALESCE(pk.since_claim, 0) AS parked_since_claim
      FROM ticket_status_changes d
      LEFT JOIN LATERAL (
          SELECT min(c.changed_at) AS at FROM ticket_status_changes c
           WHERE c.tenant_id = d.tenant_id AND c.ticket_id = d.ticket_id
             AND c.to_status = ` + ticket.SQLStatusInProgress + ` AND c.id < d.id) c ON true
      LEFT JOIN LATERAL (
          SELECT sum(extract(epoch FROM n.changed_at - p.changed_at)) AS total,
                 sum(extract(epoch FROM n.changed_at - p.changed_at)) FILTER (WHERE p.changed_at >= c.at) AS since_claim
            FROM ticket_status_changes p
            CROSS JOIN LATERAL (
                SELECT n.changed_at FROM ticket_status_changes n
                 WHERE n.tenant_id = p.tenant_id AND n.ticket_id = p.ticket_id AND n.id > p.id
                 ORDER BY n.id LIMIT 1) n
           WHERE NOT $4 AND p.tenant_id = d.tenant_id AND p.ticket_id = d.ticket_id
             AND p.to_status = 'parked' AND p.id < d.id) pk ON true
     WHERE d.to_status = ` + ticket.SQLStatusDone + ` AND d.changed_at >= $1 AND d.changed_at < $2
       AND ($3 = '' OR d.priority = $3)
), adjusted AS (
    SELECT week, GREATEST(lead - parked, 0) AS lead,
           GREATEST(claimed - parked_since_claim, 0) AS claimed
      FROM done
)
SELECT week, count(*),
       percentile_cont(ARRAY[0.5, 0.9]) WITHIN GROUP (ORDER BY lead),
       count(claimed),
       percentile_cont(ARRAY[0.5, 0.9]) WITHIN GROUP (ORDER BY claimed)
  FROM adjusted GROUP BY GROUPING SETS ((), (week)) ORDER BY week NULLS FIRST`

	queryThroughput = `
SELECT (date_trunc('day', changed_at AT TIME ZONE 'UTC') AT TIME ZONE 'UTC') AS day, count(*)
  FROM ticket_status_changes
 WHERE to_status = ` + ticket.SQLStatusDone + ` AND changed_at >= $1 AND changed_at < $2
   AND ($3 = '' OR priority = $3)
 GROUP BY 1 ORDER BY 1`

	queryHistoryFrom = `SELECT min(changed_at) FROM ticket_status_changes`

	queryNow = `
SELECT t.status, count(*),
       count(*) FILTER (WHERE NOT EXISTS (
           SELECT 1 FROM ticket_status_changes c WHERE c.tenant_id = t.tenant_id AND c.ticket_id = t.id))
  FROM tickets t
 WHERE t.status NOT IN ` + ticket.TerminalStatusesSQL + ` AND ($1 = '' OR t.priority = $1)
 GROUP BY t.status ORDER BY t.status`

	queryWaiting = `
SELECT t.id, t.title, t.priority, l.changed_at
  FROM tickets t
  CROSS JOIN LATERAL (
      SELECT l.changed_at FROM ticket_status_changes l
       WHERE l.tenant_id = t.tenant_id AND l.ticket_id = t.id
       ORDER BY l.id DESC LIMIT 1) l
 WHERE t.status = $1 AND ($2 = '' OR t.priority = $2)
 ORDER BY l.changed_at, t.id LIMIT $3 OFFSET $4`

	queryWaitingTotal = `
SELECT count(*) FROM tickets t
 WHERE t.status = $1 AND ($2 = '' OR t.priority = $2)
   AND EXISTS (SELECT 1 FROM ticket_status_changes c WHERE c.tenant_id = t.tenant_id AND c.ticket_id = t.id)`
)

// readTx runs fn in a read-only transaction. The timeout bounds each
// statement (Postgres applies it per statement) and, as a deadline, the whole
// report, so five slow queries cannot add up to five timeouts.
func (s *PostgresStore) readTx(ctx context.Context, fn func(q db.Querier) error) error {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	err := s.db.InReadTx(ctx, func(q db.Querier) error {
		if _, err := q.Exec(ctx, `SELECT set_config('statement_timeout', $1, true)`,
			strconv.FormatInt(s.timeout.Milliseconds(), 10)); err != nil {
			return fmt.Errorf("reports: set statement timeout: %w", err)
		}
		return fn(q)
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == queryCanceled || errors.Is(err, context.DeadlineExceeded) {
		return ErrTooSlow
	}
	return err
}

func (s *PostgresStore) Summary(ctx context.Context, f *Filter) (*Summary, error) {
	out := &Summary{From: f.From, To: f.To}
	err := s.readTx(ctx, func(q db.Querier) error {
		if err := q.QueryRow(ctx, queryHistoryFrom).Scan(&out.HistoryFrom); err != nil {
			return fmt.Errorf("reports: history from: %w", err)
		}
		var err error
		if out.States, err = queryStateFigures(ctx, q, f); err != nil {
			return err
		}
		if out.Lead, out.LeadByWeek, err = queryLeadFigures(ctx, q, f); err != nil {
			return err
		}
		if out.Throughput, err = queryDays(ctx, q, f); err != nil {
			return err
		}
		out.IncludeParked = f.IncludeParked
		out.Now, out.ParkedNotShown, err = queryWhereNow(ctx, q, f)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func percentiles(v []float64) *Percentiles {
	if len(v) != 2 {
		return nil
	}
	return &Percentiles{P50: v[0], P90: v[1]}
}

func queryStateFigures(ctx context.Context, q db.Querier, f *Filter) ([]StateFigure, error) {
	rows, err := q.Query(ctx, queryStates, f.From, f.To, f.Priority)
	if err != nil {
		return nil, fmt.Errorf("reports: states: %w", err)
	}
	defer rows.Close()
	var out []StateFigure
	for rows.Next() {
		var (
			s StateFigure
			p []float64
		)
		if err := rows.Scan(&s.State, &s.Stays, &s.Ongoing, &p, &s.TotalSeconds); err != nil {
			return nil, fmt.Errorf("reports: states: %w", err)
		}
		if s.State == "parked" && !f.IncludeParked {
			continue
		}
		s.Duration = percentiles(p)
		out = append(out, s)
	}
	return out, rows.Err()
}

func queryLeadFigures(ctx context.Context, q db.Querier, f *Filter) (LeadFigure, []LeadFigure, error) {
	rows, err := q.Query(ctx, queryLead, f.From, f.To, f.Priority, f.IncludeParked)
	if err != nil {
		return LeadFigure{}, nil, fmt.Errorf("reports: lead time: %w", err)
	}
	defer rows.Close()
	var (
		overall LeadFigure
		weeks   []LeadFigure
	)
	for rows.Next() {
		var (
			week      *time.Time
			lead, clm []float64
			fig       LeadFigure
		)
		if err := rows.Scan(&week, &fig.Done, &lead, &fig.ClaimedDone, &clm); err != nil {
			return LeadFigure{}, nil, fmt.Errorf("reports: lead time: %w", err)
		}
		fig.CreatedToDone, fig.ClaimedToDone = percentiles(lead), percentiles(clm)
		if week == nil {
			overall = fig
			continue
		}
		fig.WeekStart = *week
		weeks = append(weeks, fig)
	}
	return overall, weeks, rows.Err()
}

func queryDays(ctx context.Context, q db.Querier, f *Filter) ([]DayCount, error) {
	rows, err := q.Query(ctx, queryThroughput, f.From, f.To, f.Priority)
	if err != nil {
		return nil, fmt.Errorf("reports: throughput: %w", err)
	}
	defer rows.Close()
	var out []DayCount
	for rows.Next() {
		var d DayCount
		if err := rows.Scan(&d.Day, &d.Done); err != nil {
			return nil, fmt.Errorf("reports: throughput: %w", err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func queryWhereNow(ctx context.Context, q db.Querier, f *Filter) ([]WhereNow, int64, error) {
	rows, err := q.Query(ctx, queryNow, f.Priority)
	if err != nil {
		return nil, 0, fmt.Errorf("reports: tickets now: %w", err)
	}
	defer rows.Close()
	var (
		out    []WhereNow
		parked int64
	)
	for rows.Next() {
		var w WhereNow
		if err := rows.Scan(&w.Status, &w.Count, &w.WithoutHistory); err != nil {
			return nil, 0, fmt.Errorf("reports: tickets now: %w", err)
		}
		if w.Status == "parked" && !f.IncludeParked {
			parked = w.Count
			continue
		}
		out = append(out, w)
	}
	return out, parked, rows.Err()
}

func (s *PostgresStore) Waiting(ctx context.Context, status, priority string, limit, offset int) (*WaitingPage, error) {
	page := &WaitingPage{Items: []Waiting{}}
	err := s.readTx(ctx, func(q db.Querier) error {
		rows, err := q.Query(ctx, queryWaiting, status, priority, limit, offset)
		if err != nil {
			return fmt.Errorf("reports: waiting: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var w Waiting
			if err := rows.Scan(&w.TicketID, &w.Title, &w.Priority, &w.Since); err != nil {
				return fmt.Errorf("reports: waiting: %w", err)
			}
			page.Items = append(page.Items, w)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		rows.Close()
		if err := q.QueryRow(ctx, queryWaitingTotal, status, priority).Scan(&page.Total); err != nil {
			return fmt.Errorf("reports: waiting total: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return page, nil
}
