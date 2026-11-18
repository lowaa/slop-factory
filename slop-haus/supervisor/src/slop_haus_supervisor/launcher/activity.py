from __future__ import annotations

import re
from collections.abc import Sequence
from dataclasses import dataclass
from datetime import datetime
from pathlib import Path

# The tail is enough for the latest phase of every slot; reading a whole rotated log would be slow for no gain.
TAIL_BYTES = 512 * 1024
_LINE = re.compile(r"^(\d{4}-\d\d-\d\d \d\d:\d\d:\d\d),\d+ (\w+) [\w-]+: (.*)$")
_PHASE = re.compile(
    r"^(?:slot=(?P<slot>[\w.-]+) phase=(?P<phase>\S+) from=\S+(?: ticket=(?P<id>\d+))?"
    # The line format before key=value; a rotated log may still hold it.
    r"|(?P<old_slot>[\w.-]+): phase \S+ -> (?P<old_phase>\S+)(?P<old_ticket> \(ticket #\d+\))?)$"
)
# The supervisor's own start-up line: whatever precedes it belongs to a run that is gone.
_STARTED = re.compile(r"^running \d+ slot\(s\) for ")


@dataclass(frozen=True)
class SlotActivity:
    slot: str
    phase: str
    ticket: str
    since: float


@dataclass(frozen=True)
class LastError:
    message: str
    at: float


@dataclass(frozen=True)
class Activity:
    slots: tuple[SlotActivity, ...]
    last_error: LastError | None


def read_tail(path: Path, limit: int = TAIL_BYTES) -> list[str]:
    try:
        with path.open("rb") as handle:
            handle.seek(0, 2)
            size = handle.tell()
            handle.seek(max(0, size - limit))
            data = handle.read()
    except OSError:
        return []
    lines = data.decode("utf-8", errors="replace").splitlines()
    # A tail that starts mid-file begins mid-line.
    return lines[1:] if size > limit else lines


def parse(lines: Sequence[str]) -> Activity:
    """The latest phase of each slot and the latest error since the supervisor last started."""
    phases: dict[str, SlotActivity] = {}
    last_error: LastError | None = None
    for line in lines:
        match = _LINE.match(line)
        if match is None:
            continue
        stamp = datetime.strptime(match.group(1), "%Y-%m-%d %H:%M:%S").timestamp()
        level, message = match.group(2), match.group(3)
        if _STARTED.match(message):
            phases, last_error = {}, None
            continue
        if level in ("ERROR", "CRITICAL"):
            last_error = LastError(message, stamp)
        phase = _PHASE.match(message)
        if phase is not None:
            name = phase.group("slot") or phase.group("old_slot")
            ticket = f"(ticket #{phase.group('id')})" if phase.group("id") else (phase.group("old_ticket") or "").strip()
            phases[name] = SlotActivity(name, phase.group("phase") or phase.group("old_phase"), ticket, stamp)
    return Activity(tuple(phases.values()), last_error)
