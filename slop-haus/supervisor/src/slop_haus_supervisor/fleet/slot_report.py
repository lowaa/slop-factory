"""Tells slop-haus what each slot is doing, from the scheduler rather than from the slot.

A slot whose every step raises never reaches its own poll, so the one place that sees
every step end, return or raise, is the loop that calls it.
"""

from __future__ import annotations

import logging
import re
import time
from collections.abc import Callable
from dataclasses import dataclass, replace
from pathlib import Path
from typing import Any, Protocol

from slop_haus_supervisor.slophaus.client import SlopHaus, SlotSnapshot
from slop_haus_supervisor.workspace.bundle import read_bundle_hash
from slop_haus_supervisor.workspace.layout_report import describe_layout

MAX_ERROR_CLASS = 64
_WORD_BREAK = re.compile(r"(?<=[a-z0-9])(?=[A-Z])|(?<=[A-Z])(?=[A-Z][a-z])")
_OUTSIDE_TOKEN = re.compile(r"[^a-z0-9_]+")


def error_class(err: BaseException) -> str:
    """The exception's type name as a short lower-case token (`ApiError` -> `api_error`).

    Only the type: the message can hold a ticket's text or a credential.
    """
    spelt = _WORD_BREAK.sub("_", type(err).__name__).lower()
    return _OUTSIDE_TOKEN.sub("_", spelt).strip("_")[:MAX_ERROR_CLASS] or "error"


class Reportable(Protocol):
    agent: str
    directory: Path

    def snapshot(self) -> SlotSnapshot: ...


@dataclass
class _Track:
    error_class: str = ""
    streak: int = 0
    last_phase: str | None = None
    last_attempt: float | None = None
    last_error_class: str = ""
    layout_sent: bool = False


class SlotStateReporter:
    """Each slot's thread calls `after_step` for its own slot only, so the tracks share nothing."""

    def __init__(
        self,
        client: SlopHaus,
        logger: logging.Logger,
        min_interval: float = 30.0,
        monotonic: Callable[[], float] = time.monotonic,
        layout_of: Callable[[Path], dict[str, Any] | None] = describe_layout,
        bundle_of: Callable[[Path], str] = read_bundle_hash,
    ) -> None:
        self._client = client
        self._log = logger
        self._min_interval = min_interval
        self._monotonic = monotonic
        self._layout_of = layout_of
        self._bundle_of = bundle_of
        self._tracks: dict[str, _Track] = {}

    def after_step(self, slot: Reportable, error: BaseException | None) -> None:
        track = self._tracks.setdefault(slot.agent, _Track())
        self._note(track, error)
        snapshot = replace(
            slot.snapshot(), error_class=track.error_class, error_streak=track.streak, bundle_hash=self._bundle(slot)
        )
        now = self._monotonic()
        if not self._due(track, snapshot, now):
            return
        # At the first report and whenever the phase moves, which covers the end of each run; the
        # slot root is looked at, not the run's repo cwd. Unsent until it is taken, so it is retried.
        if not track.layout_sent or snapshot.phase != track.last_phase:
            track.layout_sent = False
            snapshot = replace(snapshot, layout=self._layout(slot))
        track.last_attempt = now
        track.last_phase = snapshot.phase
        track.last_error_class = snapshot.error_class
        try:
            self._client.report_slot_state(slot.agent, snapshot)
            track.layout_sent = track.layout_sent or snapshot.layout is not None
        except Exception as err:
            # A report that fails must never stop the slot; the next one replaces it.
            self._log.warning("%s: slot state not reported: %s", slot.agent, type(err).__name__)

    def _bundle(self, slot: Reportable) -> str:
        # Every report: an update between two reports changes it, and it is one small file.
        try:
            return self._bundle_of(slot.directory)
        except Exception as err:
            self._log.warning("%s: workspace bundle hash not read: %s", slot.agent, type(err).__name__)
            return ""

    def _layout(self, slot: Reportable) -> dict[str, Any] | None:
        try:
            return self._layout_of(slot.directory)
        except Exception as err:
            self._log.warning("%s: workspace layout not read: %s", slot.agent, type(err).__name__)
            return None

    @staticmethod
    def _note(track: _Track, error: BaseException | None) -> None:
        if error is None:
            track.error_class, track.streak = "", 0
            return
        spelt = error_class(error)
        track.streak = track.streak + 1 if spelt == track.error_class else 1
        track.error_class = spelt

    def _due(self, track: _Track, snapshot: SlotSnapshot, now: float) -> bool:
        if track.last_attempt is None:
            return True
        if snapshot.phase != track.last_phase or snapshot.error_class != track.last_error_class:
            return True
        return now - track.last_attempt >= self._min_interval
