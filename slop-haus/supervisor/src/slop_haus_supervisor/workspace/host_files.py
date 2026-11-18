"""Two files a Remote Control host and the supervisor that may run it share. They carry no credential, so they live outside `remote_control/`, which only itself may import."""

from __future__ import annotations

import contextlib
import os
from pathlib import Path

from slop_haus_supervisor.service.instance_lock import AlreadyRunning, InstanceLock

IDLE = "idle"
# Where the host publishes its idle state, from the workspace root.
IDLE_MARKER_NAME = ".slop-haus/conductor/idle"
BUSY = "busy"


class IdleMarker:
    """Tells a supervisor that runs this host whether a session is live, through a file in the workspace.

    A file, not a socket: the supervisor only ever reads it, and a host that died leaves a stale file that
    the supervisor ignores because it knows the process is gone.
    """

    def __init__(self, path: Path) -> None:
        self.path = path
        self._last: str | None = None

    def publish(self, idle: bool) -> None:
        state = IDLE if idle else BUSY
        if state == self._last:
            return
        tmp = self.path.with_name(self.path.name + ".tmp")
        try:
            self.path.parent.mkdir(parents=True, exist_ok=True)
            tmp.write_text(state + "\n")
            os.replace(tmp, self.path)
        except OSError:
            # Unknown reads as busy, which only delays a restart or a sync.
            return
        self._last = state

    def clear(self) -> None:
        self._last = None
        with contextlib.suppress(OSError):
            self.path.unlink()


def read_idle(path: Path) -> bool:
    """True only when the host says so; a missing or unreadable file is not idle."""
    try:
        return path.read_text().strip() == IDLE
    except OSError:
        return False


SUPERVISOR = "supervisor"
HOST = "host"


class HostLaunchRefused(Exception):
    pass


def lock_path(root: Path) -> Path:
    return root / ".slop-haus" / "host.lock"


def supervisor_lock(root: Path) -> InstanceLock:
    """Held by a host the supervisor started, for as long as that host lives, so no second launcher starts one."""
    return InstanceLock(lock_path(root), SUPERVISOR)


def host_lock(root: Path) -> InstanceLock:
    """Held by a manually launched host; one that the supervisor runs relies on the supervisor's instead."""
    return InstanceLock(lock_path(root), HOST)


def refusal(err: AlreadyRunning) -> HostLaunchRefused:
    if err.owner == SUPERVISOR:
        return HostLaunchRefused("this Remote Control is managed by the supervisor on this machine")
    return HostLaunchRefused("a Remote Control host is already running in this workspace")
