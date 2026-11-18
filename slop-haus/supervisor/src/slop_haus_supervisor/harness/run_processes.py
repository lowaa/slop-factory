"""Finding and killing everything a harness run started, by the token in its environment."""

from __future__ import annotations

import os
import signal
import subprocess

RUN_TOKEN_ENV = "SLOP_HAUS_SUPERVISOR_RUN"


class RunProcesses:
    """What one harness run started, found by a token in its environment.

    The harness's own process group is not enough: Claude Code and pi start each command the model
    runs in a session of its own, and a command the model backgrounds is reparented away once its
    shell exits, so it is in neither the group nor the harness's process tree. Its environment is
    still inherited, so a random token put there at start-up names exactly the processes of this
    run: not the owner's other processes, nor another run's.

    A pid is never signalled on the strength of an earlier look. The token is checked again just
    before the signal. On Linux that is through a pidfd opened first, so a pid reused by an
    unrelated process cannot be hit. Elsewhere (macOS has no pidfd) the check and the kill are two
    steps, which leaves a window of microseconds in which a process that exited and whose pid was
    reused could be hit. A command that clears its own environment escapes: this finds what the
    model started in the ordinary way, not what hides from it.
    """

    def __init__(self, token: str) -> None:
        self._marker = f"{RUN_TOKEN_ENV}={token}".encode()

    def find(self) -> list[int]:
        if os.path.isdir("/proc/self"):
            pids = [int(name) for name in os.listdir("/proc") if name.isdigit()]
            return [pid for pid in pids if pid != os.getpid() and self._carries(pid)]
        return self._find_by_ps()

    def _carries(self, pid: int) -> bool:
        if os.path.isdir("/proc/self"):
            try:
                with open(f"/proc/{pid}/environ", "rb") as f:
                    return self._marker in f.read().split(b"\0")
            except OSError:
                # Gone since the listing, or not ours to read.
                return False
        return pid in self._find_by_ps(pid)

    def _find_by_ps(self, only: int | None = None) -> list[int]:
        """macOS has no /proc; `ps -E` prints a process's environment after its command, for the user's own processes."""
        argv = ["ps", "-E", "-ww", "-o", "pid=,command=", "-p", str(only)] if only else ["ps", "-E", "-ww", "-x", "-o", "pid=,command="]
        try:
            out = subprocess.run(argv, capture_output=True, check=False, timeout=10).stdout
        except (OSError, subprocess.SubprocessError):
            return []
        found: list[int] = []
        for line in out.splitlines():
            pid, _, rest = line.strip().partition(b" ")
            if pid.isdigit() and int(pid) != os.getpid() and (b" " + self._marker + b" ") in (b" " + rest + b" "):
                found.append(int(pid))
        return found

    def signal(self, sig: int) -> int:
        """Sends sig to every process of the run, and says how many it found."""
        pids = self.find()
        for pid in pids:
            self._send(pid, sig)
        return len(pids)

    def _send(self, pid: int, sig: int) -> bool:
        pidfd: int | None = None
        pidfd_open = getattr(os, "pidfd_open", None)
        if pidfd_open is not None and getattr(signal, "pidfd_send_signal", None) is not None:
            try:
                pidfd = pidfd_open(pid)
            except OSError:
                return False
        try:
            if not self._carries(pid):
                return False
            if pidfd is not None:
                signal.pidfd_send_signal(pidfd, sig)
            else:
                os.kill(pid, sig)
            return True
        except (ProcessLookupError, PermissionError):
            return False
        finally:
            if pidfd is not None:
                os.close(pidfd)

    def kill_all(self) -> None:
        """KILL until none is left: a process that forks as it is killed is found on the next pass."""
        for _ in range(10):
            if not self.signal(signal.SIGKILL):
                return
