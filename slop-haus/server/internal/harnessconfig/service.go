package harnessconfig

import (
	"context"
	"log/slog"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
)

// Service owns the rules: which settings exist, what values each takes,
// and how an override lays over the defaults. Store owns the SQL.
type Service struct {
	store    Store
	variants Variants
	gate     OverrideGate
	events   *audit.Recorder
	logger   *slog.Logger
}

// Variants says which agents run on a variant. Satisfied by variant's store;
// an interface because variant imports this package for its validation.
type Variants interface {
	// InVariants is every agent of the tenant that runs on one.
	InVariants(ctx context.Context) (map[uuid.UUID]*VariantMembership, error)
	// ForAgent is nil for an agent in none.
	ForAgent(ctx context.Context, agentID uuid.UUID) (*VariantMembership, error)
}

// OverrideGate is what the config of a conductor needs apart from other
// agents': its override is its owner's alone to set or remove. Satisfied by
// *agent.ConductorService.
type OverrideGate interface {
	MayChangeConfig(ctx context.Context, who *authn.User, agentID uuid.UUID) (bool, error)
}

// WithOverrideGate decides who may change an agent's override. Without it
// no override can be changed: this is policy, so the default is closed.
func WithOverrideGate(g OverrideGate) Option { return func(s *Service) { s.gate = g } }

// authorizeOverride refuses anyone but the agent's rightful editor.
func (s *Service) authorizeOverride(ctx context.Context, by *authn.User, agentID uuid.UUID) error {
	if s.gate == nil {
		return ErrNotConfigOwner
	}
	allowed, err := s.gate.MayChangeConfig(ctx, by, agentID)
	if err != nil {
		return err
	}
	if !allowed {
		return ErrNotConfigOwner
	}
	return nil
}

func NewService(store Store, events *audit.Recorder, logHandler slog.Handler, opts ...Option) *Service {
	s := &Service{store: store, events: events, logger: slog.New(logHandler)}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Option configures a Service beyond what every caller needs.
type Option func(*Service)

// WithVariants makes agents in a variant resolve to it. Without it nobody is
// in one.
func WithVariants(v Variants) Option { return func(s *Service) { s.variants = v } }

// Load returns the tenant's saved scopes, each filtered to the settings
// the registry still knows.
func (s *Service) Load(ctx context.Context) (*Tenant, error) {
	t, err := s.store.Load(ctx)
	if err != nil {
		return nil, err
	}
	if t.Defaults != nil {
		t.Defaults.Values = t.Defaults.Values.Known()
	}
	for _, o := range t.Overrides {
		o.Values = o.Values.Known()
	}
	if s.variants != nil {
		if t.Variants, err = s.variants.InVariants(ctx); err != nil {
			return nil, err
		}
	}
	return t, nil
}

// Membership is the variant agentID runs on, nil for none.
func (s *Service) Membership(ctx context.Context, agentID uuid.UUID) (*VariantMembership, error) {
	if s.variants == nil {
		return nil, nil
	}
	return s.variants.ForAgent(ctx, agentID)
}

// Effective is what agentID runs with.
func (s *Service) Effective(ctx context.Context, agentID uuid.UUID) (Effective, error) {
	t, err := s.Load(ctx)
	if err != nil {
		return Effective{}, err
	}
	return t.EffectiveFor(agentID), nil
}

// RemoveOverride deletes the agent's override as the person by, so it runs on
// the tenant's defaults as they are at each start. Every setting the override
// held is audited as unset, like any other change to it.
func (s *Service) RemoveOverride(ctx context.Context, agentID uuid.UUID, by *authn.User) error {
	if err := authn.RequirePerson(by); err != nil {
		return err
	}
	if err := s.authorizeOverride(ctx, by, agentID); err != nil {
		return err
	}
	before, revision, err := s.store.Remove(ctx, agentID)
	if err != nil {
		return err
	}
	if before == nil {
		return nil
	}
	tenant, _ := db.TenantFrom(ctx)
	for _, change := range diff(before.Values.Known(), Values{}) {
		s.events.Record(ctx, audit.HarnessConfigChanged,
			"user_id", by.ID, "tenant", tenant, "scope", agentID.String(),
			"harness", change.harness, "setting", change.name,
			"old", change.old, "new", change.new, "revision", revision)
	}
	return nil
}

// Save replaces a scope's settings -- the defaults when agentID is nil --
// as the person by. Every setting that changed is recorded as an audit
// event naming who, which scope, and the value before and after: this
// decides what runs inside every agent's process, so "who put the fleet on
// that model" has to have an answer.
func (s *Service) Save(ctx context.Context, agentID *uuid.UUID, values Values, by *authn.User) (*Scope, error) {
	// An agent able to set config would choose what runs in every agent's
	// process, its own included, so the defaults need a person too.
	if err := authn.RequirePerson(by); err != nil {
		return nil, err
	}
	if agentID != nil {
		if err := s.authorizeOverride(ctx, by, *agentID); err != nil {
			return nil, err
		}
	}
	if err := values.Validate(); err != nil {
		return nil, err
	}
	before, after, err := s.store.Replace(ctx, agentID, compact(values), by.ID)
	if err != nil {
		return nil, err
	}
	after.Values = after.Values.Known()

	var old Values
	if before != nil {
		old = before.Values
	}
	tenant, _ := db.TenantFrom(ctx)
	scope := "defaults"
	if agentID != nil {
		scope = agentID.String()
	}
	for _, change := range diff(old.Known(), after.Values) {
		s.events.Record(ctx, audit.HarnessConfigChanged,
			"user_id", by.ID, "tenant", tenant, "scope", scope,
			"harness", change.harness, "setting", change.name,
			"old", change.old, "new", change.new, "revision", after.Revision)
	}
	return after, nil
}

// compact drops empty harness sections, so what is stored is only what
// is set.
func compact(v Values) Values {
	out := Values{}
	for harness, settings := range v {
		if len(settings) > 0 {
			out[harness] = settings
		}
	}
	return out
}

type change struct {
	harness, name, old, new string
}

// diff lists every setting whose value differs between a and b, an unset
// one reading as "".
func diff(a, b Values) []change {
	var out []change
	for _, d := range definitions {
		was, now := a[d.Harness][d.Name], b[d.Harness][d.Name]
		if was != now {
			out = append(out, change{harness: d.Harness, name: d.Name, old: was, new: now})
		}
	}
	return out
}
