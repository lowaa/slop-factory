"""One Remote Control host the supervisor runs for an agent, kept alive and stepped like a worker slot.

`step()` never sleeps: it does what is due and returns the seconds until it next has something to do, so
tests drive it with a fake clock and a fake spawner.
"""

from __future__ import annotations

import logging
from collections.abc import Callable, Mapping
from dataclasses import dataclass, field
from pathlib import Path
from typing import Protocol

from slop_haus_supervisor.fleet.child_process import Child, ChildSpawner
from slop_haus_supervisor.fleet.pause import PAUSE_RECHECK_INTERVAL, PauseState
from slop_haus_supervisor.fleet.phase import Phase, Reason
from slop_haus_supervisor.fleet.policy import PolicyConfig
from slop_haus_supervisor.fleet.slot_log import SlotLog
from slop_haus_supervisor.harness.text import SecretMasker
from slop_haus_supervisor.ports import Alerts, Clock
from slop_haus_supervisor.workspace.host_files import read_idle
from slop_haus_supervisor.workspace.host_files import supervisor_lock
from slop_haus_supervisor.service.instance_lock import AlreadyRunning
from slop_haus_supervisor.service import run_env
from slop_haus_supervisor.slophaus.client import SlotSnapshot
from slop_haus_supervisor.workspace.layered_env import AgentKind, EnvKind, LayeredEnv
from slop_haus_supervisor.workspace.layout import Layout

CRASH_LOOP = "crash_loop"
# Tells a host the supervisor started where the supervisor's harness logins are (<root>/harness/*). Set by the slot
# after the layers, so a layer cannot point a host at another login; a host started by hand never has it.
SHARED_LOGIN_ROOT = "SLOP_HAUS_SHARED_LOGIN_ROOT"
IDLE_MARKER = Path(".slop-haus/conductor/idle")

# What of the supervisor's own environment a host may have. Everything else of it (its tokens, its settings, a
# PYTHONPATH) stays behind: the host's environment is built up from these and the agent's layered env files.
INHERITED = frozenset(
    "PATH HOME USER LOGNAME SHELL LANG LC_ALL LC_CTYPE TZ TMPDIR XDG_CACHE_HOME XDG_CONFIG_HOME XDG_DATA_HOME "
    "XDG_RUNTIME_DIR SSH_AUTH_SOCK".split()
)


class HostWorkspace(Protocol):
    """The agent's directory (`RemoteControlSlot.directory`), as a Remote Control kind's `Provisioned` keeps it."""

    def ensure(self) -> None:
        """Signs the directory in, lays down its files and clones the manifest's repos into it; raises when it cannot."""
        ...

    def refresh(self) -> None:
        """The between-sessions sync: brings the workspace's files up to date; a failure leaves the old ones."""
        ...


# What is missing from the host's login for a harness, given the agent's secrets; "" when nothing is.
MissingLogin = Callable[[str, Mapping[str, str]], str]


@dataclass(frozen=True)
class RemoteControlConfig:
    # The worker slot's constants: the nth crash in a row waits the nth, the last repeating.
    backoffs: tuple[float, ...] = PolicyConfig().backoffs
    # A session this long was a working one: the crashes before it are forgotten.
    min_session: float = 60.0
    # Sessions shorter than that, in a row, that put the slot in a crash loop.
    crash_loop_failures: int = 5
    poll_interval: float = 2.0
    # How long a host is given to stop on SIGTERM before it is killed.
    stop_grace: float = 30.0
    pause_recheck_interval: float = PAUSE_RECHECK_INTERVAL


@dataclass(frozen=True)
class RemoteControlDeps:
    layout: Layout
    workspace: HostWorkspace
    spawner: ChildSpawner
    clock: Clock
    alerts: Alerts
    missing_login: MissingLogin
    environ: Mapping[str, str]
    logger: logging.Logger
    masker: SecretMasker = field(default_factory=SecretMasker)
    config: RemoteControlConfig = field(default_factory=RemoteControlConfig)


def host_environment(layout: Layout, agent: str, inherited: Mapping[str, str]) -> dict[str, str]:
    """Built up from nothing: the allowlisted part of the supervisor's, then the agent's secrets and run.env layers."""
    env = {name: value for name, value in inherited.items() if name in INHERITED or name == run_env.CODEX_SHARED_LOGIN}
    run_env.apply_git_defaults(env)
    env.update(LayeredEnv(layout, AgentKind.REMOTE_CONTROL, agent).process_env())
    env[SHARED_LOGIN_ROOT] = str(layout.root)
    return env


