package ticket_test

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// routeStore records which store operation the facade's calls reach. Any
// other method panics on the nil embedded Store, which shows a call that
// skipped the operations listed here.
type routeStore struct {
	ticket.Store
	calls   []string
	ttl     time.Duration
	heldErr error
	args    []any
}

func (r *routeStore) EnsureServerAgent(context.Context) (uuid.UUID, error) {
	r.calls = append(r.calls, "ensure")
	return uuid.Nil, nil
}

func (r *routeStore) ServerClaim(_ context.Context, id uuid.UUID, ticketID int64, attempt int, ttl time.Duration) (*ticket.ClaimResult, error) {
	r.args = append(r.args, id, ticketID, attempt, ttl)
	r.calls = append(r.calls, "claim")
	return nil, nil
}

func (r *routeStore) ServerClaimLanding(context.Context, uuid.UUID, int64, int, time.Duration) (*ticket.ClaimResult, error) {
	r.calls = append(r.calls, "claim_landing")
	return nil, nil
}

func (r *routeStore) ServerHandBack(_ context.Context, id uuid.UUID, ticketID int64, guidance string, affinity time.Duration) error {
	r.args = append(r.args, id, ticketID, guidance, affinity)
	r.calls = append(r.calls, "hand_back")
	return nil
}

func (r *routeStore) ServerReleaseLanding(context.Context, uuid.UUID, int64) error {
	r.calls = append(r.calls, "release_landing")
	return nil
}

func (r *routeStore) RepairMergeQueue(context.Context, time.Duration) ([]int64, error) {
	r.calls = append(r.calls, "repair")
	return nil, nil
}

func (r *routeStore) Heartbeat(_ context.Context, _ uuid.UUID, ttl time.Duration) error {
	r.calls = append(r.calls, "heartbeat")
	r.ttl = ttl
	return nil
}

func (r *routeStore) HeldAttempt(_ context.Context, id uuid.UUID, ticketID int64) (*ticket.AttemptRef, error) {
	r.args = append(r.args, id, ticketID)
	r.calls = append(r.calls, "held")
	return nil, r.heldErr
}

func TestServerAgent_RoutesThroughService(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := &routeStore{}
	a := ticket.NewServerAgent(ticket.NewService(store, slog.DiscardHandler))
	id := uuid.New()

	_, _ = a.EnsureServerAgent(ctx)
	_, _ = a.ServerClaim(ctx, id, 11, 2, 3*time.Minute)
	_, _ = a.ServerClaimLanding(ctx, id, 1, 1, time.Minute)
	require.NoError(t, a.ServerHandBack(ctx, id, 12, "g", 4*time.Minute))
	require.NoError(t, a.ServerReleaseLanding(ctx, id, 1))
	_, _ = a.RepairMergeQueue(ctx, time.Minute)
	require.NoError(t, a.Heartbeat(ctx, id, 7*time.Minute))

	assert.Equal(t, []string{"ensure", "claim", "claim_landing", "hand_back", "release_landing", "repair", "heartbeat"}, store.calls)
	assert.Equal(t, []any{id, int64(11), 2, 3 * time.Minute, id, int64(12), "g", 4 * time.Minute}, store.args)
	assert.Equal(t, 7*time.Minute, store.ttl, "the server's own lease ttl, not the default")
}

func TestServerAgent_DiscussRefusesATicketTheServerDoesNotHold(t *testing.T) {
	t.Parallel()
	store := &routeStore{heldErr: ticket.ErrNoLease}
	a := ticket.NewServerAgent(ticket.NewService(store, slog.DiscardHandler))

	agent := uuid.New()
	_, err := a.Discuss(context.Background(), agent, 5, "q", "h")

	assert.ErrorIs(t, err, ticket.ErrNoLease)
	assert.Equal(t, []any{agent, int64(5)}, store.args)
	assert.Equal(t, []string{"held"}, store.calls, "nothing moved after the lease check failed")
}
