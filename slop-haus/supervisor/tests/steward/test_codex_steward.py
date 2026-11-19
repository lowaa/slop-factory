from __future__ import annotations

import json
import os
import shutil
import sys
import tomllib
from collections.abc import Sequence
from typing import Any
from pathlib import Path

import pytest

from slop_haus_supervisor.harness.base import OutcomeKind, RunOutcome, RunSpec
from slop_haus_supervisor.harness.process import ProcessRun
from slop_haus_supervisor.steward.codex_harness import CodexStewardHarness
from slop_haus_supervisor.steward.codex_policy import PROFILE, CodexStewardPolicy, allowed_mcp_tools
from slop_haus_supervisor.steward.codex_probe import (
    CANARY,
    CANARY_PREFIX,
    CONTROL,
    CodexAppServerExec,
    ExecResult,
    SandboxProbe,
    SandboxUnsafe,
)
from slop_haus_supervisor.steward.guard import STEWARD_ALLOWED_TOOLS

WAIT = 30.0
THREAD = "0192f5a0-1111-7222-8333-444455556666"

FAKE_STEWARD_CODEX = r'''
import json, os, sys
log = os.path.join(os.environ["FAKE_DIR"], "received.jsonl")
calls = int(os.environ.get("FAKE_CALLS", "0"))
profile = os.environ.get("FAKE_PROFILE", "steward")
def out(r): print(json.dumps(r), flush=True)
def done(status): out({"method": "turn/completed", "params": {"threadId": "%s", "turn": {"id": "t1", "status": status, "items": []}}})
for line in sys.stdin:
    m = json.loads(line)
    open(log, "a").write(json.dumps(m) + "\n")
    method, rid = m.get("method"), m.get("id")
    if method == "initialize":
        out({"id": rid, "result": {}})
    elif method == "thread/start":
        out({"id": rid, "result": {"thread": {"id": "%s"}, "model": "gpt-steward-1", "activePermissionProfile": {"id": profile}}})
    elif method == "turn/start":
        out({"id": rid, "result": {"turn": {"id": "t1", "status": "inProgress"}}})
        out({"method": "turn/started", "params": {"threadId": "%s", "turn": {"id": "t1"}}})
        out({"method": "item/started", "params": {"item": {"type": "agentMessage"}}})
        if os.environ.get("FAKE_USAGE") == "1":
            b = {"inputTokens": 70, "cachedInputTokens": 0, "cacheWriteInputTokens": 0, "outputTokens": 9, "reasoningOutputTokens": 0}
            out({"method": "thread/tokenUsage/updated", "params": {"threadId": "%s", "tokenUsage": {"total": b, "last": b}}})
        for n in range(calls):
            out({"method": "item/started", "params": {"item": {"type": "commandExecution", "id": str(n)}}})
        if os.environ.get("FAKE_STUBBORN") != "1" and calls <= int(os.environ.get("FAKE_BUDGET", "99")):
            done("completed")
    elif method == "turn/interrupt":
        done("interrupted")
''' % (THREAD, THREAD, THREAD, THREAD)


def make_policy(tmp: Path, repos: tuple[Path, ...] = ()) -> CodexStewardPolicy:
    return CodexStewardPolicy(tmp / "bin" / "codex", tmp / "scratch", repos)


# ---- policy -----------------------------------------------------------------------------------------------------


def test_policy_config_is_read_only_scrubbed_and_allowlisted(tmp_path: Path) -> None:
    repo = tmp_path / "repo"
    config = make_policy(tmp_path, (repo,)).config()
    assert config["default_permissions"] == PROFILE
    assert config["shell_environment_policy.inherit"] == "none"
    assert config["shell_environment_policy.set"] == {"PATH": "/usr/bin:/bin"}
    assert config[f"permissions.{PROFILE}.network.enabled"] is False
    assert config["mcp_servers.slop-haus.enabled_tools"] == allowed_mcp_tools()
    assert set(allowed_mcp_tools()) == {t.removeprefix("mcp__slop-haus__") for t in STEWARD_ALLOWED_TOOLS if t.startswith("mcp__")}
    fs = config[f"permissions.{PROFILE}.filesystem"]
    assert fs[str(repo)] == "read" and fs[str(repo / ".env")] == "none"
    assert set(fs.values()) <= {"read", "none"}, "nothing is writable"
    assert config["features.multi_agent"] is False and config["web_search"] == "disabled"


