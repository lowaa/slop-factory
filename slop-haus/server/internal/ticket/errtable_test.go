package ticket

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/stretchr/testify/require"
	"testing"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/httperr"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/scope"
)

// A sentinel missing from the table would answer 500.
func TestErrTableCoversEverySentinel(t *testing.T) {
	t.Parallel()
	httperr.RequireCovered(t, &leaseTable, map[string]error{
		"ErrAlreadyHolding":         ErrAlreadyHolding,
		"ErrAlreadyPostReviewed":    ErrAlreadyPostReviewed,
		"ErrEnterMergeQueue":        ErrEnterMergeQueue,
		"ErrLeaveMergeQueue":        ErrLeaveMergeQueue,
		"ErrAmbiguousPR":            ErrAmbiguousPR,
		"ErrMergeUnverifiable":      ErrMergeUnverifiable,
		"ErrNotMergedIntoBase":      ErrNotMergedIntoBase,
		"ErrNotTenantOwner":         ErrNotTenantOwner,
		"ErrTicketFinished":         ErrTicketFinished,
		"ErrAttemptAmbiguous":       ErrAttemptAmbiguous,
		"ErrAttemptAwaitsChoice":    ErrAttemptAwaitsChoice,
		"ErrAttemptCap":             ErrAttemptCap,
		"ErrAttemptClosed":          ErrAttemptClosed,
		"ErrAttemptDone":            ErrAttemptDone,
		"ErrAttemptNotLive":         ErrAttemptNotLive,
		"ErrAttemptNotPromoted":     ErrAttemptNotPromoted,
		"ErrAttemptRequired":        ErrAttemptRequired,
		"ErrBatchTooLarge":          ErrBatchTooLarge,
		"ErrReplayBatchTooLarge":    ErrReplayBatchTooLarge,
		"ErrReplayBatchUnconfirmed": ErrReplayBatchUnconfirmed,
		"ErrReplayBatchShape":       ErrReplayBatchShape,
		"ErrBaseRefInvalid":         ErrBaseRefInvalid,
		"ErrBaseRefLocked":          ErrBaseRefLocked,
		"ErrBaseRefUnverifiable":    ErrBaseRefUnverifiable,
		"ErrBlockerNotRemovable":    ErrBlockerNotRemovable,
		"ErrBlockersNotEditable":    ErrBlockersNotEditable,
		"ErrClosePromoted":          ErrClosePromoted,
		"ErrConsoleUnnamedClaim":    ErrConsoleUnnamedClaim,
		"ErrDependencyCycle":        ErrDependencyCycle,
		"ErrEmptyBatch":             ErrEmptyBatch,
		"ErrEmptyComment":           ErrEmptyComment,
		"ErrEmptyEdit":              ErrEmptyEdit,
		"ErrEmptyHandoff":           ErrEmptyHandoff,
		"ErrEmptyNote":              ErrEmptyNote,
		"ErrEmptyQuestion":          ErrEmptyQuestion,
		"ErrEmptyReason":            ErrEmptyReason,
		"ErrEmptyTitle":             ErrEmptyTitle,
		"ErrInvalidAuthor":          ErrInvalidAuthor,
		"ErrInvalidEnum":            ErrInvalidEnum,
		"ErrInvalidHarness":         ErrInvalidHarness,
		"ErrInvalidPRURL":           ErrInvalidPRURL,
		"ErrInvalidPage":            ErrInvalidPage,
		"ErrInvalidRetryAfter":      ErrInvalidRetryAfter,
		"ErrInvalidSearch":          ErrInvalidSearch,
		"ErrInvalidSessionID":       ErrInvalidSessionID,
		"ErrInvalidVariant":         ErrInvalidVariant,
		"ErrLeaseLost":              ErrLeaseLost,
		"ErrLeaseRevoked":           ErrLeaseRevoked,
		"ErrLeaveParked":            ErrLeaveParked,
		"ErrLeavePreparing":         ErrLeavePreparing,
		"ErrLeaveReview":            ErrLeaveReview,
		"ErrLockHeld":               ErrLockHeld,
		"ErrMissingRole":            ErrMissingRole,
		"ErrMultipleCreators":       ErrMultipleCreators,
		"ErrNarrowAdds":             ErrNarrowAdds,
		"ErrNarrowEmpty":            ErrNarrowEmpty,
		"ErrNarrowUnknownPath":      ErrNarrowUnknownPath,
		"ErrNarrowWithSiblings":     ErrNarrowWithSiblings,
		"ErrNeedsDiscussion":        ErrNeedsDiscussion,
		"ErrNoAttemptForAgent":      ErrNoAttemptForAgent,
		"ErrNoAttemptsOnStatus":     ErrNoAttemptsOnStatus,
		"ErrReplayNotDone":          ErrReplayNotDone,
		"ErrReplayNotPromotable":    ErrReplayNotPromotable,
		"ErrNoBase":                 ErrNoBase,
		"ErrNoSnapshot":             ErrNoSnapshot,
		"ErrSnapshotExists":         ErrSnapshotExists,
		"ErrNotParked":              ErrNotParked,
		"ErrNoLease":                ErrNoLease,
		"ErrNoLiveAttempt":          ErrNoLiveAttempt,
		"ErrNoPromotedAttempt":      ErrNoPromotedAttempt,
		"ErrNoSuchAttempt":          ErrNoSuchAttempt,
		"ErrNoSuchLock":             ErrNoSuchLock,
		"ErrNoTicketForPR":          ErrNoTicketForPR,
		"ErrNotApproved":            ErrNotApproved,
		"ErrLandNotPromoted":        ErrLandNotPromoted,
		"ErrNotDone":                ErrNotDone,
		"ErrNotFound":               ErrNotFound,
		"ErrNotHolder":              ErrNotHolder,
		"ErrNotInReview":            ErrNotInReview,
		"ErrNotMergeable":           ErrNotMergeable,
		"ErrNotParkable":            ErrNotParkable,
		"ErrNotPreparing":           ErrNotPreparing,
		"ErrNotQueued":              ErrNotQueued,
		"ErrNotServerAgent":         ErrNotServerAgent,
		"ErrOwnerBoundRefused":      ErrOwnerBoundRefused,
		"ErrPRURLRequired":          ErrPRURLRequired,
		"ErrPRWrongRepo":            ErrPRWrongRepo,
		"ErrSetupIncomplete":        ErrSetupIncomplete,
		"ErrProcessingDisabled":     ErrProcessingDisabled,
		"ErrPromoteMerging":         ErrPromoteMerging,
		"ErrPromoteTicketFinished":  ErrPromoteTicketFinished,
		"ErrPromotionBlocked":       ErrPromotionBlocked,
		"ErrPromotionUnverified":    ErrPromotionUnverified,
		"ErrReviewNeedsReason":      ErrReviewNeedsReason,
		"ErrScopeConflict":          ErrScopeConflict,
		"ErrScopesSpanRepos":        ErrScopesSpanRepos,
		"ErrServerAgentDisabled":    ErrServerAgentDisabled,
		"ErrServerAgentTaken":       ErrServerAgentTaken,
		"ErrServerClaimHeld":        ErrServerClaimHeld,
		"ErrServerClaimNotApproved": ErrServerClaimNotApproved,
		"ErrServerClaimNotPromoted": ErrServerClaimNotPromoted,
		"ErrServerClaimNotQueued":   ErrServerClaimNotQueued,
		"ErrSessionHarnessMismatch": ErrSessionHarnessMismatch,
		"ErrStaleReview":            ErrStaleReview,
		"ErrStaleRevision":          ErrStaleRevision,
		"ErrUnknownLock":            ErrUnknownLock,
		"ErrUnknownRole":            ErrUnknownRole,
		"ErrVariantClaim":           ErrVariantClaim,
		"ErrVariantLocked":          ErrVariantLocked,
		"ErrVariantNotFound":        ErrVariantNotFound,
		"ErrWorkerNamedAttempt":     ErrWorkerNamedAttempt,
	}, map[string]string{
		"ErrNoTicketForPR":          "consumed by the GitHub webhook and the merge path, not answered by a ticket route",
		"ErrAmbiguousPR":            "consumed by the GitHub webhook and the merge path, not answered by a ticket route",
		"ErrStaleReview":            "consumed by the GitHub webhook, not answered by a ticket route",
		"ErrNotMergeable":           "consumed by the GitHub webhook and the merge path, not answered by a ticket route",
		"ErrAttemptNotPromoted":     "consumed by the GitHub webhook, not answered by a ticket route",
		"ErrServerAgentTaken":       "consumed by the server-merge runner",
		"ErrNotServerAgent":         "consumed by the server-merge runner",
		"ErrServerAgentDisabled":    "consumed by the server-merge runner",
		"ErrServerClaimNotPromoted": "consumed by the server-merge runner",
		"ErrServerClaimNotApproved": "consumed by the server-merge runner",
		"ErrServerClaimNotQueued":   "consumed by the server-merge runner",
		"ErrServerClaimHeld":        "consumed by the server-merge runner",
		"ErrServerClaimLaneHeld":    "consumed by the landing runner",
		"ErrOwnerBoundRefused":      "wrapped by the owner-bound resolver and handled inside the service",
	})
}

