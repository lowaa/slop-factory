// Package leaseconfig resolves the configuration a granted lease records.
package leaseconfig

import (
	"context"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/fleetinstructions"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/harnessconfig"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// Harness is the config resolution the workspace's harness_config answers
// from. Satisfied by harnessconfig.Service.
type Harness interface {
	Effective(ctx context.Context, agentID uuid.UUID) (harnessconfig.Effective, error)
}

// Fleet is the tenant's fleet instructions default. Satisfied by
// fleetinstructions.Service.
type Fleet interface {
	Default(ctx context.Context) (*fleetinstructions.Version, error)
}

// Resolver serves what the lease's session is served: the settings from the
// same Effective as GET /api/workspace/harness_config, and the fleet
// instructions the briefings are built from (a variant's pin, or none, else
// the tenant's default).
type Resolver struct {
	harness Harness
	fleet   Fleet
}

func NewResolver(harness Harness, fleet Fleet) *Resolver {
	return &Resolver{harness: harness, fleet: fleet}
}

var _ ticket.ConfigResolver = (*Resolver)(nil)

func (r *Resolver) Resolve(ctx context.Context, agentID uuid.UUID) (*ticket.ServedConfig, error) {
	eff, err := r.harness.Effective(ctx, agentID)
	if err != nil {
		return nil, err
	}
	settings := eff.Values
	if settings == nil {
		settings = harnessconfig.Values{}
	}
	served := &ticket.ServedConfig{Revision: eff.Revision, Settings: settings}
	if m := eff.Variant; m != nil {
		served.Variant = &ticket.ServedVariant{VersionID: m.VersionID, Name: m.Name, Version: m.Version, Harness: m.Harness}
		served.FleetInstructionsVersionID = m.FleetInstructionsVersionID
		return served, nil
	}
	if r.fleet != nil {
		v, err := r.fleet.Default(ctx)
		if err != nil {
			return nil, err
		}
		if v != nil {
			served.FleetInstructionsVersionID = &v.ID
		}
	}
	return served, nil
}
