package fleetinstructions

import (
	"context"
	"log/slog"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
)

// Owners answers who may change the instructions. Checked against the
// database on every write, not read from the session, which is up to one
// token lifetime stale.
type Owners interface {
	IsTenantOwner(ctx context.Context, userID, tenantID uuid.UUID) (bool, error)
}

// Service is the rules: who may change the instructions, what a text may
// be, and which version the fleet is given.
type Service struct {
	store  Store
	gate   *authn.OwnerGate
	events *audit.Recorder
	logger *slog.Logger
}

func NewService(store Store, owners Owners, events *audit.Recorder, logHandler slog.Handler) *Service {
	gate := authn.NewOwnerGate(owners, authn.WithRefusal(ErrNotOwner), authn.WithAudit(func(ctx context.Context, userID, tenantID uuid.UUID) {
		events.Record(ctx, audit.FleetInstructionsRefused, "user_id", userID, "tenant", tenantID)
	}))
	return &Service{store: store, gate: gate, events: events, logger: slog.New(logHandler)}
}

// Load is the tenant's versions of a kind, archived ones only with includeArchived.
func (s *Service) Load(ctx context.Context, kind Kind, includeArchived bool) (*Tenant, error) {
	return s.store.Load(ctx, kind, includeArchived)
}

// Default is the version agents are given, nil while the tenant has chosen
// none, when every briefing is as it was before there were fleet
// instructions.
func (s *Service) Default(ctx context.Context) (*Version, error) { return s.store.Default(ctx) }

// DefaultOf is the pinned version of a kind, nil while none is pinned. It is
// the pinned version, never the newest: what a kind's readers act on changes
// only when an owner pins another.
func (s *Service) DefaultOf(ctx context.Context, kind Kind) (*Version, error) {
	return s.store.DefaultOf(ctx, kind)
}

// Get is one version by id, archived or not.
func (s *Service) Get(ctx context.Context, id uuid.UUID) (*Version, error) {
	return s.store.Get(ctx, id)
}

// CanEdit reports whether who may change the instructions.
func (s *Service) CanEdit(ctx context.Context, who *authn.User) (bool, error) {
	return s.gate.IsOwner(ctx, who)
}

// Save stores body as the next version of name. With makeDefault the fleet
// is given it from its next workspace update; without, nothing about the
// fleet changes. The text itself is never logged: the audit records who,
// which version and how large, and the version row holds the text.
func (s *Service) Save(ctx context.Context, who *authn.User, kind Kind, name, body string, makeDefault bool) (*Version, error) {
	if err := s.gate.RequireOwner(ctx, who); err != nil {
		return nil, err
	}
	name, err := NormaliseName(name)
	if err != nil {
		return nil, err
	}
	body, err = NormaliseBodyFor(kind, body)
	if err != nil {
		return nil, err
	}
	created, previous, err := s.store.Create(ctx, kind, name, body, who.ID, makeDefault)
	if err != nil {
		return nil, err
	}
	s.events.Record(ctx, audit.FleetInstructionsVersionSaved,
		"user_id", who.ID, "tenant", who.TenantID, "kind", kind, "name", created.Name, "version", created.Version, "bytes", len(created.Body))
	if makeDefault {
		s.recordDefault(ctx, who, kind, previous, created)
	}
	return created, nil
}

// SetDefault chooses the version agents are given; nil chooses none.
func (s *Service) SetDefault(ctx context.Context, who *authn.User, kind Kind, id *uuid.UUID) error {
	if err := s.gate.RequireOwner(ctx, who); err != nil {
		return err
	}
	previous, err := s.store.SetDefault(ctx, kind, id, who.ID)
	if err != nil {
		return err
	}
	var now *Version
	if id != nil {
		t, err := s.store.Load(ctx, kind, true)
		if err != nil {
			// The change is made and cannot be undone here; say what we can.
			s.logger.Error("fleet instructions: reading back the new default for the audit record", "err", err)
		} else {
			for i := range t.Versions {
				if t.Versions[i].ID == *id {
					now = &t.Versions[i]
				}
			}
		}
	}
	s.recordDefault(ctx, who, kind, previous, now)
	return nil
}

func (s *Service) recordDefault(ctx context.Context, who *authn.User, kind Kind, previous, now *Version) {
	fields := []any{"user_id", who.ID, "tenant", who.TenantID, "kind", kind}
	for _, side := range []struct {
		prefix string
		v      *Version
	}{{"old", previous}, {"new", now}} {
		if side.v == nil {
			fields = append(fields, side.prefix+"_name", "", side.prefix+"_version", 0)
			continue
		}
		fields = append(fields, side.prefix+"_name", side.v.Name, side.prefix+"_version", side.v.Version)
	}
	s.events.Record(ctx, audit.FleetInstructionsDefaultChanged, fields...)
}

// ArchiveVersion hides one version from the board. The text is kept as it
// was: archiving is not deleting.
func (s *Service) ArchiveVersion(ctx context.Context, who *authn.User, id uuid.UUID) (*Version, error) {
	if err := s.gate.RequireOwner(ctx, who); err != nil {
		return nil, err
	}
	v, err := s.store.ArchiveVersion(ctx, id, who.ID)
	if err != nil {
		return nil, err
	}
	s.events.Record(ctx, audit.FleetInstructionsArchived,
		"user_id", who.ID, "tenant", who.TenantID, "kind", v.Kind, "name", v.Name, "version", v.Version)
	return v, nil
}

// ArchiveGroup hides every version of name, and says how many it newly hid.
func (s *Service) ArchiveGroup(ctx context.Context, who *authn.User, kind Kind, name string) (int, error) {
	if err := s.gate.RequireOwner(ctx, who); err != nil {
		return 0, err
	}
	name, err := NormaliseName(name)
	if err != nil {
		return 0, err
	}
	n, err := s.store.ArchiveGroup(ctx, kind, name, who.ID)
	if err != nil {
		return 0, err
	}
	s.events.Record(ctx, audit.FleetInstructionsArchived,
		"user_id", who.ID, "tenant", who.TenantID, "kind", kind, "name", name, "version", 0, "versions", n)
	return n, nil
}

func (s *Service) UnarchiveVersion(ctx context.Context, who *authn.User, id uuid.UUID) (*Version, error) {
	if err := s.gate.RequireOwner(ctx, who); err != nil {
		return nil, err
	}
	v, err := s.store.UnarchiveVersion(ctx, id)
	if err != nil {
		return nil, err
	}
	s.events.Record(ctx, audit.FleetInstructionsUnarchived,
		"user_id", who.ID, "tenant", who.TenantID, "kind", v.Kind, "name", v.Name, "version", v.Version)
	return v, nil
}

func (s *Service) UnarchiveGroup(ctx context.Context, who *authn.User, kind Kind, name string) (int, error) {
	if err := s.gate.RequireOwner(ctx, who); err != nil {
		return 0, err
	}
	name, err := NormaliseName(name)
	if err != nil {
		return 0, err
	}
	n, err := s.store.UnarchiveGroup(ctx, kind, name)
	if err != nil {
		return 0, err
	}
	s.events.Record(ctx, audit.FleetInstructionsUnarchived,
		"user_id", who.ID, "tenant", who.TenantID, "kind", kind, "name", name, "version", 0, "versions", n)
	return n, nil
}
