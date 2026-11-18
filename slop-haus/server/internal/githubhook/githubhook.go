// Package githubhook is slop-haus's fourth transport, after the board's
// REST, MCP and Claude Code's hooks: GitHub webhooks, so a person's review
// or merge on a ticket's PR moves the ticket as the same click on the board
// would. It stays a thin caller of ticket.Service -- which ticket a PR
// means, and whether a review still counts, are the ticket package's rules.
//
// It is also the one door open to the internet (through Tailscale Funnel),
// so it is built to be cheap to knock on: the receiver listens on a port of
// its own, serves nothing else, and checks a delivery's signature before
// anything touches the database.
//
// Deliveries come from each tenant's GitHub App, at POST /hooks/github-app.
// A delivery names the installation it is about, which finds the tenant and
// the webhook secret GitHub signed it with (see ReceiveApp). Several tenants
// may install the App on the same GitHub repo; each acts only on its own
// tickets, and nothing coordinates their fleets with each other.
package githubhook

import (
	"errors"
	"time"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/hookkit"
)

// Outcome is what became of a delivery; see hookkit.
type Outcome = hookkit.Outcome

const (
	OutcomeProcessing = hookkit.OutcomeProcessing
	OutcomeApplied    = hookkit.OutcomeApplied
	OutcomeIgnored    = hookkit.OutcomeIgnored
	OutcomeError      = hookkit.OutcomeError
)

// Delivery is one delivery the GitHub App's webhook sent.
type Delivery struct {
	ID             string
	InstallationID int64
	Event          string
	Action         string
	Outcome        Outcome
	Detail         string
	TicketID       *int64
	// AttemptNumber is the attempt of TicketID the delivery's PR was matched
	// to; set with it, nil without.
	AttemptNumber *int
	ReceivedAt    time.Time
}

var (
	// ErrDisabled is the webhook door shut: WEBHOOK_PUBLIC_BASE_URL is not set.
	ErrDisabled = errors.New("githubhook: GitHub webhooks are not configured on this server")
	// ErrUnauthorized is a delivery with no valid signature for a known
	// installation. One error for every way it can fail: telling them apart
	// would tell a stranger which installations are real.
	ErrUnauthorized = errors.New("githubhook: bad signature")
	// ErrBadDelivery is a signed delivery that is not a well-formed GitHub
	// delivery.
	ErrBadDelivery = errors.New("githubhook: malformed delivery")
)
