from __future__ import annotations

import logging
import threading
from pathlib import Path
from typing import Any

from slop_haus_supervisor.remote_control.host.conversation import WorkItem
from slop_haus_supervisor.remote_control.host.host import Host
from slop_haus_supervisor.remote_control.host.settings import LEASE_REFRESH_SECONDS, POLL_WAIT_SECONDS
from slop_haus_supervisor.remote_control.wire.client import Client
from slop_haus_supervisor.remote_control.wire.errors import ServerError, Unreachable
from slop_haus_supervisor.text import clean_text

log = logging.getLogger(__name__)

# What this code does with the work the server queues. The board offers an
# action only to a host that lists it: a host on older code ignores work it
# does not know, so the server cannot tell from the queue alone.
CAPABILITIES = ["interrupt"]


def host_version(source: Path | None = None) -> str:
    """The build id of the package this host runs from, "" when it cannot say.

    install-app.sh unpacks the server's package into `<workspace>/.slop-haus/app`
    and records the package's build id beside it in `app.version`, the same id
    the server compares a host's report with. Run from anywhere else (a
    checkout, a supervisor-run host) there is no such file, and a made-up id
    would read as a host that is behind.
    """
    here = (source or Path(__file__)).resolve()
    for parent in here.parents:
        if parent.name == "app" and parent.parent.name == ".slop-haus":
            try:
                lines = (parent.parent / "app.version").read_text(encoding="utf-8").splitlines()
            except OSError:
                return ""
            return lines[0].strip() if lines else ""
    return ""


class Poller:
    """Long-polls the Remote Control's queue and hands what it has to the host.

    Delivery is at least once: the server sends an item again until a poll
    acknowledges it, and the host acknowledges what it has handled. The next
    poll waits for the host to finish with a batch, so its ack says what that
    batch did; an item the host held back or could not answer is simply sent
    again, which the host recognises (it knows what it holds).
    """

    def __init__(self, client: Client, host_id: str, host: Host, wait_seconds: int = POLL_WAIT_SECONDS,
                 max_backoff: float = 30.0, lease_refresh: float = LEASE_REFRESH_SECONDS,
                 version: str | None = None) -> None:
        self.client = client
        self.host_id = host_id
        self.host = host
        self.wait_seconds = wait_seconds
        self.max_backoff = max_backoff
        self.lease_refresh = lease_refresh
        self.version = version if version is not None else host_version()
        self.thread = threading.Thread(target=self.run, name="poller", daemon=True)

    def start(self) -> None:
        self.thread.start()

    def run(self) -> None:
        backoff = 1.0
        stop = self.host.stopping
        while not stop.is_set():
            try:
                items = self.poll_once()
                backoff = 1.0
            except ServerError as e:
                if e.status in (401, 403):
                    log.error(
                        "slop-haus refused this workspace's credential (%d): is it a Remote Control's, and enabled? Stopping.",
                        e.status,
                    )
                    stop.set()
                    return
                # 409 is another host process for this Remote Control still
                # polling: it ages out on the server after a minute or two,
                # unless it exits cleanly and releases.
                log.warning("poll failed, trying again in %.0fs: %s", backoff, clean_text(str(e), 300))
                stop.wait(backoff)
                backoff = min(backoff * 2, self.max_backoff)
                continue
            except Unreachable as e:
                log.warning("poll failed, trying again in %.0fs: %s", backoff, clean_text(str(e), 300))
                stop.wait(backoff)
                backoff = min(backoff * 2, self.max_backoff)
                continue
            if items:
                before = self.host.current_ack()
                self.host.offer(items)
                # The next poll's ack must say what this batch did, and what it
                # left (a report not delivered, a message held for the end of a
                # turn) comes round again.
                # Meanwhile the server must keep hearing from this host: a poll is
                # what holds its claim on the conductor, and handling a batch can
                # outlast the server's host timeout, when a second host would take
                # over the working copy.
                while not self.host.batch_done.wait(self.lease_refresh) and not stop.is_set():
                    self.refresh_lease()
                if self.host.current_ack() == before:
                    # The server answers at once with what is not acknowledged,
                    # which would spin.
                    stop.wait(2.0)

    def refresh_lease(self) -> None:
        """A poll that does not wait and whose items are ignored: they are the
        ones being handled, and come round again with the next real poll."""
        try:
            self.post_poll(0)
        except ServerError as e:
            if e.status in (401, 403):
                log.error("slop-haus refused this workspace's credential (%d). Stopping.", e.status)
                self.host.stopping.set()
                return
            log.warning("lease refresh failed: %s", clean_text(str(e), 300))
        except Unreachable as e:
            log.warning("lease refresh failed: %s", clean_text(str(e), 300))

    def post_poll(self, wait_seconds: int) -> dict[str, Any]:
        return self.client.post("/api/conductor/poll", {
            "host_id": self.host_id, "ack": self.host.current_ack(), "wait_seconds": wait_seconds,
            "capabilities": self.host.capabilities(CAPABILITIES), "version": self.version,
        }, timeout=wait_seconds + 20)

    def poll_once(self) -> list[WorkItem]:
        reply = self.post_poll(self.wait_seconds)
        out: list[WorkItem] = []
        raw_items: Any = reply.get("items") or []
        for raw in raw_items if isinstance(raw_items, list) else []:  # pyright: ignore[reportUnknownVariableType]
            if isinstance(raw, dict) and isinstance(raw.get("seq"), int):  # pyright: ignore[reportUnknownMemberType]
                item: dict[str, Any] = raw  # pyright: ignore[reportUnknownVariableType]
                if item["seq"] > self.host.current_ack():
                    out.append(WorkItem(seq=item["seq"], type=str(item.get("type")),
                                        conversation=str(item.get("conversation")), raw=item))
        return out
