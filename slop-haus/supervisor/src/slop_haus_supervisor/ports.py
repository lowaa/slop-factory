"""Seams to the machine: time, processes and files, so `fleet/` never touches the OS."""

from __future__ import annotations

from collections.abc import Mapping

import os
import subprocess
import time
from dataclasses import dataclass
from pathlib import Path
from typing import Protocol


class Clock(Protocol):
    def now(self) -> float:
        """Wall-clock seconds since the epoch (usage-limit resets are absolute times)."""
        ...


@dataclass(frozen=True)
class ProcessResult:
    returncode: int
    stdout: str
    stderr: str


class ProcessRunner(Protocol):
    def run(self, argv: list[str], cwd: Path, timeout: float | None = None, env: Mapping[str, str] | None = None) -> ProcessResult:
        """Raises subprocess.TimeoutExpired when `timeout` passes first. `env` is added to the inherited environment."""
        ...


class Alerts(Protocol):
    def alert(self, message: str) -> None:
        """Tell a person something needs attention that no ticket comment will reach."""
        ...


class SystemClock:
    def now(self) -> float:
        return time.time()


class SubprocessRunner:
    def run(self, argv: list[str], cwd: Path, timeout: float | None = None, env: Mapping[str, str] | None = None) -> ProcessResult:
        full_env = None if env is None else {**os.environ, **env}
        proc = subprocess.run(argv, cwd=cwd, capture_output=True, text=True, check=False, timeout=timeout, env=full_env)
        return ProcessResult(proc.returncode, proc.stdout, proc.stderr)
