from __future__ import annotations

import os
import shlex
import shutil
import signal
import time
from collections.abc import Callable, Mapping
from dataclasses import dataclass
from pathlib import Path

from slop_haus_supervisor.launcher.activity import Activity, parse, read_tail
from slop_haus_supervisor.launcher.multiplexer import LauncherError, Multiplexer, Tmux
from slop_haus_supervisor.service.run_command import log_file, run_argv
from slop_haus_supervisor.service.service_units import ServiceError, ServiceManager
from slop_haus_supervisor.workspace import status
from slop_haus_supervisor.workspace.host_files import IDLE_MARKER_NAME, lock_path, read_idle
from slop_haus_supervisor.workspace.layout import Layout

# Longer than `run`'s default drain timeout plus the scheduler's join and kill graces.
DEFAULT_STOP_WAIT = 300.0
# How long `start` waits to see the supervisor take its lock before saying it did not come up.
START_WAIT = 15.0
SIGNAL_GAP = 1.0
# `restart --when-idle` gives up after this long without every host being between sessions.
DEFAULT_IDLE_DEADLINE = 3600.0
IDLE_POLL = 5.0
# Set by every harness on the sessions it starts; see harness/{claude,codex,pi}.py.
HOST_SESSION_ENV = "SLOP_HAUS_HOST_SESSION"
HOST_SESSION_WARNING = (
    "this is an agent session: the supervisor started here is a descendant of the agent's process, and the harness "
    "kills that whole tree when the session ends, taking the supervisor and every slot with it"
)


def host_session_permits_start(env: Mapping[str, str], allow: bool, say: Callable[[str], None] = print) -> bool:
    """Whether `start` may go ahead: not from an agent session unless `allow`, which warns instead."""
    if not env.get(HOST_SESSION_ENV):
        return True
    if not allow:
        say(f"refusing to start: {HOST_SESSION_WARNING}.")
        say("run `slop-haus start` from a login shell or over SSH, or pass --allow-host-session to start it anyway")
        return False
    say(f"warning: {HOST_SESSION_WARNING}")
    return True


@dataclass
class Clock:
    sleep: Callable[[float], None] = time.sleep
    monotonic: Callable[[], float] = time.monotonic
    wall: Callable[[], float] = time.time