def test_flags_are_toml_that_parses_back_to_the_config(tmp_path: Path) -> None:
    policy = make_policy(tmp_path, (tmp_path / "it's a repo",))
    flags = policy.flags()
    sandbox = {k: v for k, v in policy.config().items() if not k.startswith("mcp_servers.")}
    assert flags[::2] == ["-c"] * len(sandbox)
    for flag, (key, value) in zip(flags[1::2], sandbox.items(), strict=True):
        assert flag.startswith(key + "=")
        parsed = tomllib.loads(f"v = {flag[len(key) + 1:]}")["v"]
        assert parsed == value


# ---- the harness ------------------------------------------------------------------------------------------------


def harness(tmp_path: Path, max_calls: int, **env: str) -> CodexStewardHarness:
    script = tmp_path / "fake.py"
    script.write_text(FAKE_STEWARD_CODEX)
    log = tmp_path / "log"
    log.mkdir(exist_ok=True)
    environ = {"PATH": os.environ["PATH"], "FAKE_DIR": str(log), **env}
    return CodexStewardHarness(tmp_path / "root", "https://s/", make_policy(tmp_path), max_calls,
                               command=[sys.executable, str(script)], environ=environ, grace=2.0, start_timeout=WAIT)


def run(h: CodexStewardHarness, tmp_path: Path) -> RunOutcome:
    scratch = tmp_path / "scratch"
    scratch.mkdir(exist_ok=True)
    handle = h.start(RunSpec("steward", scratch, 0, "", "wake", "", "briefing"))
    assert isinstance(handle, ProcessRun)
    try:
        outcome = handle.wait(WAIT)
    finally:
        handle.kill()
    assert outcome is not None
    return outcome


def received(tmp_path: Path) -> list[dict[str, Any]]:
    return [json.loads(line) for line in (tmp_path / "log" / "received.jsonl").read_text().splitlines()]


def test_thread_is_started_under_the_profile_with_no_legacy_sandbox_override(tmp_path: Path) -> None:
    assert run(harness(tmp_path, 40), tmp_path).kind is OutcomeKind.FINISHED
    params = next(m for m in received(tmp_path) if m.get("method") == "thread/start")["params"]
    assert "sandbox" not in params and params["approvalPolicy"] == "never"
    assert params["config"]["default_permissions"] == PROFILE
    assert "mcp_servers.slop-haus.disabled_tools" not in params["config"]


def test_process_carries_the_policy_flags(tmp_path: Path) -> None:
    h = harness(tmp_path, 40)
    spec = RunSpec("steward", tmp_path, 0, "", "p")
    argv = h.plan(spec).argv
    assert argv[-1] == "app-server" and "default_permissions=\"steward\"" in argv


def test_a_tool_call_budget_interrupts_the_turn(tmp_path: Path) -> None:
    outcome = run(harness(tmp_path, 3, FAKE_CALLS="5", FAKE_BUDGET="3"), tmp_path)
    assert outcome.kind is OutcomeKind.OVER_BUDGET
    interrupts = [m for m in received(tmp_path) if m.get("method") == "turn/interrupt"]
    assert len(interrupts) == 1 and interrupts[0]["params"] == {"threadId": THREAD, "turnId": "t1"}


def test_within_the_budget_the_turn_is_not_interrupted(tmp_path: Path) -> None:
    outcome = run(harness(tmp_path, 3, FAKE_CALLS="3", FAKE_BUDGET="3"), tmp_path)
    assert outcome.kind is OutcomeKind.FINISHED
    assert not [m for m in received(tmp_path) if m.get("method") == "turn/interrupt"]


def test_a_thread_without_the_steward_profile_is_not_run(tmp_path: Path) -> None:
    outcome = run(harness(tmp_path, 40, FAKE_PROFILE=":workspace"), tmp_path)
    assert outcome.kind is OutcomeKind.CRASHED and "permission profile" in outcome.detail
    assert not [m for m in received(tmp_path) if m.get("method") == "turn/start"]


# ---- the probe, against a model of the sandbox -------------------------------------------------------------------


