package ticket

import (
	"context"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/harness"
)

// Every route here acts as an authenticated agent, resolved once at the
// edge by internal/authn and taken from the request context. An earlier
// version trusted an X-Agent-Id header, which meant anything that could
// reach the API could claim, complete or release another agent's work and
// comment in its name -- the lease exclusivity and comment authorship this
// design rests on were guarantees on paper only.

// callerAgentID returns the authenticated agent's id.
//
// FromContext failing here is a wiring bug, not an auth failure: the
// middleware rejects an unauthenticated caller before the handler runs, so
// reaching this with no agent means the route was registered without
// authn.RequireAgent().
func callerAgentID(ctx context.Context) (uuid.UUID, error) {
	found, err := authn.FromContext(ctx)
	if err != nil {
		return uuid.UUID{}, huma.Error500InternalServerError("agent route is missing its auth middleware")
	}
	return found.AgentID(), nil
}

// -- Shared wire types --

// BlockedBody is the wire representation of a Blocked outcome.
type BlockedBody struct {
	TicketID            int64  `json:"ticket_id"`
	ConflictingTicketID int64  `json:"conflicting_ticket_id"`
	ConflictingPath     string `json:"conflicting_path"`
	HolderAgentID       string `json:"holder_agent_id"`
}

func blockedToBody(b Blocked) BlockedBody {
	return BlockedBody{
		TicketID:            b.TicketID,
		ConflictingTicketID: b.ConflictingTicketID,
		ConflictingPath:     b.ConflictingPath,
		HolderAgentID:       b.HolderAgentID.String(),
	}
}

// -- Claim --

type ClaimBody struct {
	// TicketID requests one specific ticket; omitted means "whatever's
	// next".
	TicketID *int64 `json:"ticket_id,omitempty"`
	// Harness is declared by a headless run, which has no session to report
	// yet. It is checked against the agent's variant as a session's is.
	Harness harness.Name `json:"harness,omitempty"`
	// HarnessVersion and BundleHash are recorded with the lease for the
	// attempt's record of what it ran with. Both are optional: a supervisor
	// that predates them sends neither.
	HarnessVersion string `json:"harness_version,omitempty" maxLength:"100" doc:"the version the harness reports, e.g. from --version"`
	BundleHash     string `json:"bundle_hash,omitempty" maxLength:"64" doc:"the hash in the workspace's .slop-haus/bundle-hash; hex, and dropped when it is not"`
}

type ClaimRequest struct {
	Body ClaimBody
}

// ClaimResponseBody mirrors ClaimResult on the wire: at most one of Ticket
// and Blocked is set, and both are null when the queue is simply empty.
type ClaimResponseBody struct {
	Ticket  *TicketBody  `json:"ticket"`
	Blocked *BlockedBody `json:"blocked"`
	// SharedOverlaps are held scopes the granted ticket overlaps only inside
	// shared paths: nothing was blocked, but a clash there is resolved as
	// each says when the base branch is merged.
	SharedOverlaps []SharedOverlapBody `json:"shared_overlaps,omitempty"`
	// LockingOffOverlaps are held scopes the granted ticket overlaps that
	// would have blocked it, with the tenant's scope locking off.
	LockingOffOverlaps []LockingOffOverlapBody `json:"locking_off_overlaps,omitempty"`
	// Attempt and Guidance are what MCP's ticket_next returns beside the
	// ticket, for a client that claims over REST: which attempt the lease is
	// on and where its work goes. Set with Ticket.
	Attempt  *AttemptView `json:"attempt,omitempty"`
	Guidance *Guidance    `json:"guidance,omitempty"`
}

// SharedOverlapBody is SharedOverlap on the wire.
type SharedOverlapBody struct {
	TicketID int64  `json:"ticket_id" doc:"the ticket holding the other scope"`
	Path     string `json:"path" doc:"what the two scopes share"`
	Resolve  string `json:"resolve" enum:"migration,regenerate,merge" doc:"how a clash there is settled when the base branch is merged"`
}

// LockingOffOverlapBody is LockingOffOverlap on the wire.
type LockingOffOverlapBody struct {
	TicketID int64  `json:"ticket_id" doc:"the ticket holding the other scope"`
	Path     string `json:"path" doc:"what the two scopes share"`
}

func lockingOffOverlapBodies(overlaps []LockingOffOverlap) []LockingOffOverlapBody {
	out := make([]LockingOffOverlapBody, len(overlaps))
	for i, o := range overlaps {
		out[i] = LockingOffOverlapBody(o)
	}
	return out
}

