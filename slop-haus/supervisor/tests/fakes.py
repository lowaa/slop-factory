"""Hand-written fakes: each returns what the test scripted and records its calls.

None of them re-implements a rule; the policy and slot own those.
"""

from __future__ import annotations

import threading
from collections.abc import Callable, Mapping

from dataclasses import dataclass, field
from pathlib import Path

from slop_haus_supervisor.harness.text import SecretMasker
from slop_haus_supervisor.fleet.policy import PolicyConfig
from slop_haus_supervisor.fleet.slot import Phase, Slot, SlotDeps
from slop_haus_supervisor.ports import ProcessResult
from slop_haus_supervisor.harness.base import RunOutcome, RunSpec, SessionNotFound, Usage
from slop_haus_supervisor.review.result import ReviewResult
from slop_haus_supervisor.review.reviewer import ReviewJob
from slop_haus_supervisor.slophaus.client import Claim, ClaimRefused, Event, EventKind, SlotSnapshot, WorkerState
from slop_haus_supervisor.workspace.briefing import BriefingUnavailable
from slop_haus_supervisor.workspace.handover import Prepared, RepoSnapshot, SafetyPush


class FakeClock:
    def __init__(self, start: float = 1_000_000.0) -> None:
        self.t = start

    def now(self) -> float:
        return self.t

    def advance(self, seconds: float) -> None:
        self.t += seconds

    def __call__(self) -> float:
        return self.t


class FakeRunner:
    """Records every command. It runs nothing unless the test gives it `answer`, which decides each result."""

    def __init__(self, answer: Callable[[list[str]], ProcessResult] | None = None) -> None:
        self.answer = answer
        self.calls: list[list[str]] = []

    def run(self, argv: list[str], cwd: Path, timeout: float | None = None, env: Mapping[str, str] | None = None) -> ProcessResult:
        self.calls.append(argv)
        if self.answer is None:
            raise AssertionError("nothing is run")
        return self.answer(argv)


class FakeSlot:
    """Each step() runs the next item of `on_step`: an exception to raise, a callable, or a delay to return."""

    def __init__(self, agent: str = "a", phase: Phase = Phase.IDLE) -> None:
        self.agent = agent
        self.directory = Path("/slots") / agent
        self.phase = phase
        self.steps = 0
        self.on_step: list[object] = []
        self.stepped = threading.Event()
        self.abandoned = threading.Event()
        self.lock_released = 0

    def abandon_run(self) -> None:
        self.abandoned.set()

    def release_lock_for_stop(self) -> None:
        self.lock_released += 1

    info: tuple[str, float] | None = None

    def pause_info(self) -> tuple[str, float] | None:
        return self.info

    unpause_answer = ""
    on_try_unpause: Callable[[], object] = staticmethod(lambda: None)
    unpaused_manually: list[bool]

    def try_unpause(self, manual: bool = False) -> str:
        if not hasattr(self, "unpaused_manually"):
            self.unpaused_manually = []
        self.unpaused_manually.append(manual)
        self.on_try_unpause()
        return self.unpause_answer

    def snapshot(self) -> SlotSnapshot:
        return SlotSnapshot(self.phase.value, 0.0)

    def step(self) -> float:
        self.steps += 1
        self.stepped.set()
        action = self.on_step.pop(0) if self.on_step else 0.0
        if isinstance(action, BaseException):
            raise action
        if callable(action):
            action()
            return 0.0
        return float(action) if isinstance(action, (int, float)) else 0.0


class FakeHandle:
    """Scripted run: wait() pops the script, advancing the fake clock by the timeout asked for.

    `None` in the script means "still alive". When the script runs out the run stays alive.
    """

    def __init__(self, clock: FakeClock, session_id: str, script: list[RunOutcome | None]) -> None:
        self._clock = clock
        self._session_id = session_id
        self._script = list(script)
        self.killed = False
        self.on_kill: Callable[[], None] | None = None

    @property
    def session_id(self) -> str:
        return self._session_id

    def wait(self, timeout: float) -> RunOutcome | None:
        self._clock.advance(timeout)
        return self._script.pop(0) if self._script else None

    def kill(self) -> None:
        if self.on_kill is not None:
            self.on_kill()
        self.killed = True


class FakeReview:
    """Scripted review: wait() pops the script, advancing the fake clock by the timeout asked for; None is "still running"."""

    def __init__(self, clock: FakeClock, script: list[ReviewResult | None]) -> None:
        self._clock = clock
        self._script = list(script)
        self.killed = False

    def wait(self, timeout: float) -> ReviewResult | None:
        self._clock.advance(timeout)
        return self._script.pop(0) if self._script else None

    def kill(self) -> None:
        self.killed = True


