from __future__ import annotations

import logging
import threading
from collections.abc import Callable
from pathlib import Path
from typing import Any

from slop_haus_supervisor.remote_control.wire.client import JsonObject
from slop_haus_supervisor.remote_control.wire.errors import ServerError, Unreachable
from slop_haus_supervisor.remote_control.wire.reporter import Poster
from slop_haus_supervisor.workspace.layout_report import describe_layout

log = logging.getLogger(__name__)

LAYOUT_PATH = "/api/conductor/layout"
POST_TIMEOUT_SECONDS = 10.0


class LayoutReporter:
    """Tells slop-haus what the host's workspace looks like on disk, at start and whenever it changes.

    A conductor has no slot-state report to ride on and no session-start hook, so this is its own
    call. The layout is decoration for the Workspace page: a failure is logged and tried again at
    the next look, never allowed to hold the host up.
    """

    def __init__(self, client: Poster, root: Path, stop: threading.Event, interval: float = 60.0,
                 layout_of: Callable[[Path], dict[str, Any] | None] = describe_layout) -> None:
        self.client = client
        self.root = root
        self.stop = stop
        self.interval = interval
        self.layout_of = layout_of
        self.sent: JsonObject | None = None

    def start(self) -> threading.Thread:
        thread = threading.Thread(target=self.run, name="layout-reporter", daemon=True)
        thread.start()
        return thread

    def run(self) -> None:
        while not self.stop.is_set():
            self.report_once()
            self.stop.wait(self.interval)

    def report_once(self) -> bool:
        """Send the layout if it differs from the last one the server took. Whether the server has it."""
        try:
            layout = self.layout_of(self.root)
        except Exception as e:  # decoration must never take the host down
            log.warning("workspace layout not read: %s", type(e).__name__)
            return False
        if layout is None:
            # git could not say: the server keeps its last report rather than a wrong one.
            return False
        if layout == self.sent:
            return True
        try:
            self.client.post(LAYOUT_PATH, layout, POST_TIMEOUT_SECONDS)
        except (Unreachable, ServerError) as e:
            log.warning("could not report the workspace layout, trying again: %s", e)
            return False
        self.sent = layout
        return True
