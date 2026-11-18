"""Converges this host on the board's desired state, and reports how far it got.

Every directory is derived from an agent's name through `Layout`, and nothing else in the document is
used as a path or a command. While the tenant's `provision_from_board` is off the server's document is
empty, so there is nothing to do.
"""

from __future__ import annotations

import logging
import os
import subprocess
from collections.abc import Callable, Mapping, Sequence
from dataclasses import dataclass
from pathlib import Path
from typing import Protocol

from slop_haus_supervisor.fleet.host_retirement import HostRetirement
from slop_haus_supervisor.harness import registry
from slop_haus_supervisor.harness.tool_check import ToolChecker, ToolLookup
from slop_haus_supervisor.reconcile.document import DesiredAgent, parse
from slop_haus_supervisor.reconcile.ledger import ResumeLedger
from slop_haus_supervisor.slophaus.transport import ApiError
from slop_haus_supervisor.workspace import config as cfg
from slop_haus_supervisor.workspace import layout as lay
from slop_haus_supervisor.workspace.layout import Layout, SlotService, UnpushedCheck
from slop_haus_supervisor.workspace.provisioned import Kind

MAX_REASON = 500
# A failed provision or removal is not tried again before this, unless the owner changed the entry.
RETRY_AFTER = 300.0
# The daemon has no terminal: a prompt must fail the setup, not wait for a keyboard.
SETUP_TIMEOUT = 15 * 60.0


class Api(SlotService, Protocol):
    def desired_state(self) -> dict[str, object]: ...

    def report_observed(self, reports: list[dict[str, object]]) -> None: ...


class Host(Protocol):
    """The running fleet, as the scheduler holds it."""

    def slot_names(self) -> set[str]: ...

    def start_slot(self, name: str, harness: str) -> None:
        """Begin stepping a slot whose directory and credential are in place, running `harness`; raises ConfigError if it cannot."""
        ...

    def harness_of(self, name: str) -> str:
        """The harness a running slot or host was started with."""
        ...

    def start_host(self, name: str, harness: str, held: bool) -> None:
        """Begin stepping a Remote Control host slot; raises ConfigError for a name or harness it cannot run."""
        ...

    def hold(self, name: str, held: bool) -> None: ...

    def pause_reason(self, name: str) -> str | None: ...

    def unpause(self, names: list[str] | None) -> Mapping[str, str]: ...

    def retire(self, name: str) -> bool:
        """No new work for the slot; True once its run and aftercare have ended and it is gone."""
        ...


def headless_run(argv: list[str], cwd: Path) -> int:
    try:
        return subprocess.run(
            argv, cwd=cwd, check=False, stdin=subprocess.DEVNULL, timeout=SETUP_TIMEOUT, env=_no_prompt_env()
        ).returncode
    except subprocess.TimeoutExpired:
        return 124


def _no_prompt_env() -> dict[str, str]:
    return {**os.environ, "GIT_TERMINAL_PROMPT": "0"}


@dataclass(frozen=True)
class _Report:
    generation: int
    phase: str
    reason: str = ""
    # The harness a started slot runs; empty when none is, as for a retired one.
    harness: str = ""

    def body(self, agent_id: str) -> dict[str, object]:
        out: dict[str, object] = {"id": agent_id, "generation": self.generation, "phase": self.phase}
        if self.reason:
            out["reason"] = self.reason[:MAX_REASON]
        if self.harness:
            out["harness"] = self.harness
        return out


