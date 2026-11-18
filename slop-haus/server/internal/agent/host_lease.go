package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/httperr"
)

// A Remote Control agent has one host at a time, whichever machine runs it:
// two would fight over one identity. The host takes a lease, renews it while it
// serves, and releases it when it stops. The server refuses a second holder
// while the lease is live and frees it when a renewal is missed. Making the
// old host stop serving once it has missed one is the host's job; a lease the
// server expired cannot reach into a machine that lost its network.

const (
	// DefaultHostLeaseTTL is long enough for a host to miss a renewal or two
	// without losing the lease to a successor.
	DefaultHostLeaseTTL = 90 * time.Second
	maxHolderLen        = 100
)

var (
	// ErrHostLeaseHeld is an acquire while another holder's lease is live.
	ErrHostLeaseHeld = errors.New("agent: another host holds this agent's lease")
	// ErrHostLeaseLost is a renewal by a holder that does not hold a live lease:
	// it expired, or another host took it. The caller must stop serving.
	ErrHostLeaseLost = errors.New("agent: you do not hold a live host lease")
	// ErrNotRemoteControl is a lease asked for by an agent that is not a Remote Control host.
	ErrNotRemoteControl = errors.New("agent: only a Remote Control agent has a host lease")
	// ErrHolderInvalid is a holder id the lease refuses.
	ErrHolderInvalid = errors.New("agent: invalid lease holder")
)

// HostLease is a live lease.
type HostLease struct {
	Holder    string
	ExpiresAt time.Time
}

// HostLeaseStore keeps the leases. Expiry is judged by the database's clock.
type HostLeaseStore interface {
	// Acquire takes the agent's lease for holder if none is live or holder
	// already has it, ErrHostLeaseHeld otherwise.
	Acquire(ctx context.Context, agentID uuid.UUID, holder string, ttl time.Duration) (*HostLease, error)
	// Renew extends holder's live lease, ErrHostLeaseLost if it has none.
	Renew(ctx context.Context, agentID uuid.UUID, holder string, ttl time.Duration) (*HostLease, error)
	// Release ends holder's lease. A lease held by anyone else, or none, is left alone.
	Release(ctx context.Context, agentID uuid.UUID, holder string) error
}

type PostgresHostLeaseStore struct {
	db *db.DB
}

func NewPostgresHostLeaseStore(database *db.DB) *PostgresHostLeaseStore {
	return &PostgresHostLeaseStore{db: database}
}

var _ HostLeaseStore = (*PostgresHostLeaseStore)(nil)

func (s *PostgresHostLeaseStore) Acquire(ctx context.Context, agentID uuid.UUID, holder string, ttl time.Duration) (*HostLease, error) {
	lease := &HostLease{Holder: holder}
	err := s.db.QueryRow(ctx, `
		INSERT INTO remote_control_host_leases AS l (agent_id, holder, expires_at)
		VALUES ($1, $2, clock_timestamp() + make_interval(secs => $3))
		ON CONFLICT (tenant_id, agent_id) DO UPDATE
		   SET acquired_at = CASE WHEN l.holder = EXCLUDED.holder THEN l.acquired_at ELSE clock_timestamp() END,
		       holder = EXCLUDED.holder, expires_at = EXCLUDED.expires_at
		 WHERE l.holder = EXCLUDED.holder OR l.expires_at <= clock_timestamp()
		RETURNING expires_at`, agentID, holder, ttl.Seconds()).Scan(&lease.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrHostLeaseHeld
	}
	if err != nil {
		return nil, fmt.Errorf("agent: acquire host lease: %w", err)
	}
	return lease, nil
}

