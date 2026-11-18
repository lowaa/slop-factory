"""One slot: one worker agent in one directory, advanced a step at a time.

`step()` never sleeps. It does what is due and returns how many seconds until it
next has something to do, so the scheduler owns all waiting and tests drive the
slot with a fake clock and no threads.
"""

from __future__ import annotations

import logging
from collections.abc import Callable
from dataclasses import dataclass, field, replace
from datetime import UTC, datetime
from pathlib import Path

from slop_haus_supervisor.fleet import login_failure, policy
from slop_haus_supervisor.fleet.aftercare import Aftercare
from slop_haus_supervisor.fleet.lease_watch import LeaseWatch
from slop_haus_supervisor.fleet.pause import PAUSE_RECHECK_INTERVAL, CauseCheck, PauseState
from slop_haus_supervisor.fleet.phase import Phase, Reason
from slop_haus_supervisor.fleet.policy import Action, ActionKind, AttemptHistory, Decision, PolicyConfig
from slop_haus_supervisor.fleet.slot_log import SlotLog, prompt_kind
from slop_haus_supervisor.harness.base import (
    Harness,
    OutcomeKind,
    RunHandle,
    RunOutcome,
    RunSpec,
    SessionNotFound,
)
from slop_haus_supervisor.harness.text import SecretMasker
from slop_haus_supervisor.ports import Alerts, Clock
from slop_haus_supervisor.review.result import ReviewOutcome, ReviewResult
from slop_haus_supervisor.review.reviewer import ReviewJob, ReviewRun, Reviews
from slop_haus_supervisor.slophaus.client import (
    Claim,
    ClaimRefused,
    Event,
    EventKind,
    LockState,
    ReviewRequest,
    SlopHaus,
    SlotSnapshot,
)
from slop_haus_supervisor.slophaus.transport import ApiError as TransportError
from slop_haus_supervisor.workspace.briefing import Briefing
from slop_haus_supervisor.workspace.bundle import read_bundle_hash
from slop_haus_supervisor.workspace.config import ConfigError
from slop_haus_supervisor.workspace.envfile import EnvFileError
from slop_haus_supervisor.workspace.harness_config import HarnessConfigError
from slop_haus_supervisor.workspace.run_dir import run_directory
from slop_haus_supervisor.workspace.handover import Prepared, RepoSnapshot, SlotRepo


_log = logging.getLogger(__name__)


def _fails_again(err: Exception) -> bool:
    """A start that failed on the host's own configuration (a refused run.env line, say) fails every retry the same way."""
    # A transport error is a ConfigError only so the command line prints it without a trace; it is a network failure.
    return isinstance(err, (EnvFileError, ConfigError, HarnessConfigError)) and not isinstance(err, TransportError)


@dataclass
class _FinishedRun:
    """A run's outcome and how far its follow-up got.

    The handle is gone once the run has ended, so a call after it that raises must
    be retried from here, not from a handle the slot no longer has. Usage is
    reported once however many steps it takes to get past the other calls.
    """

    outcome: RunOutcome
    usage_reported: bool = False
    decision: Decision | None = None


@dataclass
class Held:
    """What the slot knows of the ticket it holds, kept after the claim is freed where the next claim needs it."""

    # Where git runs: the ticket's repo, which in a nested workspace is not the root. Kept after the
    # claim is freed, because the branch left behind is cleared there.
    repo_dir: Path
    claim: Claim | None = None
    session_id: str | None = None
    history: AttemptHistory = field(default_factory=AttemptHistory)
    interrupted: bool = False
    withheld: list[str] = field(default_factory=lambda: list[str]())
    # Runs in a row that would not start; the held ticket is retried, not a fresh claim.
    failed_starts: int = 0


@dataclass(frozen=True)
class SlotDeps:
    client: SlopHaus
    harness: Harness
    repo: SlotRepo
    clock: Clock
    alerts: Alerts
    config: PolicyConfig
    briefing: Briefing
    logger: logging.Logger = field(default=_log)
    # Reasons come from the server, git and the harness; known secret values never reach the log.
    masker: SecretMasker = field(default_factory=SecretMasker)
    pause_recheck_interval: float = PAUSE_RECHECK_INTERVAL
    # The variant of a run of this agent (its board harness config for the harness it runs); None leaves the claim's.
    variant_of: Callable[[str], str] | None = None
    # Runs the supervisor's review of a pushed head; None on a host that cannot, and the agent is told so.
    reviewer: Reviews | None = None


