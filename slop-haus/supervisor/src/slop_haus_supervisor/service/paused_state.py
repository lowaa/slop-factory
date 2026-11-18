"""Which slots are paused and why, written for `status`, which runs in another process."""

from __future__ import annotations

import json
from dataclasses import asdict, dataclass
from pathlib import Path

from slop_haus_supervisor.service.control import write_atomically


@dataclass(frozen=True)
class PausedSlot:
    slot: str
    reason: str
    since: float


def write(path: Path, paused: list[PausedSlot]) -> None:
    path.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
    write_atomically(path, [asdict(p) for p in paused])


def read(path: Path) -> list[PausedSlot]:
    try:
        raw = json.loads(path.read_text())
        return [PausedSlot(str(r["slot"]), str(r["reason"]), float(r["since"])) for r in raw]
    except (OSError, ValueError, KeyError, TypeError):
        return []
