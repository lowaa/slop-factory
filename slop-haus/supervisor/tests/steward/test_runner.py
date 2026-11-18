from __future__ import annotations

import logging
from collections.abc import Callable, Sequence
from pathlib import Path

import pytest

from slop_haus_supervisor.harness.base import OutcomeKind, RunOutcome, RunSpec
from slop_haus_supervisor.steward.events import WakeBatcher, WakeEvent, WakeKind
from slop_haus_supervisor.harness.text import SecretMasker
from slop_haus_supervisor.steward.harness import build_steward_harness
from slop_haus_supervisor.steward.guard import STEWARD_ALLOWED_TOOLS, steward_denied_tools
from slop_haus_supervisor.steward.runner import StewardConfig, StewardRunner, steward_command
from fakes import FakeClock, FakeHandle, FakeHarness, FakeStewardBriefing

RESTART = WakeEvent(WakeKind.DISCUSSION, 7, "stopped by a restart")
DONE = RunOutcome(OutcomeKind.FINISHED)


class FakeBoard:
    def __init__(self) -> None:
        self.pending: list[WakeEvent] = []

    def new_events(self) -> Sequence[WakeEvent]:
        out, self.pending = self.pending, []
        return out


class FakeReports:
    def agreement_report(self) -> str:
        return "restart_resume: 3/3 agreed"


class FakeLease:
    def __init__(self) -> None:
        self.free = True
        self.held = False
        self.acquired = 0

    def acquire(self) -> bool:
        if not self.free:
            return False
        self.held = True
        self.acquired += 1
        return True

    def release(self) -> None:
        self.held = False


class Proposing(FakeHarness):
    """Stands in for the agent: on start it records what a restart_resume proposal would be."""

    def __init__(self, clock: FakeClock, lease: FakeLease) -> None:
        super().__init__()
        self.clock = clock
        self.lease = lease
        self.proposals: list[tuple[str, int]] = []
        self.held_during_run: list[bool] = []
        self.on_start: Callable[[RunSpec], None] = lambda spec: None

    def start(self, spec: RunSpec) -> FakeHandle:
        self.held_during_run.append(self.lease.held)
        self.on_start(spec)
        return super().start(spec)


class Rig:
    def __init__(self, tmp_path: Path) -> None:
        self.clock = FakeClock()
        self.board = FakeBoard()
        self.lease = FakeLease()
        self.harness = Proposing(self.clock, self.lease)
        self.config = StewardConfig(batch_window=120, sweep_every=1800, run_timeout=60, poll_interval=10, backoff=600)
        self.runner = StewardRunner(
            self.harness, self.board, FakeStewardBriefing(), FakeReports(), self.lease, self.clock,
            tmp_path / "scratch", logging.getLogger("t"), self.config,
        )

    def script(self, *outcomes: RunOutcome | None) -> None:
        self.harness.handles.append(FakeHandle(self.clock, "s", list(outcomes)))


@pytest.fixture
def rig(tmp_path: Path) -> Rig:
    return Rig(tmp_path)


def test_events_within_the_window_share_one_run(rig: Rig) -> None:
    rig.script(DONE)
    rig.board.pending = [RESTART]
    assert rig.runner.tick() is None
    rig.clock.advance(60)
    rig.board.pending = [WakeEvent(WakeKind.AGENT_FILED, 9)]
    assert rig.runner.tick() is None
    rig.clock.advance(61)
    assert rig.runner.tick() == DONE
    assert len(rig.harness.calls) == 1
    prompt = rig.harness.calls[0].spec.prompt
    assert "ticket #7" in prompt and "ticket #9" in prompt
    assert rig.runner.tick() is None


def test_sweep_wakes_after_the_period(rig: Rig) -> None:
    rig.script(DONE)
    rig.clock.advance(1799)
    assert rig.runner.tick() is None
    rig.clock.advance(2)
    assert rig.runner.tick() == DONE
    assert "sweep" in rig.harness.calls[0].spec.prompt


def test_one_steward_at_a_time_per_tenant(rig: Rig) -> None:
    rig.lease.free = False
    rig.board.pending = [RESTART]
    rig.runner.tick()
    rig.clock.advance(121)
    assert rig.runner.tick() is None
    assert rig.harness.calls == []
    rig.lease.free = True
    rig.script(DONE)
    rig.board.pending = [RESTART]
    rig.runner.tick()
    rig.clock.advance(121)
    assert rig.runner.tick() == DONE
    assert rig.harness.held_during_run == [True]
    assert not rig.lease.held


