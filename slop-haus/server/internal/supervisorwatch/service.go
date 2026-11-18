// Package supervisorwatch is how a quiet supervisor is noticed: the Workers
// page shows it, and a collector exports the numbers the alert is raised on.
package supervisorwatch

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/tenantstate"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/wakeup"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/workspace"
)

// OfflineAfter is how long without a poll makes a supervisor offline. A poll
// is held open for 30 s, so a live one is never this quiet. The alert rule
// in the slop-haus template's monitoring.yaml says the same five minutes.
const OfflineAfter = 5 * time.Minute

type Store interface {
	Facts(ctx context.Context) (*Facts, error)
	Tenants(ctx context.Context) ([]uuid.UUID, error)
	ReportSlot(ctx context.Context, agentID uuid.UUID, r *SlotReport) error
	// Slots are the context tenant's enabled agents' latest reports.
	Slots(ctx context.Context) ([]*SlotRow, error)
	// Supervisors are the context tenant's enabled supervisors' last polls.
	Supervisors(ctx context.Context) ([]*SupervisorRow, error)
}

// Policy is whether the fleet may start new work. While it may not, work
// waiting is deliberate and nobody is late for it.
type Policy interface {
	TicketProcessingEnabled(ctx context.Context) (bool, error)
}

type Service struct {
	store  Store
	policy Policy
	claims Claimability
	bounds Bounds
	now    func() time.Time
	// Versions is what "current" means for the stale marks.
	versions Versions
}

// Versions are what slots and supervisors are compared with to be marked
// stale. A service starts with what this server serves; the zero value marks
// nothing (every status is unknown), which tests use.
type Versions struct {
	// Bundle is the managed asset bundle hash the server serves now.
	Bundle string
	// Protocol is the newest supervisor protocol the server speaks.
	Protocol int
}

// WithVersions replaces what slots and supervisors are compared with.
func (s *Service) WithVersions(v Versions) *Service {
	s.versions = v
	return s
}

func NewService(store Store, policy Policy, claims Claimability) *Service {
	return &Service{
		store: store, policy: policy, claims: claims, bounds: DefaultBounds(), now: time.Now,
		versions: Versions{Bundle: workspace.BundleHash(), Protocol: wakeup.SupervisorProtocol},
	}
}

// WithBounds replaces the stall bounds, which tests shorten.
func (s *Service) WithBounds(b Bounds) *Service {
	s.bounds = b
	return s
}

// Status is the context tenant's view for the board.
type Status struct {
	Claimable    int64
	LastPolledAt *time.Time
	// Offline is a supervisor that has polled before and has stopped. A
	// tenant that never ran one is not offline, it has none.
	Offline bool
	Slots   []*Slot
	// Supervisors are the supervisors that have polled, by name.
	Supervisors []*Supervisor
	Current     Versions
}

// Supervisor is a SupervisorRow judged against the current protocol.
type Supervisor struct {
	SupervisorRow
	ProtocolStatus VersionStatus
}

func (s *Service) Status(ctx context.Context) (*Status, error) {
	f, err := s.facts(ctx)
	if err != nil {
		return nil, err
	}
	slots, err := s.Slots(ctx)
	if err != nil {
		return nil, err
	}
	supervisors, err := s.Supervisors(ctx)
	if err != nil {
		return nil, err
	}
	return &Status{
		Supervisors:  supervisors,
		Current:      s.versions,
		Claimable:    f.Claimable,
		LastPolledAt: f.LastPolledAt,
		Offline:      f.LastPolledAt != nil && s.now().Sub(*f.LastPolledAt) > OfflineAfter,
		Slots:        slots,
	}, nil
}

// Supervisors judges the context tenant's supervisors' protocol.
func (s *Service) Supervisors(ctx context.Context) ([]*Supervisor, error) {
	rows, err := s.store.Supervisors(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*Supervisor, 0, len(rows))
	for _, row := range rows {
		out = append(out, &Supervisor{SupervisorRow: *row, ProtocolStatus: protocolStatus(row.Protocol, s.versions.Protocol)})
	}
	return out, nil
}

func (s *Service) facts(ctx context.Context) (*Facts, error) {
	f, err := s.store.Facts(ctx)
	if err != nil {
		return nil, err
	}
	enabled, err := s.policy.TicketProcessingEnabled(ctx)
	if err != nil {
		return nil, err
	}
	if !enabled {
		f.Claimable = 0
	}
	return f, nil
}

// Fleet is every tenant's facts combined: tickets summed, the newest poll
// and claim of any.
func (s *Service) Fleet(ctx context.Context) (*Facts, error) {
	var total Facts
	err := tenantstate.Each(ctx, s.store, func(ctx context.Context, _ uuid.UUID) error {
		f, err := s.facts(ctx)
		if err != nil {
			return err
		}
		total.Claimable += f.Claimable
		total.LastPolledAt = later(total.LastPolledAt, f.LastPolledAt)
		total.LastClaimedAt = later(total.LastClaimedAt, f.LastClaimedAt)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &total, nil
}

func later(a, b *time.Time) *time.Time {
	if a == nil || (b != nil && b.After(*a)) {
		return b
	}
	return a
}
