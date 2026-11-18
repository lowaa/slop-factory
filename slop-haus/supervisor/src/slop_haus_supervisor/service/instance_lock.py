from __future__ import annotations

import fcntl
import os
from pathlib import Path
from types import TracebackType
from typing import IO


class AlreadyRunning(Exception):
    def __init__(self, message: str, owner: str = "") -> None:
        super().__init__(message)
        # What the holder wrote beside its pid (see InstanceLock), so a caller can say who holds it.
        self.owner = owner


class InstanceLock:
    """One `run` per tenant root. The OS drops the lock with the process, so a crash never leaves it held."""

    def __init__(self, path: Path, owner: str = "") -> None:
        self._path = path
        self._owner = owner
        self._file: IO[str] | None = None

    def __enter__(self) -> InstanceLock:
        # Not opened for write: a second instance must not truncate the holder's pid.
        fd = os.open(self._path, os.O_RDWR | os.O_CREAT, 0o600)
        handle = os.fdopen(fd, "r+")
        try:
            fcntl.flock(handle, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            holder = handle.read().strip()
            handle.close()
            pid, _, owner = holder.partition(" ")
            raise AlreadyRunning(
                f"another slop-haus-supervisor run holds {self._path}" + (f" (pid {pid})" if pid else ""), owner
            ) from None
        handle.truncate(0)
        handle.write(f"{os.getpid()} {self._owner}".rstrip() + "\n")
        handle.flush()
        self._file = handle
        return self

    def __exit__(self, *_exc: type[BaseException] | BaseException | TracebackType | None) -> None:
        if self._file is not None:
            self._file.close()
            self._file = None