class FakeSandbox:
    """Answers the probe's commands like a sandbox that holds, unless a hole is switched on."""

    def __init__(self, tmp: Path, holes: frozenset[str]) -> None:
        self.tmp = tmp
        self.holes = holes

    def run(self, argv: Sequence[str], cwd: Path) -> ExecResult:
        script = argv[-1]
        if "broken" in self.holes:
            raise RuntimeError("no sandbox")
        if script == "env":
            leaked = "".join(f"{h.removeprefix('env:')}=v\n" for h in self.holes if h.startswith("env:"))
            return ExecResult(0, "PWD=/x\n" + leaked)
        if script.startswith("touch"):
            wrote = "write" in self.holes
            return ExecResult(0 if wrote else 1, "wrote\n" if wrote else "")
        if "/dev/tcp" in script:
            connected = "network" in self.holes
            return ExecResult(0 if connected else 1, "connected\n" if connected else "")
        if script.startswith("cat"):
            if f"{self.tmp}/scratch/" in script:
                return ExecResult(1, "") if "no_control" in self.holes else ExecResult(0, CONTROL)
            return ExecResult(0, CANARY) if "read_forbidden" in self.holes else ExecResult(1, "", "No such file")
        raise AssertionError(script)


def probe(tmp_path: Path, *holes: str) -> list[str]:
    forbidden = tmp_path / "home"
    forbidden.mkdir(exist_ok=True)
    repo = tmp_path / "repo"
    repo.mkdir(exist_ok=True)
    policy = make_policy(tmp_path, (repo,))
    try:
        SandboxProbe(FakeSandbox(tmp_path, frozenset(holes)), policy, [forbidden, tmp_path / "missing"]).check()
    except SandboxUnsafe as err:
        return err.problems
    assert not list(forbidden.glob(CANARY_PREFIX + "*")), "canaries are removed"
    return []


def test_a_sandbox_that_holds_passes(tmp_path: Path) -> None:
    assert probe(tmp_path) == []


@pytest.mark.parametrize(
    ("hole", "mentions"),
    [
        ("read_forbidden", "home is readable"),
        ("env:GITHUB_TOKEN", "GITHUB_TOKEN"),
        ("env:CODEX_HOME", "CODEX_HOME"),
        ("write", "can write"),
        ("network", "network"),
        ("no_control", "control read"),
        ("broken", "could not be asked"),
    ],
)
def test_each_hole_makes_the_probe_refuse(tmp_path: Path, hole: str, mentions: str) -> None:
    problems = probe(tmp_path, hole)
    assert problems and any(mentions in p for p in problems)


# ---- the probe, against the real Codex sandbox -------------------------------------------------------------------

CODEX = shutil.which("codex")
# macOS has no /usr/bin/cat; on Linux the sandbox binds /usr.
CAT = "/bin/cat" if sys.platform == "darwin" else "/usr/bin/cat"


class Real:
    def __init__(self, tmp: Path, policy: CodexStewardPolicy) -> None:
        self.policy = policy
        self.exec = CodexAppServerExec(("codex",), policy, {"PATH": os.environ["PATH"], "CODEX_HOME": str(tmp / "codex_home")})


def real(tmp: Path, repos: tuple[Path, ...] = ()) -> Real:
    (tmp / "codex_home").mkdir(exist_ok=True)
    return Real(tmp, CodexStewardPolicy.for_command(("codex",), tmp / "scratch", repos))


@pytest.mark.skipif(CODEX is None, reason="codex is not installed")
def test_the_real_sandbox_passes_and_hides_the_host(tmp_path: Path) -> None:
    repo = tmp_path / "repo"
    repo.mkdir()
    (repo / "f.txt").write_text("code")
    (repo / ".env").write_text("KEY=1")
    r = real(tmp_path, (repo,))
    forbidden = [tmp_path / "home", tmp_path / "codex_home"]
    forbidden[0].mkdir()
    SandboxProbe(r.exec, r.policy, forbidden).check()
    ok = r.exec.run([CAT, str(repo / "f.txt")], tmp_path)
    assert ok.stdout == "code", "the review checkout is readable"
    assert r.exec.run([CAT, str(repo / ".env")], tmp_path).exit_code != 0, "its key file is not"


@pytest.mark.skipif(CODEX is None, reason="codex is not installed")
def test_the_real_probe_refuses_when_a_forbidden_dir_is_granted(tmp_path: Path) -> None:
    home = tmp_path / "home"
    home.mkdir()
    r = real(tmp_path, (home,))  # the hole: the forbidden directory is a "review checkout"
    with pytest.raises(SandboxUnsafe, match="home is readable"):
        SandboxProbe(r.exec, r.policy, [home]).check()


