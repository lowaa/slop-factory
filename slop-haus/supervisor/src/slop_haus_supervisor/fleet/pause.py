"""A paused slot stays idle until its cause clears or a person says so."""

from __future__ import annotations

import threading
from collections.abc import Callable
from typing import Protocol

from slop_haus_supervisor.fleet.phase import Phase
from slop_haus_supervisor.fleet.slot_log import SlotLog
from slop_haus_supervisor.ports import Alerts, Clock

# A paused slot looks again at what paused it this often.
PAUSE_RECHECK_INTERVAL = 3 * 60.0
# Says why a pause's cause is still there, or "" once it has cleared.
CauseCheck = Callable[[], str]


class PhaseHost(Protocol):
    """The slot a pause belongs to: pausing and unpausing are phase changes of it."""

    @property
    def phase(self) -> Phase: ...

    @phase.setter
    def phase(self, value: Phase) -> None: ...

    @property
    def phase_since(self) -> float: ...


class PauseState:
    def __init__(self, host: PhaseHost, log: SlotLog, alerts: Alerts, clock: Clock, recheck_interval: float) -> None:
        self._host = host
        self._log = log
        self._alerts = alerts
        self._clock = clock
        self._recheck_interval = recheck_interval
        self._reason = ""
        # Taken by whoever may end a pause, the slot's own thread and the control channel's.
        self._lock = threading.Lock()
        self._cause_check: CauseCheck | None = None
        self._next_recheck = 0.0

    def begin(self, reason: str, cause_check: CauseCheck | None = None) -> None:
        """`cause_check` lets the slot end its own pause once the cause is gone; without one a person must."""
        self._log.warning("paused: %s", self._log.clean(reason))
        with self._lock:
            self._host.phase = Phase.PAUSED
            self._reason = reason
            self._cause_check = cause_check
            self._next_recheck = self._clock.now() + self._recheck_interval
        # A paused slot stays idle until a person acts, so no path may pause without telling one.
        self._alerts.alert(f"{self._log.agent} paused: {self._log.clean(reason)}")

    def recheck_due(self) -> bool:
        return self._clock.now() >= self._next_recheck

    def seconds_to_recheck(self) -> float:
        return max(0.0, self._next_recheck - self._clock.now())

    def unpause(self) -> None:
        """A person fixed what paused the slot (the login, say)."""
        with self._lock:
            if self._host.phase == Phase.PAUSED:
                self._log.info("unpaused (was: %s)", self._log.clean(self._reason))
                self._end()

    def try_unpause(self, manual: bool = False) -> str:
        """Unpauses when the pause's cause has cleared; otherwise says why it has not ("" when unpaused).

        A cause that cannot be checked (a failed harness login) only ends on a person's say-so, `manual`.
        """
        with self._lock:
            if self._host.phase != Phase.PAUSED:
                return ""
            check = self._cause_check
            if check is None:
                if not manual:
                    self._next_recheck = self._clock.now() + self._recheck_interval
                    return self._log.clean(self._reason)
                self._log.info("unpaused by request (was: %s)", self._log.clean(self._reason))
                self._end()
                return ""
            try:
                remaining = check()
            except Exception as err:
                remaining = f"checking failed: {type(err).__name__}"
            if remaining:
                self._next_recheck = self._clock.now() + self._recheck_interval
                return self._log.clean(remaining)
            self._log.info("unpaused: %s cleared", self._log.clean(self._reason))
            self._end()
            return ""

    def info(self) -> tuple[str, float] | None:
        """The reason (masked, one line) and when the slot paused, read together; None when not paused."""
        with self._lock:
            if self._host.phase != Phase.PAUSED:
                return None
            return self._log.clean(self._reason), self._host.phase_since

    def _end(self) -> None:
        self._host.phase = Phase.IDLE
        self._reason = ""
        self._cause_check = None
