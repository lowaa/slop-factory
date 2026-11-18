from __future__ import annotations

import json
import logging
import shutil
from collections.abc import Sequence
from pathlib import Path

import pytest

from slop_haus_supervisor.harness.base import Harness, OutcomeKind, RunOutcome
from slop_haus_supervisor.harness.factory import UnsupportedHarness
from slop_haus_supervisor.steward.codex_policy import CodexStewardPolicy
from slop_haus_supervisor.steward.codex_probe import CONTROL, ExecResult, SandboxProbe, SandboxUnsafe
from slop_haus_supervisor.steward.runner import StewardConfig, StewardRunner
from slop_haus_supervisor.slophaus.supervisor_api import ApiError, SlotRef
from slop_haus_supervisor.steward.server import BoardStewardStatus
from slop_haus_supervisor.steward.selector import MAX_REASON, StewardHarnessSelector
from slop_haus_supervisor.steward.events import WakeEvent, WakeKind
from slop_haus_supervisor.workspace.harness_config import HarnessConfig, HarnessConfigError
from fakes import FakeClock, FakeHandle, FakeHarness, FakeStewardBriefing

LOG = logging.getLogger("t")


def config(harness: str) -> HarnessConfig:
    parsed = HarnessConfig.parse(json.dumps({"revision": 1, "harness": harness, "settings": {}}).encode())
    assert parsed is not None
    return parsed


class FakeStore:
    def __init__(self, result: HarnessConfig | Exception) -> None:
        self.result = result

    def refresh(self) -> HarnessConfig:
        if isinstance(self.result, Exception):
            raise self.result
        return self.result


class RecordingStatus:
    def __init__(self) -> None:
        self.reports: list[tuple[str, str]] = []

    def report(self, phase: str, reason: str = "") -> None:
        self.reports.append((phase, reason))


class Rig:
    def __init__(self, harness: str) -> None:
        self.store = FakeStore(config(harness))
        self.status = RecordingStatus()
        self.built: list[str] = []
        self.roots: list[Path] = []
        self.built_roots: list[tuple[Path, ...]] = []
        self.refuse: dict[str, Exception] = {}
        self.selector = StewardHarnessSelector(self.store, self.build, lambda: self.roots, self.status, LOG)  # pyright: ignore[reportArgumentType]

    def build(self, name: str, roots: Sequence[Path]) -> Harness:
        self.built.append(name)
        self.built_roots.append(tuple(roots))
        if name in self.refuse:
            raise self.refuse[name]
        return FakeHarness()


def test_an_unset_harness_is_claude_and_is_built_once() -> None:
    rig = Rig("")
    first = rig.selector.current()
    assert rig.selector.current() is first
    assert rig.built == ["claude"]
    assert rig.selector.name == "claude"


def test_a_changed_harness_rebuilds_at_the_next_run() -> None:
    rig = Rig("claude")
    claude = rig.selector.current()
    rig.store.result = config("codex")
    codex = rig.selector.current()
    assert codex is not claude
    assert rig.built == ["claude", "codex"]
    assert rig.selector.name == "codex"


def test_a_codex_steward_is_rebuilt_and_reprobed_when_a_slot_is_added() -> None:
    rig = Rig("codex")
    rig.roots = [Path("/slots/a")]
    rig.selector.current()
    rig.selector.current()
    rig.roots = [Path("/slots/a"), Path("/slots/b")]
    rig.selector.current()
    assert rig.built_roots == [(Path("/slots/a"),), (Path("/slots/a"), Path("/slots/b"))]


def test_a_claude_steward_ignores_the_slot_set() -> None:
    rig = Rig("claude")
    rig.selector.current()
    rig.roots = [Path("/slots/a")]
    rig.selector.current()
    assert rig.built == ["claude"]


def test_a_refused_probe_is_reported_with_what_it_refused_and_is_retried_next_run() -> None:
    rig = Rig("codex")
    rig.refuse["codex"] = SandboxUnsafe(["the shell can open network connections"])
    with pytest.raises(SandboxUnsafe):
        rig.selector.current()
    assert rig.status.reports == [("failed", "Codex steward not started: sandbox probe refused: the shell can open network connections")]
    del rig.refuse["codex"]
    rig.selector.current()
    assert rig.built == ["codex", "codex"]
    assert rig.status.reports[-1] == ("running", ""), "a fixed host clears the board's refusal"


def test_a_harness_with_no_steward_is_reported_and_never_falls_back_to_claude() -> None:
    rig = Rig("pi")
    rig.refuse["pi"] = UnsupportedHarness("the steward cannot run on pi")
    with pytest.raises(UnsupportedHarness):
        rig.selector.current()
    assert rig.built == ["pi"]
    assert rig.status.reports[0][0] == "failed"


def test_a_long_refusal_fits_what_the_server_stores() -> None:
    rig = Rig("codex")
    rig.refuse["codex"] = SandboxUnsafe(["x" * 2000])
    with pytest.raises(SandboxUnsafe):
        rig.selector.current()
    assert len(rig.status.reports[0][1]) == MAX_REASON