@pytest.mark.skipif(CODEX is None, reason="codex is not installed")
def test_the_real_probe_refuses_an_unscrubbed_environment(tmp_path: Path) -> None:
    class Inherit(CodexStewardPolicy):
        def config(self) -> dict[str, Any]:
            return {**super().config(), "shell_environment_policy.inherit": "all"}

    (tmp_path / "codex_home").mkdir()
    policy = Inherit(CodexStewardPolicy.for_command(("codex",), tmp_path / "scratch").codex_executable, tmp_path / "scratch")
    sandbox = CodexAppServerExec(("codex",), policy, {"PATH": os.environ["PATH"], "CODEX_HOME": str(tmp_path / "codex_home")})
    with pytest.raises(SandboxUnsafe, match="environment"):
        SandboxProbe(sandbox, policy, []).check()


def test_a_turn_that_finishes_over_budget_is_over_budget(tmp_path: Path) -> None:
    outcome = run(harness(tmp_path, 3, FAKE_CALLS="5", FAKE_BUDGET="99"), tmp_path)
    assert outcome.kind is OutcomeKind.OVER_BUDGET


def test_the_probe_does_not_clobber_or_follow_what_it_did_not_make(tmp_path: Path) -> None:
    forbidden = tmp_path / "home"
    forbidden.mkdir()
    victim = tmp_path / "victim"
    victim.write_text("keep")
    mine = make_policy(tmp_path)
    SandboxProbe(FakeSandbox(tmp_path, frozenset()), mine, [forbidden]).check()
    (forbidden / "other-file").write_text("keep")
    SandboxProbe(FakeSandbox(tmp_path, frozenset()), mine, [forbidden]).check()
    assert (forbidden / "other-file").read_text() == "keep" and victim.read_text() == "keep"


# ---- build_steward_harness ---------------------------------------------------------------------------------------


def test_the_builder_probes_before_returning_a_codex_steward(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    from slop_haus_supervisor.harness.text import SecretMasker
    from slop_haus_supervisor.steward import harness as builder
    from slop_haus_supervisor.steward.runner import StewardConfig

    def which(command: str) -> str:
        return f"/opt/{command}"

    monkeypatch.setattr(shutil, "which", which)
    seen: list[tuple[Sequence[Path], Sequence[Path]]] = []

    class Refusing:
        def __init__(self, sandbox: object, policy: CodexStewardPolicy, forbidden: Sequence[Path]) -> None:
            seen.append((policy.review_roots, forbidden))

        def check(self) -> None:
            raise SandboxUnsafe(["held open"])

    monkeypatch.setattr(builder, "SandboxProbe", Refusing)
    repo, home = tmp_path / "repo", tmp_path / "home"
    with pytest.raises(SandboxUnsafe):
        builder.build_steward_harness(tmp_path, "https://s", StewardConfig(), {}, SecretMasker(), tmp_path / "t.sh", "codex", [repo], [home])
    assert seen == [((repo,), [home])]


def test_the_builder_refuses_a_harness_whose_steward_is_no_before_building(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    from slop_haus_supervisor.harness.factory import UnsupportedHarness
    from slop_haus_supervisor.harness.text import SecretMasker
    from slop_haus_supervisor.steward import harness as builder
    from slop_haus_supervisor.steward.runner import StewardConfig

    def unreachable(*_args: object, **_kwargs: object) -> object:
        raise AssertionError("a steward of 'no' reached the factory")

    monkeypatch.setattr(builder, "build_harness", unreachable)
    with pytest.raises(UnsupportedHarness, match="steward"):
        builder.build_steward_harness(tmp_path, "https://s", StewardConfig(), {}, SecretMasker(), tmp_path / "t.sh", "pi")


def test_an_over_budget_turn_keeps_the_usage_it_spent_under_the_logins_billing_mode(tmp_path: Path) -> None:
    home = tmp_path / "root" / "harness" / "codex"
    home.mkdir(parents=True)
    (home / "auth.json").write_text(json.dumps({"auth_mode": "apikey"}))
    outcome = run(harness(tmp_path, 3, FAKE_CALLS="5", FAKE_BUDGET="3", FAKE_USAGE="1"), tmp_path)
    assert outcome.kind is OutcomeKind.OVER_BUDGET
    (entry,) = outcome.usage.models
    assert (entry.model, entry.billing_mode, entry.counters["input"], entry.counters["output"]) == ("gpt-steward-1", "metered", 70, 9)
