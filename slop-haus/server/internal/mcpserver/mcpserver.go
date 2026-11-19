// Package mcpserver exposes slop-haus's coordination surface to agents as
// MCP tools.
//
// Every tool is a thin caller of the same services the REST handlers use.
// A rule reimplemented slightly differently here is how "two agents can't
// hold overlapping paths" (where the tenant locks scopes) stops being a property of the system and becomes
// a property of whichever door the agent came through.
//
// Two constraints shape the surface:
//
// Tool schemas cost context on every turn, for every agent. The surface is
// kept to a dozen tools and the responses terse -- a claim that returns
// forty lines of timestamps and nulls is real money across four agents
// running all day.
//
// Every tool is a heartbeat. Any call extends the caller's lease and marks
// its session live, so there is no separate keepalive to forget; and any
// call that actually changes something resets the stop-hook loop breaker.
package mcpserver

import (
	"context"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/agentmsg"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/llmusage"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/pendingaction"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

// Tickets is the slice of ticket.Service the tools use.
type Tickets interface {
	Claim(ctx context.Context, agentID uuid.UUID, only *int64) (*ticket.ClaimResult, error)
	Heartbeat(ctx context.Context, agentID uuid.UUID) error
	ReleaseWith(ctx context.Context, agentID uuid.UUID, p ticket.ReleaseParams) (*ticket.ReleaseOutcome, error)
	Done(ctx context.Context, agentID uuid.UUID, params *ticket.DoneParams) (*ticket.Ticket, error)
	ExtendScope(ctx context.Context, agentID uuid.UUID, paths []model.Path) (*ticket.ExtendResult, error)
	NarrowScope(ctx context.Context, agentID uuid.UUID, remove, keep []string) (*ticket.NarrowResult, error)
	AcquireIntegrationLock(ctx context.Context, agentID uuid.UUID) error
	ReleaseIntegrationLock(ctx context.Context, agentID uuid.UUID) error
	LeaseFor(ctx context.Context, agentID uuid.UUID) (*ticket.Lease, error)
	RecentRevocation(ctx context.Context, agentID uuid.UUID) (*ticket.RevocationContext, error)

	// DetailFor is a ticket with the conversation agentID may see: the ticket's
	// own comments and its attempt's, never a sibling's. Every read of a
	// ticket's conversation for an agent goes through it.
	DetailFor(ctx context.Context, id int64, agentID uuid.UUID) (*ticket.Detail, error)
	List(ctx context.Context, filter *ticket.ListFilter) ([]ticket.Ticket, error)
	Create(ctx context.Context, params *ticket.CreateParams) (*ticket.Ticket, error)
	ScopeOverlaps(ctx context.Context, id int64) ([]ticket.ScopeOverlap, error)
	AddComment(ctx context.Context, ticketID int64, author ticket.Author, body string) (*ticket.Comment, error)
	AgentCommentOn(ctx context.Context, agentID uuid.UUID, ticketID int64, attempt *int, body string) (*ticket.Comment, error)
	ResolveAttempt(ctx context.Context, ticketID int64, named *int) (*ticket.AttemptRef, error)
	Discuss(ctx context.Context, agentID uuid.UUID, question, handoff string) (*ticket.Ticket, error)
	Review(ctx context.Context, agentID uuid.UUID, params *ticket.ReviewParams) (*ticket.Ticket, error)
	ChangeGateFor(ctx context.Context, agentID uuid.UUID) (*ticket.ChangeGate, error)
	Prepare(ctx context.Context, agentID uuid.UUID, reason string) (*ticket.Ticket, error)
	EditHeld(ctx context.Context, agentID uuid.UUID, params *ticket.EditParams) (*ticket.EditResult, error)
	// The agent forms decide for themselves whether the caller is owner-bound
	// (a conductor, or a console) or a worker, so a handler cannot pick the
	// wrong rule.
	AgentAddDependency(ctx context.Context, agentID uuid.UUID, ticketID, dependsOnID int64) (*ticket.DependencyChange, error)
	AgentRemoveDependency(ctx context.Context, agentID uuid.UUID, ticketID, dependsOnID int64) (*ticket.DependencyChange, error)
}

// Messages is the slice of agentmsg.Service the preamble of every tool uses.
type Messages interface {
	Deliver(ctx context.Context, agentID uuid.UUID) ([]agentmsg.Message, error)
}

// Sessions is the slice of session.Service the tools drive.
type Sessions interface {
	Touch(ctx context.Context, agentID uuid.UUID) error
	RecordProgress(ctx context.Context, agentID uuid.UUID) error
}

// Agents resolves the caller's name for checkin's briefing.
type Agents interface {
	Name(ctx context.Context, agentID uuid.UUID) (string, error)
}

// Roles tells checkin which roles a human has given the caller. Read only,
// on purpose: no tool changes an agent's roles, since a role gates which
// tickets it may claim.
type Roles interface {
	RoleNames(ctx context.Context, agentID uuid.UUID) ([]string, error)
}

// Workspace tells a freshly granted agent whether its workspace is fit for
// the ticket, against the tenant's workspace manifest. Optional: without
// one, nothing is said, which is also what a tenant with no manifest gets.
type Workspace interface {
	// ClaimNotice is "" unless the agent's last reported layout is wrong in
	// a repo the scopes need.
	ClaimNotice(ctx context.Context, agentID uuid.UUID, scopes []string) string
}

// Proposers says whether an agent may propose human actions: an enabled
// console, conductor or steward, read at call time.
type Proposers interface {
	MayPropose(ctx context.Context, agentID uuid.UUID) (bool, error)
}

// Declarer records which ticket a console's or conductor's usage goes to
// (llmusage.Service).
type Declarer interface {
	Declare(ctx context.Context, ticketID *int64, attempt *int) (*llmusage.Declaration, error)
}

// Proposals is the slice of pendingaction.Service the console tools use.
// There is deliberately no approve here: only a person's session approves.
type Proposals interface {
	Propose(ctx context.Context, params *pendingaction.ProposeParams) (*pendingaction.Action, error)
	List(ctx context.Context, filter *pendingaction.ListFilter) ([]pendingaction.Action, error)
	Withdraw(ctx context.Context, agentID, id uuid.UUID) (*pendingaction.Action, error)
}
