from __future__ import annotations

import os
import signal
import subprocess
import threading
from collections.abc import Mapping, Sequence
from pathlib import Path
from typing import IO

from slop_haus_supervisor.ports import ProcessResult


class ReviewKilled(Exception):
    """The review was ended from outside (lease revoked, time budget, supervisor stopping)."""


class ProcessGroup:
    """Every process one review starts, so a kill from another thread ends all of them, git included.

    Each runs in its own session: review.sh execs `go run`, which starts the reviewer binary and its
    provider CLI under it, and the group signal reaches those too.
    """

    def __init__(self) -> None:
        self._lock = threading.Lock()
        self._live: list[subprocess.Popen[bytes]] = []
        self._killed = False

    @property
    def killed(self) -> bool:
        return self._killed

    def start(
        self,
        argv: Sequence[str],
        cwd: Path,
        env: Mapping[str, str] | None,
        stdout: IO[bytes] | int,
        stderr: IO[bytes] | int,
    ) -> subprocess.Popen[bytes]:
        # Under the lock, so a kill racing the spawn either stops it or sees the process.
        with self._lock:
            if self._killed:
                raise ReviewKilled()
            proc = subprocess.Popen(
                list(argv), cwd=cwd, env=None if env is None else dict(env), stdin=subprocess.DEVNULL,
                stdout=stdout, stderr=stderr, start_new_session=True,
            )
            self._live.append(proc)
        return proc

    def wait(self, proc: subprocess.Popen[bytes], timeout: float | None = None) -> int:
        """The exit code; raises ReviewKilled when a kill ended it, TimeoutExpired (after ending it) when `timeout` passed."""
        try:
            code = proc.wait(timeout=timeout)
        except subprocess.TimeoutExpired:
            self._end(proc)
            raise
        finally:
            self._forget(proc)
        if self._killed:
            raise ReviewKilled()
        return code

    def run(self, argv: Sequence[str], cwd: Path, timeout: float, env: Mapping[str, str] | None = None) -> ProcessResult:
        proc = self.start(argv, cwd, env, subprocess.PIPE, subprocess.PIPE)
        try:
            out, err = proc.communicate(timeout=timeout)
        except subprocess.TimeoutExpired:
            self._end(proc)
            proc.communicate()
            raise
        finally:
            self._forget(proc)
        if self._killed:
            raise ReviewKilled()
        return ProcessResult(proc.returncode, out.decode(errors="replace"), err.decode(errors="replace"))

    def kill(self) -> None:
        with self._lock:
            self._killed = True
            live = list(self._live)
        for proc in live:
            # A reaped process's pid may already be someone else's group.
            if proc.returncode is None:
                _signal_group(proc, signal.SIGKILL)

    def _end(self, proc: subprocess.Popen[bytes]) -> None:
        _signal_group(proc, signal.SIGKILL)
        proc.wait()

    def _forget(self, proc: subprocess.Popen[bytes]) -> None:
        with self._lock:
            if proc in self._live:
                self._live.remove(proc)


def _signal_group(proc: subprocess.Popen[bytes], sig: int) -> None:
    try:
        os.killpg(proc.pid, sig)
    except (ProcessLookupError, PermissionError):
        pass
