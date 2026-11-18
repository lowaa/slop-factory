package ticket

// reviewOutcomeCleared are the SET clauses that wipe a review's outcome.
// Every route into review applies them, so an approval is always of the
// change as it stands now, never of an earlier version of it.
var reviewOutcomeCleared = []string{
	"approved_at = NULL", "approved_by_user_id = NULL", "approved_by_user_id_deleted = false", "changes_requested_at = NULL",
}

// reviewStartedNow marks when a review cycle began, on every route into
// review: a review given on GitHub before it belongs to an earlier cycle.
// See inCurrentReview.
const reviewStartedNow = "review_started_at = now()"