class FakeReviewer:
    def __init__(self) -> None:
        self.jobs: list[ReviewJob] = []
        self.reviews: list[FakeReview] = []
        self.start_error: Exception | None = None

    def start(self, job: ReviewJob) -> FakeReview:
        self.jobs.append(job)
        if self.start_error is not None:
            raise self.start_error
        if not self.reviews:
            raise AssertionError("unexpected review: none scripted")
        return self.reviews.pop(0)


@dataclass
class HarnessCall:
    verb: str
    spec: RunSpec
    session_id: str | None


class FakeBriefing:
    """Each directory's scripted briefing, or a refusal when it has none."""

    def __init__(self) -> None:
        self.by_directory: dict[Path, str] = {}
        self.default: str | None = "the briefing"
        self.loads: list[Path] = []

    def load(self, directory: Path) -> str:
        self.loads.append(directory)
        text = self.by_directory.get(directory, self.default)
        if text is None:
            raise BriefingUnavailable(f"{directory} has no briefing")
        return text


class FakeStewardBriefing:
    def load(self) -> str:
        return "steward briefing"


class FakeHarness:
    name = "fake"

    def __init__(self) -> None:
        self.handles: list[FakeHandle] = []
        self.calls: list[HarnessCall] = []
        self.resume_missing = False
        # Raised, one per launch, before any scripted run is used: a start that fails as env() does.
        self.launch_errors: list[Exception] = []
        # Shared with the repo fake so a test can see which came first.
        self.events: list[str] = []

    def check_installed(self) -> None:
        pass

    def config_dir(self, root: Path) -> Path:
        return root / "harness" / "fake"

    def _next(self, verb: str, spec: RunSpec, session_id: str | None) -> FakeHandle:
        self.calls.append(HarnessCall(verb, spec, session_id))
        self.events.append("launch")
        if self.launch_errors:
            raise self.launch_errors.pop(0)
        if not self.handles:
            raise AssertionError(f"unexpected {verb}: no scripted run left")
        return self.handles.pop(0)

    def start(self, spec: RunSpec) -> FakeHandle:
        return self._next("start", spec, None)

    def resume(self, spec: RunSpec, session_id: str) -> FakeHandle:
        if self.resume_missing:
            self.calls.append(HarnessCall("resume-missing", spec, session_id))
            self.resume_missing = False
            raise SessionNotFound(session_id)
        return self._next("resume", spec, session_id)

    @property
    def verbs(self) -> list[str]:
        return [c.verb for c in self.calls]


class FakeSlopHaus:
    def __init__(self) -> None:
        self.events: list[list[Event]] = []
        self.claims: list[Claim | ClaimRefused] = []
        self.states: list[WorkerState] = []
        self.calls: list[tuple[str, ...]] = []
        self.claimed_ids: list[int | None] = []
        self.claimed_bundle_hashes: list[str] = []
        # Verbs that raise on their next call, once.
        self.fail_once: dict[str, Exception] = {}
        self.slot_reports: list[SlotSnapshot] = []
        self.default_variant = "opus-high"

    def _maybe_fail(self, verb: str) -> None:
        if verb in self.fail_once:
            raise self.fail_once.pop(verb)

    def poll(self, agent: str, timeout: float) -> list[Event]:
        self.calls.append(("poll", str(timeout)))
        return self.events.pop(0) if self.events else []

    def claim(self, agent: str, ticket_id: int | None = None, bundle_hash: str = "") -> Claim:
        self.calls.append(("claim",))
        self.claimed_bundle_hashes.append(bundle_hash)
        self.claimed_ids.append(ticket_id)
        item = self.claims.pop(0)
        if isinstance(item, ClaimRefused):
            raise item
        return item

    def heartbeat(self, agent: str) -> None:
        self.calls.append(("heartbeat",))

    def state(self, agent: str) -> WorkerState:
        self.calls.append(("state",))
        self._maybe_fail("state")
        # The last scripted state repeats, so a waiting slot can look as often as it likes.
        if not self.states:
            return WorkerState(False)
        return self.states.pop(0) if len(self.states) > 1 else self.states[0]

    def take_lock(self, agent: str) -> None:
        self.calls.append(("take_lock",))

    def release_lock(self, agent: str) -> None:
        self.calls.append(("release_lock",))
        self._maybe_fail("release_lock")

    def record_session(self, agent: str, harness: str, session_id: str) -> None:
        self.calls.append(("record_session", harness, session_id))

    def escalate(self, agent: str, note: str) -> None:
        self.calls.append(("escalate", note))
        self._maybe_fail("escalate")

    def release(self, agent: str, reason: str) -> None:
        self.calls.append(("release", reason))
        self._maybe_fail("release")

    def report_usage(self, agent: str, usage: Usage) -> None:
        self.calls.append(("report_usage", str(sum(m.counters.get("output", 0) for m in usage.models))))
        self._maybe_fail("report_usage")

    def report_slot_state(self, agent: str, snapshot: SlotSnapshot) -> None:
        self.slot_reports.append(snapshot)
        self._maybe_fail("report_slot_state")

    def count(self, verb: str) -> int:
        return sum(1 for c in self.calls if c[0] == verb)

    def notes(self) -> list[str]:
        return self.notes_of("escalate")

    def notes_of(self, verb: str) -> list[str]:
        return [c[1] for c in self.calls if c[0] == verb]


