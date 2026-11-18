package ticket

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/scope"
)

// The claim rule of the landing queue: one acting engine (serial or batch) per
// app at a time. A queue entry may leave 'queued' only if no other entry in its
// app is active. An entry whose paths are not wholly inside one app (shared
// paths, several apps) counts in every app: it waits for all of them, and
// blocks all of them while it is active. It is a query over the rows, with no
// lane table to drift from them, and it is read under the claim lock, which
// every claimer holds.

// activeLandingPhases are the queue_state values an engine is acting in. They
// mirror landing.Phase.Active; this package cannot import landing.
const activeLandingPhases = `'serial_merging_main', 'serial_waiting_checks', 'serial_merging', 'batch_testing', 'batch_merging'`

// laneApps are the workspace-path prefixes of the apps the manifest declares
// independent and enforced, as the batch policy reads them: with no enforcing
// repo, or more than one, there are none and every entry counts in every app.
func (s *PostgresStore) laneApps(ctx context.Context) ([]string, error) {
	if s.workspace == nil {
		return nil, nil
	}
	pl, err := s.workspace.Place(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("load the apps: %w", err)
	}
	var enforcing []scope.WorkspaceRepo
	for _, r := range pl.All {
		if r.BatchWithinApps && len(r.IndependentApps) > 0 {
			enforcing = append(enforcing, r)
		}
	}
	if len(enforcing) != 1 {
		return nil, nil
	}
	var apps []string
	for _, app := range enforcing[0].IndependentApps {
		if enforcing[0].Path == "" {
			apps = append(apps, app)
			continue
		}
		apps = append(apps, enforcing[0].Path+"/"+app)
	}
	return apps, nil
}

// appOf is the one app all of paths are inside; false when they are in none or
// several, or there are no paths.
func appOf(apps, paths []string) (string, bool) {
	app := ""
	for _, path := range paths {
		found := ""
		for _, a := range apps {
			if strings.HasPrefix(path, a) && len(a) > len(found) {
				found = a
			}
		}
		if found == "" || (app != "" && app != found) {
			return "", false
		}
		app = found
	}
	return app, app != ""
}

// requireLaneFree is the claim rule for the entry of ticketID. It is nil for an
// entry that is not queued: the rule is checked only in the move out of
// 'queued', and a row already acting, or one a batch holds, is not moved out
// of it again. A claimed entry that has not yet moved is not counted: the
// server agent holds one lease at a time (requireNoActiveLease), which is what
// keeps a second claim out until the first has moved.
func requireLaneFree(ctx context.Context, q db.Querier, apps []string, ticketID int64) error {
	var state string
	err := q.QueryRow(ctx, `SELECT queue_state FROM landings WHERE ticket_id = $1`, ticketID).Scan(&state)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return fmt.Errorf("read the entry's state: %w", err)
	}
	if state != "queued" {
		return nil
	}
	mine, err := pathsForTicket(ctx, q, ticketID)
	if err != nil {
		return err
	}
	myApp, myOK := appOf(apps, mine)

	rows, err := q.Query(ctx, `
SELECT w.ticket_id, coalesce(array_agg(p.path) FILTER (WHERE p.path IS NOT NULL), '{}')
  FROM landings w LEFT JOIN ticket_paths p ON p.ticket_id = w.ticket_id
 WHERE w.ticket_id <> $1 AND w.queue_state IN (`+activeLandingPhases+`)
 GROUP BY w.ticket_id`, ticketID)
	if err != nil {
		return fmt.Errorf("read the active entries: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var other int64
		var paths []string
		if err := rows.Scan(&other, &paths); err != nil {
			return fmt.Errorf("scan an active entry: %w", err)
		}
		if otherApp, otherOK := appOf(apps, paths); !myOK || !otherOK || otherApp == myApp {
			return fmt.Errorf("%w: #%d is active", ErrServerClaimLaneHeld, other)
		}
	}
	return rows.Err()
}
