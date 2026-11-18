from __future__ import annotations

from dataclasses import dataclass
from enum import StrEnum


class WakeKind(StrEnum):
    DISCUSSION = "discussion"
    AGENT_FILED = "agent_filed"
    SWEEP = "sweep"


@dataclass(frozen=True)
class WakeEvent:
    kind: WakeKind
    ticket_id: int | None = None
    # A short, already-bounded note (e.g. that a restart stopped the run); the steward reads the ticket itself.
    detail: str = ""


class WakeBatcher:
    """Collects wake events and hands them out as one batch once the window since the first has passed.

    Batching means a burst of tickets entering discussion costs one run, not one each.
    The periodic sweep is raised here too, when `sweep_every` has passed since the last batch.
    """

    def __init__(self, window: float, sweep_every: float, now: float) -> None:
        self._window = window
        self._sweep_every = sweep_every
        self._first_at: float | None = None
        self._pending: dict[tuple[WakeKind, int | None], WakeEvent] = {}
        self._last_batch_at = now

    def add(self, event: WakeEvent, now: float) -> None:
        if self._first_at is None:
            self._first_at = now
        self._pending.setdefault((event.kind, event.ticket_id), event)

    def requeue(self, events: list[WakeEvent], now: float) -> None:
        """Puts back a batch that could not run, to be due again after a fresh window."""
        for event in events:
            self.add(event, now)

    def take(self, now: float) -> list[WakeEvent] | None:
        """The due batch, or None. A sweep is added when nothing else woke the steward for `sweep_every`."""
        if self._first_at is None:
            if now - self._last_batch_at < self._sweep_every:
                return None
            self._last_batch_at = now
            return [WakeEvent(WakeKind.SWEEP)]
        if now - self._first_at < self._window:
            return None
        batch = list(self._pending.values())
        self._pending.clear()
        self._first_at = None
        self._last_batch_at = now
        return batch
