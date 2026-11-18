package ticket

import (
	"strings"

	"github.com/google/uuid"
)

// The cause of a status change is stored as a free string (see
// ticket_status_changes), written by the code that moves the status. A
// proposal's approval writes "proposal:<id>", or that with " auto_approved"
// when the approver's standing choice approved it, or " steward_approved"
// when the steward did under the owner's settings (pendingaction.CauseFor,
// AutoCauseFor and StewardCauseFor); both read as automatic. It is parsed here so no client splits strings.
const (
	proposalCausePrefix = "proposal:"
	autoApprovedSuffix  = " auto_approved"
	stewardSuffix       = " steward_approved"
)

// StatusCauseKind says which fields of a StatusCause are set.
type StatusCauseKind string

const (
	CauseProposal StatusCauseKind = "proposal"
	CauseOther    StatusCauseKind = "other"
)

// StatusCause is why a ticket's status changed: a proposal's approval, or
// whatever else the recorded text says ("claimed", "lease_expired", a lease's
// release reason...).
type StatusCause struct {
	Kind         StatusCauseKind
	ProposalID   *uuid.UUID
	AutoApproved bool
	// Text is the recorded cause, set for CauseOther.
	Text string
}

// ParseStatusCause turns a recorded cause into a StatusCause. It is nil for
// no cause. A "proposal:" string that is not exactly a proposal id, with
// the one known suffix, is not claimed as a proposal: it reads as other,
// with its text, so a malformed or future cause is shown rather than
// mislinked.
func ParseStatusCause(raw string) *StatusCause {
	if raw == "" {
		return nil
	}
	if rest, ok := strings.CutPrefix(raw, proposalCausePrefix); ok {
		rest, auto := strings.CutSuffix(rest, autoApprovedSuffix)
		if !auto {
			rest, auto = strings.CutSuffix(rest, stewardSuffix)
		}
		if id, err := uuid.Parse(rest); err == nil && id.String() == rest {
			return &StatusCause{Kind: CauseProposal, ProposalID: &id, AutoApproved: auto}
		}
	}
	return &StatusCause{Kind: CauseOther, Text: raw}
}
