from __future__ import annotations

import logging
import threading
import time
from collections.abc import Callable
from typing import Any, Protocol

from slop_haus_supervisor.remote_control.wire.errors import ServerError, Unreachable
from slop_haus_supervisor.text import clean_text

log = logging.getLogger(__name__)

ACQUIRE = "/api/agent/host_lease"
RENEW = "/api/agent/host_lease/renew"
RELEASE = "/api/agent/host_lease/release"
CALL_TIMEOUT = 10.0
# Renewals are tried this many times per ttl, so a lease survives a missed one or two.
RENEWALS_PER_TTL = 3


class Poster(Protocol):
    def post(self, path: str, body: dict[str, Any], timeout: float) -> dict[str, Any]: ...


class LeaseRefused(Exception):
    """Another host holds this agent's lease."""


class HostLease:
    """The server's one-host-per-agent lease, held for as long as this host serves.

    The server frees a lease whose renewal is missed, so a host that cannot prove it renewed must stop serving
    before a successor can take over: it fences itself by setting `stop` once its last grant has run out.
    """

    def __init__(self, client: Poster, holder: str, stop: threading.Event, monotonic: Callable[[], float] = time.monotonic) -> None:
        self._client = client
        self._holder = holder
        self._stop = stop
        self._monotonic = monotonic
        self._valid_until = 0.0
        self._ttl = 0.0

    def acquire(self) -> None:
        """Raises LeaseRefused while another host's lease is live; Unreachable and ServerError for anything else."""
        try:
            self._grant(ACQUIRE)
        except ServerError as err:
            if err.status == 409:
                raise LeaseRefused(f"another Remote Control host is serving this agent: {err.detail}") from err
            raise

    def _grant(self, path: str) -> None:
        # Counted from before the call: the server's grant started no later than the answer arrived.
        asked = self._monotonic()
        reply = self._client.post(path, {"holder": self._holder}, CALL_TIMEOUT)
        ttl = reply.get("ttl_seconds")
        self._ttl = float(ttl) if isinstance(ttl, (int, float)) and ttl > 0 else self._ttl
        if self._ttl <= 0:
            raise ServerError(502, "the server sent no lease length")
        self._valid_until = asked + self._ttl

    @property
    def renew_interval(self) -> float:
        return self._ttl / RENEWALS_PER_TTL

    def renew_once(self) -> bool:
        """False, with `stop` set, once the host may no longer serve."""
        try:
            self._grant(RENEW)
            return True
        except ServerError as err:
            if err.status in (401, 403, 409):
                self._fence(f"the server no longer gives this host the lease ({err.status}: {clean_text(err.detail, 200)})")
                return False
            log.warning("lease renewal failed: %s", clean_text(str(err), 300))
        except Unreachable as err:
            log.warning("lease renewal failed: %s", clean_text(str(err), 300))
        if self._monotonic() >= self._valid_until:
            self._fence("the lease ran out without a renewal getting through")
            return False
        return True

    def _fence(self, why: str) -> None:
        log.error("%s: stopping, so that no second host is served beside this one", why)
        self._stop.set()

    def run(self) -> None:
        while not self._stop.wait(self.renew_interval):
            if not self.renew_once():
                return

    def start(self) -> threading.Thread:
        thread = threading.Thread(target=self.run, name="host-lease", daemon=True)
        thread.start()
        return thread

    def release(self) -> None:
        try:
            self._client.post(RELEASE, {"holder": self._holder}, CALL_TIMEOUT)
        except (ServerError, Unreachable) as err:
            log.warning("could not release the host lease; it lapses by itself: %s", clean_text(str(err), 300))
