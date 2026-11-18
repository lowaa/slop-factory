"""The slot decision tables as pure functions.

Nothing here touches a clock, a process or the server: the slot feeds in what it
observed and acts on the answer, so every row of the tables is a plain unit test.
"""

from __future__ import annotations

from dataclasses import dataclass, field, replace
from enum import StrEnum

from slop_haus_supervisor.fleet import login_failure
from slop_haus_supervisor.harness.base import OutcomeKind, RunOutcome
from slop_haus_supervisor.review.result import ReviewOutcome, ReviewResult, RunResult
from slop_haus_supervisor.slophaus.client import ChecksState, LockState, ReviewRequest, WorkerState

NUDGE_PROMPT = (
    "You stopped without finishing the ticket. Finish with a closing call: ticket_finish, "
    "ticket_discuss, ticket_release or ticket_prepare."
)
LOCK_HELD_PROMPT = "You now hold the integration lock."
MESSAGE_PROMPT = "A person has sent you a message: call checkin."
INTERRUPTED_NOTE = (
    "Your earlier run was interrupted by a supervisor stop: the branch and checkout may hold uncommitted or "
    "WIP-committed work from it. Review it with git status and git log, then continue the ticket."
)


def withheld_note(files: list[str]) -> str:
    return (
        "These untracked files look like secrets or are oversized, so they were not committed or pushed "
        "and are still in the checkout: " + ", ".join(files) + ". Decide what to do with them."
    )


def handover_prompt(reason: str) -> str:
    # Worded so the model does not take it for a person's request: it once answered an
    # earlier "Stop: ..." by asking the owner whether to carry on, sending the ticket to discussion.
    return (
        f"The supervisor is handing this ticket back (reason: {reason}). This is not a person's request and "
        "needs no question. Commit and push what you have to the ticket's branch, write the handoff, and call "
        "ticket_release. Use ticket_discuss only if a person has to decide something."
    )


HANDOVER_PROMPT = handover_prompt("the supervisor cannot continue this run")


def checks_finished_prompt(state: ChecksState) -> str:
    return f"checks finished: {state.value}"


REVIEW_FINDINGS_NOTE = (
    "Read the findings with review_findings and validate each one: reproduce it, read the call sites or write the "
    "failing test, since reviewers produce false positives. Label each with review_label. Then fix what is confirmed, "
    "push, and call review_request with the new head, or carry on with the ticket if nothing needs a change."
)
REVIEW_LOCK_HELD_PROMPT = (
    "The supervisor did not review: you hold the integration lock, and a review would hold up every other merge "
    "for its length. Release the lock (integration_unlock), then call review_request again; queue for the lock "
    "once the review is done."
)
REVIEW_FAILED_NOTE = (
    "A failed or unposted run leaves the request open: call review_request again to retry, or ticket_discuss if it "
    "keeps failing."
)


def _run_summary(result: RunResult) -> str:
    line = f"{result.verdict}, {result.findings} open finding{'' if result.findings == 1 else 's'}"
    if result.incomplete and result.verdict != "incomplete":
        line += ", incomplete"
    if not result.reported:
        line += ", not posted to slop-haus"
    return line


def review_prompt(result: ReviewResult, branch: str) -> str:
    """What the agent hears once the supervisor's review of its head has ended; the findings themselves are on the server."""
    head = result.head_sha[:12]
    match result.outcome:
        case ReviewOutcome.REVIEWED:
            # A REVIEWED result without its run is not expected; say it failed rather than crash the slot.
            run = result.run or RunResult(error="no result was recorded")
            if run.failed:
                return f"The supervisor's review of {head} failed ({run.error}). " + REVIEW_FAILED_NOTE
            text = f"The supervisor's review of {head} is done: {_run_summary(run)}. " + REVIEW_FINDINGS_NOTE
            if not run.reported:
                text += " " + REVIEW_FAILED_NOTE
            return text
        case ReviewOutcome.NOT_PUSHED:
            return (
                f"The supervisor did not review {head}: that commit is not on origin's {branch}, so nothing was posted. "
                "Push the branch, then call review_request again."
            )
        case ReviewOutcome.NO_REVIEWER:
            return (
                f"The supervisor cannot review on this host: {result.detail}. Nothing was reviewed or posted, and asking "
                "again here will not help: call ticket_discuss so a person can set the reviewer up or decide how to go on."
            )
        case ReviewOutcome.FAILED | ReviewOutcome.KILLED:
            return (
                f"The supervisor's review of {head} did not finish: {result.detail or 'it was stopped'}. Its run may not "
                "have been posted. Call review_request again to retry, or ticket_discuss if it keeps failing."
            )


