package landing

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/change"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/landing/guidance"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// failure is how a landing ended short of a merge.
type failure struct {
	outcome  Outcome
	guidance string
	// detail is stored with the outcome; it never holds a host's raw error,
	// which could carry anything the host echoes back.
	detail string
	// keepsPlace is for a handback that was the server's own fault: the next
	// finish call keeps the ticket's place in the queue.
	keepsPlace bool
	// mergedNotClosed is a change that is on the base whose ticket could not be
	// closed: not a failure to land, and never handed back.
	mergedNotClosed bool
	// noPipeline is a never-started head the CI has no pipeline for: its
	// pull_request event went missing, which a new commit on the branch clears.
	noPipeline bool
	// collision is what the handback collided with, when it is one.
	collision *guidance.Collision
}

// withCollision takes a collision's account into the failure: its guidance in
// place of the generic text, and its cause and predecessors in the detail so
// that repeated collisions can be counted.
func (f *failure) withCollision(c *guidance.Collision) *failure {
	f.collision = c
	if c.Guidance != "" {
		f.guidance = c.Guidance
	}
	if c.Counts {
		f.detail = c.Detail() + ": " + f.detail
	}
	return f
}

func (f *failure) Error() string { return string(f.outcome) + ": " + f.detail }

const resolveTail = " Merge the change's base branch (origin/<base>) into your branch, resolve any conflicts, run the tests, push, and call ticket_finish again."

// switchedOff is the handback of every ticket still queued when the tenant's
// merge_method goes back to worker (or the kill switch is turned off): they land
// in the old flow, where the worker takes the integration lock itself.
func switchedOff() *failure {
	return &failure{outcome: MergeRefused, detail: "merge queue switched off",
		guidance: "The server's merge queue was switched off before it landed your change, so nothing is wrong with the change. " +
			"Land it yourself the old way: merge origin/<base> into your branch if it has moved, push, wait for green checks, " +
			"take integration_lock, then change_merge."}
}

func mergeMainConflict(err error) *failure {
	return &failure{outcome: MergeMainConflict, detail: "merging the base into the head conflicted",
		guidance: "The server tried to merge the base branch into your change and it conflicts (" + singleLine(err.Error()) + ")." + resolveTail}
}

func mainMoved(err error) *failure {
	return &failure{outcome: MainMoved, detail: "head does not contain the base's latest commit",
		guidance: "The server merged the base branch into your change and it has moved again: " + singleLine(err.Error()) + "." + resolveTail}
}

// updatePending is a branch update the host accepted and has not applied. A
// batch does not wait for it as a serial landing does: the member keeps its
// place and the update, once applied, is what the next try finds.
func updatePending() *failure {
	return &failure{outcome: CodeHostError, detail: "the base's merge into the head is still pending", keepsPlace: true,
		guidance: "The server asked the code host to merge the base branch into your change and the host had not applied it yet. Nothing is wrong with your change: call ticket_finish again."}
}

func redChecks(head string, names []string, retried bool) *failure {
	note := "The server did not retry them."
	if retried {
		note = "The server retried them once and they failed again."
	}
	return &failure{outcome: ChecksRed, detail: "checks failed on " + head,
		guidance: fmt.Sprintf("The server's checks failed on %s: %s. %s Fix them, push, and call ticket_finish again.", head, strings.Join(names, ", "), note)}
}

func pendingTimeout(head, why string) *failure {
	return &failure{outcome: ChecksPendingTimeout, detail: why, keepsPlace: true,
		guidance: fmt.Sprintf("The server waited for the checks on %s and they did not finish (%s). Look at CI, not at the code, then call ticket_finish again.", head, why)}
}

// neverStartedCause is what the CI says about a head that has no checks.
type neverStartedCause int

const (
	// ciNoPipeline: the CI answered and has no pipeline for the commit, so its
	// push or pull_request event never made one.
	ciNoPipeline neverStartedCause = iota
	// ciPipelineExists: a pipeline exists but reported no check.
	ciPipelineExists
	// ciUnreadable: the CI could not be asked.
	ciUnreadable
)

func neverStarted(head string, after time.Duration, cause neverStartedCause) *failure {
	var look string
	switch cause {
	case ciNoPipeline:
		look = "Woodpecker has no pipeline for this commit: its pull_request event never arrived or was dropped, so look at the webhook delivery, not at a stuck pipeline"
	case ciPipelineExists:
		look = "a pipeline exists for this commit but has reported no check: look for one stuck in created"
	default:
		look = "the server could not ask Woodpecker whether it has a pipeline for this commit: look at CI (a missing webhook, a pipeline stuck in created)"
	}
	return &failure{outcome: ChecksNeverStarted, detail: "CI never started on " + head, keepsPlace: true, noPipeline: cause == ciNoPipeline,
		guidance: fmt.Sprintf("The server waited and CI never started on %s: no check was reported for it in %s. Nothing is wrong with the change: %s, then call ticket_finish again.", head, after, look)}
}