class SupervisorSession:
    """Runs one tenant's `run` detached in a multiplexer session, and stops, reports on and reads it.

    With an installed service `unit`, start and stop go through its manager instead, so the unit is the one launch path
    and `start` cannot race it (exit 75).
    """

    def __init__(
        self,
        layout: Layout,
        tenant: str,
        mux: Multiplexer,
        say: Callable[[str], None] = print,
        clock: Clock | None = None,
        kill: Callable[[int, int], None] = os.kill,
        which: Callable[[str], str | None] = shutil.which,
        busy_hosts: Callable[[], list[str]] | None = None,
        unit: ServiceManager | None = None,
    ) -> None:
        self._layout = layout
        self._tenant = tenant
        self._mux = mux
        self._say = say
        self._clock = clock or Clock()
        self._kill = kill
        self._which = which
        self._busy_hosts = busy_hosts or (lambda: busy_hosts_of(self._layout))
        self._unit = unit

    @property
    def session(self) -> str:
        return f"slop-haus-{self._tenant}"

    @property
    def log_file(self) -> Path:
        return log_file(self._layout)

    def _holder(self) -> tuple[bool, str]:
        return status.holder(self._layout.root / ".run.lock")

    def attach_hint(self) -> str:
        return "attach with `slop-haus attach` (detach again with Ctrl-A D in screen, Ctrl-B D in tmux)"

    def command(self) -> list[str]:
        run = run_argv(self._which("uv") or "uv", self._layout)
        # tmux hands its command to a shell as one string; screen execs the words.
        return [shlex.join(run)] if isinstance(self._mux, Tmux) else run

    def start(self) -> int:
        running, pid = self._holder()
        if running or self._mux.exists(self.session):
            where = f"session {self.session}" if self._mux.exists(self.session) else f"pid {pid}, not in a {self._mux.binary} session"
            self._say(f"already running ({where}); {self.attach_hint()}")
            return 1
        if self._unit is not None:
            return self._start_unit(self._unit)
        self._mux.start(self.session, self.command(), self._layout.root)
        deadline = self._clock.monotonic() + START_WAIT
        while self._clock.monotonic() < deadline:
            if self._holder()[0]:
                self._say(f"started in {self._mux.binary} session {self.session}; log: {self.log_file}")
                self._say(f"{self.attach_hint()}; `slop-haus status` and `slop-haus logs` to watch it")
                return 0
            if not self._mux.exists(self.session):
                break
            self._clock.sleep(0.5)
        self._say(f"the supervisor did not stay up; the end of {self.log_file}:")
        for line in read_tail(self.log_file)[-15:]:
            self._say(f"  {line}")
        return 1

    def _start_unit(self, unit: ServiceManager) -> int:
        try:
            unit.load()
            unit.start()
        except ServiceError as err:
            raise LauncherError(str(err)) from err
        deadline = self._clock.monotonic() + START_WAIT
        while self._clock.monotonic() < deadline:
            if self._holder()[0]:
                self._say(f"started under {unit.name}; log: {self.log_file}")
                self._say("`slop-haus status` and `slop-haus logs` to watch it")
                return 0
            if unit.state().exit_status is not None:
                break
            self._clock.sleep(0.5)
        self._say(f"the supervisor did not stay up; the end of {self.log_file}:")
        for line in read_tail(self.log_file)[-15:]:
            self._say(f"  {line}")
        return 1

    def _stop_unit(self, unit: ServiceManager, timeout: float) -> int:
        try:
            unit.stop()
        except ServiceError as err:
            raise LauncherError(str(err)) from err
        # systemctl stop blocks through the drain, but launchd may release the lock a moment after it returns.
        deadline = self._clock.monotonic() + timeout
        while self._holder()[0]:
            if self._clock.monotonic() >= deadline:
                self._say(f"the unit stopped but a supervisor still holds the tenant after {timeout:.0f}s; `slop-haus stop --now` ends it")
                return 1
            self._clock.sleep(1.0)
        self._say(f"stopped ({unit.name})")
        return 0

    def _unit_owns_stop(self, now: bool) -> bool:
        """Whether the unit stops it. --now is a second signal, which only the process can be sent, and a supervisor
        that is running but not the unit's (started before the install) has no unit to stop."""
        if self._unit is None or now:
            return False
        # A second stop during the unit's drain must not become the second signal, which ends the runs.
        state = self._unit.state()
        return not self._holder()[0] or state.running or state.stopping

    def stop(self, now: bool = False, timeout: float = DEFAULT_STOP_WAIT) -> int:
        if self._unit is not None and self._unit_owns_stop(now):
            return self._stop_unit(self._unit, timeout)
        running, pid = self._holder()
        if not running or not pid.isdigit():
            self._say("not running")
            return 0
        # The first signal drains; the second ends the runs, so --now sends both. Signals do not queue:
        # a second one sent before the first was handled would be folded into it and only drain.
        self._kill(int(pid), signal.SIGTERM)
        if now:
            self._clock.sleep(SIGNAL_GAP)
            self._kill(int(pid), signal.SIGTERM)
        if now:
            self._say(f"stopping pid {pid} at once (second signal sent)")
        else:
            self._say(f"draining pid {pid}: no new work, and running work gets to a wait point; `slop-haus stop --now` ends it at once")
        deadline = self._clock.monotonic() + timeout
        next_note = self._clock.monotonic() + 30
        while self._holder()[0]:
            if self._clock.monotonic() >= deadline:
                self._say(f"still running after {timeout:.0f}s; `slop-haus stop --now` ends the runs")
                return 1
            if self._clock.monotonic() >= next_note:
                self._say("still draining...")
                next_note += 30
            self._clock.sleep(1.0)
        self._say("stopped")
        return 0

    def restart(self, when_idle: bool, deadline: float = DEFAULT_IDLE_DEADLINE, timeout: float = DEFAULT_STOP_WAIT) -> int:
        """Stop, then start. With `when_idle` it first waits for every Remote Control host to be between sessions.

        Past `deadline` it gives up and leaves the supervisor running: a restart for an update never ends a
        conversation. Without `when_idle` the stop drains, and cuts off what outlasts the drain timeout.
        """
        if when_idle and not self._wait_for_idle(deadline):
            return 1
        if self.stop(timeout=timeout) != 0:
            return 1
        return self.start()

    def _wait_for_idle(self, deadline: float) -> bool:
        if not self._holder()[0]:
            return True
        give_up = self._clock.monotonic() + deadline
        next_note = self._clock.monotonic()
        while busy := self._busy_hosts():
            now = self._clock.monotonic()
            if now >= give_up:
                self._say(f"not restarted: {', '.join(busy)} still in a session after {deadline:.0f}s; the supervisor keeps running")
                return False
            if now >= next_note:
                self._say(f"waiting for {', '.join(busy)} to finish their session...")
                next_note = now + 60
            self._clock.sleep(IDLE_POLL)
        return True

    def activity(self) -> Activity:
        return parse(read_tail(self.log_file))

    def session_lines(self) -> list[str]:
        """The multiplexer session and the log's last error: what only this host knows, beside the board's view."""
        running, _ = self._holder()
        lines: list[str] = []
        if running and self._mux.exists(self.session):
            lines.append(f"  session: {self._mux.binary} {self.session} (`slop-haus attach`); log: {self.log_file}")
        elif not running:
            lines.append("  start it with `slop-haus start`")
        activity = self.activity()
        if activity.last_error is not None:
            lines.append(f"  last error ({age(self._clock.wall() - activity.last_error.at)} ago): {activity.last_error.message}")
        return lines

    def status_lines(self) -> list[str]:
        """What `status` adds to the lock-based line: the session, each slot's phase and its age, and the last error."""
        running, _ = self._holder()
        lines: list[str] = []
        if running and self._mux.exists(self.session):
            lines.append(f"  session: {self._mux.binary} {self.session} (`slop-haus attach`); log: {self.log_file}")
        activity = self.activity()
        now = self._clock.wall()
        if running:
            for slot in activity.slots:
                ticket = f" {slot.ticket}" if slot.ticket else ""
                lines.append(f"  {slot.slot}: {slot.phase}{ticket} for {age(now - slot.since)}")
        else:
            lines.append("  start it with `slop-haus start`")
        if activity.last_error is not None:
            lines.append(f"  last error ({age(now - activity.last_error.at)} ago): {activity.last_error.message}")
        return lines

    def logs_argv(self, lines: int) -> list[str]:
        if not self.log_file.is_file():
            raise LauncherError(f"{self.log_file} does not exist: the supervisor has not been started with `slop-haus start`")
        return ["tail", "-n", str(lines), "-f", str(self.log_file)]

    def attach_argv(self) -> list[str]:
        if not self._mux.exists(self.session):
            raise LauncherError(f"no {self._mux.binary} session {self.session}: start it with `slop-haus start`")
        return self._mux.attach_argv(self.session)


def age(seconds: float) -> str:
    total = max(0, int(seconds))
    if total >= 3600:
        return f"{total // 3600}h{total % 3600 // 60:02d}m"
    if total >= 60:
        return f"{total // 60}m"
    return f"{total}s"


def busy_hosts_of(layout: Layout) -> list[str]:
    """Remote Control hosts running in a session. A host that is not running is not busy, whatever a dead one's marker says."""
    hosts = layout.root / "remote-control-hosts"
    if not hosts.is_dir():
        return []
    busy: list[str] = []
    for directory in sorted(hosts.iterdir()):
        running, _ = status.holder(lock_path(directory))
        if running and not read_idle(directory / IDLE_MARKER_NAME):
            busy.append(directory.name)
    return busy