func (s *PostgresHostLeaseStore) Renew(ctx context.Context, agentID uuid.UUID, holder string, ttl time.Duration) (*HostLease, error) {
	lease := &HostLease{Holder: holder}
	err := s.db.QueryRow(ctx, `
		UPDATE remote_control_host_leases
		   SET expires_at = clock_timestamp() + make_interval(secs => $3)
		 WHERE agent_id = $1 AND holder = $2 AND expires_at > clock_timestamp()
		RETURNING expires_at`, agentID, holder, ttl.Seconds()).Scan(&lease.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrHostLeaseLost
	}
	if err != nil {
		return nil, fmt.Errorf("agent: renew host lease: %w", err)
	}
	return lease, nil
}

func (s *PostgresHostLeaseStore) Release(ctx context.Context, agentID uuid.UUID, holder string) error {
	if _, err := s.db.Exec(ctx, `DELETE FROM remote_control_host_leases WHERE agent_id = $1 AND holder = $2`, agentID, holder); err != nil {
		return fmt.Errorf("agent: release host lease: %w", err)
	}
	return nil
}

// HostLeaseService hands a Remote Control agent's lease to the one host that
// asks first.
type HostLeaseService struct {
	store  HostLeaseStore
	agents *Service
	ttl    time.Duration
	logger *slog.Logger
}

// NewHostLeaseService leases for ttl, DefaultHostLeaseTTL when zero; tests pass short ones.
func NewHostLeaseService(store HostLeaseStore, agents *Service, ttl time.Duration, logHandler slog.Handler) *HostLeaseService {
	if ttl <= 0 {
		ttl = DefaultHostLeaseTTL
	}
	return &HostLeaseService{store: store, agents: agents, ttl: ttl, logger: slog.New(logHandler)}
}

// checkHost refuses a holder id that is not usable and an agent that is not a live Remote Control one.
func (s *HostLeaseService) checkHost(ctx context.Context, agentID uuid.UUID, holder string) error {
	if holder == "" || len(holder) > maxHolderLen {
		return ErrHolderInvalid
	}
	found, err := s.agents.store.Get(ctx, agentID)
	if err != nil {
		return err
	}
	switch {
	case !found.IsConductor():
		return ErrNotRemoteControl
	case found.Archived():
		return ErrArchived
	case found.Disabled():
		return ErrIsDisabled
	}
	return nil
}

func (s *HostLeaseService) Acquire(ctx context.Context, agentID uuid.UUID, holder string) (*HostLease, error) {
	if err := s.checkHost(ctx, agentID, holder); err != nil {
		return nil, err
	}
	lease, err := s.store.Acquire(ctx, agentID, holder, s.ttl)
	if err != nil {
		return nil, err
	}
	s.logger.InfoContext(ctx, "audit", "action", "remote_control_host_lease_acquired", "agent", agentID, "holder", holder)
	return lease, nil
}

func (s *HostLeaseService) Renew(ctx context.Context, agentID uuid.UUID, holder string) (*HostLease, error) {
	if err := s.checkHost(ctx, agentID, holder); err != nil {
		return nil, err
	}
	return s.store.Renew(ctx, agentID, holder, s.ttl)
}

func (s *HostLeaseService) Release(ctx context.Context, agentID uuid.UUID, holder string) error {
	if holder == "" || len(holder) > maxHolderLen {
		return ErrHolderInvalid
	}
	if err := s.store.Release(ctx, agentID, holder); err != nil {
		return err
	}
	s.logger.InfoContext(ctx, "audit", "action", "remote_control_host_lease_released", "agent", agentID, "holder", holder)
	return nil
}

var hostLeaseRules = []httperr.Rule{
	{Err: ErrHostLeaseHeld, Kind: httperr.Conflict, Message: "another host is running this agent: stop it, or wait for its lease to expire"},
	{Err: ErrHostLeaseLost, Kind: httperr.Conflict, Message: "you no longer hold this agent's host lease: stop serving"},
	{Err: ErrNotRemoteControl, Kind: httperr.Forbidden, Message: "only a Remote Control agent has a host lease"},
	{Err: ErrHolderInvalid, Kind: httperr.Invalid, Message: "holder must be 1 to 100 characters"},
}