func mergedNotClosed(detail string) *failure {
	return &failure{outcome: CodeHostError, detail: "merged, but " + detail, mergedNotClosed: true}
}

func hostError(what string) *failure {
	return &failure{outcome: CodeHostError, detail: what, keepsPlace: true,
		guidance: "The server could not land your change because the code host or the store failed (" + what + "). Nothing is wrong with your change: call ticket_finish again."}
}

func lockTimeout(d string) *failure {
	return &failure{outcome: LockTimeout, detail: "integration lock busy at timeout", keepsPlace: true,
		guidance: "The server could not land your change: the integration lock stayed taken for " + d + ". Nothing is wrong with it: call ticket_finish again."}
}

// known maps what the gate or the host said to a failure, nil for anything it
// does not know, which is a host error: reported without the error's text, and
// the only kind that is tried again.
func known(err error) *failure {
	switch {
	case errors.Is(err, codehost.ErrMergeConflict):
		return mergeMainConflict(err)
	case errors.Is(err, change.ErrBehindBase):
		return mainMoved(err)
	case errors.Is(err, change.ErrChecksNotPassed):
		return &failure{outcome: ChecksRed, detail: "checks not passed", guidance: "The merge gate says the checks have not passed: " + singleLine(err.Error()) + ". Fix that and call ticket_finish again."}
	case errors.Is(err, change.ErrHeadNotReviewed), errors.Is(err, change.ErrHeadMismatch), errors.Is(err, change.ErrHeadMoved), errors.Is(err, codehost.ErrHeadMoved):
		return &failure{outcome: HeadChanged, detail: "head is not the one landed",
			guidance: "The server did not land your change: its head changed, or is not the one a person approved. Call ticket_finish again so the new head is reviewed."}
	case errors.Is(err, change.ErrNotApproved):
		return &failure{outcome: NotApprovedForHead, detail: "no approval", guidance: "The server did not land your change: no approval covers it now. Hand it over with ticket_finish."}
	case isRefusal(err), errors.Is(err, codehost.ErrNotMergeable), errors.Is(err, codehost.ErrRejected):
		return &failure{outcome: MergeRefused, detail: "refused by the merge gate", guidance: "The merge gate refused your change: " + singleLine(err.Error()) + ". Fix that and call ticket_finish again."}
	}
	return nil
}

func isRefusal(err error) bool {
	for _, target := range []error{
		change.ErrLockNotHeld, change.ErrDraft, change.ErrWrongRepo, change.ErrWrongBase, change.ErrNoRepo, change.ErrNoHost,
		change.ErrNoChange, change.ErrMergeInFlight, ticket.ErrNeedsDiscussion, ticket.ErrAttemptAwaitsChoice, ticket.ErrNoLease,
	} {
		if errors.Is(err, target) {
			return true
		}
	}
	return false
}

func isNotClaimable(err error) bool {
	for _, target := range []error{
		ticket.ErrServerClaimNotPromoted, ticket.ErrServerClaimNotApproved, ticket.ErrServerClaimNotQueued,
		ticket.ErrServerClaimHeld, ticket.ErrServerClaimLaneHeld, ticket.ErrAlreadyHolding, ticket.ErrServerAgentDisabled, ticket.ErrServerAgentTaken,
	} {
		if errors.Is(err, target) {
			return true
		}
	}
	return false
}

func singleLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// batchInteraction turns a red that is green on the base and on the change's
// own head, found when the change was landed behind the members of its batch
// that landed first, into the batch's interaction. The later of two members is
// the one handed back: it is the one that has to be rebased onto the earlier.
func batchInteraction(f *failure, ahead []int64) *failure {
	names := make([]string, len(ahead))
	for i, id := range ahead {
		names[i] = fmt.Sprintf("#%d", id)
	}
	out := *f
	out.outcome = BatchInteraction
	out.guidance = fmt.Sprintf("The server tested your change in a batch with %s, which landed ahead of it. The batch was red, and your change is green on its own, "+
		"so the failure comes from it together with %s.\n%s", strings.Join(names, ", "), strings.Join(names, ", "), f.guidance)
	return &out
}