def test_an_unreadable_config_keeps_the_harness_already_chosen() -> None:
    rig = Rig("codex")
    rig.selector.current()
    rig.store.result = HarnessConfigError("down")
    rig.selector.current()
    assert rig.built == ["codex"]


# ---- the runner, with the real builder's probe over a fake SandboxExec --------------------------------------------


class FakeSandbox:
    """A sandbox that holds, or leaks the network."""

    def __init__(self, tmp: Path, leaks_network: bool) -> None:
        self.tmp = tmp
        self.leaks_network = leaks_network

    def run(self, argv: Sequence[str], cwd: Path) -> ExecResult:
        script = argv[-1]
        if script == "env":
            return ExecResult(0, "PWD=/x\n")
        if script.startswith("touch"):
            return ExecResult(1, "")
        if "/dev/tcp" in script:
            return ExecResult(0 if self.leaks_network else 1, "connected\n" if self.leaks_network else "")
        if script.startswith("cat"):
            return ExecResult(0, CONTROL) if str(self.tmp / "scratch") in script else ExecResult(1, "")
        raise AssertionError(script)


class Board:
    def __init__(self) -> None:
        self.pending = [WakeEvent(WakeKind.DISCUSSION, 7)]

    def new_events(self) -> Sequence[WakeEvent]:
        out, self.pending = self.pending, []
        return out


class Reports:
    def agreement_report(self) -> str:
        return "none"


class Lease:
    def acquire(self) -> bool:
        return True

    def release(self) -> None:
        pass


def steward_runner(tmp_path: Path, leaks_network: bool, rig: Rig, monkeypatch: pytest.MonkeyPatch) -> tuple[StewardRunner, FakeHarness, FakeClock]:
    # The policy resolves the codex binary; the sandbox is faked, so it need not exist.
    monkeypatch.setattr(shutil, "which", lambda command, *_a, **_k: f"/opt/{command}")  # pyright: ignore[reportUnknownArgumentType, reportUnknownLambdaType]
    clock, claude = FakeClock(), FakeHarness()

    def build(name: str, _roots: Sequence[Path]) -> Harness:
        if name == "claude":
            return claude
        policy = CodexStewardPolicy.for_command(("codex",), tmp_path / "scratch", [tmp_path / "repo"])
        (tmp_path / "scratch").mkdir(exist_ok=True)
        (tmp_path / "forbidden").mkdir(exist_ok=True)
        SandboxProbe(FakeSandbox(tmp_path, leaks_network), policy, [tmp_path / "forbidden"]).check()
        return codex

    codex = FakeHarness()
    selector = StewardHarnessSelector(rig.store, build, lambda: [], rig.status, LOG)  # pyright: ignore[reportArgumentType]
    config = StewardConfig(batch_window=0, run_timeout=60, poll_interval=1, backoff=600)
    runner = StewardRunner(selector, Board(), FakeStewardBriefing(), Reports(), Lease(), clock, tmp_path / "scratch", LOG, config)
    return runner, codex, clock


def test_a_codex_steward_whose_probe_refuses_does_not_run_and_the_owner_is_told(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    rig = Rig("codex")
    runner, codex, _ = steward_runner(tmp_path, leaks_network=True, rig=rig, monkeypatch=monkeypatch)
    assert runner.tick() is None
    assert codex.calls == []
    assert len(rig.status.reports) == 1
    phase, reason = rig.status.reports[0]
    assert phase == "failed"
    assert reason.startswith("Codex steward not started: sandbox probe refused:") and "network" in reason


def test_a_codex_steward_whose_probe_passes_runs_and_is_reported_running(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    rig = Rig("codex")
    runner, codex, clock = steward_runner(tmp_path, leaks_network=False, rig=rig, monkeypatch=monkeypatch)
    codex.handles.append(FakeHandle(clock, "s", [RunOutcome(OutcomeKind.FINISHED)]))
    assert runner.tick() is not None
    assert len(codex.calls) == 1
    assert rig.status.reports == [("running", "")]


# ---- BoardStewardStatus -------------------------------------------------------------------------------------------


class FakeApi:
    def __init__(self, fail: bool = False) -> None:
        self.fail = fail
        self.sent: list[list[dict[str, object]]] = []

    def ensure_steward(self) -> SlotRef:
        return SlotRef("steward-id", "steward")

    def report_observed(self, reports: list[dict[str, object]]) -> None:
        if self.fail:
            raise ApiError("down")
        self.sent.append(reports)


def test_the_board_gets_the_steward_id_phase_and_reason_and_no_generation() -> None:
    api = FakeApi()
    status = BoardStewardStatus(api, LOG)  # pyright: ignore[reportArgumentType]
    status.report("failed", "why")
    status.report("running")
    assert api.sent == [[{"id": "steward-id", "phase": "failed", "reason": "why"}], [{"id": "steward-id", "phase": "running"}]]


def test_a_report_the_board_cannot_take_is_logged_not_raised() -> None:
    BoardStewardStatus(FakeApi(fail=True), LOG).report("failed", "why")  # pyright: ignore[reportArgumentType]
