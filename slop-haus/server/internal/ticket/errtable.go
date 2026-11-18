package ticket

import (
	"errors"
	"slices"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/httperr"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/scope"
)

// mainRules answer a Service error with the status it carries, and with the
// sentinel's own text as the detail: a sentinel's text is vetted, an
// unrecognised error's is not, so the table's default never echoes it.
var mainRules = []httperr.Rule{
	{Err: ErrNotFound, Kind: httperr.NotFound},
	{Err: ErrVariantNotFound, Kind: httperr.NotFound},
	{Err: ErrNoSuchAttempt, Kind: httperr.NotFound},
	{Err: ErrNoSnapshot, Kind: httperr.NotFound},
	{Err: ErrEmptyComment, Kind: httperr.Invalid},
	{Err: ErrEmptyTitle, Kind: httperr.Invalid},
	{Err: scope.ErrInvalidPath, Kind: httperr.Invalid},
	{Err: ErrDependencyCycle, Kind: httperr.Invalid},
	{Err: ErrInvalidEnum, Kind: httperr.Invalid},
	{Err: ErrEmptyQuestion, Kind: httperr.Invalid},
	{Err: ErrEmptyHandoff, Kind: httperr.Invalid},
	{Err: ErrReviewNeedsReason, Kind: httperr.Invalid},
	{Err: ErrInvalidAuthor, Kind: httperr.Invalid},
	{Err: ErrMultipleCreators, Kind: httperr.Invalid},
	{Err: ErrEmptyReason, Kind: httperr.Invalid},
	{Err: ErrEmptyNote, Kind: httperr.Invalid},
	{Err: ErrSessionHarnessMismatch, Kind: httperr.Invalid},
	{Err: ErrInvalidHarness, Kind: httperr.Invalid},
	{Err: ErrInvalidSessionID, Kind: httperr.Invalid},
	{Err: ErrEmptyEdit, Kind: httperr.Invalid},
	{Err: ErrInvalidPRURL, Kind: httperr.Invalid},
	{Err: ErrPRURLRequired, Kind: httperr.Invalid},
	{Err: ErrUnknownRole, Kind: httperr.Invalid},
	{Err: ErrInvalidSearch, Kind: httperr.Invalid},
	{Err: ErrInvalidPage, Kind: httperr.Invalid},
	{Err: ErrEmptyBatch, Kind: httperr.Invalid},
	{Err: ErrBatchTooLarge, Kind: httperr.Invalid},
	{Err: ErrReplayBatchTooLarge, Kind: httperr.Invalid},
	{Err: ErrReplayBatchUnconfirmed, Kind: httperr.Invalid},
	{Err: ErrReplayBatchShape, Kind: httperr.Invalid},
	{Err: ErrScopesSpanRepos, Kind: httperr.Invalid},
	{Err: ErrPRWrongRepo, Kind: httperr.Invalid},
	{Err: ErrBaseRefInvalid, Kind: httperr.Invalid},
	{Err: ErrBaseRefLocked, Kind: httperr.Conflict},
	{Err: ErrBaseRefUnverifiable, Kind: httperr.Unavailable},
	{Err: ErrNarrowUnknownPath, Kind: httperr.Invalid},
	{Err: ErrNarrowAdds, Kind: httperr.Invalid},
	{Err: ErrNarrowEmpty, Kind: httperr.Invalid},
	{Err: ErrInvalidVariant, Kind: httperr.Invalid},
	{Err: ErrClosePromoted, Kind: httperr.Invalid},
	{Err: ErrInvalidRetryAfter, Kind: httperr.Invalid},
	{Err: ErrWorkerNamedAttempt, Kind: httperr.Invalid},
	{Err: ErrNotPreparing, Kind: httperr.Conflict},
	{Err: ErrLeavePreparing, Kind: httperr.Conflict},
	{Err: ErrStaleRevision, Kind: httperr.Conflict},
	{Err: ErrNotInReview, Kind: httperr.Conflict},
	{Err: ErrLeaveReview, Kind: httperr.Conflict},
	{Err: ErrEnterMergeQueue, Kind: httperr.Conflict},
	{Err: ErrLeaveMergeQueue, Kind: httperr.Conflict},
	{Err: ErrNotDone, Kind: httperr.Conflict},
	{Err: ErrAlreadyPostReviewed, Kind: httperr.Conflict},
	{Err: ErrNotParkable, Kind: httperr.Conflict},
	{Err: ErrLeaveParked, Kind: httperr.Conflict},
	{Err: ErrScopeConflict, Kind: httperr.Conflict},
	{Err: ErrAttemptCap, Kind: httperr.Conflict},
	{Err: ErrNoAttemptsOnStatus, Kind: httperr.Conflict},
	{Err: ErrNoBase, Kind: httperr.Conflict},
	{Err: ErrReplayNotDone, Kind: httperr.Conflict},
	{Err: ErrReplayNotPromotable, Kind: httperr.Conflict},
	{Err: ErrSnapshotExists, Kind: httperr.Conflict},
	{Err: ErrNotParked, Kind: httperr.Conflict},
	{Err: ErrVariantLocked, Kind: httperr.Conflict},
	{Err: ErrAttemptRequired, Kind: httperr.Conflict},
	{Err: ErrAttemptAmbiguous, Kind: httperr.Conflict},
	{Err: ErrAttemptNotLive, Kind: httperr.Conflict},
	{Err: ErrAttemptClosed, Kind: httperr.Conflict},
	{Err: ErrAttemptDone, Kind: httperr.Conflict},
	{Err: ErrNarrowWithSiblings, Kind: httperr.Conflict},
	{Err: ErrPromoteTicketFinished, Kind: httperr.Conflict},
	{Err: ErrPromoteMerging, Kind: httperr.Conflict},
	{Err: ErrPromotionBlocked, Kind: httperr.Conflict},
	{Err: ErrNoPromotedAttempt, Kind: httperr.Conflict},
	{Err: ErrNoLiveAttempt, Kind: httperr.Conflict},
	{Err: ErrPromotionUnverified, Kind: httperr.Unavailable},
	{Err: authn.ErrNotPerson, Kind: httperr.Forbidden},
	{Err: ErrNotTenantOwner, Kind: httperr.Forbidden},
	{Err: ErrNotMergedIntoBase, Kind: httperr.Conflict},
	{Err: ErrTicketFinished, Kind: httperr.Conflict},
	{Err: ErrMergeUnverifiable, Kind: httperr.Unavailable},
	{Err: ErrBlockersNotEditable, Kind: httperr.Forbidden},
	{Err: ErrBlockerNotRemovable, Kind: httperr.Forbidden},
}