@dataclass(frozen=True)
class PolicyConfig:
    backoffs: tuple[float, ...] = (30.0, 120.0, 600.0)
    max_nudges: int = 2
    usage_limit_cap: float = 5 * 3600.0
    # Probe interval when the harness reports no reset time.
    usage_limit_probe: float = 30 * 60.0
    run_budget: float = 3 * 3600.0
    heartbeat_interval: float = 600.0
    state_poll_interval: float = 60.0
    run_slice: float = 30.0
    idle_poll_timeout: float = 30.0
    # Total time holding the integration lock before a person is alerted to a slow queue.
    lock_alert_after: float = 45 * 60.0
    # One slop-review pass over the whole change at xhigh effort; capped by run_budget too. A sizeable change
    # takes it tens of minutes.
    review_timeout: float = 90 * 60.0

    @property
    def review_budget(self) -> float:
        return min(self.review_timeout, self.run_budget)

    @property
    def max_resumes(self) -> int:
        return len(self.backoffs)


class ActionKind(StrEnum):
    FREE = "free"
    WAIT_LOCK = "wait_lock"
    WAIT_CHECKS = "wait_checks"
    TAKE_LOCK = "take_lock"
    # Join the lock queue (or take the lock if it is free) and wait; no run is resumed yet.
    QUEUE_LOCK = "queue_lock"
    RESUME = "resume"
    BACKOFF = "backoff"
    WAIT_RESET = "wait_reset"
    HANDOVER = "handover"
    # Run the supervisor's review of the agent's pushed head; the agent is resumed with the result.
    REVIEW = "review"


@dataclass(frozen=True)
class Action:
    kind: ActionKind
    prompt: str = ""
    # Absolute time a BACKOFF or WAIT_RESET resumes at.
    until: float = 0.0
    reason: str = ""
    # HANDOVER only: skip the resume-to-push step (the model cannot run) and pause the slot after.
    model_unavailable: bool = False
    # The harness's message for a HANDOVER that blames it; the slot masks it before it is shown.
    detail: str = ""
    # REVIEW only: the request to review.
    review: ReviewRequest | None = None


@dataclass(frozen=True)
class AttemptHistory:
    """Counters that live as long as one attempt; the slot starts a fresh one per claim."""

    resumes: int = 0
    # Runs in a row that ended with nothing changed on the server; any progress resets it.
    nudges: int = 0
    limit_since: float | None = None
    # What the server showed at the last decision, to tell progress from a stall.
    seen: tuple[str, LockState, ChecksState] | None = None
    # Review requests already taken up, by head and request time: one stays open on the server when its runs
    # were not all posted, and must not be reviewed again on every look. Asking again makes a new request.
    reviewed: frozenset[tuple[str, str]] = frozenset()


@dataclass(frozen=True)
class Decision:
    action: Action
    history: AttemptHistory = field(default_factory=AttemptHistory)


def decide_from_state(cfg: PolicyConfig, state: WorkerState, history: AttemptHistory) -> Decision:
    """What to do given the server's view and nobody running.

    Used after a finished run and again on every look while the slot waits, so a held
    lock is never left without someone driving (the stranded-lock incident).
    """
    if not state.lease_held:
        return Decision(Action(ActionKind.FREE), history)
    history, fresh = _observe(state, history)
    # The offer lapses within minutes, which a review would outlast.
    if state.lock == LockState.OFFERED:
        return Decision(Action(ActionKind.TAKE_LOCK), history)
    review = _review_due(state, history)
    if review is not None:
        return review
    if state.lock == LockState.QUEUED:
        return Decision(Action(ActionKind.WAIT_LOCK), history)
    # Before the lock a run ends its turn to wait for green checks, so pending checks
    # are a wait whether or not the lock is held (else the run is nudged into a handover).
    if state.checks == ChecksState.PENDING:
        return Decision(Action(ActionKind.WAIT_CHECKS), history)
    if state.checks in (ChecksState.PASSED, ChecksState.FAILED):
        prompt = checks_finished_prompt(state.checks)
        if state.lock == LockState.HELD:
            return Decision(Action(ActionKind.RESUME, prompt), history)
        # New since the last decision, the finished checks are the supervisor's own wake-up
        # and not the model stalling. Otherwise the run may simply be done with nothing to
        # do, so the resume counts toward the nudge cap and cannot loop.
        if fresh:
            return Decision(Action(ActionKind.RESUME, prompt), history)
        if history.nudges < cfg.max_nudges:
            return Decision(Action(ActionKind.RESUME, prompt), replace(history, nudges=history.nudges + 1))
    return _nudge(cfg, history)


def _review_due(state: WorkerState, history: AttemptHistory) -> Decision | None:
    """A review request not yet taken up is reviewed before anything else the agent waits on.

    The agent ends its turn on asking, so nobody is running; checks keep while the review runs. A lock
    offer made during the review lapses untaken: holding the integration lock through a review would stop
    every other agent's merge for its length, where a lapsed offer costs only this agent a place in the queue.
    """
    request = state.review_request
    if request is None:
        return None
    key = (request.head_sha, request.requested_at)
    if key in history.reviewed:
        return None
    history = replace(history, reviewed=history.reviewed | {key})
    if state.lock == LockState.HELD:
        # For the same reason a lock already held is not kept through one: the agent lets it go and asks again.
        return Decision(Action(ActionKind.RESUME, REVIEW_LOCK_HELD_PROMPT), history)
    return Decision(Action(ActionKind.REVIEW, review=request), history)


