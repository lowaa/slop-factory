package variant

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
)

// Owners answers who may use variants. Checked against the database on
// every call, not read from the session, which is up to one token lifetime
// stale.
type Owners interface {
	IsTenantOwner(ctx context.Context, userID, tenantID uuid.UUID) (bool, error)
}

// Service is the rules: who may use variants, and what a version may hold.
// Every method is owners only, reads included: what the fleet could run on
// is the owners' to see.
type Service struct {
	store  Store
	gate   *authn.OwnerGate
	events *audit.Recorder
	logger *slog.Logger
}

func NewService(store Store, owners Owners, events *audit.Recorder, logHandler slog.Handler) *Service {
	gate := authn.NewOwnerGate(owners, authn.WithRefusal(ErrNotOwner), authn.WithAudit(func(ctx context.Context, userID, tenantID uuid.UUID) {
		events.Record(ctx, audit.VariantRefused, "user_id", userID, "tenant", tenantID)
	}))
	return &Service{store: store, gate: gate, events: events, logger: slog.New(logHandler)}
}

// List is the tenant's versions, by name and then newest first; archived
// ones only with includeArchived.
func (s *Service) List(ctx context.Context, who *authn.User, includeArchived bool) ([]Version, error) {
	if err := s.gate.RequireOwner(ctx, who); err != nil {
		return nil, err
	}
	return s.store.List(ctx, includeArchived)
}

// Options is the versions a ticket can be routed to: every unarchived one.
// Any signed-in member may read them, since any member may route a ticket;
// owners still decide which exist. Unlike List it carries no settings.
func (s *Service) Options(ctx context.Context, who *authn.User) ([]Version, error) {
	if who == nil {
		return nil, authn.ErrNoUser
	}
	return s.store.List(ctx, false)
}

// SaveRequest is a version to save as the next of Name.
type SaveRequest struct {
	Name     string
	Harness  string
	Settings map[string]string
	// FleetInstructionsVersionID nil is no fleet instructions.
	FleetInstructionsVersionID *uuid.UUID
}

func (s *Service) Save(ctx context.Context, who *authn.User, req *SaveRequest) (*Version, error) {
	if err := s.gate.RequireOwner(ctx, who); err != nil {
		return nil, err
	}
	name, err := NormaliseName(req.Name)
	if err != nil {
		return nil, err
	}
	settings, err := ValidateSettings(req.Harness, req.Settings)
	if err != nil {
		return nil, err
	}
	created, err := s.store.Create(ctx, &Version{
		Name: name, Harness: req.Harness, Settings: settings,
		FleetInstructionsVersionID: req.FleetInstructionsVersionID, CreatedBy: &who.ID,
	})
	if err != nil {
		return nil, err
	}
	s.events.Record(ctx, audit.VariantVersionSaved,
		"user_id", who.ID, "tenant", who.TenantID, "name", created.Name, "version", created.Version, "harness", created.Harness)
	return created, nil
}

func (s *Service) ArchiveVersion(ctx context.Context, who *authn.User, id uuid.UUID) (*Version, error) {
	return s.setVersionArchived(ctx, who, id, true)
}

func (s *Service) UnarchiveVersion(ctx context.Context, who *authn.User, id uuid.UUID) (*Version, error) {
	return s.setVersionArchived(ctx, who, id, false)
}

func (s *Service) setVersionArchived(ctx context.Context, who *authn.User, id uuid.UUID, archive bool) (*Version, error) {
	if err := s.gate.RequireOwner(ctx, who); err != nil {
		return nil, err
	}
	v, err := s.store.SetVersionArchived(ctx, id, archive, who.ID)
	if err != nil {
		return nil, err
	}
	s.events.Record(ctx, archiveAction(archive),
		"user_id", who.ID, "tenant", who.TenantID, "name", v.Name, "version", v.Version)
	return v, nil
}

// ArchiveGroup hides every version of name, and says how many it newly hid.
func (s *Service) ArchiveGroup(ctx context.Context, who *authn.User, name string) (int, error) {
	return s.setGroupArchived(ctx, who, name, true)
}

func (s *Service) UnarchiveGroup(ctx context.Context, who *authn.User, name string) (int, error) {
	return s.setGroupArchived(ctx, who, name, false)
}

func (s *Service) setGroupArchived(ctx context.Context, who *authn.User, name string, archive bool) (int, error) {
	if err := s.gate.RequireOwner(ctx, who); err != nil {
		return 0, err
	}
	name, err := NormaliseName(name)
	if err != nil {
		return 0, err
	}
	n, err := s.store.SetGroupArchived(ctx, name, archive, who.ID)
	if err != nil {
		return 0, err
	}
	s.events.Record(ctx, archiveAction(archive),
		"user_id", who.ID, "tenant", who.TenantID, "name", name, "version", 0, "versions", n)
	return n, nil
}

func archiveAction(archive bool) audit.Action {
	if archive {
		return audit.VariantArchived
	}
	return audit.VariantUnarchived
}

// SetAgentVariant puts an agent on a version, or takes it off with nil.
// Owners only, and only ever a signed-in person's: nothing an agent's token
// reaches calls it, so an agent cannot choose what it runs on.
func (s *Service) SetAgentVariant(ctx context.Context, who *authn.User, agentID uuid.UUID, versionID *uuid.UUID) error {
	if err := s.gate.RequireOwner(ctx, who); err != nil {
		return err
	}
	previous, now, err := s.store.SetAgentVariant(ctx, agentID, versionID)
	if err != nil {
		return err
	}
	if (previous == nil) == (now == nil) && (previous == nil || previous.ID == now.ID) {
		return nil
	}
	s.events.Record(ctx, audit.AgentVariantChanged,
		"user_id", who.ID, "tenant", who.TenantID, "agent", agentID.String(),
		"old", versionLabel(previous), "new", versionLabel(now))
	return nil
}

func versionLabel(v *Version) string {
	if v == nil {
		return ""
	}
	return fmt.Sprintf("%s v%d (%s)", v.Name, v.Version, v.ID)
}