class Reconciler:
    def __init__(
        self,
        layout: Layout,
        api: Api,
        host: Host,
        repo: UnpushedCheck,
        ledger: ResumeLedger,
        logger: logging.Logger,
        monotonic: Callable[[], float],
        run: lay.Interactive = headless_run,
        host_retirement: HostRetirement | None = None,
        tools: ToolChecker | None = None,
        extra_tools: Callable[[], Sequence[str]] = lambda: (),
    ) -> None:
        self._layout = layout
        self._api = api
        self._host = host
        self._repo = repo
        self._ledger = ledger
        self._log = logger
        self._monotonic = monotonic
        self._run = run
        # Without it the supervisor runs no Remote Control host, and every one is the owner's as before.
        self._host_retirement = host_retirement
        # Without it no tool is checked, as before.
        self._tools = tools
        self._extra_tools = extra_tools
        self._sent: dict[str, _Report] = {}
        # agent id -> (generation, when) of a failure not to be repeated at once.
        self._failed: dict[str, tuple[int, float]] = {}

    def tick(self) -> None:
        document = parse(self._api.desired_state())
        reports = {r.id: _Report(r.generation, "failed", r.reason) for r in document.rejected if r.id is not None}
        for rejected in document.rejected:
            self._log.warning("reconcile: entry refused: %r", rejected.reason)
        for agent in document.agents:
            reports[agent.id] = self._apply(agent)
        self._send(reports)

    def _send(self, reports: dict[str, _Report]) -> None:
        changed = {agent_id: r for agent_id, r in reports.items() if self._sent.get(agent_id) != r}
        if not changed:
            return
        self._api.report_observed([r.body(agent_id) for agent_id, r in changed.items()])
        self._sent.update(changed)

    def _say(self, agent: DesiredAgent, phase: str, reason: str = "") -> None:
        """Out at once, so the board shows work that takes a while."""
        self._send({agent.id: _Report(agent.generation, phase, reason)})

    def _apply(self, agent: DesiredAgent) -> _Report:
        try:
            name = cfg.check_name(agent.kind.value, agent.name)
            if agent.supervisor_launched and self._host_retirement is not None:
                return self._remove_host(agent, name) if agent.removed else self._keep(agent, name, self._start_host)
            if agent.kind is Kind.REMOTE_CONTROL and self._host_retirement is not None and name in self._host.slot_names():
                # Handed back to the owner (or removed after being handed back): the host this supervisor started ends first.
                if not self._host.retire(name):
                    return _Report(agent.generation, "provisioning", "waiting for the host's current session to end")
            if agent.kind is not Kind.SLOT:
                # The supervisor holds no identity for these: the steward runs by itself, and a manually launched
                # Remote Control host is run by the owner where they want it.
                return _Report(agent.generation, "retired" if agent.removed else "running")
            if agent.removed:
                return self._remove(agent, name)
            if agent.harness not in registry.WORKER_NAMES:
                return _Report(agent.generation, "failed", f"the {agent.harness} harness is not available for slots on this host")
            return self._keep(agent, name, self._provision)
        except (cfg.ConfigError, ApiError, OSError) as err:
            self._log.warning("reconcile: %r: %s", agent.name[:100], err)
            self._failed[agent.id] = (agent.generation, self._monotonic())
            return _Report(agent.generation, "failed", str(err))

    def _backing_off(self, agent: DesiredAgent) -> bool:
        failed = self._failed.get(agent.id)
        return failed is not None and failed[0] == agent.generation and self._monotonic() - failed[1] < RETRY_AFTER

    def _keep(self, agent: DesiredAgent, name: str, start: Callable[[DesiredAgent, str], None]) -> _Report:
        if name in self._host.slot_names() and self._host.harness_of(name) != agent.harness:
            # Drained like a removal: a live run finishes, then the agent is started with the new harness, so it never changes mid-run.
            if not self._host.retire(name):
                return _Report(agent.generation, "provisioning", "waiting for the current run to end before changing the harness", self._host.harness_of(name))
        if name not in self._host.slot_names():
            if self._backing_off(agent):
                return self._sent.get(agent.id) or _Report(agent.generation, "failed", "provisioning failed; trying again later")
            missing = self._harness_problem(agent)
            if missing:
                # Not tried again before RETRY_AFTER: the supervisor's PATH is fixed until it restarts, so looking sooner finds the same.
                self._failed[agent.id] = (agent.generation, self._monotonic())
                return _Report(agent.generation, "failed", missing)
            self._say(agent, "provisioning")
            start(agent, name)
            self._failed.pop(agent.id, None)
        self._host.hold(name, agent.state == "paused")
        if self._ledger.raised(agent.id, agent.resume_generation) and agent.state != "paused":
            self._host.unpause([name])
        harness = self._host.harness_of(name)
        if agent.state == "paused":
            return _Report(agent.generation, "paused", "held by the owner", harness)
        reason = self._host.pause_reason(name)
        return _Report(agent.generation, "paused", reason, harness) if reason is not None else _Report(agent.generation, "running", self._extra_tools_warning(agent), harness)

    def _lookup(self, name: str) -> ToolLookup:
        assert self._tools is not None
        return self._tools.lookup(name)

    def _harness_problem(self, agent: DesiredAgent) -> str:
        if self._tools is None:
            return ""
        return self._lookup(agent.harness).problem(self._layout.root / "run.env")

    def _extra_tools_warning(self, agent: DesiredAgent) -> str:
        """The slot still works without these, so this is a warning on a running slot, not a failure."""
        if self._tools is None:
            return ""
        run_env = self._layout.root / "run.env"
        problems = [p for p in (self._lookup(tool).problem(run_env) for tool in self._extra_tools()) if p]
        return "warning: " + "; ".join(problems) + " (steps that need it will fail)" if problems else ""

    def _start_host(self, agent: DesiredAgent, name: str) -> None:
        # The slot makes and updates its own workspace when it starts, so a bad one is the slot's to report.
        self._host.start_host(name, agent.harness, agent.state == "paused")

    def _provision(self, agent: DesiredAgent, name: str) -> None:
        directory = self._layout.slot(name)
        config = cfg.load(self._layout.config_file)
        # A directory set up by an earlier run of this supervisor is kept: `ensure` sets a slot up afresh.
        if name not in config.slots or not directory.is_dir():
            lay.add_slot(self._layout, name, None, run=self._run, service=self._api)
        self._host.start_slot(name, agent.harness)

    def _remove_host(self, agent: DesiredAgent, name: str) -> _Report:
        assert self._host_retirement is not None
        retirement = self._host_retirement
        return self._retire(agent, name, lambda: retirement.retire(name))

    def _remove(self, agent: DesiredAgent, name: str) -> _Report:
        return self._retire(agent, name, lambda: self._remove_slot(name))

    def _remove_slot(self, name: str) -> None:
        directory = self._layout.slot(name)
        known = name in cfg.load(self._layout.config_file).slots or directory.exists() or self._api.find_slot(name) is not None
        if known:
            lay.remove_slot(self._layout, name, self._api, self._repo)

    def _retire(self, agent: DesiredAgent, name: str, finish: Callable[[], object]) -> _Report:
        done = self._sent.get(agent.id)
        if done is not None and done.generation == agent.generation and done.phase == "retired":
            return done
        if self._backing_off(agent):
            return self._sent.get(agent.id) or _Report(agent.generation, "failed", "removal refused; trying again later")
        # Drain first: the run in progress and its aftercare end before anything is revoked or deleted.
        if not self._host.retire(name):
            return _Report(agent.generation, "provisioning", "waiting for the slot's current run to end")
        finish()
        self._failed.pop(agent.id, None)
        return _Report(agent.generation, "retired")
