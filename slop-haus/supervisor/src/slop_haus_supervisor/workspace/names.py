from __future__ import annotations

import re
import socket
from collections.abc import Iterable

from slop_haus_supervisor.workspace import config as cfg


def short_hostname() -> str:
    """The machine's name up to the first dot, made safe to be part of a slot name."""
    label = socket.gethostname().split(".")[0].lower()
    return re.sub(r"[^a-z0-9-]+", "-", label).strip("-") or "host"


def next_number(prefix: str, taken: Iterable[str]) -> int:
    """One past the highest `<prefix><n>` in `taken`; gaps are never filled.

    A retired slot's agent keeps its name on the server, so a number once used must not come round again.
    """
    highest = 0
    for name in taken:
        match = re.fullmatch(re.escape(prefix) + r"(\d+)", name)
        if match:
            highest = max(highest, int(match[1]))
    return highest + 1


class SlotNamer:
    """Hands out `<short hostname>-<n>` names, numbered per tenant."""

    def __init__(self, hostname: str, taken: Iterable[str]) -> None:
        self._prefix = f"{cfg.check_name('host', hostname)}-"
        self._taken = set(taken)
        self._next = next_number(self._prefix, self._taken)

    def next(self) -> str:
        name = f"{self._prefix}{self._next}"
        self._next += 1
        self._taken.add(name)
        return name
