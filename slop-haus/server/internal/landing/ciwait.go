package landing

import (
	"context"
	"errors"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/change"
)

// ciWait waits for the checks on a head and decides green, red or stuck. The
// single landing and a batch's run both wait through it, so a stall fix is made
// once. Its retry state lasts for the landing that owns it: a red check is run
// again once per landing, across all of its waits.
type ciWait struct {
	r *Runner
	// read returns the checks to judge: the held change's, or a batch branch's.
	read func(ctx context.Context) (*change.Report, *failure)
	// onCIPassed, when set, is told of the checks the host showed pending that
	// the CI said had passed.
	onCIPassed func(names []string)
	// decorateRed, when set, may add to or replace a final red failure, with
	// what the landing knows of why it is red.
	decorateRed func(ctx context.Context, fail *failure, failing []string) *failure
	// retried is whether a red check was run again.
	retried bool
	// noRetry is whether CI said it cannot run a pipeline again.
	noRetry bool
	// restarted is whether a lost pipeline was cancelled and restarted: it is
	// done once per landing, so a pipeline that is lost again is a handback.
	restarted bool
	// retriedPipe is the pipeline that was run again: until a newer one
	// exists, the host's failure is still that one's.
	retriedPipe int64
	// passed are the checks the host showed pending that the CI said had
	// passed, for the head await last returned: the gate is told so.
	passed []change.CheckLine
	// flakyRetried are the known-flaky entries the retry was made for, until
	// the retry's result is recorded. Empty for a retry made because the
	// failure passed on the base's tip.
	flakyRetried []flakyEntry
}

// waitLogEvery is how often a wait for checks says it is still waiting, so a
// stalled queue shows in the logs.
const waitLogEvery = 5 * time.Minute

// await waits for checks that have passed on the head. wantHead, when
// set, is the head the wait is for; otherwise notHead is the head merge-main
// replaced, which an update still pending has not yet moved off. Checks read
// for any other head are stale. seen is a report already read, if any.
//
// The wait follows the newest pipeline on the head, not the first it saw: the
// woodpecker-watchdog cancels a pipeline stuck past the repo timeout (or
// orphaned by a Woodpecker restart) and restarts it once as a new pipeline on
// the same commit, and the host can show the cancelled one as a failure for a
// moment before the restart. A failure is red only when the newest pipeline
// says so. The bound is the repo's CI timeout plus a margin for one restart,
// never a constant, so a ticket the watchdog is about to rescue is not handed
// back, and a pipeline that never ends is a handback and not a frozen queue.
func (w *ciWait) await(ctx context.Context, wantHead, notHead string, seen *change.Report) (*change.Report, *failure) {
	report, fail := w.awaitChecks(ctx, wantHead, notHead, seen)
	if fail == nil && report != nil {
		w.recordRetry(ctx, nil)
	}
	// A wait that ends otherwise (timeout, cancelled, host error) says nothing
	// of whether the retry rescued: leave it unrecorded rather than let a later
	// green wait claim it.
	w.flakyRetried = nil
	return report, fail
}

