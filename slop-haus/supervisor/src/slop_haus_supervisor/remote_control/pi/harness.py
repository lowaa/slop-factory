from __future__ import annotations

import dataclasses
from collections.abc import Sequence
from pathlib import Path

from slop_haus_supervisor.harness.base import RunSpec
from slop_haus_supervisor.harness.pi import PiHarness
from slop_haus_supervisor.harness.process import HarnessSession


class RemoteControlPi(PiHarness):
    """pi as the Remote Control starts it: its own agent directory in the host's state directory
    keeps the owner's pi settings, extensions and login out of the conversation."""

    def __init__(self, state_dir: Path, extension: Path, system_prompt: str, variant: str = "", *,
                 command: Sequence[str] = ("pi",), grace: float = 5.0,
                 login_dir: Path | None = None) -> None:
        super().__init__(state_dir, extension, system_prompt, command=command, grace=grace)
        self._variant = variant
        self._login_dir = login_dir

    def use_variant(self, variant: str) -> None:
        """The model flags of the sessions it starts from now on."""
        self._variant = variant

    def config_dir(self, root: Path) -> Path:
        """`login_dir` is the supervisor's pi login, shared with its worker slots."""
        return self._login_dir or root / "pi-agent"

    def open_session(self, spec: RunSpec, session_id: str | None = None, resume: bool = False) -> HarnessSession:
        # pi's --session-id creates a session that is missing, so there is no resume to get wrong.
        return super().open_session(dataclasses.replace(spec, variant=self._variant), session_id)
