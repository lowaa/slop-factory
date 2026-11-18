package reports

import (
	"context"
	"time"
)

// What the independent-apps merge rule said about changes whose head was
// behind the base, recorded in merge_drift_decisions while the gate still
// refuses them all. The report is what the rule is judged by: how often it
// would have accepted, and whether the checks of the change that then took the
// base passed.
//
// A decision belongs to the period if it was decided in [from, to). Its CI
// outcome is whatever has been linked by now, so a would-accept decided in the
// period but not yet concluded is AwaitingCI, never counted as a pass.

// MaxShadowFailures bounds the list of would-accept decisions whose CI failed.
const MaxShadowFailures = 50

// MaxShadowReasons bounds the refusal reasons listed; a reason names a file,
// so there can be many distinct ones.
const MaxShadowReasons = 20

// ShadowReason is how many decisions gave a reason.
type ShadowReason struct {
	Decision, Reason string
	Count            int64
}

// ShadowFailure is a would-accept decision whose later checks failed.
type ShadowFailure struct {
	TicketID     int64
	Title        string
	ChangeID     int64
	HeadSHA      string
	CIHeadSHA    string
	DriftDirs    []string
	DecidedAt    time.Time
	CIRecordedAt time.Time
}

// ShadowDecisions is the report.
type ShadowDecisions struct {
	From, To                 time.Time
	WouldAccept, WouldRefuse int64
	// Of the would-accepts: checks passed, checks failed, no outcome yet.
	AcceptCIPassed, AcceptCIFailed, AcceptAwaitingCI int64
	Reasons                                          []ShadowReason
	// Failures are the most recent AcceptCIFailed ones, at most
	// MaxShadowFailures.
	Failures []ShadowFailure
}

// ShadowDecisions reports the last days days, ending now.
func (s *Service) ShadowDecisions(ctx context.Context, days int) (*ShadowDecisions, error) {
	f, err := s.usagePeriod(days)
	if err != nil {
		return nil, err
	}
	return s.store.ShadowDecisions(ctx, f.From, f.To)
}