def _observe(state: WorkerState, history: AttemptHistory) -> tuple[AttemptHistory, bool]:
    """Progress is any change in what the server shows since the last decision; it clears the nudges."""
    seen = (state.ticket_status, state.lock, state.checks)
    if seen == history.seen:
        return history, False
    return replace(history, nudges=0, seen=seen), True


def decide_on_adopt(cfg: PolicyConfig, state: WorkerState, history: AttemptHistory) -> Decision:
    """What a restarted slot does with a lease it finds live on the server.

    A drain releases the lock of a slot that is not running, so passed checks with no lock
    and no place in the queue mean the slot must queue again, not resume a run for nothing.
    """
    if state.lease_held and state.lock == LockState.HELD:
        return Decision(Action(ActionKind.RESUME, LOCK_HELD_PROMPT), history)
    if state.lease_held and state.lock == LockState.NONE:
        # The review a stop cut short is run again: the request is still open and this slot never saw it.
        # Before queueing for the lock; with a place in the queue or an offer, decide_from_state orders them.
        review = _review_due(state, history)
        if review is not None:
            return review
    if state.lease_held and state.lock == LockState.NONE and state.checks == ChecksState.PASSED:
        return Decision(Action(ActionKind.QUEUE_LOCK), history)
    return decide_from_state(cfg, state, history)


def _nudge(cfg: PolicyConfig, history: AttemptHistory) -> Decision:
    if history.nudges >= cfg.max_nudges:
        return Decision(
            Action(
                ActionKind.HANDOVER,
                reason=f"the model stopped without a closing call after {cfg.max_nudges} nudges",
            ),
            history,
        )
    return Decision(
        Action(ActionKind.RESUME, NUDGE_PROMPT), replace(history, nudges=history.nudges + 1)
    )


def decide_after_run(
    cfg: PolicyConfig,
    now: float,
    outcome: RunOutcome,
    state: WorkerState,
    history: AttemptHistory,
) -> Decision:
    # A lease that is gone ends the slot's job whatever the run did.
    if not state.lease_held or outcome.kind == OutcomeKind.KILLED:
        return Decision(Action(ActionKind.FREE), history)

    match outcome.kind:
        case OutcomeKind.FINISHED:
            return decide_from_state(cfg, state, replace(history, limit_since=None))
        case OutcomeKind.TRANSIENT | OutcomeKind.CRASHED:
            if history.resumes >= cfg.max_resumes:
                return Decision(
                    Action(
                        ActionKind.HANDOVER,
                        reason=f"gave up after {cfg.max_resumes} resumes: {outcome.kind.value}"
                        f" {outcome.detail}".rstrip(),
                    ),
                    history,
                )
            delay = cfg.backoffs[history.resumes]
            return Decision(
                Action(ActionKind.BACKOFF, "Your previous run ended unexpectedly; carry on.", now + delay),
                replace(history, resumes=history.resumes + 1),
            )
        case OutcomeKind.OVER_BUDGET:
            if history.resumes >= cfg.max_resumes:
                return Decision(
                    Action(ActionKind.HANDOVER, reason="run budget exceeded and the resume cap is used"),
                    history,
                )
            return Decision(
                Action(ActionKind.RESUME, "Your run hit its time budget; carry on."),
                replace(history, resumes=history.resumes + 1),
            )
        case OutcomeKind.USAGE_LIMIT:
            since = history.limit_since if history.limit_since is not None else now
            deadline = since + cfg.usage_limit_cap
            if now >= deadline:
                return Decision(
                    Action(ActionKind.HANDOVER, reason="usage limit did not reset within the cap"),
                    replace(history, limit_since=since),
                )
            until = outcome.reset_at if outcome.reset_at is not None else now + cfg.usage_limit_probe
            return Decision(
                Action(
                    ActionKind.WAIT_RESET,
                    "The usage limit has reset; carry on.",
                    min(until, deadline),
                ),
                replace(history, limit_since=since),
            )
        case OutcomeKind.AUTH_FAILED:
            return Decision(
                Action(
                    ActionKind.HANDOVER,
                    reason=login_failure.SUBJECT,
                    detail=outcome.detail,
                    model_unavailable=True,
                ),
                history,
            )
        case OutcomeKind.PERMISSION_REFUSED:
            return Decision(
                Action(ActionKind.HANDOVER, reason=f"permission refused: {outcome.detail}"),
                history,
            )


def lock_alert_due(cfg: PolicyConfig, now: float, held_since: float | None, alerted: bool) -> bool:
    return held_since is not None and not alerted and now - held_since >= cfg.lock_alert_after