func sharedOverlapBodies(overlaps []SharedOverlap) []SharedOverlapBody {
	out := make([]SharedOverlapBody, len(overlaps))
	for i, o := range overlaps {
		out[i] = SharedOverlapBody{TicketID: o.TicketID, Path: o.Path, Resolve: o.Resolve}
	}
	return out
}

type ClaimResponse struct {
	Body ClaimResponseBody
}

func (h *Handler) ClaimNext(ctx context.Context, input *ClaimRequest) (*ClaimResponse, error) {
	agentID, err := callerAgentID(ctx)
	if err != nil {
		return nil, err
	}

	result, err := h.svc.ClaimWith(ctx, agentID, &ClaimOptions{
		Only: input.Body.TicketID, Harness: string(input.Body.Harness),
		HarnessVersion: input.Body.HarnessVersion, BundleHash: input.Body.BundleHash,
	})
	if err != nil {
		return nil, h.mapLeaseError(err)
	}

	resp := &ClaimResponse{}
	if result.Ticket != nil {
		body := ticketToBody(*result.Ticket)
		resp.Body.Ticket = &body
		resp.Body.SharedOverlaps = sharedOverlapBodies(result.SharedOverlaps)
		resp.Body.LockingOffOverlaps = lockingOffOverlapBodies(result.LockingOffOverlaps)
		branch := ClaimBranch(result)
		number := 1
		if result.Attempt != nil {
			number = result.Attempt.Number
		}
		resp.Body.Attempt = NewAttemptView(result.Ticket, number, branch).WithSession(result.Session)
		resp.Body.Guidance = GuidanceFor(result.Ticket, branch).OrNil()
	}
	if result.Blocked != nil {
		body := blockedToBody(*result.Blocked)
		resp.Body.Blocked = &body
	}
	return resp, nil
}

// -- Session --

type RecordSessionBody struct {
	Harness   harness.Name `json:"harness"`
	SessionID string       `json:"session_id" minLength:"1" maxLength:"200" doc:"the harness's id for the run's session, to resume it with"`
}

type RecordSessionRequest struct {
	Body RecordSessionBody
}

// RecordSession sets the session of the attempt the caller holds, replacing
// an earlier run's.
func (h *Handler) RecordSession(ctx context.Context, input *RecordSessionRequest) (*struct{}, error) {
	agentID, err := callerAgentID(ctx)
	if err != nil {
		return nil, err
	}
	if err := h.svc.RecordSession(ctx, agentID, &AttemptSession{Harness: string(input.Body.Harness), SessionID: input.Body.SessionID}); err != nil {
		return nil, h.mapLeaseError(err)
	}
	return nil, nil
}

// -- Escalate --

type EscalateBody struct {
	Note string `json:"note" minLength:"1" doc:"why the supervisor is giving the ticket up; recorded as a supervisor comment and as the handoff"`
}

type EscalateRequest struct {
	Body EscalateBody
}

// EscalateClaim sends the ticket the caller holds to discussion.
func (h *Handler) EscalateClaim(ctx context.Context, input *EscalateRequest) (*TicketResponse, error) {
	agentID, err := callerAgentID(ctx)
	if err != nil {
		return nil, err
	}
	t, err := h.svc.Escalate(ctx, agentID, input.Body.Note)
	if err != nil {
		return nil, h.mapLeaseError(err)
	}
	return &TicketResponse{Body: ticketToBody(*t)}, nil
}

// -- Heartbeat --

type HeartbeatRequest struct {
}

// Heartbeat still fails with a revocation notice (409), unlike the MCP
// tools that don't need the held ticket: extending the lease is this
// route's whole job, so with the lease gone the notice is its answer.
func (h *Handler) Heartbeat(ctx context.Context, input *HeartbeatRequest) (*struct{}, error) {
	agentID, err := callerAgentID(ctx)
	if err != nil {
		return nil, err
	}
	if err := h.svc.Heartbeat(ctx, agentID); err != nil {
		return nil, h.mapLeaseError(err)
	}
	return nil, nil
}

// -- Release --

type ReleaseClaimBody struct {
	Reason string `json:"reason"`
	// BlockedOn records a dependency and moves the ticket to blocked
	// rather than open; omitted means the ticket simply goes back to open.
	BlockedOn *int64 `json:"blocked_on,omitempty"`
	// Handoff is where the work stands, for the next claimant; omitted
	// leaves the previous handoff as the latest.
	Handoff string `json:"handoff,omitempty"`
}

type ReleaseClaimRequest struct {
	Body ReleaseClaimBody
}