class Slot:
    def __init__(self, agent: str, directory: Path, deps: SlotDeps) -> None:
        self.agent = agent
        self.directory = directory
        self._held = Held(directory)
        self._d = deps
        self._phase = Phase.IDLE
        self._phase_since = deps.clock.now()
        self._slot_log = SlotLog(agent, deps.logger, deps.masker, deps.clock)
        self._pause_state = PauseState(self, self._slot_log, deps.alerts, deps.clock, deps.pause_recheck_interval)
        self._watch = LeaseWatch(agent, deps.client, deps.alerts, deps.config, deps.clock)
        self._aftercare = Aftercare(deps.repo, deps.client, deps.alerts, self._slot_log)
        self._handle: RunHandle | None = None
        self._review: ReviewRun | None = None
        self._review_request: ReviewRequest | None = None
        self._review_started = 0.0
        self._abandoned = False
        self._finished: _FinishedRun | None = None
        self._run_started = 0.0
        self._refs_before: RepoSnapshot | None = None
        self._handover: Action | None = None
        self._last_branch: str | None = None
        self._wake_at = 0.0
        self._resume_prompt = ""
        self._next_state_poll = 0.0
        # A slot looks for a lease its predecessor left on the server once, before its first poll.
        self._adoption_checked = False
        # Why an idle slot is idle: what its last poll said, "starting" until it has polled.
        self._idle_reason = Reason.STARTING

    def step(self) -> float:
        """Do what is due; return seconds until the next step is worth taking."""
        if self._abandoned:
            return self._d.config.run_slice
        match self.phase:
            case Phase.IDLE:
                self._step_idle()
            case Phase.PAUSED:
                if self._pause_state.recheck_due():
                    self.try_unpause()
            case Phase.RUNNING:
                self._step_running()
            case Phase.WAIT_REVIEW:
                self._step_reviewing()
            case Phase.BACKOFF | Phase.WAIT_RESET | Phase.WAIT_LOCK | Phase.WAIT_CHECKS:
                self._step_waiting()
        return self._next_step_in()

    @property
    def phase(self) -> Phase:
        return self._phase

    @phase.setter
    def phase(self, value: Phase) -> None:
        # Re-entering the phase it is in (a waiting slot looks again) is not a change.
        if value != self._phase:
            self._slot_log.info("phase=%s from=%s%s", value.value, self._phase.value, self._ticket_field())
            self._phase = value
            self._phase_since = self._now()
            if value == Phase.IDLE:
                self._slot_log.idle_entered()

    @property
    def history(self) -> AttemptHistory:
        return self._held.history

    @property
    def phase_since(self) -> float:
        return self._phase_since

    def reason(self) -> tuple[Reason, str] | None:
        """Why the slot is not running a ticket, with a short note; None while it runs one."""
        claim = self._held.claim
        ticket = f"ticket #{claim.ticket_id}" if claim is not None else ""
        match self._phase:
            case Phase.RUNNING:
                return None
            case Phase.IDLE:
                if claim is not None:
                    # Only a step that failed between the claim and the run leaves one; "no claimable" would hide it.
                    return Reason.OTHER, f"holds {ticket} with no run"
                return self._idle_reason, ""
            case Phase.PAUSED:
                return Reason.SLOT_PAUSED, ""
            case Phase.WAIT_LOCK:
                return Reason.LOCK_QUEUED, ticket
            case Phase.WAIT_CHECKS:
                return Reason.WAITING_CI, ticket
            case Phase.WAIT_REVIEW:
                return Reason.REVIEWING, ticket
            case Phase.BACKOFF:
                return Reason.BACKOFF_AFTER_ERROR, ticket
            case Phase.WAIT_RESET:
                return Reason.COOLDOWN, ticket
        return Reason.OTHER, ""

    def snapshot(self) -> SlotSnapshot:
        """Phase, how long in it, the ticket held, why it waits and why it is paused; the error fields are the reporter's."""
        reason = self.reason()
        claim = self._held.claim
        paused = self.pause_info()
        return SlotSnapshot(
            self._phase.value,
            max(0.0, self._now() - self._phase_since),
            claim.ticket_id if claim is not None else None,
            pause_reason=paused[0] if paused is not None else "",
            reason=reason[0].value if reason is not None else "",
            reason_detail=reason[1] if reason is not None else "",
        )

    def abandon_run(self) -> None:
        """Ends the harness process for a supervisor that is exiting; the slot acts on nothing after it.

        The lease stays with the server, and a later supervisor resumes the session, as after a crash.
        """
        self._abandoned = True
        # The request stays open on the server, so the next supervisor reviews the head again.
        self._end_review()
        handle = self._handle
        if handle is not None:
            # Saved before the kill so the work is never only in the working tree, and again after it for
            # whatever the run wrote in between.
            self._aftercare.save_interrupted_work(self._held.claim, self._held.repo_dir)
            handle.kill()
            self._aftercare.save_interrupted_work(self._held.claim, self._held.repo_dir)
        self.release_lock_for_stop()

    def release_lock_for_stop(self) -> None:
        """Lets go of the integration lock a stopping slot would otherwise hold for as long as nothing runs.

        The lease stays: the next supervisor adopts it and queues for the lock again.
        """
        claim = self._held.claim
        if claim is None:
            return
        try:
            if self._d.client.state(self.agent).lock != LockState.HELD:
                return
            self._d.client.release_lock(self.agent)
        except Exception as err:
            self._slot_log.warning(
                "could not release the integration lock on stopping: %s", self._slot_log.clean(f"{type(err).__name__}: {err}")
            )
            return
        self._watch.lock_clear()
        self._slot_log.info("released the integration lock of ticket #%d on stopping; the lease stays", claim.ticket_id)

    def unpause(self) -> None:
        """A person fixed what paused the slot (the login, say)."""
        self._pause_state.unpause()

    def try_unpause(self, manual: bool = False) -> str:
        """Unpauses when the pause's cause has cleared; otherwise says why it has not ("" when unpaused)."""
        return self._pause_state.try_unpause(manual)

    def pause_info(self) -> tuple[str, float] | None:
        """The reason (masked, one line) and when the slot paused, read together; None when not paused."""
        return self._pause_state.info()

    def _ticket_field(self) -> str:
        return f" ticket={self._held.claim.ticket_id}" if self._held.claim is not None else ""

    def _now(self) -> float:
        return self._d.clock.now()

    def _next_step_in(self) -> float:
        now = self._now()
        match self.phase:
            case Phase.IDLE | Phase.RUNNING | Phase.WAIT_REVIEW:
                return 0.0
            case Phase.PAUSED:
                return self._pause_state.seconds_to_recheck()
            case Phase.BACKOFF | Phase.WAIT_RESET:
                due = self._wake_at
            case Phase.WAIT_LOCK | Phase.WAIT_CHECKS:
                due = self._next_state_poll
        due = min(due, self._watch.heartbeat_due_in())
        return max(0.0, due - now)

    def _step_idle(self) -> None:
        if not self._adoption_checked:
            # Set only once the server has answered, so a failed read is tried again.
            adopted = self._adopt_live_lease()
            self._adoption_checked = True
            if adopted:
                return
        if not self._clear_previous_work():
            return
        events = self._d.client.poll(self.agent, self._d.config.idle_poll_timeout)
        self._idle_reason = self._reason_from(events)
        for event in events:
            if event.kind == EventKind.CLAIMABLE:
                self._claim_and_start(event.ticket_id)
                return
        self._slot_log.last_poll = "no claimable work"
        self._slot_log.idle_heartbeat()

    @staticmethod
    def _reason_from(events: list[Event]) -> Reason:
        kinds = {event.kind for event in events}
        if EventKind.DISABLED in kinds:
            return Reason.FLEET_PAUSED
        if EventKind.RESTART in kinds:
            return Reason.UPDATING
        return Reason.NO_CLAIMABLE

    def _clear_previous_work(self) -> bool:
        branch = self._last_branch
        if branch is None:
            return True
        blocked = self._aftercare.clear_previous_work(self._held.repo_dir, branch)
        if blocked is not None:
            self._pause(blocked.reason, blocked.cause_check)
            return False
        self._last_branch = None
        return True


    def _adopt_live_lease(self) -> bool:
        """Takes up the lease the server still holds for this agent, as a restart loses the slot's memory of it.

        A drain leaves the lease (and maybe the lock) with the server by design; without this the
        slot polls for new work while its old lease, pinned by the lock, never ends.
        """
        state = self._d.client.state(self.agent)
        if not state.lease_held or state.ticket_id is None:
            return False
        ticket_id = state.ticket_id
        # The server makes no attempt branch for a ticket without paths, and its lease carries no attempt.
        has_paths = bool(state.attempt)
        claim = Claim(
            ticket_id=ticket_id,
            branch=f"ticket-{ticket_id}-a{state.attempt}" if has_paths else "",
            has_paths=has_paths,
            paths=state.paths,
            # Unknown here; the branch is already on the remote, which is what the checkout is put on.
            base_commit="",
            variant=self._d.client.default_variant,
            session_id=state.session_id,
        )
        self._slot_log.last_poll = f"resumed #{ticket_id}"
        if not self._begin(claim):
            return True
        if state.lock == LockState.HELD:
            self._watch.lock_taken()
        decision = policy.decide_on_adopt(self._d.config, state, self._held.history)
        self._slot_log.info("adopting live lease on ticket #%d: %s", ticket_id, self._describe(decision.action))
        if decision.action.kind == ActionKind.RESUME and self._held.interrupted:
            decision = Decision(
                replace(decision.action, prompt=decision.action.prompt + " " + policy.INTERRUPTED_NOTE),
                decision.history,
            )
        self._apply(decision)
        self._slot_log.info("resumed live lease on ticket #%d (phase %s)", ticket_id, self.phase.value)
        return True

    def _claim_and_start(self, ticket_id: int | None) -> None:
        # Before the claim: a slot that cannot brief a run must not take a ticket it would hold idle.
        briefing = self._d.briefing.load(self.directory)
        try:
            claim = self._d.client.claim(self.agent, ticket_id, read_bundle_hash(self.directory))
        except ClaimRefused as refused:
            self._slot_log.refusal(ticket_id, str(refused))
            return
        self._slot_log.info("claimed ticket #%d on branch %s", claim.ticket_id, claim.branch)
        self._slot_log.last_poll = f"claimed #{claim.ticket_id}"
        if not self._begin(claim):
            return
        prompt = self._initial_prompt(claim)
        if self._held.interrupted:
            self._slot_log.info("ticket #%d has work from an interrupted run; continuing it", claim.ticket_id)
            prompt += " " + policy.INTERRUPTED_NOTE
            if self._held.withheld:
                prompt += " " + policy.withheld_note(self._held.withheld)
        self._start_run(prompt, briefing)

    def _begin(self, claim: Claim) -> bool:
        """Takes the ticket up as the one held; False when the checkout would not take its branch."""
        self._held = Held(
            run_directory(self.directory, claim.paths),
            claim=claim,
            session_id=claim.session_id,
        )
        self._watch.start()
        return self._prepare_checkout(claim)

    def _prepare_checkout(self, claim: Claim) -> bool:
        """The run starts on its own branch: one forgotten switch would put its commits on the last ticket's."""
        if not claim.has_paths:
            # The server makes no branch for such a ticket, so there is nothing to switch to.
            prepared = self._check_clean()
            target = "the current checkout"
        else:
            prepared = self._d.repo.prepare_branch(self._held.repo_dir, claim.branch, claim.base_commit)
            target = claim.branch
        if prepared.ok:
            self._held.interrupted = prepared.interrupted
            self._held.withheld = prepared.withheld
            if prepared.warning:
                self._slot_log.warning("%s", self._slot_log.clean(prepared.warning))
            return True
        reason = f"slot error: could not put the checkout on {target}: {prepared.error}"
        self._d.client.escalate(self.agent, f"The supervisor did not start a run. {self._slot_log.clean(reason)}")
        self._free("slot not ready")
        # Recheck by the same fetch and clean test that refused it; not by switching, which would act unasked.
        self._pause(reason, self._checkout_still_unfit)
        return False

    def _check_clean(self) -> Prepared:
        # A nested workspace's root is not a repo, so a ticket with no paths has no checkout to check.
        if self._held.repo_dir.is_dir() and not (self._held.repo_dir / ".git").exists():
            return Prepared(True)
        return self._d.repo.check_clean(self._held.repo_dir)

    def _checkout_still_unfit(self) -> str:
        checked = self._check_clean()
        return "" if checked.ok else f"the slot checkout is still not ready: {checked.error}"


    @staticmethod
    def _initial_prompt(claim: Claim) -> str:
        if not claim.has_paths:
            return (
                f"Work ticket #{claim.ticket_id}. It has no paths and so no branch: do not commit. "
                "Anything that must land goes through a follow-up ticket with paths."
            )
        return f"Work ticket #{claim.ticket_id} on branch {claim.branch}."

    def _start_run(self, prompt: str, briefing: str | None = None) -> None:
        claim = self._need_claim()
        try:
            if briefing is None:
                briefing = self._d.briefing.load(self.directory)
            variant = self._d.variant_of(self.agent) if self._d.variant_of is not None else claim.variant
            spec = RunSpec(self.agent, self._held.repo_dir, claim.ticket_id, claim.branch, prompt, variant, briefing, self.directory)
            # Before the process exists: a fast run's first commit must not become the baseline.
            refs_before = self._d.repo.snapshot(self._held.repo_dir)
            handle, session = self._launch(spec, claim)
        except Exception as err:
            self._start_failed(prompt, err)
            return
        self._slot_log.info("run started on ticket #%d: %s, %s prompt", claim.ticket_id, session, prompt_kind(prompt))
        # Kept before the session is recorded: a failed report must not leave a run nobody watches.
        self._handle = handle
        self._refs_before = refs_before
        self._run_started = self._now()
        self._held.failed_starts = 0
        self.phase = Phase.RUNNING
        if handle.session_id != self._held.session_id:
            self._held.session_id = handle.session_id
            self._d.client.record_session(self.agent, self._d.harness.name, handle.session_id)

    def _launch(self, spec: RunSpec, claim: Claim) -> tuple[RunHandle, str]:
        harness = self._d.harness
        if self._held.session_id is None:
            return harness.start(spec), "fresh session"
        try:
            return harness.resume(spec, self._held.session_id), "resumed session"
        except SessionNotFound:
            # The handoff on the ticket covers the gap a fresh session leaves.
            return harness.start(replace(spec, prompt=self._initial_prompt(claim))), "fresh session (the recorded one is gone)"

    def _start_failed(self, prompt: str, err: Exception) -> None:
        """A run that would not start is retried on the held ticket, or the ticket goes back when retrying cannot help.

        Left to the scheduler, the slot would stay idle holding the ticket: its polls find nothing claimable, as
        the agent already holds one, and the ticket waits for a supervisor restart.
        """
        claim = self._need_claim()
        why = self._slot_log.clean(f"{type(err).__name__}: {err}")
        self._held.failed_starts += 1
        backoffs = self._d.config.backoffs
        if _fails_again(err) or self._held.failed_starts > len(backoffs):
            reason = f"slot error: a run would not start: {why}"
            self._slot_log.warning("run on ticket #%d would not start, handing it back: %s", claim.ticket_id, why)
            try:
                self._aftercare.release(claim, self._held.repo_dir, reason)
            except Exception as release_err:
                # Still held: the retry ends here again, so the hand-back is tried until it is taken.
                self._slot_log.warning(
                    "could not hand ticket #%d back: %s", claim.ticket_id, self._slot_log.clean(f"{type(release_err).__name__}: {release_err}")
                )
                self._retry_start(prompt, backoffs[-1] if backoffs else self._d.config.state_poll_interval)
                return
            self._free("would not start")
            # On a person's say-so only: what to fix is on this host, and they unpause once it is fixed.
            self._pause(reason)
            return
        delay = backoffs[self._held.failed_starts - 1]
        self._slot_log.warning("run on ticket #%d would not start, trying it again in %.0fs: %s", claim.ticket_id, delay, why)
        self._retry_start(prompt, delay)

    def _retry_start(self, prompt: str, delay: float) -> None:
        self.phase = Phase.BACKOFF
        self._wake_at = self._now() + delay
        self._resume_prompt = prompt

    def _need_claim(self) -> Claim:
        if self._held.claim is None:
            raise RuntimeError("slot holds no ticket")
        return self._held.claim

    def _need_handle(self) -> RunHandle:
        if self._handle is None:
            raise RuntimeError("slot has no run")
        return self._handle

    def _step_running(self) -> None:
        if self._finished is None:
            self._finished = self._wait_for_outcome()
            if self._finished is None:
                return
        # A call already under way when the run was abandoned finishes, but nothing new starts: the
        # lease stays with the server for the next supervisor to resume.
        if not self._abandoned:
            self._after_run(self._finished)

    def _wait_for_outcome(self) -> _FinishedRun | None:
        cfg = self._d.config
        handle = self._need_handle()
        outcome = handle.wait(cfg.run_slice)
        if self._abandoned:
            return None
        if outcome is None:
            if not self._maintain():
                handle.kill()
                return None
            if self._watch.revoked():
                handle.kill()
                self._free("lease revoked")
                return None
            if self._now() - self._run_started < cfg.run_budget:
                return None
            handle.kill()
            outcome = RunOutcome(OutcomeKind.OVER_BUDGET, "run budget")
        self._handle = None
        self._slot_log.info(
            "run ended on ticket #%d: %s after %.0fs",
            self._need_claim().ticket_id,
            outcome.kind.value,
            self._now() - self._run_started,
        )
        self._check_stray_work()
        return _FinishedRun(outcome)


    def _after_run(self, finished: _FinishedRun) -> None:
        """Each call that can raise is done once; a raise leaves the rest for the next step."""
        cfg = self._d.config
        if not finished.usage_reported:
            self._d.client.report_usage(self.agent, finished.outcome.usage)
            finished.usage_reported = True
        if self._handover is not None:
            self._finish_handover(self._handover)
            return
        if finished.decision is None:
            state = self._watch.observe()
            # decide_after_run adds to the history, so it is decided once and kept.
            finished.decision = policy.decide_after_run(
                cfg, self._now(), finished.outcome, state, self._held.history
            )
            self._slot_log.info("decision after run: %s", self._describe(finished.decision.action))
        if finished.decision.action.kind == ActionKind.TAKE_LOCK:
            # Taking the offer must not repeat if the resume after it fails.
            finished.decision = self._take_lock_offer(finished.decision)
        self._apply(finished.decision)
        self._finished = None


    def _step_waiting(self) -> None:
        cfg = self._d.config
        now = self._now()
        if not self._maintain():
            return
        if self.phase in (Phase.BACKOFF, Phase.WAIT_RESET):
            if self._watch.revoked():
                self._free("lease revoked")
                return
            if now >= self._wake_at:
                self._start_run(self._resume_prompt)
            return
        # Revocation first: a revoked lease must not start another run.
        if self._watch.revoked():
            self._free("lease revoked")
            return
        if self._watch.take_message():
            self._start_run(policy.MESSAGE_PROMPT)
            return
        if now < self._next_state_poll:
            return
        self._next_state_poll = now + cfg.state_poll_interval
        state = self._watch.observe()
        self._apply(policy.decide_from_state(cfg, state, self._held.history))

    def _maintain(self) -> bool:
        """False when the lease turned out to be gone (the slot is then freed)."""
        if self._watch.maintain():
            return True
        self._free("lease ended on the server")
        return False

    def _apply(self, decision: Decision) -> None:
        self._held.history = decision.history
        action = decision.action
        now = self._now()
        match action.kind:
            case ActionKind.FREE:
                self._free("finished on the server")
            case ActionKind.WAIT_LOCK:
                self.phase = Phase.WAIT_LOCK
                self._next_state_poll = now + self._d.config.state_poll_interval
            case ActionKind.QUEUE_LOCK:
                self._d.client.take_lock(self.agent)
                self.phase = Phase.WAIT_LOCK
                self._next_state_poll = now + self._d.config.state_poll_interval
            case ActionKind.WAIT_CHECKS:
                self.phase = Phase.WAIT_CHECKS
                self._next_state_poll = now + self._d.config.state_poll_interval
            case ActionKind.TAKE_LOCK:
                self._apply(self._take_lock_offer(decision))
            case ActionKind.RESUME:
                self._start_run(action.prompt)
            case ActionKind.BACKOFF | ActionKind.WAIT_RESET:
                self.phase = Phase.BACKOFF if action.kind == ActionKind.BACKOFF else Phase.WAIT_RESET
                self._wake_at = action.until
                self._resume_prompt = action.prompt
            case ActionKind.HANDOVER:
                self._begin_handover(action)
            case ActionKind.REVIEW:
                assert action.review is not None
                self._start_review(action.review)

    def _start_review(self, request: ReviewRequest) -> None:
        claim = self._need_claim()
        head = request.head_sha[:12]
        reviewer = self._d.reviewer
        if reviewer is None:
            self._review_ended(ReviewResult(ReviewOutcome.NO_REVIEWER, request.head_sha, detail="this supervisor has no reviewer"))
            return
        try:
            review = reviewer.start(ReviewJob(self.agent, self._held.repo_dir, claim.branch if claim.has_paths else "", request))
        except Exception as err:
            self._review_ended(ReviewResult(ReviewOutcome.FAILED, request.head_sha, detail=f"{type(err).__name__}: {err}"))
            return
        self._slot_log.info("review started on ticket #%d: head %s", claim.ticket_id, head)
        self._review = review
        self._review_request = request
        self._review_started = self._now()
        self.phase = Phase.WAIT_REVIEW

    def _step_reviewing(self) -> None:
        review = self._review
        if review is None:
            # Unreachable: whatever ends or drops a review also moves the slot out of this phase.
            raise RuntimeError("slot is waiting on no review")
        result = review.wait(self._d.config.run_slice)
        if self._abandoned:
            return
        if result is None:
            if not self._maintain() or self._revoked_during_review():
                return
            if self._now() - self._review_started < self._d.config.review_budget:
                return
            request = self._review_request
            self._end_review()
            result = ReviewResult(
                ReviewOutcome.FAILED, request.head_sha if request is not None else "",
                detail=f"it passed its time budget of {self._d.config.review_budget / 60:.0f} minutes and was stopped",
            )
        self._review = None
        self._review_ended(result)

    def _revoked_during_review(self) -> bool:
        if not self._watch.revoked():
            return False
        self._free("lease revoked")
        return True

    def _review_ended(self, result: ReviewResult) -> None:
        claim = self._need_claim()
        detail = self._slot_log.clean(result.detail)
        run = result.run
        if run is not None:
            run = replace(run, error=self._slot_log.clean(run.error))
        result = replace(result, detail=detail, run=run)
        self._slot_log.info(
            "review ended on ticket #%d: %s after %.0fs%s%s",
            claim.ticket_id,
            result.outcome.value,
            self._now() - self._review_started if self._review_started else 0.0,
            ", run failed" if run is not None and run.failed else "",
            f" ({detail})" if detail else "",
        )
        self._review_request = None
        self._review_started = 0.0
        self._start_run(policy.review_prompt(result, claim.branch))

    def _end_review(self) -> None:
        """Kills a review in progress; its directory is removed before this returns."""
        review, self._review = self._review, None
        self._review_request = None
        if review is not None:
            review.kill()

    def _take_lock_offer(self, decision: Decision) -> Decision:
        """The offer lapses after 5 minutes, so the supervisor takes it before resuming."""
        self._d.client.take_lock(self.agent)
        self._slot_log.info("took the integration lock offered to ticket #%d", self._need_claim().ticket_id)
        self._watch.lock_taken()
        return Decision(Action(ActionKind.RESUME, prompt=policy.LOCK_HELD_PROMPT), decision.history)

    def _describe(self, action: Action) -> str:
        text = action.kind.value
        if action.kind in (ActionKind.BACKOFF, ActionKind.WAIT_RESET):
            text += f" until {datetime.fromtimestamp(action.until, UTC).strftime('%H:%M:%SZ')}"
        if action.reason:
            text += f" ({self._slot_log.clean(action.reason)})"
        return text

    def _begin_handover(self, action: Action) -> None:
        if action.model_unavailable:
            action = replace(action, reason=login_failure.reason(self._d.harness.name, action.detail, self._d.masker))
        self._handover = action
        if action.model_unavailable or self._held.session_id is None:
            self._finish_handover(action)
            return
        # One last resume so the model pushes and writes its own handoff.
        self._start_run(policy.HANDOVER_PROMPT)


    def _free(self, why: str) -> None:
        if self._held.claim is not None:
            self._slot_log.info("freed ticket #%d: %s", self._held.claim.ticket_id, why)
            self._last_branch = self._held.claim.branch if self._held.claim.has_paths else None
        self._held.claim = None
        self._end_review()
        self._handle = None
        self._finished = None
        self._handover = None
        self._watch.lock_clear()
        self.phase = Phase.IDLE

    def _check_stray_work(self) -> None:
        if self._refs_before is None:
            return
        self._aftercare.check_stray_work(self._need_claim(), self._held.repo_dir, self._refs_before)

    def _finish_handover(self, action: Action) -> None:
        self._aftercare.hand_back(self._need_claim(), self._held.repo_dir, action)
        model_unavailable = action.model_unavailable
        self._free("handed back")
        if model_unavailable:
            self._pause(action.reason)

    def _pause(self, reason: str, cause_check: CauseCheck | None = None) -> None:
        self._pause_state.begin(reason, cause_check)
