// Package intake is the door external systems -- Alertmanager, a support
// desk, anything with an event an agent should look into -- open, update and
// resolve tickets through: POST /hooks/intake/{source_id}. It is a thin
// caller of ticket.Service, as githubhook is, and shares that door's
// plumbing (hookkit).
//
// What it takes is text from outside, and the ticket queue is read by agents
// as instructions, so it is built around that:
//
//   - A source is registered by a person, with a credential that is shown
//     once. Its config -- trust, status and priority limits, roles, paths --
//     is the only thing that decides what its tickets look like; a payload
//     supplies content, a capped priority and a guarded status, nothing more.
//   - An untrusted source's tickets are always created preparing, so a person
//     reads them before any agent can claim one (Source.Status).
//   - What a sender says is stored apart from the ticket's own words and is
//     only ever shown as external content, fenced and labelled (see
//     ticket.RenderExternal).
//   - It listens on an address of its own, serving nothing else, and
//     authenticates before it parses anything or touches a tenant's data.
package intake

import (
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/hookkit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

type Trust string

const (
	TrustTrusted   Trust = "trusted"
	TrustUntrusted Trust = "untrusted"
)

type AuthKind string

const (
	AuthBearer AuthKind = "bearer"
	AuthHMAC   AuthKind = "hmac"
)

type ReopenPolicy string

const (
	// ReopenNew files another ticket when an open arrives for a key whose
	// ticket is done.
	ReopenNew ReopenPolicy = "new"
	// ReopenExisting returns the done ticket to the queue instead.
	ReopenExisting ReopenPolicy = "reopen"
)

// Source is one registered sender.
type Source struct {
	ID       uuid.UUID
	Name     string
	Adapter  string
	Trust    Trust
	AuthKind AuthKind
	// DefaultStatus is what a ticket is created as when the payload asks
	// for nothing; Status is the one place trust limits it.
	DefaultStatus   ticket.Status
	DefaultPriority model.Priority
	MaxPriority     model.Priority
	// RoleNames and RoleMatch are what the source's tickets require of the
	// agent that claims them, and Paths their scope. Only a person sets
	// them, here; a payload never does.
	RoleNames    []string
	RoleMatch    model.RoleMatch
	Paths        []string
	ReopenPolicy ReopenPolicy
	// ResolveCloses lets a resolve cancel the key's ticket while it is
	// unclaimed (see Plan). Only a trusted source may have it.
	ResolveCloses bool
	CreatedBy     *uuid.UUID
	// CreatedByDeleted says CreatedBy is nil because that person's account
	// was deleted.
	CreatedByDeleted bool
	CreatedAt        time.Time
	RevokedAt        *time.Time
	LastDeliveryAt   *time.Time
}

// Outcome is what became of a delivery; see hookkit.
type Outcome = hookkit.Outcome

const (
	OutcomeProcessing = hookkit.OutcomeProcessing
	OutcomeApplied    = hookkit.OutcomeApplied
	OutcomeIgnored    = hookkit.OutcomeIgnored
	OutcomeError      = hookkit.OutcomeError
	// OutcomeRejected is a delivery from a source that authenticated but
	// was not a well-formed event.
	OutcomeRejected Outcome = "rejected"
	// OutcomeRateLimited is one turned away for exceeding its source's rate.
	OutcomeRateLimited Outcome = "rate_limited"
)

// Delivery is one authenticated delivery and what came of it.
type Delivery struct {
	ID       string
	SourceID uuid.UUID
	Event    string
	Key      string
	Outcome  Outcome
	// Downgraded is the sender asking for open and a trust rule making it
	// preparing; Detail says so too.
	Downgraded bool
	Detail     string
	TicketID   *int64
	ReceivedAt time.Time
}

var (
	// ErrDisabled is the door shut: the server has no intake listener.
	ErrDisabled = errors.New("intake: the intake door is not configured on this server")
	// ErrHMACDisabled is an HMAC source asked for on a server with no
	// INTAKE_KEY to derive its secret from.
	ErrHMACDisabled = errors.New("intake: HMAC sources need INTAKE_KEY, which this server does not have")
	// ErrNotFound is a source that is not this tenant's, or is revoked.
	ErrNotFound = errors.New("intake: no such source")
	// ErrUnauthorized is a delivery with no valid credential, or for a
	// source that does not exist or is revoked. One error for all of them:
	// telling them apart would tell a stranger which ids are real.
	ErrUnauthorized = errors.New("intake: unknown source or bad credential")
	// ErrRateLimited is a source over its rate.
	ErrRateLimited = errors.New("intake: rate limit exceeded")
	// ErrInvalidSource is a source spec that is not acceptable; its message
	// says what to fix.
	ErrInvalidSource = errors.New("intake: invalid source")
	// ErrBadDelivery is an authenticated delivery that is not a well-formed
	// event; the message says why, for the sender.
	// ErrNotOwner is a person who is not the tenant's owner asking for what
	// only an owner may set.
	ErrNotOwner    = errors.New("intake: only the team's owner may let a source close tickets")
	ErrBadDelivery = errors.New("intake: malformed delivery")
)