func (w *ciWait) awaitChecks(ctx context.Context, wantHead, notHead string, seen *change.Report) (*change.Report, *failure) {
	r := w.r
	bound := checksBound(ctx, r.o.Pipelines, &r.cfg, r.log)
	deadline := r.clock.Now().Add(bound)
	var first int64
	extended := false
	report := seen
	started := r.clock.Now()
	var noneSince, loggedAt time.Time
	pendingSince := map[string]time.Time{}
	w.passed = nil
	for {
		if report == nil {
			var fail *failure
			if report, fail = w.read(ctx); fail != nil {
				return nil, fail
			}
		}
		head := report.HeadSHA
		stale := (wantHead != "" && !strings.EqualFold(head, wantHead)) || (wantHead == "" && notHead != "" && strings.EqualFold(head, notHead))
		var pipe *Pipeline
		var pipeErr error
		if !stale {
			pipe, pipeErr = r.pipeline(ctx, head)
		}
		if pipe != nil {
			switch {
			case first == 0:
				first = pipe.ID
				// The bound counts from when the pipeline was created, not
				// from when this runner began to wait: an API restart (every
				// deploy) must not give a lost pipeline a fresh bound.
				if !pipe.Created.IsZero() && pipe.Created.Before(r.clock.Now()) {
					// An expired one still gets the restart margin: it may be the
					// pipeline the watchdog cancelled, whose restart is yet to appear.
					deadline = pipe.Created.Add(bound)
					if floor := r.clock.Now().Add(r.cfg.RestartMargin); deadline.Before(floor) {
						deadline = floor
					}
				}
			case pipe.ID != first && !extended:
				// The watchdog restarted it: the restart gets its own bound.
				extended = true
				if d := pipe.Created.Add(bound); d.After(deadline) {
					deadline = d
				}
			}
		}
		if !stale {
			switch report.State {
			case "success":
				return report, nil
			case "failure":
				if fail := w.judgeRed(ctx, report, pipe, &deadline, bound); fail != nil {
					return nil, fail
				}
			case "pending":
				done, red := w.crossCheck(ctx, report, pendingSince)
				if done {
					return report, nil
				}
				if red == nil && pipe != nil && slices.ContainsFunc(report.Checks, func(c change.CheckLine) bool { return c.State == "failure" }) {
					// One workflow has failed while others still run.
					cp := *report
					cp.State = "failure"
					red = &cp
				}
				if red != nil {
					if fail := w.judgeRed(ctx, red, pipe, &deadline, bound); fail != nil {
						return nil, fail
					}
				}
			}
		}
		if !r.clock.Now().Before(deadline) {
			if !stale && w.canRestartLost(pipe, first) {
				fail := w.restartLost(ctx, head, pipe)
				if fail != nil {
					return nil, fail
				}
				// The restart is a new pipeline: it gets the whole bound.
				deadline = r.clock.Now().Add(bound)
				first = 0
				extended = false
				report = nil
				if !r.sleep(ctx, r.cfg.PollInterval) {
					return nil, r.cancelled()
				}
				continue
			}
			return nil, pendingTimeout(head, "checks "+report.State+" after "+bound.String())
		}
		if !stale && report.State == "none" && len(report.Checks) == 0 {
			if noneSince.IsZero() {
				noneSince = r.clock.Now()
			}
			cause, wait := ciNoPipeline, r.cfg.NoPipelineWait
			switch {
			case pipeErr != nil:
				cause, wait = ciUnreadable, r.cfg.NeverStartedWait
			case pipe != nil:
				cause, wait = ciPipelineExists, r.cfg.NeverStartedWait
			}
			if r.clock.Now().Sub(noneSince) >= wait {
				return nil, neverStarted(head, wait, cause)
			}
		} else {
			noneSince = time.Time{}
		}
		if loggedAt.IsZero() || r.clock.Now().Sub(loggedAt) >= waitLogEvery {
			loggedAt = r.clock.Now()
			r.log.InfoContext(ctx, "landing: waiting for checks", "head", head, "state", report.State, "checks", len(report.Checks), "waited", loggedAt.Sub(started))
		}
		if !r.sleep(ctx, r.cfg.PollInterval) {
			return nil, r.cancelled()
		}
		if fail := r.stillOn(ctx); fail != nil {
			return nil, fail
		}
		report = nil
	}
}

// canRestartLost is whether the bound ran out on the pipeline the wait began
// on, still reported going: Woodpecker keeps saying so for one whose workflows
// died, and nothing else would end it.
func (w *ciWait) canRestartLost(pipe *Pipeline, first int64) bool {
	return !w.restarted && pipe != nil && pipe.ID == first &&
		(pipe.State == PipelineRunning || pipe.State == PipelinePending)
}

func (w *ciWait) restartLost(ctx context.Context, head string, pipe *Pipeline) *failure {
	r := w.r
	w.restarted = true
	err := r.hostCall(ctx, func() error { return r.o.Pipelines.Restart(ctx, head, pipe.ID) })
	if errors.Is(err, ErrNoRetry) {
		return pendingTimeout(head, "its pipeline "+strconv.FormatInt(pipe.ID, 10)+" is still reported running past the CI timeout and this CI cannot restart it")
	}
	if err != nil {
		return r.classify(ctx, err)
	}
	r.log.WarnContext(ctx, "landing: cancelled and restarted a pipeline still running past the CI timeout", "head", head, "pipeline", pipe.ID)
	return nil
}