func (h *Handler) ReleaseClaim(ctx context.Context, input *ReleaseClaimRequest) (*TicketResponse, error) {
	agentID, err := callerAgentID(ctx)
	if err != nil {
		return nil, err
	}

	out, err := h.svc.ReleaseWith(ctx, agentID, ReleaseParams{
		Reason: input.Body.Reason, Handoff: input.Body.Handoff, BlockedOn: input.Body.BlockedOn,
	})
	if err != nil {
		return nil, h.mapLeaseError(err)
	}
	return &TicketResponse{Body: ticketToBody(*out.Ticket)}, nil
}

// -- Integration lock --

type IntegrationLockRequest struct {
}

func (h *Handler) LockIntegration(ctx context.Context, input *IntegrationLockRequest) (*struct{}, error) {
	agentID, err := callerAgentID(ctx)
	if err != nil {
		return nil, err
	}
	if err := h.svc.AcquireIntegrationLock(ctx, agentID); err != nil {
		return nil, h.mapLeaseError(err)
	}
	return nil, nil
}

func (h *Handler) UnlockIntegration(ctx context.Context, input *IntegrationLockRequest) (*struct{}, error) {
	agentID, err := callerAgentID(ctx)
	if err != nil {
		return nil, err
	}
	if err := h.svc.ReleaseIntegrationLock(ctx, agentID); err != nil {
		return nil, h.mapLeaseError(err)
	}
	return nil, nil
}

// LockLongHeldAfter is how long a hold is before the board calls it long.
// Holds seen in practice run seconds to about 20 minutes; a stalled CI left
// one for 4 hours while its holder heartbeated, so no heartbeat check
// caught it. The VMRule SlopHausIntegrationLockHeldLong in the slop-haus
// template's monitoring.yaml says the same 90 minutes.
const LockLongHeldAfter = 90 * time.Minute

// LockHolderBody is whoever holds the integration lock, on the wire.
type LockHolderBody struct {
	AgentID               string    `json:"agent_id"`
	AgentName             string    `json:"agent_name"`
	TicketID              int64     `json:"ticket_id"`
	TicketTitle           string    `json:"ticket_title"`
	PRURL                 string    `json:"pr_url,omitempty" doc:"The holder's ticket's pull request, when one is recorded"`
	AcquiredAt            time.Time `json:"acquired_at"`
	HeldSeconds           int64     `json:"held_seconds" doc:"How long it has been held, by the database clock"`
	LastHeartbeatAt       time.Time `json:"last_heartbeat_at" doc:"When the holder's lease was last renewed"`
	SinceHeartbeatSeconds int64     `json:"since_heartbeat_seconds" doc:"How long ago that was, by the database clock"`
	Dead                  bool      `json:"dead" doc:"No heartbeat for at least the tenant's integration_lock.dead_holder_after. The lock is not released for it: a person decides"`
	LongHeld              bool      `json:"long_held" doc:"Held for at least 90 minutes, far past a normal hold: its checks may be stuck. Nothing is released for it: a person decides"`
}

// LockQueueEntryBody is one agent waiting for the integration lock.
type LockQueueEntryBody struct {
	AgentID       string     `json:"agent_id"`
	AgentName     string     `json:"agent_name"`
	TicketID      int64      `json:"ticket_id"`
	TicketTitle   string     `json:"ticket_title"`
	QueuedAt      time.Time  `json:"queued_at"`
	WaitedSeconds int64      `json:"waited_seconds" doc:"How long it has waited, by the database clock"`
	Priority      string     `json:"priority" enum:"low,normal,high,urgent" doc:"The ticket's priority; it decides, with aging, the order of the queue"`
	MovedUpBy     string     `json:"moved_up_by,omitempty" enum:"priority,aging" doc:"Present when the entry stands ahead of an agent that queued before it, and why"`
	OfferedAt     *time.Time `json:"offered_at,omitempty" doc:"When the lock came free with this agent first in line"`
}

type IntegrationLockStateResponse struct {
	Body struct {
		Holder *LockHolderBody      `json:"holder,omitempty" doc:"Absent when nobody holds the lock"`
		Queue  []LockQueueEntryBody `json:"queue" doc:"Who is waiting for it, the next to get it first"`
	}
}

func lockHolderBody(h *LockHolder) *LockHolderBody {
	if h == nil {
		return nil
	}
	return &LockHolderBody{
		AgentID: h.AgentID.String(), AgentName: h.AgentName, TicketID: h.TicketID,
		TicketTitle: h.TicketTitle, PRURL: h.PRURL, AcquiredAt: h.AcquiredAt,
		HeldSeconds: int64(h.Held.Seconds()), LastHeartbeatAt: h.LastHeartbeatAt,
		SinceHeartbeatSeconds: int64(h.SinceHeartbeat.Seconds()), Dead: h.Dead,
		LongHeld: h.Held >= LockLongHeldAfter,
	}
}