class FakeRepo:
    def __init__(self) -> None:
        self.unpushed = False
        self.push_result = SafetyPush(True, commit="abc123")
        self.pushes: list[str] = []
        self.interrupted_pushes: list[str] = []
        self.released: list[str] = []
        self.prepared: list[str] = []
        self.prepare_result = Prepared(True)
        self.stray: list[str] = []
        self.prepared_base: list[str] = []
        self.prepared_in: list[Path] = []
        self.events: list[str] = []
        self.superseded_checked: list[str] = []

    def prepare_branch(self, directory: Path, branch: str, base_commit: str = "") -> Prepared:
        self.prepared.append(branch)
        self.prepared_base.append(base_commit)
        self.prepared_in.append(directory)
        self.events.append("prepare")
        return self.prepare_result

    def check_clean(self, directory: Path) -> Prepared:
        self.events.append("check_clean")
        return self.prepare_result

    def snapshot(self, directory: Path) -> RepoSnapshot:
        self.events.append("snapshot")
        return RepoSnapshot(None, {})

    def stray_work(self, directory: Path, branch: str, before: RepoSnapshot) -> list[str]:
        return self.stray

    def has_unpushed_work(self, directory: Path, branch: str) -> bool:
        return self.unpushed

    def safety_push(self, directory: Path, branch: str) -> SafetyPush:
        self.pushes.append(branch)
        if self.push_result.ok:
            self.unpushed = False
        return self.push_result

    def drop_superseded(self, directory: Path, branch: str) -> bool:
        self.superseded_checked.append(branch)
        return False

    def interrupted_push(self, directory: Path, ticket_id: int, branch: str) -> SafetyPush:
        self.events.append("interrupted_push")
        self.interrupted_pushes.append(branch)
        return self.push_result

    def release_worktrees(self, directory: Path, branch: str) -> None:
        self.released.append(branch)


class FakeAlerts:
    def __init__(self) -> None:
        self.messages: list[str] = []

    def alert(self, message: str) -> None:
        self.messages.append(message)


CLAIM = Claim(ticket_id=7, branch="ticket-7-a1", base_commit="deadbeef", variant="opus-high")
CLAIMABLE = [Event(EventKind.CLAIMABLE, ticket_id=7)]


@dataclass
class Rig:
    clock: FakeClock = field(default_factory=FakeClock)
    harness: FakeHarness = field(default_factory=FakeHarness)
    client: FakeSlopHaus = field(default_factory=FakeSlopHaus)
    repo: FakeRepo = field(default_factory=FakeRepo)
    alerts: FakeAlerts = field(default_factory=FakeAlerts)
    config: PolicyConfig = field(default_factory=PolicyConfig)
    briefing: FakeBriefing = field(default_factory=lambda: FakeBriefing())
    masker: SecretMasker = field(default_factory=SecretMasker)
    variant_of: Callable[[str], str] | None = None
    reviewer: FakeReviewer | None = field(default_factory=FakeReviewer)
    _slot: Slot | None = None

    @property
    def slot(self) -> Slot:
        if self._slot is None:
            self.harness.events = self.repo.events
            deps = SlotDeps(
                self.client, self.harness, self.repo, self.clock, self.alerts, self.config, self.briefing, masker=self.masker,
                variant_of=self.variant_of, reviewer=self.reviewer,
            )
            self._slot = Slot("agent-1", Path("/slots/agent-1"), deps)
        return self._slot

    def script_review(self, *script: ReviewResult | None) -> FakeReview:
        assert self.reviewer is not None
        review = FakeReview(self.clock, list(script))
        self.reviewer.reviews.append(review)
        return review

    def script_run(self, *script: RunOutcome | None, session_id: str = "s1") -> FakeHandle:
        handle = FakeHandle(self.clock, session_id, list(script))
        self.harness.handles.append(handle)
        return handle
