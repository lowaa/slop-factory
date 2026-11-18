"""The steward runner's ports over the server's supervisor routes."""

from __future__ import annotations

import logging
from collections.abc import Sequence

from slop_haus_supervisor.slophaus.supervisor_api import SupervisorApi
from slop_haus_supervisor.steward.events import WakeEvent, WakeKind


class ServerSteward:
    """StewardBoard, StewardBriefing, AgreementReports, StewardLease and StewardModes, all through the supervisor's own credential."""

    def __init__(self, api: SupervisorApi) -> None:
        self._api = api
        # None until the first read, which starts from now: events from before this supervisor started are the sweep's.
        self._cursor: int | None = None

    def new_events(self) -> Sequence[WakeEvent]:
        events, self._cursor = self._api.steward_events(self._cursor)
        wakes: list[WakeEvent] = []
        for kind, ticket_id in events:
            try:
                wakes.append(WakeEvent(WakeKind(kind), ticket_id))
            except ValueError:
                # A kind a newer server sends; it is on the board for the sweep.
                continue
        return wakes

    def load(self) -> str:
        return self._api.steward_briefing()

    def agreement_report(self) -> str:
        return self._api.steward_agreement()

    def mode(self) -> str:
        return self._api.steward_mode()

    def acquire(self) -> bool:
        return self._api.acquire_steward_lease()

    def release(self) -> None:
        self._api.release_steward_lease()


class BoardStewardStatus:
    """StewardStatus over the observed-state report: the steward has no desired-state entry, so it is keyed by agent alone."""

    def __init__(self, api: SupervisorApi, logger: logging.Logger) -> None:
        self._api = api
        self._log = logger

    def report(self, phase: str, reason: str = "") -> None:
        try:
            steward = self._api.ensure_steward()
            entry: dict[str, object] = {"id": steward.id, "phase": phase}
            if reason:
                entry["reason"] = reason
            self._api.report_observed([entry])
        except Exception as err:  # noqa: BLE001 -- the board is told on a best effort; the log has the reason too
            self._log.warning("steward: could not report %s to the board: %s", phase, err)