// The statuses are spelled out here rather than read from the table, so a
// rule moved to the wrong kind fails.
var mainStatuses = map[int][]error{
	403: {authn.ErrNotPerson, ErrBlockersNotEditable, ErrBlockerNotRemovable},
	404: {ErrNotFound, ErrVariantNotFound, ErrNoSuchAttempt, ErrNoSnapshot},
	409: {ErrNotPreparing, ErrLeavePreparing, ErrStaleRevision, ErrNotInReview, ErrLeaveReview, ErrNotDone, ErrAlreadyPostReviewed, ErrNotParkable, ErrLeaveParked, ErrScopeConflict, ErrAttemptCap, ErrNoAttemptsOnStatus, ErrReplayNotDone, ErrReplayNotPromotable, ErrNoBase, ErrSnapshotExists, ErrNotParked, ErrVariantLocked, ErrAttemptRequired, ErrAttemptAmbiguous, ErrAttemptNotLive, ErrAttemptClosed, ErrAttemptDone, ErrNarrowWithSiblings, ErrPromoteTicketFinished, ErrPromoteMerging, ErrPromotionBlocked, ErrNoPromotedAttempt, ErrNoLiveAttempt},
	422: {ErrEmptyComment, ErrEmptyTitle, scope.ErrInvalidPath, ErrDependencyCycle, ErrInvalidEnum, ErrEmptyQuestion, ErrEmptyHandoff, ErrReviewNeedsReason, ErrInvalidAuthor, ErrMultipleCreators, ErrEmptyReason, ErrEmptyNote, ErrSessionHarnessMismatch, ErrInvalidHarness, ErrInvalidSessionID, ErrEmptyEdit, ErrInvalidPRURL, ErrPRURLRequired, ErrUnknownRole, ErrInvalidSearch, ErrInvalidPage, ErrEmptyBatch, ErrBatchTooLarge, ErrReplayBatchTooLarge, ErrReplayBatchUnconfirmed, ErrReplayBatchShape, ErrScopesSpanRepos, ErrPRWrongRepo, ErrNarrowUnknownPath, ErrNarrowAdds, ErrNarrowEmpty, ErrInvalidVariant, ErrClosePromoted, ErrInvalidRetryAfter, ErrWorkerNamedAttempt},
	503: {ErrPromotionUnverified},
}

