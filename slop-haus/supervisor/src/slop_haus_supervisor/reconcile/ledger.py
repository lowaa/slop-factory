from __future__ import annotations

import json
from pathlib import Path
from typing import cast

from slop_haus_supervisor.service.control import write_atomically


class ResumeLedger:
    """The `resume_generation` the reconciler last applied per agent, kept across restarts.

    An agent first seen is recorded without being applied: a supervisor that starts has every slot unpaused,
    and a counter raised while it was down is not a request that should fire on a later pause.
    """

    def __init__(self, path: Path) -> None:
        self._path = path
        self._applied: dict[str, int] = {}
        try:
            raw: object = json.loads(path.read_text())
            if isinstance(raw, dict):
                self._applied = {str(k): v for k, v in cast(dict[object, object], raw).items() if isinstance(v, int)}
        except (OSError, ValueError):
            pass

    def raised(self, agent_id: str, current: int) -> bool:
        """Whether `current` exceeds what was applied; records it either way."""
        before = self._applied.get(agent_id)
        if before == current:
            return False
        self._applied[agent_id] = current
        self._path.parent.mkdir(parents=True, exist_ok=True)
        write_atomically(self._path, self._applied)
        return before is not None and current > before
