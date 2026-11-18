// Package hookserver answers Claude Code's hook callbacks, which is what
// makes an agent keep working without a human prodding it.
//
// Claude Code's `http` hook type POSTs the hook's JSON input to a URL and
// reads the JSON response as the hook's output, so the whole agent loop is
// this server answering two requests -- no wrapper script, no local binary
// on the critical path.
//
// The contract these endpoints keep: hooks never mutate ticket or lease
// state. They write session bookkeeping (counters, last-seen, the session
// binding) and the loop breaker marks the session stuck, because those are
// facts about the agent rather than decisions about the work. Everything
// that assigns, releases or completes a ticket stays behind the agent's own
// tool calls, where it is atomic, audited and uniform.
package hookserver

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"

	"github.com/danielgtaylor/huma/v2"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/agent"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/agentmsg"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/session"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/tenantstate"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// Tickets is the slice of ticket.Service the hooks read.
type Tickets interface {
	LeaseFor(ctx context.Context, agentID uuid.UUID) (*ticket.Lease, error)
	Get(ctx context.Context, id int64) (*ticket.Ticket, error)
}

// Messages is the slice of agentmsg.Service the hooks need: whether the
// human has said something to the agent, and handing it over.
type Messages interface {
	Pending(ctx context.Context, agentID uuid.UUID) (int, error)
	Deliver(ctx context.Context, agentID uuid.UUID) ([]agentmsg.Message, error)
}

// Sessions is the slice of session.Service the hooks drive.
type Sessions interface {
	Start(ctx context.Context, agentID uuid.UUID, claudeSessionID, cwd string) (*session.StartResult, error)
	RecordBlock(ctx context.Context, agentID uuid.UUID, claudeSessionID string) (count int, tripped bool, err error)
	TouchSession(ctx context.Context, agentID uuid.UUID, claudeSessionID string) (session.State, error)
	ResetBlocks(ctx context.Context, agentID uuid.UUID, claudeSessionID string) error
	Describe(ctx context.Context, agentID uuid.UUID, claudeSessionID string, d *session.Description) error
	MarkStuck(ctx context.Context, agentID uuid.UUID, claudeSessionID, reason string) error
}

// Layouts is the slice of agent.LayoutStore the hooks write: an agent's
// latest report of what its workspace looks like.
type Layouts interface {
	SaveLayout(ctx context.Context, agentID uuid.UUID, layout *agent.Layout) error
}

// permissiveSchema builds the schema for a hook payload and then allows
// whatever else the sender includes.
//
// Every hook input here describes only the fields slop-haus reads. The
// sender owns the rest of the payload and adds to it over time, so an
// unknown property must be ignored rather than rejected.
func permissiveSchema(r huma.Registry, t reflect.Type) *huma.Schema {
	// allowRef=false matters: with a ref, the returned value is a $ref
	// wrapper and setting AdditionalProperties on it changes nothing about
	// the definition it points at -- the request still gets rejected, with
	// no sign of why.
	schema := r.Schema(t, false, t.Name())
	schema.AdditionalProperties = true
	return schema
}

// SelfDescription is the optional slop_haus object a hook payload may carry:
// the run's harness, model and labels (see session.Description).
//
// It is deliberately left out of the payload schemas, which leaves it one
// of the additional properties permissiveSchema accepts in any shape, and
// it is decoded leniently: a malformed one is dropped rather than failing
// the hook -- for Stop, a rejected payload would let the agent quit.
type SelfDescription struct {
	SlopHaus json.RawMessage `json:"slop_haus,omitempty"`
}

// describe records the payload's description against the run, if it
// carried one. Liveness bookkeeping, like the session binding: a failure is
// logged and never changes the hook's answer.
func (h *Handler) describe(ctx context.Context, agentID uuid.UUID, claudeSessionID string, d *session.Description) {
	if d == nil {
		return
	}
	if err := h.sessions.Describe(ctx, agentID, claudeSessionID, d); err != nil && !errors.Is(err, session.ErrNotFound) {
		h.logger.Warn("describe session", "agent_id", agentID, "err", err)
	}
}

// recordLayout stores the workspace layout the payload carried, if any.
// Like describe, bookkeeping about the agent: a failure is logged and never
// changes the hook's answer. A payload with no layout -- a workspace that
// has not run update.sh -- records nothing, and the agent shows as
// unknown; nothing here or elsewhere acts on that.
func (h *Handler) recordLayout(ctx context.Context, agentID uuid.UUID, raw json.RawMessage) {
	if h.layouts == nil {
		return
	}
	layout := agent.ParseLayout(raw)
	if layout == nil {
		return
	}
	if err := h.layouts.SaveLayout(ctx, agentID, layout); err != nil {
		h.logger.Warn("record workspace layout", "agent_id", agentID, "err", err)
	}
}

// isConductor reports whether the caller's credential is a conductor's.
//
// A conductor is not a worker: its host runs the harness and reports its
// conversations itself, so the hooks a worker's workspace runs mean nothing
// for it. The scripts skip a session the host started, but a script copy that
// predates that, or a harness run by hand with a conductor's credential, still
// reaches here; refusing to act on it keeps its session out of the worker
// bookkeeping and never sends it to claim work.
func isConductor(caller authn.Identity) bool {
	kind, ok := caller.(interface{ IsConductor() bool })
	return ok && kind.IsConductor()
}

// archived reports whether the caller's tenant is archived, in which case a
// hook is answered as a no-op: no session bookkeeping, no probe for work,
// nothing that touches a lease. Stop in particular must let the agent go,
// or an archived tenant's agent would be kept running by its own hook.
func (h *Handler) archived(ctx context.Context, hook string) (bool, error) {
	archived, err := tenantstate.ArchivedInContext(ctx, h.archive)
	if err != nil {
		return false, err
	}
	if archived {
		h.logger.Info("dropped a hook for an archived tenant", "hook", hook)
	}
	return archived, nil
}
