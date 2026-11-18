package ticket

import (
	"context"
	"fmt"
	"time"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
)

// AttemptRun is what one lease of an attempt ran with, beside the variant
// (which the attempt carries). An attempt resumed under a later lease has one
// per lease, because the server release or the workspace bundle may differ.
// A field is nil when it was not recorded: a lease from before they were, or
// a claim by a supervisor that did not send it.
type AttemptRun struct {
	AcquiredAt               time.Time
	Harness                  *string
	HarnessVersion           *string
	Model                    *string
	BundleHash               *string
	FleetInstructionsName    *string
	FleetInstructionsVersion *int
}

// AttemptRunBody is AttemptRun on the wire.
type AttemptRunBody struct {
	AcquiredAt        time.Time                 `json:"acquired_at"`
	Harness           *string                   `json:"harness"`
	HarnessVersion    *string                   `json:"harness_version"`
	Model             *string                   `json:"model"`
	BundleHash        *string                   `json:"bundle_hash"`
	FleetInstructions *FleetInstructionsRefBody `json:"fleet_instructions" doc:"the fleet instructions version served in the briefings; null when none was"`
}

// FleetInstructionsRefBody names a fleet instructions version.
type FleetInstructionsRefBody struct {
	Name    string `json:"name"`
	Version int    `json:"version"`
}

func attemptRunBodies(runs []AttemptRun) []AttemptRunBody {
	out := make([]AttemptRunBody, len(runs))
	for i := range runs {
		r := &runs[i]
		out[i] = AttemptRunBody{AcquiredAt: r.AcquiredAt, Harness: r.Harness, HarnessVersion: r.HarnessVersion,
			Model: r.Model, BundleHash: r.BundleHash}
		if r.FleetInstructionsName != nil && r.FleetInstructionsVersion != nil {
			out[i].FleetInstructions = &FleetInstructionsRefBody{Name: *r.FleetInstructionsName, Version: *r.FleetInstructionsVersion}
		}
	}
	return out
}

// attemptRuns reads the recorded runs of a ticket's leases by attempt number,
// oldest lease first. A lease with no lease_configs row (granted with no
// resolver, or before they were recorded) has no run.
func attemptRuns(ctx context.Context, q db.Querier, ticketID int64) (map[int][]AttemptRun, error) {
	rows, err := q.Query(ctx, `
		SELECT l.attempt_number, l.acquired_at, c.harness, c.harness_version, c.model, c.bundle_hash,
		       fi.name, fi.version
		  FROM leases l
		  JOIN lease_configs c ON c.tenant_id = l.tenant_id AND c.lease_id = l.id
		  LEFT JOIN fleet_instruction_versions fi
		         ON fi.tenant_id = c.tenant_id AND fi.id = c.fleet_instructions_version_id
		 WHERE l.ticket_id = $1
		 ORDER BY l.acquired_at, l.id`, ticketID)
	if err != nil {
		return nil, fmt.Errorf("list runs of ticket #%d: %w", ticketID, err)
	}
	defer rows.Close()
	byAttempt := map[int][]AttemptRun{}
	for rows.Next() {
		var number int
		var r AttemptRun
		if err := rows.Scan(&number, &r.AcquiredAt, &r.Harness, &r.HarnessVersion, &r.Model, &r.BundleHash,
			&r.FleetInstructionsName, &r.FleetInstructionsVersion); err != nil {
			return nil, fmt.Errorf("scan run of ticket #%d: %w", ticketID, err)
		}
		byAttempt[number] = append(byAttempt[number], r)
	}
	return byAttempt, rows.Err()
}
