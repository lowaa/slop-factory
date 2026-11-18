package ticket

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
)

// The kinds of actor a status change records (see ticket_status_changes).
const (
	moverAgent  = "agent"
	moverUser   = "user"
	moverSystem = "system"
)

// statusMover is who is about to change a ticket's status, and why where
// that is known. The database's trigger records every status change but
// cannot know either, so the code that writes a status says so first, in
// settings that last to the end of the transaction: each site that writes a
// status sets its own immediately before, so none inherits another's.
type statusMover struct {
	kind  string
	id    *uuid.UUID
	cause string
}

func agentMover(id uuid.UUID, cause string) *statusMover {
	return &statusMover{kind: moverAgent, id: &id, cause: cause}
}

// userMover is a person's move; id is nil where the route does not know
// which person.
func userMover(id *uuid.UUID, cause string) *statusMover {
	return &statusMover{kind: moverUser, id: id, cause: cause}
}

// systemMover is an automatic move: an unblock, a lease expiring.
func systemMover(cause string) *statusMover {
	return &statusMover{kind: moverSystem, cause: cause}
}

// authorMover is the move of whoever authored a change.
func authorMover(a Author, cause string) *statusMover {
	switch {
	case a.AgentID != nil:
		return agentMover(*a.AgentID, cause)
	case a.UserID != nil:
		return userMover(a.UserID, cause)
	}
	return systemMover(cause)
}

// set makes m the mover for the rest of q's transaction.
func (m *statusMover) set(ctx context.Context, q db.Querier) error {
	var id string
	if m.id != nil {
		id = m.id.String()
	}
	if _, err := q.Exec(ctx, `
		SELECT set_config('slophaus.actor_kind', $1, true),
		       set_config('slophaus.actor_id', $2, true),
		       set_config('slophaus.cause', $3, true)`, m.kind, id, m.cause); err != nil {
		return fmt.Errorf("record who moves the ticket: %w", err)
	}
	return nil
}

type userKey struct{}

// WithUser notes on ctx the person a transition made under it is on behalf
// of, for the status change record.
func WithUser(ctx context.Context, userID uuid.UUID) context.Context {
	return context.WithValue(ctx, userKey{}, userID)
}

type causeKey struct{}

// WithCause notes on ctx why the status changes made under it are made, for
// the status change record's cause. A cause the transition itself knows (a
// lease that ended) wins over it.
func WithCause(ctx context.Context, cause string) context.Context {
	return context.WithValue(ctx, causeKey{}, cause)
}

func causeFrom(ctx context.Context) string {
	cause, _ := ctx.Value(causeKey{}).(string)
	return cause
}

func userFrom(ctx context.Context) *uuid.UUID {
	id, ok := ctx.Value(userKey{}).(uuid.UUID)
	if !ok {
		return nil
	}
	return &id
}

// byUser notes the signed-in person on ctx, if there is one, so the status
// changes a board route makes are recorded as theirs.
func byUser(ctx context.Context) context.Context {
	if user, err := authn.UserFromContext(ctx); err == nil {
		return WithUser(ctx, user.ID)
	}
	return ctx
}
