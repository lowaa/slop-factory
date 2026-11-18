package ticket

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// ServerAgent is what the server's merge engines use of the ticket side. Its
// operations reach the store through Service, not around it.
type ServerAgent struct {
	*Service
}

func NewServerAgent(svc *Service) *ServerAgent {
	return &ServerAgent{Service: svc}
}

// Heartbeat extends the lease to ttl.
func (a *ServerAgent) Heartbeat(ctx context.Context, agent uuid.UUID, ttl time.Duration) error {
	return a.Service.HeartbeatFor(ctx, agent, ttl)
}

// Discuss moves the ticket the server holds into discussion.
func (a *ServerAgent) Discuss(ctx context.Context, agent uuid.UUID, ticketID int64, question, handoff string) (*Ticket, error) {
	return a.Service.DiscussHeld(ctx, agent, ticketID, question, handoff)
}