var leaseStatuses = map[int][]error{
	403: {ErrMissingRole},
	404: {ErrNoSuchLock, ErrNotQueued, ErrUnknownLock},
	409: {ErrNoLease, ErrNotHolder, ErrAlreadyHolding, ErrLockHeld, ErrConsoleUnnamedClaim, ErrProcessingDisabled, ErrSetupIncomplete, ErrVariantClaim, ErrNotApproved, ErrLandNotPromoted, ErrNeedsDiscussion, ErrLeaseRevoked, ErrLeaseLost, ErrNoAttemptForAgent, ErrAttemptAwaitsChoice},
}

func TestErrTableAnswersTheDocumentedStatus(t *testing.T) {
	t.Parallel()
	h := &Handler{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	check := func(t *testing.T, mapFn func(error) error, golden map[int][]error) {
		for status, errs := range golden {
			for _, e := range errs {
				wrapped := fmt.Errorf("context: %w", e)
				got := mapFn(wrapped)
				var se huma.StatusError
				require.ErrorAs(t, got, &se, e.Error())
				require.Equal(t, status, se.GetStatus(), e.Error())
				require.Contains(t, got.Error(), wrapped.Error())
			}
		}
	}
	t.Run("main", func(t *testing.T) { check(t, h.mapError, mainStatuses) })
	t.Run("lease", func(t *testing.T) {
		check(t, h.mapLeaseError, mainStatuses)
		check(t, h.mapLeaseError, leaseStatuses)
	})
	t.Run("unexpected", func(t *testing.T) {
		got := h.mapError(errors.New("secret internals"))
		var se huma.StatusError
		require.ErrorAs(t, got, &se)
		require.Equal(t, http.StatusInternalServerError, se.GetStatus())
		require.NotContains(t, got.Error(), "secret")
	})
}