def test_run_is_headless_with_the_briefing_and_report(rig: Rig) -> None:
    rig.script(DONE)
    rig.board.pending = [RESTART]
    rig.runner.tick()
    rig.clock.advance(121)
    rig.runner.tick()
    spec = rig.harness.calls[0].spec
    assert spec.system_prompt == "steward briefing"
    assert "restart_resume: 3/3 agreed" in spec.prompt
    assert spec.ticket_id == 0 and spec.branch == ""
    assert spec.directory.is_dir()


def test_restart_discussion_produces_a_restart_resume_proposal(rig: Rig) -> None:
    def agent(spec: RunSpec) -> None:
        # The scripted agent reads the briefing's event list and proposes, as the real one is told to.
        for line in spec.prompt.splitlines():
            if "stopped by a restart" in line and "ticket #7" in line:
                rig.harness.proposals.append(("restart_resume", 7))

    rig.harness.on_start = agent
    rig.script(DONE)
    rig.board.pending = [RESTART]
    rig.runner.tick()
    rig.clock.advance(121)
    rig.runner.tick()
    assert rig.harness.proposals == [("restart_resume", 7)]


def test_run_over_its_time_budget_is_killed_and_backs_off(rig: Rig) -> None:
    rig.script(None, None, None, None, None, None, None)
    handle = rig.harness.handles[0]
    rig.board.pending = [RESTART]
    rig.runner.tick()
    rig.clock.advance(121)
    out = rig.runner.tick()
    assert out is not None and out.kind is OutcomeKind.OVER_BUDGET
    assert handle.killed
    assert not rig.lease.held
    rig.board.pending = [RESTART]
    rig.clock.advance(300)
    rig.runner.tick()
    rig.clock.advance(121)
    assert rig.runner.tick() is None  # still backing off
    assert len(rig.harness.calls) == 1


def test_usage_limit_backs_off(rig: Rig) -> None:
    rig.script(RunOutcome(OutcomeKind.USAGE_LIMIT))
    rig.board.pending = [RESTART]
    rig.runner.tick()
    rig.clock.advance(121)
    out = rig.runner.tick()
    assert out is not None and out.kind is OutcomeKind.USAGE_LIMIT
    rig.script(DONE)
    rig.board.pending = [RESTART]
    rig.clock.advance(130)
    rig.runner.tick()
    rig.clock.advance(121)
    assert rig.runner.tick() is None
    assert len(rig.harness.calls) == 1


def test_busy_lease_keeps_the_batch_for_the_next_window(rig: Rig) -> None:
    rig.lease.free = False
    rig.board.pending = [RESTART]
    rig.runner.tick()
    rig.clock.advance(121)
    assert rig.runner.tick() is None
    rig.lease.free = True
    rig.script(DONE)
    rig.clock.advance(121)
    assert rig.runner.tick() == DONE
    assert "ticket #7" in rig.harness.calls[0].spec.prompt


def test_harness_carries_the_deny_list_and_turn_budget(tmp_path: Path) -> None:
    h = build_steward_harness(tmp_path, "http://s", StewardConfig(max_turns=12), {}, SecretMasker([]), tmp_path / "token.sh")
    argv = h.argv("sid", False, tmp_path / "mcp.json")
    assert argv[argv.index("--max-turns") + 1] == "12"
    denied = argv[argv.index("--disallowedTools") + 1].split(",")
    assert set(steward_denied_tools()) <= set(denied)


def test_batcher_dedupes_the_same_ticket() -> None:
    b = WakeBatcher(10, 1000, 0)
    b.add(RESTART, 0)
    b.add(RESTART, 1)
    assert b.take(11) == [RESTART]


def test_guard_denies_writes_and_worker_tools_but_not_reads() -> None:
    denied = steward_denied_tools()
    for tool in ("Edit", "Write", "Bash(git push:*)", "mcp__slop-haus__ticket_next", "mcp__slop-haus__integration_lock", "mcp__slop-haus__ticket_finish"):
        assert tool in denied
    assert not any(d.startswith(("Bash(git log", "Bash(gh pr diff", "Bash(gh pr view")) for d in denied)
    assert "mcp__slop-haus__steward_assess" not in denied
    assert not any("propose_" in d for d in denied)


def test_turn_budget_is_in_the_command() -> None:
    assert steward_command(25) == ("claude", "--max-turns", "25")


