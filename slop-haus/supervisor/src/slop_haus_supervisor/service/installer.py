from __future__ import annotations

import difflib
import time
from collections.abc import Callable
from dataclasses import dataclass
from typing import Protocol
from pathlib import Path

from slop_haus_supervisor.launcher.activity import Activity
from slop_haus_supervisor.launcher.supervisor_session import SupervisorSession
from slop_haus_supervisor.service.exit_codes import ExitCode
from slop_haus_supervisor.service.service_units import ServiceError, ServiceManager
from slop_haus_supervisor.setup.prerequisites import Finding
from slop_haus_supervisor.workspace import status
from slop_haus_supervisor.workspace.envfile import write_private
from slop_haus_supervisor.workspace.layout import Layout

# Long enough for uv to sync the environment on a first start.
FOLLOW_SECONDS = 120.0
FOLLOW_POLL = 1.0
IDLE_POLL = 5.0
DEFAULT_IDLE_DEADLINE = 3600.0
LOG_LINES = 15

EXIT_MEANINGS = {
    int(ExitCode.ALREADY_RUNNING): "another supervisor holds this tenant (75)",
    int(ExitCode.CONFIG): "configuration error (78)",
}


@dataclass(frozen=True)
class InstallOptions:
    when_idle: bool = False
    yes: bool = False
    idle_deadline: float = DEFAULT_IDLE_DEADLINE
    follow_seconds: float = FOLLOW_SECONDS


@dataclass
class Clock:
    sleep: Callable[[float], None] = time.sleep
    monotonic: Callable[[], float] = time.monotonic


class Host(Protocol):
    """What the installer asks of a supervisor already running, wherever it was launched from."""

    def running(self) -> bool: ...
    def activity(self) -> Activity: ...
    def drain(self) -> int:
        """The same drain as `slop-haus stop`; 0 once it has stopped."""
        ...


class SessionHost:
    def __init__(self, layout: Layout, session: SupervisorSession) -> None:
        self._layout = layout
        self._session = session

    def running(self) -> bool:
        return status.holder(self._layout.root / ".run.lock")[0]

    def activity(self) -> Activity:
        return self._session.activity()

    def drain(self) -> int:
        return self._session.stop()


class ServiceInstaller:
    def __init__(
        self,
        manager: ServiceManager,
        host: Host,
        prerequisites: Callable[[], list[Finding]],
        uv: Callable[[], str | None],
        ask: Callable[[str, str], str],
        say: Callable[[str], None] = print,
        clock: Clock | None = None,
    ) -> None:
        self._manager = manager
        self._host = host
        self._prerequisites = prerequisites
        self._uv = uv
        self._ask = ask
        self._say = say
        self._clock = clock or Clock()

    def install(self, options: InstallOptions) -> int:
        if not self._check_prerequisites():
            return 1
        uv = self._uv()
        if uv is None:
            self._say("uv was not found; install it first")
            return 1
        try:
            # Before anything is written, so a refused handover leaves the host as it was.
            if not self._hand_over(options):
                return 1
            changed = self._write(self._manager.unit_path, self._manager.render(uv))
            if note := self._manager.linger_note():
                self._say(f"NOTE: {note}")
            self._manager.load()
            self._manager.start()
        except ServiceError as err:
            self._say(str(err))
            return 1
        self._say(f"started under {self._manager.name}" + ("" if changed else " (the unit was already up to date)"))
        return self._follow(options.follow_seconds)

    def uninstall(self) -> int:
        path = self._manager.unit_path
        try:
            if path.exists():
                self._manager.stop()
                self._manager.unload()
        except ServiceError as err:
            self._say(str(err))
            return 1
        if not path.exists():
            self._say(f"{path} does not exist; nothing to remove")
            return 0
        path.unlink()
        self._say(f"removed {path}; the supervisor is stopped. `slop-haus start` runs it again")
        return 0

    def _check_prerequisites(self) -> bool:
        missing = [f for f in self._prerequisites() if not f.ok and f.blocking]
        for finding in missing:
            self._say(f"[MISSING] {finding.name}: {finding.detail}")
        if missing:
            self._say("not installed: the service would fail at start-up. Fix the above (`slop-haus setup` offers to add a PATH entry), then run this again")
        return not missing

    def _write(self, path: Path, text: str) -> bool:
        old = path.read_text() if path.exists() else None
        if old == text:
            self._say(f"{path} is up to date")
            return False
        if old is not None:
            diff = difflib.unified_diff(old.splitlines(), text.splitlines(), "installed", "new", lineterm="", n=1)
            self._say(f"{path} changed:\n" + "\n".join(diff))
        else:
            self._say(f"writing {path}")
        path.parent.mkdir(parents=True, exist_ok=True)
        # Renamed over the old unit, so an interruption never leaves the manager a half-written one.
        write_private(path, text)
        return True

    def _hand_over(self, options: InstallOptions) -> bool:
        """Drains a supervisor that is already running, so the unit does not start only to exit 75."""
        if not self._host.running():
            return True
        busy = self._busy_slots()
        if busy and options.when_idle:
            if not self._wait_for_idle(options.idle_deadline):
                return False
        elif busy and not options.yes:
            answer = self._ask(f"{', '.join(busy)} mid-ticket; draining waits for them to reach a wait point. Drain the running supervisor now?", "n")
            if not answer.lower().startswith("y"):
                self._say("not installed over a busy supervisor: re-run with --when-idle to wait for the slots, or --yes to drain now")
                return False
        self._say("a supervisor is already running; draining it before the service starts")
        if self._host.drain() != 0:
            self._say("the supervisor did not stop; see `slop-haus stop --now`")
            return False
        return True

    def _busy_slots(self) -> list[str]:
        return [s.slot for s in self._host.activity().slots if s.phase == "running"]

    def _wait_for_idle(self, deadline: float) -> bool:
        give_up = self._clock.monotonic() + deadline
        while busy := self._busy_slots():
            if self._clock.monotonic() >= give_up:
                self._say(f"not installed: {', '.join(busy)} still mid-ticket after {deadline:.0f}s; the supervisor keeps running")
                return False
            self._say(f"waiting for {', '.join(busy)} to finish...")
            self._clock.sleep(IDLE_POLL)
        return True

    def _follow(self, seconds: float) -> int:
        """Until the slots are up or the unit has exited; the log is shown instead of leaving it to be found."""
        give_up = self._clock.monotonic() + seconds
        while self._clock.monotonic() < give_up:
            state = self._manager.state()
            if state.exit_status is not None:
                self._say(f"the supervisor exited: {EXIT_MEANINGS.get(state.exit_status, f'status {state.exit_status}')}; the end of its log:")
                self._show_log()
                return 1
            if state.running and self._host.running() and self._host.activity().slots:
                self._say(f"up: {len(self._host.activity().slots)} slot(s); `slop-haus status` and `slop-haus logs` to watch it")
                return 0
            self._clock.sleep(FOLLOW_POLL)
        self._say(f"the slots did not come up within {seconds:.0f}s; the end of the log:")
        self._show_log()
        return 1

    def _show_log(self) -> None:
        for line in self._manager.recent_log(LOG_LINES):
            self._say(f"  {line}")
