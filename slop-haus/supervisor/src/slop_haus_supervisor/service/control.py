"""A local request/reply spool between `slot unpause` and the running supervisor.

Files under <root>/state/, not a socket or a port: nothing listens, so there is nothing to reach
from another host or another user, and a deep root cannot overrun a socket path's length limit.
"""

from __future__ import annotations

import json
import os
import re
import tempfile
import time
import uuid
from collections.abc import Callable, Mapping
from dataclasses import dataclass
from typing import cast
from pathlib import Path

_REQUEST_ID = re.compile(r"[0-9a-f]{32}")
# A request nobody answered (the supervisor died after it was written) is not left behind forever.
STALE_AFTER = 10 * 60.0

# Slot name -> "" when unpaused, else why not. None asks for every slot.
UnpauseHandler = Callable[[list[str] | None], Mapping[str, str]]


class ControlTimeout(Exception):
    pass


def write_atomically(path: Path, payload: object) -> None:
    # A name of its own per writer: two `slot unpause` runs at once must not share one temp file.
    fd, tmp = tempfile.mkstemp(dir=path.parent, prefix=".", suffix=".tmp")
    with os.fdopen(fd, "w") as handle:
        json.dump(payload, handle)
    os.replace(tmp, path)


@dataclass(frozen=True)
class ControlSpool:
    directory: Path

    @property
    def _requests(self) -> Path:
        return self.directory / "requests"

    @property
    def _replies(self) -> Path:
        return self.directory / "replies"

    def prepare(self) -> None:
        for path in (self.directory, self._requests, self._replies):
            path.mkdir(mode=0o700, parents=True, exist_ok=True)

    def request_unpause(
        self,
        slots: list[str] | None,
        timeout: float,
        poll: float = 0.2,
        sleep: Callable[[float], None] = time.sleep,
        monotonic: Callable[[], float] = time.monotonic,
    ) -> dict[str, str]:
        """Asks the running supervisor to unpause `slots` (all when None) and waits for its answer."""
        self.prepare()
        request_id = uuid.uuid4().hex
        reply = self._replies / f"{request_id}.json"
        write_atomically(self._requests / f"{request_id}.json", {"slots": slots})
        deadline = monotonic() + timeout
        try:
            while True:
                if reply.is_file():
                    return {str(k): str(v) for k, v in json.loads(reply.read_text()).items()}
                if monotonic() >= deadline:
                    raise ControlTimeout("the supervisor did not answer; is it running?")
                sleep(poll)
        finally:
            for leftover in (self._requests / f"{request_id}.json", reply):
                leftover.unlink(missing_ok=True)

    def serve_pending(self, handler: UnpauseHandler) -> int:
        """Answers each waiting request once; returns how many were answered."""
        if not self._requests.is_dir():
            return 0
        answered = 0
        for path in sorted(self._requests.glob("*.json")):
            if not _REQUEST_ID.fullmatch(path.stem):
                continue
            try:
                raw: dict[str, object] = json.loads(path.read_text())
                slots = raw.get("slots")
                wanted = [str(name) for name in cast(list[object], slots)] if isinstance(slots, list) else None
            except (OSError, ValueError, AttributeError):
                path.unlink(missing_ok=True)
                continue
            self._replies.mkdir(mode=0o700, parents=True, exist_ok=True)
            write_atomically(self._replies / path.name, dict(handler(wanted)))
            path.unlink(missing_ok=True)
            answered += 1
        return answered

    def sweep_stale(self, now: float | None = None) -> None:
        """Removes replies whose asker gave up, and requests left from before this supervisor started."""
        cutoff = (time.time() if now is None else now) - STALE_AFTER
        for folder in (self._requests, self._replies):
            if not folder.is_dir():
                continue
            for path in folder.iterdir():
                try:
                    if path.stat().st_mtime < cutoff:
                        path.unlink(missing_ok=True)
                except OSError:
                    continue
