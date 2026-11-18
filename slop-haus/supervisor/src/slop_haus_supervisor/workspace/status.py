from __future__ import annotations

import fcntl
import os
import time
from collections.abc import Callable
from dataclasses import dataclass
from pathlib import Path

from slop_haus_supervisor.service import paused_state
from slop_haus_supervisor.workspace import config as cfg
from slop_haus_supervisor.workspace.layered_env import dropped_message, dropped_names
from slop_haus_supervisor.workspace.layout import Layout


@dataclass(frozen=True)
class TenantStatus:
    tenant: str
    root: Path
    running: bool
    pid: str
    slots: tuple[str, ...]
    paused: tuple[paused_state.PausedSlot, ...] = ()
    now: float = 0.0
    dropped: tuple[str, ...] = ()

    def lines(self) -> list[str]:
        state = f"running (pid {self.pid})" if self.running and self.pid else "running" if self.running else "not running"
        slots = ", ".join(self.slots) if self.slots else "none"
        lines = [f"{self.tenant}: {state}  [{self.root}]", f"  slots: {slots}"]
        lines += [f"  {dropped_message(name)}" for name in self.dropped]
        for p in self.paused:
            lines.append(f"  PAUSED {p.slot} for {_age(self.now - p.since)}: {p.reason}")
            lines.append(f"    `slop-haus slot unpause {p.slot}` once the cause is fixed; it also clears itself")
        return lines


def _age(seconds: float) -> str:
    minutes = max(0, int(seconds // 60))
    return f"{minutes // 60}h{minutes % 60:02d}m" if minutes >= 60 else f"{minutes}m"


def holder(lock: Path) -> tuple[bool, str]:
    """Whether a `run` holds the tenant's lock, probed without taking it (the OS drops a dead holder's)."""
    try:
        fd = os.open(lock, os.O_RDONLY)
    except FileNotFoundError:
        return False, ""
    with os.fdopen(fd) as handle:
        try:
            fcntl.flock(handle, fcntl.LOCK_SH | fcntl.LOCK_NB)
        except BlockingIOError:
            return True, handle.read().strip()
        return False, ""


def of_root(root: Path, clock: Callable[[], float] = time.time) -> TenantStatus:
    layout = Layout(root)
    config = cfg.load(layout.config_file)
    running, pid = holder(root / ".run.lock")
    # The file outlives a supervisor that died while a slot was paused, so only a running one's is read.
    paused = tuple(paused_state.read(layout.paused_file)) if running else ()
    return TenantStatus(config.tenant, root, running, pid, config.slots, paused, clock(), tuple(dropped_names(layout)))