// leaseRules are the outcomes the claim path can produce. They come before
// mainRules in leaseTable so the ticket and comment routes' table need not
// know leases exist.
var leaseRules = []httperr.Rule{
	{Err: ErrNoLease, Kind: httperr.Conflict},
	{Err: ErrNotHolder, Kind: httperr.Conflict},
	{Err: ErrAlreadyHolding, Kind: httperr.Conflict},
	{Err: ErrLockHeld, Kind: httperr.Conflict},
	{Err: ErrConsoleUnnamedClaim, Kind: httperr.Conflict},
	{Err: ErrProcessingDisabled, Kind: httperr.Conflict},
	{Err: ErrSetupIncomplete, Kind: httperr.Conflict},
	{Err: ErrVariantClaim, Kind: httperr.Conflict},
	{Err: ErrNotApproved, Kind: httperr.Conflict},
	{Err: ErrLandNotPromoted, Kind: httperr.Conflict},
	{Err: ErrNeedsDiscussion, Kind: httperr.Conflict},
	{Err: ErrLeaseRevoked, Kind: httperr.Conflict},
	{Err: ErrLeaseLost, Kind: httperr.Conflict},
	{Err: ErrNoAttemptForAgent, Kind: httperr.Conflict},
	{Err: ErrAttemptAwaitsChoice, Kind: httperr.Conflict},
	{Err: ErrNoSuchLock, Kind: httperr.NotFound},
	{Err: ErrNotQueued, Kind: httperr.NotFound},
	{Err: ErrUnknownLock, Kind: httperr.NotFound},
	{Err: ErrMissingRole, Kind: httperr.Forbidden},
}

const unexpectedError = "unexpected ticket error"

var errTable = httperr.Table{Rules: mainRules, Unexpected: unexpectedError}

var leaseTable = httperr.Table{
	Rules:      slices.Concat(leaseRules, mainRules),
	Unexpected: unexpectedError,
}

// bulkTable is errTable for a bulk route: a refused batch keeps every refusal
// in its body.
var bulkTable = httperr.Table{
	Rules: mainRules,
	Custom: func(err error) error {
		var refused *BatchRefusedError
		if errors.As(err, &refused) {
			return bulkRefusedBody(refused)
		}
		return nil
	},
	Unexpected: unexpectedError,
}

func (h *Handler) mapError(err error) error {
	return httperr.Map(h.logger, err, &errTable)
}

func (h *Handler) mapLeaseError(err error) error {
	return httperr.Map(h.logger, err, &leaseTable)
}

func (h *Handler) mapBulkError(err error) error {
	return httperr.Map(h.logger, err, &bulkTable)
}
