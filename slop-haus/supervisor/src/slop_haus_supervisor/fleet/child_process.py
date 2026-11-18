"""A long-lived child the supervisor starts and watches.

`ProcessRunner` runs a command to completion; a Remote Control host is meant to outlive the call, so it
needs a port of its own.
"""

from __future__ import annotations

import os
import signal
import subprocess
from collections.abc import Mapping, Sequence
from pathlib import Path
from typing import Protocol


class Child(Protocol):
    def poll(self) -> int | None:
        """The exit code, or None while the child runs."""
        ...

    def terminate(self, stop_wait: float) -> None:
        """Asks it and what it started to stop, and ends them after `stop_wait` seconds if they have not."""
        ...


class ChildSpawner(Protocol):
    def spawn(self, argv: Sequence[str], cwd: Path, env: Mapping[str, str]) -> Child:
        """`env` is the child's whole environment: nothing is inherited."""
        ...


class _SubprocessChild:
    def __init__(self, proc: subprocess.Popen[bytes]) -> None:
        self._proc = proc

    def poll(self) -> int | None:
        return self._proc.poll()

    def terminate(self, stop_wait: float) -> None:
        self._signal_group(signal.SIGTERM)
        try:
            self._proc.wait(timeout=stop_wait)
        except subprocess.TimeoutExpired:
            pass
        # Also what the host started that outlived it or ignored the TERM.
        self._signal_group(signal.SIGKILL)
        self._proc.wait()

    def _signal_group(self, sig: int) -> None:
        try:
            os.killpg(self._proc.pid, sig)
        except (ProcessLookupError, PermissionError):
            pass


class SubprocessSpawner:
    def spawn(self, argv: Sequence[str], cwd: Path, env: Mapping[str, str]) -> Child:
        # Its own process group, so ending the host ends what its sessions started. Output goes to the supervisor's.
        proc = subprocess.Popen(list(argv), cwd=cwd, env=dict(env), stdin=subprocess.DEVNULL, start_new_session=True)
        return _SubprocessChild(proc)
