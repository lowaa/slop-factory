package variantcompare

import (
	"context"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// VariantRef is the variant version an attempt is routed to.
type VariantRef struct {
	Name    string
	Version int
}

// RunRecord is what one lease of an attempt ran with; "" is not recorded.
type RunRecord struct {
	HarnessVersion    string
	Model             string
	BundleHash        string
	FleetInstructions string
}

// AttemptRow is a finished attempt as the tables keep it, beside what the
// other ports add.
type AttemptRow struct {
	Ref ticket.AttemptRef
	// Variant is nil for an attempt routed to no variant.
	Variant  *VariantRef
	Promoted bool

	ChangesRequested bool
	// HumanComments and HumanWords are what people wrote in the attempt's
	// conversation. Remote Control is general overhead by decision and is not
	// in them.
	HumanComments    int64
	HumanWords       int64
	HumanWaitSeconds int64

	Runs []RunRecord
}

// Source reads the attempts of a set of tickets. It is read-only.
type Source interface {
	// BatchTickets are the tickets a replay batch added attempts to.
	BatchTickets(ctx context.Context, batch uuid.UUID) ([]int64, error)
	// FinishedAttempts are the tickets' attempts that finished their own work
	// (waiting on review, or over), and the count of those that did not.
	FinishedAttempts(ctx context.Context, ticketIDs []int64) (finished []AttemptRow, unfinished int, err error)
	// GuidanceCosts is the guidance charged to each attempt by declaration.
	GuidanceCosts(ctx context.Context, refs []ticket.AttemptRef) (map[ticket.AttemptRef][]ticket.CostFigure, error)
}

// SignalsReader reads the stored signals of attempts without refreshing
// them: a comparison must not call the code host for a hundred attempts.
type SignalsReader interface {
	Get(ctx context.Context, refs []ticket.AttemptRef) (map[ticket.AttemptRef]*ticket.AttemptSignalsBody, error)
}