// crossCheck asks the CI about checks the host has shown pending for longer
// than CheckStuckAfter, since a status the CI never delivered stays pending
// for good. done is whether every pending check is one the CI says passed
// (w.ciPassed then names them, for the gate to be told). red is the report
// with the checks the CI says failed marked so, or niw. A check the CI does
// not know, or says is still going, keeps the wait as it is.
func (w *ciWait) crossCheck(ctx context.Context, report *change.Report, since map[string]time.Time) (done bool, red *change.Report) {
	r := w.r
	now := r.clock.Now()
	var passed []change.CheckLine
	var failed, passedNames []string
	allPassed := true
	for _, c := range report.Checks {
		if c.State != "pending" {
			delete(since, c.Name)
			continue
		}
		if _, ok := since[c.Name]; !ok {
			since[c.Name] = now
		}
		if c.TargetURL == "" || now.Sub(since[c.Name]) < r.cfg.CheckStuckAfter {
			allPassed = false
			continue
		}
		verdict, err := r.o.Pipelines.CheckVerdict(ctx, report.HeadSHA, c.TargetURL)
		switch {
		case err != nil && !errors.Is(err, ErrUnknown):
			r.log.WarnContext(ctx, "landing: ask the CI about a pending check", "check", c.Name, "err", err)
			allPassed = false
		case err != nil || verdict == CIRunning:
			allPassed = false
		case verdict == CIFailed:
			failed = append(failed, c.Name)
			allPassed = false
		default:
			passed = append(passed, c)
			passedNames = append(passedNames, c.Name)
		}
	}
	if len(failed) > 0 {
		cp := *report
		cp.State = "failure"
		cp.Checks = slices.Clone(report.Checks)
		for i := range cp.Checks {
			if slices.Contains(failed, cp.Checks[i].Name) {
				cp.Checks[i].State = "failure"
			}
		}
		return false, &cp
	}
	if !allPassed || len(passed) == 0 {
		return false, nil
	}
	r.log.WarnContext(ctx, "landing: status not delivered to the host; the CI says success", "head", report.HeadSHA, "checks", passedNames)
	w.passed = passed
	if w.onCIPassed != nil {
		w.onCIPassed(passedNames)
	}
	return true, nil
}

func (r *Runner) pipeline(ctx context.Context, head string) (*Pipeline, error) {
	p, err := r.o.Pipelines.Newest(ctx, head)
	if err != nil && !errors.Is(err, ErrUnknown) {
		r.log.WarnContext(ctx, "landing: read the newest pipeline", "err", err)
	}
	return p, err
}

// judgeRed decides what a failure on the host is. It returns a failure when it
// is final; nil to keep waiting (a pipeline still going, a restart expected, or
// a retry just made).
func (w *ciWait) judgeRed(ctx context.Context, report *change.Report, pipe *Pipeline, deadline *time.Time, bound time.Duration) *failure {
	r := w.r
	if w.retried && w.retriedPipe != 0 && (pipe == nil || pipe.ID <= w.retriedPipe) {
		// Until a newer pipeline is seen the failure is the retried one's; an
		// unreadable pipeline is not the retry failing.
		return nil
	}
	var failing []string
	early := false
	if pipe != nil {
		switch pipe.State {
		case PipelinePending, PipelineRunning:
			// Going on: red now only for a failure the newest pipeline itself
			// shows and no retry will clear. The rest of its workflows would
			// only delay a handback that is already certain.
			failing = failedIn(report, pipe)
			if len(failing) == 0 || w.willRetry(ctx, failing) {
				return nil
			}
			early = true
		case PipelineSuccess:
			// Green: the failure is an older pipeline's, or the host has yet
			// to hear of the newer result.
			return nil
		case PipelineCancelled:
			// Cancelled, not failed: the watchdog restarts it within the margin.
			if r.clock.Now().Sub(pipe.Finished) < r.cfg.RestartMargin {
				return nil
			}
			return pendingTimeout(report.HeadSHA, "its pipeline was cancelled and not restarted")
		}
	}
	if failing == nil {
		for _, c := range report.Checks {
			if c.State == "failure" {
				failing = append(failing, c.Name)
			}
		}
	}
	// An early red was already judged not to be retried; asking again could
	// flip on a base that moved meanwhile, and a running pipeline cannot be retried.
	if !early && w.willRetry(ctx, failing) {
		w.retried = true
		matched := w.flakyMatches(failing)
		if pipe != nil {
			w.retriedPipe = pipe.ID
		}
		switch err := r.hostCall(ctx, func() error { return r.o.Pipelines.Retry(ctx, report.HeadSHA) }); {
		case errors.Is(err, ErrNoRetry):
			// CI that cannot retry: the failure stands, and is not asked again.
			w.retried = false
			w.noRetry = true
		case err != nil:
			return r.classify(ctx, err)
		default:
			// Only a retry that was made has a result to record.
			w.flakyRetried = matched
			// The retry is a new pipeline: it gets the whole bound.
			*deadline = r.clock.Now().Add(bound)
			return nil
		}
	}
	if w.retried {
		w.recordRetry(ctx, failing)
	}
	fail := redChecks(report.HeadSHA, failing, w.retried)
	if w.decorateRed != nil {
		return w.decorateRed(ctx, fail, failing)
	}
	return fail
}

