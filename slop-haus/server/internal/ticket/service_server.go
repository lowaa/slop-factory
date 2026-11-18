package ticket

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// ServerStore is the server agent's operations on the lease and queue tables.
// Callers reach them through Service, so there is one place to add policy for
// claims and hand-backs; today these are pass-throughs.
type ServerStore interface {
	EnsureServerAgent(ctx context.Context) (uuid.UUID, error)
	ServerClaim(ctx context.Context, serverAgent uuid.UUID, ticketID int64, attempt int, ttl time.Duration) (*ClaimResult, error)
	ServerClaimLanding(ctx context.Context, serverAgent uuid.UUID, ticketID int64, attempt int, ttl time.Duration) (*ClaimResult, error)
	ServerHandBack(ctx context.Context, serverAgent uuid.UUID, ticketID int64, guidance string, affinity time.Duration) error
	ServerReleaseLanding(ctx context.Context, serverAgent uuid.UUID, ticketID int64) error
	RepairMergeQueue(ctx context.Context, grace time.Duration) ([]int64, error)
}

func (s *Service) EnsureServerAgent(ctx context.Context) (uuid.UUID, error) {
	return s.store.EnsureServerAgent(ctx)
}

func (s *Service) ServerClaim(ctx context.Context, server uuid.UUID, ticketID int64, attempt int, ttl time.Duration) (*ClaimResult, error) {
	return s.store.ServerClaim(ctx, server, ticketID, attempt, ttl)
}

func (s *Service) ServerClaimLanding(ctx context.Context, server uuid.UUID, ticketID int64, attempt int, ttl time.Duration) (*ClaimResult, error) {
	return s.store.ServerClaimLanding(ctx, server, ticketID, attempt, ttl)
}

func (s *Service) ServerHandBack(ctx context.Context, server uuid.UUID, ticketID int64, guidance string, affinity time.Duration) error {
	return s.store.ServerHandBack(ctx, server, ticketID, guidance, affinity)
}

func (s *Service) ServerReleaseLanding(ctx context.Context, server uuid.UUID, ticketID int64) error {
	return s.store.ServerReleaseLanding(ctx, server, ticketID)
}

func (s *Service) RepairMergeQueue(ctx context.Context, grace time.Duration) ([]int64, error) {
	return s.store.RepairMergeQueue(ctx, grace)
}

// HeartbeatFor extends the agent's lease to ttl, which the server agent sets
// from its own configuration where Heartbeat takes the default.
func (s *Service) HeartbeatFor(ctx context.Context, agentID uuid.UUID, ttl time.Duration) error {
	return s.store.Heartbeat(ctx, agentID, ttl)
}

// DiscussHeld is Discuss for an agent that is one per tenant, like the server:
// the lease is checked to be ticketID's first, so a caller whose lease ended
// and whose agent has since claimed another ticket cannot discuss that one.
// The check and the move are two steps; the window is short beside the
// caller's, and the move only ever asks a human to look.
func (s *Service) DiscussHeld(ctx context.Context, agentID uuid.UUID, ticketID int64, question, handoff string) (*Ticket, error) {
	if _, err := s.store.HeldAttempt(ctx, agentID, ticketID); err != nil {
		return nil, err
	}
	return s.Discuss(ctx, agentID, question, handoff)
}