class RemoteControlSlot:
    def __init__(self, agent: str, harness: str, deps: RemoteControlDeps) -> None:
        self.agent = agent
        self.harness = harness
        self.directory = deps.layout.remote_control_workspace(agent)
        self._d = deps
        self._phase = Phase.IDLE
        self._phase_since = deps.clock.now()
        self._log = SlotLog(agent, deps.logger, deps.masker, deps.clock)
        self._pause = PauseState(self, self._log, deps.alerts, deps.clock, deps.config.pause_recheck_interval)
        self._child: Child | None = None
        self._started_at = 0.0
        self._wake_at = 0.0
        self._crashes = 0
        self._instant_failures = 0
        self._last_exit = ""
        # Why the last start could not set the workspace up, for the board; "" once one has.
        self._start_error = ""
        self._idle = True
        self._abandoned = False
        self._draining = False
        self._held = False

    @property
    def phase(self) -> Phase:
        return self._phase

    @phase.setter
    def phase(self, value: Phase) -> None:
        if value != self._phase:
            self._log.info("phase=%s from=%s", value.value, self._phase.value)
            self._phase = value
            self._phase_since = self._d.clock.now()

    @property
    def phase_since(self) -> float:
        return self._phase_since

    def step(self) -> float:
        if self._abandoned:
            return self._d.config.poll_interval
        match self.phase:
            case Phase.PAUSED:
                if self._pause.recheck_due():
                    self.try_unpause()
            case Phase.IDLE:
                self._start()
            case Phase.BACKOFF:
                if self._d.clock.now() >= self._wake_at:
                    self._start()
            case Phase.RUNNING:
                self._watch()
            case _:
                raise AssertionError(f"a host is never in phase {self.phase}")
        return self._next_step_in()

    def _next_step_in(self) -> float:
        match self.phase:
            case Phase.PAUSED:
                return self._pause.seconds_to_recheck()
            case Phase.BACKOFF:
                return max(0.0, self._wake_at - self._d.clock.now())
            case Phase.RUNNING:
                return self._d.config.poll_interval
            case _:
                return 0.0

    def _start(self) -> None:
        if self._draining or self._held:
            # No new session: a host is only started for an owner who has not held it or a supervisor still serving.
            self.phase = Phase.IDLE
            return
        # Raises are the scheduler's to retry: a workspace that cannot be set up fails the same way each time.
        try:
            self._d.workspace.ensure()
        except Exception as err:
            self._start_error = self._log.clean(str(err))
            raise
        self._start_error = ""
        directory = self.directory
        if not self._free_of_hand_launch(directory):
            return
        self._d.workspace.refresh()
        secrets = LayeredEnv(self._d.layout, AgentKind.REMOTE_CONTROL, self.agent).values(EnvKind.SECRETS)
        missing = self._d.missing_login(self.harness, secrets)
        if missing:
            self._pause.begin(f"needs secret {missing} on host", self._login_missing)
            return
        env = host_environment(self._d.layout, self.agent, self._d.environ)
        argv = [
            "uv", "run", "--frozen", "--no-dev", "--project", str(self._d.layout.app),
            "slop-haus-remote-control", "--workspace", str(directory), "--harness", self.harness, "--python-launch", "--supervised",
        ]
        self._d.masker.extend(env.values())
        # The last host's word is not this one's.
        (directory / IDLE_MARKER).unlink(missing_ok=True)
        try:
            self._child = self._d.spawner.spawn(argv, self._d.layout.app, env)
        except OSError as err:
            self._ended(f"could not start: {type(err).__name__}", lasted=0.0)
            return
        self._started_at = self._d.clock.now()
        self._idle = False
        self._log.info("host started")
        self.phase = Phase.RUNNING

    def _login_missing(self) -> str:
        secrets = LayeredEnv(self._d.layout, AgentKind.REMOTE_CONTROL, self.agent).values(EnvKind.SECRETS)
        missing = self._d.missing_login(self.harness, secrets)
        return f"needs secret {missing} on host" if missing else ""

    def _free_of_hand_launch(self, directory: Path) -> bool:
        """The host takes the workspace lock itself, so it outlives the supervisor's if that is killed; here it is only looked at."""
        if not self._hand_launch_running(directory):
            return True
        self._pause.begin(
            f"a Remote Control host started by hand is running in {directory}: stop it, and the supervisor takes over",
            lambda: "a Remote Control host started by hand is still running" if self._hand_launch_running(directory) else "",
        )
        return False

    @staticmethod
    def _hand_launch_running(directory: Path) -> bool:
        lock = supervisor_lock(directory)
        try:
            lock.__enter__()
        except AlreadyRunning:
            return True
        lock.__exit__(None, None, None)
        return False

    def _watch(self) -> None:
        child = self._child
        if child is None:
            raise RuntimeError("no host to watch")
        code = child.poll()
        if code is None:
            self._idle = read_idle(self.directory / IDLE_MARKER)
            if self._idle and (self._draining or self._held):
                self._stop_between_sessions(child)
            return
        self._child = None
        self._ended(f"exited with status {code}", lasted=self._d.clock.now() - self._started_at)

    def _stop_between_sessions(self, child: Child) -> None:
        """A drain or a hold lets a live session finish; the host is only ended once it reports none."""
        self._log.info("host stopped between sessions (%s)", "draining" if self._draining else "held")
        self._child = None
        child.terminate(self._d.config.stop_grace)
        self._idle = True
        self.phase = Phase.IDLE

    def _ended(self, what: str, lasted: float) -> None:
        """Any end while the host is meant to run is a crash."""
        cfg = self._d.config
        self._idle = True
        self._last_exit = what
        if lasted >= cfg.min_session:
            self._crashes = 0
            self._instant_failures = 0
        else:
            self._instant_failures += 1
        delay = cfg.backoffs[min(self._crashes, len(cfg.backoffs) - 1)] if cfg.backoffs else 0.0
        self._crashes += 1
        self._log.warning("host %s after %.0fs", what, lasted)
        if self._instant_failures >= cfg.crash_loop_failures:
            self._crashes = self._instant_failures = 0
            self._pause.begin(f"{CRASH_LOOP}: the host {what} {cfg.crash_loop_failures} times in a row within {cfg.min_session:.0f}s of starting")
            return
        self._wake_at = self._d.clock.now() + delay
        self.phase = Phase.BACKOFF

    def snapshot(self) -> SlotSnapshot:
        paused = self.pause_info()
        reason = self._reason()
        return SlotSnapshot(
            self._phase.value,
            max(0.0, self._d.clock.now() - self._phase_since),
            pause_reason=paused[0] if paused is not None else "",
            reason=reason[0].value if reason is not None else "",
            reason_detail=reason[1] if reason is not None else "",
            idle=self._phase != Phase.RUNNING or self._idle,
        )

    def _reason(self) -> tuple[Reason, str] | None:
        match self._phase:
            case Phase.RUNNING:
                return None
            case Phase.PAUSED:
                return Reason.SLOT_PAUSED, ""
            case _ if self._start_error:
                # The scheduler retries the start; until one works, the board shows why the last one did not, which
                # after a crash is newer than the crash.
                return Reason.BACKOFF_AFTER_ERROR, self._start_error
            case Phase.BACKOFF:
                return Reason.BACKOFF_AFTER_ERROR, self._log.clean(f"host {self._last_exit}")
            case _:
                return Reason.STARTING, ""

    def pause_info(self) -> tuple[str, float] | None:
        return self._pause.info()

    def unpause(self) -> None:
        self._pause.unpause()

    def try_unpause(self, manual: bool = False) -> str:
        return self._pause.try_unpause(manual)

    def abandon_run(self) -> None:
        """Ends the host for a supervisor that is exiting; the slot starts nothing after it."""
        self._abandoned = True
        child, self._child = self._child, None
        if child is not None:
            child.terminate(self._d.config.stop_grace)

    def begin_drain(self) -> None:
        """A stop or a retirement: no new session, and the live one gets to finish."""
        self._draining = True

    def set_held(self, held: bool) -> None:
        """The owner's hold: no new session, the live one finishes, and the host starts again on release."""
        self._held = held

    def release_lock_for_stop(self) -> None:
        """A host holds no integration lock, and the workspace lock is the host process's own."""
