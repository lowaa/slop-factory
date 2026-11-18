from __future__ import annotations

import json
import os
import tempfile
from pathlib import Path
from typing import Any, cast


class SessionBaseline:
    """The last modelUsage a session reported, kept in a file of its own.

    Every headless run is a process of its own, and a resumed session's modelUsage
    starts from the runs before it, so a run's usage is the change from this.
    """

    def __init__(self, path: Path) -> None:
        self.path = path

    def load(self) -> dict[str, dict[str, float]]:
        try:
            data: Any = json.loads(self.path.read_text(encoding="utf-8"))
        except (OSError, ValueError):
            return {}
        return cast(dict[str, dict[str, float]], data) if isinstance(data, dict) else {}

    def save(self, reported: dict[str, dict[str, float]]) -> None:
        self.path.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
        fd, tmp = tempfile.mkstemp(dir=self.path.parent, prefix=".tmp-")
        try:
            with os.fdopen(fd, "w", encoding="utf-8") as f:
                json.dump(reported, f)
            os.replace(tmp, self.path)
        except BaseException:
            try:
                os.unlink(tmp)
            except OSError:
                pass
            raise