def test_harness_runs_on_an_allowlist_not_bypassed_permissions(tmp_path: Path) -> None:
    h = build_steward_harness(tmp_path, "http://s", StewardConfig(), {}, SecretMasker([]), tmp_path / "token.sh")
    argv = h.argv("sid", False, tmp_path / "mcp.json")
    assert argv[argv.index("--permission-mode") + 1] == "dontAsk"
    assert argv[argv.index("--tools") + 1].split(",") == ["Read", "Grep", "Glob"]
    allowed = argv[argv.index("--allowedTools") + 1].split(",")
    assert set(allowed) == set(STEWARD_ALLOWED_TOOLS)
    # Pinned independently of the constant: widening the allowlist has to change this test too.
    assert set(allowed) == {
        "Read", "Grep", "Glob", "mcp__slop-haus__ticket_get", "mcp__slop-haus__ticket_attachment_get", "mcp__slop-haus__ticket_search", "mcp__slop-haus__steward_assess",
        "mcp__slop-haus__steward_draft", "mcp__slop-haus__steward_label_discussion", "mcp__slop-haus__propose_discussion_answer", "mcp__slop-haus__propose_ready", "mcp__slop-haus__propose_status",
        "mcp__slop-haus__propose_review_decision", "mcp__slop-haus__propose_ticket_edit", "mcp__slop-haus__propose_manifest_edit",
    }
    # Bash, writes, web and the worker's ticket tools are refused because they are not listed.
    for tool in ("Bash", "Edit", "Write", "WebFetch", "WebSearch", "mcp__slop-haus__ticket_finish", "mcp__slop-haus__ticket_comment"):
        assert tool not in allowed
    assert not any(t.startswith("Bash") for t in argv[argv.index("--tools") + 1].split(","))


def test_a_run_over_its_turn_budget_backs_off(rig: Rig) -> None:
    over = RunOutcome(OutcomeKind.OVER_BUDGET, "the run reached its turn budget (--max-turns)")
    rig.script(over)
    rig.board.pending = [RESTART]
    rig.runner.tick()
    rig.clock.advance(121)
    assert rig.runner.tick() == over
    rig.board.pending = [RESTART]
    rig.clock.advance(121)
    assert rig.runner.tick() is None


def test_stop_ends_a_run_in_progress(rig: Rig) -> None:
    rig.script(None, None)
    handle = rig.harness.handles[0]
    rig.harness.on_start = lambda spec: rig.runner.stop()
    rig.board.pending = [RESTART]
    rig.runner.tick()
    rig.clock.advance(121)
    outcome = rig.runner.tick()
    assert outcome is not None and outcome.kind is OutcomeKind.KILLED
    assert handle.killed
    assert rig.runner.tick() is None


def test_an_identity_that_cannot_be_ensured_means_no_run(tmp_path: Path) -> None:
    class Broken:
        def ensure(self) -> Path:
            raise OSError("no")

    rig = Rig(tmp_path)
    runner = StewardRunner(
        rig.harness, rig.board, FakeStewardBriefing(), FakeReports(), rig.lease, rig.clock,
        tmp_path / "scratch", logging.getLogger("t"), rig.config, identity=Broken(),
    )
    rig.board.pending = [RESTART]
    runner.tick()
    rig.clock.advance(121)
    assert runner.tick() is None
    assert rig.harness.calls == []
    assert not rig.lease.held


def test_the_warning_for_an_identity_that_cannot_be_ensured_carries_the_masked_message(
    tmp_path: Path, caplog: pytest.LogCaptureFixture
) -> None:
    class Broken:
        def ensure(self) -> Path:
            raise OSError("update.sh failed with token s3cr3t-token-value")

    rig = Rig(tmp_path)
    runner = StewardRunner(
        rig.harness, rig.board, FakeStewardBriefing(), FakeReports(), rig.lease, rig.clock,
        tmp_path / "scratch", logging.getLogger("t"), rig.config, identity=Broken(), masker=SecretMasker(["s3cr3t-token-value"]),
    )
    rig.board.pending = [RESTART]
    runner.tick()
    rig.clock.advance(121)
    with caplog.at_level(logging.WARNING, logger="t"):
        runner.tick()

    assert "OSError: update.sh failed with token ***" in caplog.text
    assert "s3cr3t-token-value" not in caplog.text


def test_stop_kills_the_run_at_once(rig: Rig) -> None:
    rig.script(None)
    handle = rig.harness.handles[0]
    rig.harness.on_start = lambda spec: rig.runner.stop()
    rig.board.pending = [RESTART]
    rig.runner.tick()
    rig.clock.advance(121)
    rig.runner.tick()
    assert handle.killed


def test_events_are_kept_when_the_harness_cannot_start(rig: Rig) -> None:
    def boom(spec: RunSpec) -> None:
        raise OSError("no claude")

    rig.harness.on_start = boom
    rig.board.pending = [RESTART]
    rig.runner.tick()
    rig.clock.advance(121)
    with pytest.raises(OSError):
        rig.runner.tick()
    assert not rig.lease.held
    rig.harness.on_start = lambda spec: None
    rig.script(DONE)
    rig.clock.advance(121)
    assert rig.runner.tick() == DONE
    assert "ticket #7" in rig.harness.calls[0].spec.prompt