// willRetry is whether the failure is to be retried once rather than decided.
func (w *ciWait) willRetry(ctx context.Context, failing []string) bool {
	return !w.retried && !w.noRetry && w.worthRetry(ctx, failing)
}

// pipelineInTarget names the pipeline a check's target_url points at.
var pipelineInTarget = regexp.MustCompile(`/pipeline/(\d+)(?:/|$)`)

// failedIn is the checks the host shows failed that belong to the pipeline. A
// check whose pipeline cannot be read from its URL is left out: it may be an
// older pipeline's, as the watchdog's cancelled one is.
func failedIn(report *change.Report, pipe *Pipeline) []string {
	var out []string
	for _, c := range report.Checks {
		if c.State != "failure" {
			continue
		}
		if m := pipelineInTarget.FindStringSubmatch(c.TargetURL); m != nil && m[1] == strconv.FormatInt(pipe.ID, 10) {
			out = append(out, c.Name)
		}
	}
	return out
}

// worthRetry is whether every failing check is known flaky, or passed on the
// base's tip, so that the failure is not this change's.
func (w *ciWait) worthRetry(ctx context.Context, failing []string) bool {
	if len(failing) == 0 {
		return false
	}
	if w.flakyMatches(failing) != nil {
		return true
	}
	ok, err := w.r.o.Pipelines.PassedOnBase(ctx, failing)
	return err == nil && ok
}

// knownFlaky is the entry that covers a failing check, by its full name or its
// workflow's.
func (w *ciWait) knownFlaky(check string) (flakyEntry, bool) {
	entries, _ := parseKnownFlaky(w.r.cfg.KnownFlaky)
	for _, e := range entries {
		if e.Name == check || e.Name == checkWorkflow(check) {
			return e, true
		}
	}
	return flakyEntry{}, false
}

// flakyMatches are the entries covering the failing checks, or nil unless
// every one of them is covered.
func (w *ciWait) flakyMatches(failing []string) []flakyEntry {
	var out []flakyEntry
	for _, name := range failing {
		e, ok := w.knownFlaky(name)
		if !ok {
			return nil
		}
		if !slices.ContainsFunc(out, func(o flakyEntry) bool { return o.Name == e.Name }) {
			out = append(out, e)
		}
	}
	return out
}

// recordRetry ends the known-flaky retry, if one is open: a workflow still
// failing was confirmed flaky-red, any other was rescued by the retry (even if
// the landing then fails on a different check, which the retry also ran). The
// result is counted and logged once; the 14-day rescue rate is read off the
// counter.
func (w *ciWait) recordRetry(ctx context.Context, stillFailing []string) {
	entries := w.flakyRetried
	w.flakyRetried = nil
	r := w.r
	now := r.clock.Now()
	for _, e := range entries {
		result := FlakyRescued
		if slices.ContainsFunc(stillFailing, func(c string) bool { return c == e.Name || checkWorkflow(c) == e.Name }) {
			result = FlakyConfirmed
		}
		expired := e.expired(now)
		if expired {
			r.log.WarnContext(ctx, "landing: known-flaky entry is past its review date and still retried", "workflow", e.Name, "owner", e.Owner, "review", e.Review.Format(flakyReviewLayout))
		}
		r.log.InfoContext(ctx, "landing: known-flaky retry result", "workflow", e.Name, "result", result, "owner", e.Owner, "expired", expired)
		if fo, ok := r.o.Observer.(FlakyObserver); ok {
			fo.LandingFlakyRetry(e.Name, result, expired)
		}
	}
}
