"""The system prompt a slot's headless runs get, from the files the server renders into the slot."""

from __future__ import annotations

import logging
from pathlib import Path
from typing import Protocol

from slop_haus_supervisor.ports import ProcessRunner
from slop_haus_supervisor.workspace.provisioned import REFRESH_TIMEOUT, Kind, Provisioned
from slop_haus_supervisor.workspace.reconcile import WorkspaceReconciler

BRIEFING = Path(".slop-haus") / "headless-briefing.md"


class BriefingUnavailable(Exception):
    """A slot has no usable briefing; a run without one would have no landing or review rules."""


class Briefing(Protocol):
    def load(self, directory: Path) -> str:
        """The system prompt for a run in `directory`; raises BriefingUnavailable."""
        ...


def _read(path: Path) -> str:
    try:
        text = path.read_text()
    except OSError as err:
        raise BriefingUnavailable(f"{path} is not readable: {type(err).__name__}") from err
    if not text.strip():
        raise BriefingUnavailable(f"{path} is empty")
    return text


class SlotBriefing:
    """Refreshes the slot's managed files, then reads the briefing, which carries the worker rules and the fleet instructions."""

    def __init__(
        self,
        runner: ProcessRunner,
        logger: logging.Logger,
        refresh_timeout: float = REFRESH_TIMEOUT,
        reconciler: WorkspaceReconciler | None = None,
    ) -> None:
        self._runner = runner
        self._reconciler = reconciler
        self._timeout = refresh_timeout
        self._log = logger

    def load(self, directory: Path) -> str:
        # Before the refresh, so update.sh clones into paths the reconcile has just vacated.
        self._reconcile(directory)
        Provisioned(directory, Kind.SLOT, self._runner, self._log, refresh_timeout=self._timeout).refresh()
        return _read(directory / BRIEFING)

    def _reconcile(self, directory: Path) -> None:
        if self._reconciler is None:
            return
        try:
            self._reconciler.reconcile(directory)
        except Exception as err:  # noqa: BLE001 -- a reconcile that fails must not stop the run it precedes
            self._log.warning("%s: workspace reconcile failed: %s", directory.name, type(err).__name__)