class FakeModes:
    def __init__(self, mode: str | Exception) -> None:
        self.mode_now = mode
        self.reads = 0

    def mode(self) -> str:
        self.reads += 1
        if isinstance(self.mode_now, Exception):
            raise self.mode_now
        return self.mode_now


class CountingIdentity:
    def __init__(self) -> None:
        self.ensured = 0

    def ensure(self) -> Path:
        self.ensured += 1
        return Path("token.sh")


def runner_with(rig: Rig, tmp_path: Path, modes: FakeModes, identity: CountingIdentity) -> StewardRunner:
    return StewardRunner(
        rig.harness, rig.board, FakeStewardBriefing(), FakeReports(), rig.lease, rig.clock,
        tmp_path / "scratch", logging.getLogger("t"), rig.config, identity=identity, modes=modes,
    )


def test_no_steward_runs_or_enrols_while_the_mode_is_off_and_it_starts_once_it_is_not(tmp_path: Path) -> None:
    rig = Rig(tmp_path)
    modes, identity = FakeModes("off"), CountingIdentity()
    runner = runner_with(rig, tmp_path, modes, identity)
    rig.board.pending = [RESTART]
    rig.script(DONE)
    runner.tick()
    rig.clock.advance(121)

    assert runner.tick() is None
    assert rig.harness.calls == []
    assert identity.ensured == 0
    assert not rig.lease.held

    rig.clock.advance(60)
    assert runner.tick() is None
    assert modes.reads == 1, "an off tenant is looked at once per recheck period, not every tick"

    modes.mode_now = "shadow"
    rig.clock.advance(rig.config.off_recheck)
    assert runner.tick() is not None
    assert identity.ensured == 1
    assert len(rig.harness.calls) == 1


def test_a_mode_that_cannot_be_read_means_no_run_until_the_backoff_passes(tmp_path: Path) -> None:
    rig = Rig(tmp_path)
    identity, modes = CountingIdentity(), FakeModes(OSError("down"))
    runner = runner_with(rig, tmp_path, modes, identity)
    rig.board.pending = [RESTART]
    rig.script(DONE)
    runner.tick()
    rig.clock.advance(121)

    assert runner.tick() is None
    rig.clock.advance(rig.config.backoff - 1)
    assert runner.tick() is None
    assert modes.reads == 1
    assert rig.harness.calls == [] and identity.ensured == 0

    modes.mode_now = "live"
    rig.clock.advance(2)
    assert runner.tick() == DONE, "the event queued before the failure is still run"


class FakeVariants:
    def __init__(self, variant: str) -> None:
        self.text = variant

    def variant(self) -> str:
        return self.text


def runner_with_variants(rig: Rig, tmp_path: Path, variants: FakeVariants) -> StewardRunner:
    return StewardRunner(
        rig.harness, rig.board, FakeStewardBriefing(), FakeReports(), rig.lease, rig.clock,
        tmp_path / "scratch", logging.getLogger("t"), rig.config, variants=variants,
    )


def run_with_variants(rig: Rig, tmp_path: Path, variants: FakeVariants) -> RunOutcome | None:
    runner = runner_with_variants(rig, tmp_path, variants)
    rig.board.pending = [RESTART]
    runner.tick()
    rig.clock.advance(121)
    return runner.tick()


def test_the_run_takes_its_variant_from_the_steward_config(tmp_path: Path) -> None:
    rig = Rig(tmp_path)
    rig.script(DONE)
    assert run_with_variants(rig, tmp_path, FakeVariants("model=claude-opus-4-7,effort=high")) == DONE
    assert rig.harness.calls[0].spec.variant == "model=claude-opus-4-7,effort=high"


def test_an_invalid_variant_means_no_run_and_keeps_the_events(tmp_path: Path) -> None:
    rig = Rig(tmp_path)
    rig.script(DONE)
    variants = FakeVariants("effort=turbo")
    runner = runner_with_variants(rig, tmp_path, variants)
    rig.board.pending = [RESTART]
    runner.tick()
    rig.clock.advance(121)
    assert runner.tick() is None
    assert rig.harness.calls == []
    assert not rig.lease.held

    variants.text = ""
    rig.clock.advance(rig.config.backoff + 1)
    assert runner.tick() == DONE
    assert "ticket #7" in rig.harness.calls[0].spec.prompt


def test_a_variant_reaches_the_claude_env(tmp_path: Path) -> None:
    from slop_haus_supervisor.harness.variant import parse_variant

    h = build_steward_harness(tmp_path, "http://s", StewardConfig(), {}, SecretMasker([]), tmp_path / "token.sh")
    env = h.env(parse_variant("model=claude-opus-4-7,effort=high"))
    assert env["ANTHROPIC_MODEL"] == "claude-opus-4-7" and env["CLAUDE_CODE_EFFORT_LEVEL"] == "high"