func lockQueueBody(queue []LockQueueEntry) []LockQueueEntryBody {
	out := make([]LockQueueEntryBody, len(queue))
	for i, e := range queue {
		out[i] = LockQueueEntryBody{
			AgentID: e.AgentID.String(), AgentName: e.AgentName, TicketID: e.TicketID,
			TicketTitle: e.TicketTitle, QueuedAt: e.QueuedAt, WaitedSeconds: int64(e.Waited.Seconds()),
			OfferedAt: e.OfferedAt, Priority: string(e.Priority), MovedUpBy: e.MovedUpBy,
		}
	}
	return out
}

// LockNameInput addresses one of the tenant's locks. Absent is the default
// lock, so a call that predates per-branch locks means what it always did.
type LockNameInput struct {
	Name string `query:"name" doc:"The lock's name, as listed by getIntegrationLocks. Absent: the default integration lock"`
}

func (h *Handler) GetIntegrationLock(ctx context.Context, input *LockNameInput) (*IntegrationLockStateResponse, error) {
	state, err := h.svc.IntegrationLockStateNamed(ctx, input.Name)
	if err != nil {
		return nil, h.mapLeaseError(err)
	}
	resp := &IntegrationLockStateResponse{}
	resp.Body.Holder = lockHolderBody(state.Holder)
	resp.Body.Queue = lockQueueBody(state.Queue)
	return resp, nil
}

// NamedLockBody is one of the tenant's locks, with the repo and branch it
// serves when it is a per-branch one.
type NamedLockBody struct {
	Name   string               `json:"name" doc:"Pass as the name query parameter to address this lock"`
	Repo   string               `json:"repo,omitempty" doc:"Absent on the default lock"`
	Branch string               `json:"branch,omitempty" doc:"Absent on the default lock"`
	Holder *LockHolderBody      `json:"holder,omitempty" doc:"Absent when nobody holds the lock"`
	Queue  []LockQueueEntryBody `json:"queue" doc:"Who is waiting for it, the next to get it first"`
}

type IntegrationLocksResponse struct {
	Body struct {
		Locks []NamedLockBody `json:"locks" doc:"The default lock first, then each per-branch lock that is held or queued for"`
	}
}

func (h *Handler) GetIntegrationLocks(ctx context.Context, _ *struct{}) (*IntegrationLocksResponse, error) {
	locks, err := h.svc.IntegrationLocks(ctx)
	if err != nil {
		return nil, h.mapLeaseError(err)
	}
	resp := &IntegrationLocksResponse{}
	resp.Body.Locks = make([]NamedLockBody, len(locks))
	for i, l := range locks {
		repo, branch := ParseLockName(l.Name)
		resp.Body.Locks[i] = NamedLockBody{
			Name: l.Name, Repo: repo, Branch: branch,
			Holder: lockHolderBody(l.Holder), Queue: lockQueueBody(l.Queue),
		}
	}
	return resp, nil
}

// ForceUnlockIntegration is a person's release of the lock. Logged with who
// held it, since it is the one way to take a lock from an agent that has not
// finished with it.
func (h *Handler) ForceUnlockIntegration(ctx context.Context, input *LockNameInput) (*struct{}, error) {
	holder, err := h.svc.ForceReleaseNamedLock(ctx, input.Name)
	if err != nil {
		return nil, h.mapLeaseError(err)
	}
	h.logger.Info("integration lock released by a person", "lock", input.Name,
		"agent_id", holder.AgentID, "ticket_id", holder.TicketID, "held", holder.Held)
	return nil, nil
}

type DropLockWaiterRequest struct {
	LockNameInput
	AgentID string `path:"agent_id" format:"uuid"`
}

func (h *Handler) DropLockWaiter(ctx context.Context, input *DropLockWaiterRequest) (*struct{}, error) {
	agentID, err := uuid.Parse(input.AgentID)
	if err != nil {
		return nil, huma.Error422UnprocessableEntity("agent_id must be a uuid")
	}
	if err := h.svc.DropNamedLockWaiter(ctx, input.Name, agentID); err != nil {
		return nil, h.mapLeaseError(err)
	}
	h.logger.Info("agent removed from the integration lock queue by a person", "lock", input.Name, "agent_id", agentID)
	return nil, nil
}
