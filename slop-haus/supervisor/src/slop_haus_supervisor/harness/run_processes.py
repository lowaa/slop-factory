"""Finding and killing everything a harness run started, by the token in its environment."""

from __future__ import annotations

import os
import signal
import subprocess
import sys

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
    unrelated process cannot be hit. Elsewhere (macOS has no pidfd, nor do uv's standalone Linux
    Pythons, which the supervisor runs on) the check and the kill are two steps, which leaves a window of microseconds in which a process that exited and whose pid was
    reused could be hit. A command that clears its own environment escapes: this finds what the
    model started in the ordinary way, not what hides from it.

    Without /proc (macOS) the environment is read with `ps -E`, which omits it for Apple platform
    binaries (/bin/bash, /bin/sleep, /usr/bin/*), so a run's processes there are those whose
    environment carries the token plus all their descendants by parent pid, and the re-check before a
    signal applies the same rule. What has once been found stays the run's while its start time is
    unchanged, so stopping the harness first does not lose what it started. The limit: the look happens
    when the run is stopped, not during it, so a platform-binary command already reparented away by then
    (a background job whose shell exited, in a session of its own) has neither the token on show nor a
    marked ancestor, and is not found. Linux reads /proc/<pid>/environ and has no such gap.
    """

    def __init__(self, token: str) -> None:
        self._marker = f"{RUN_TOKEN_ENV}={token}".encode()
        # Where there is no /proc: the pids found so far, with their start times (see marked_and_descendants).
        self.remembered: dict[int, bytes] = {}

    def find(self) -> list[int]:
        if os.path.isdir("/proc/self"):
            pids = [int(name) for name in os.listdir("/proc") if name.isdigit()]
            return [pid for pid in pids if pid != os.getpid() and self.carries(pid)]
        return self._find_by_ps()

    def carries(self, pid: int) -> bool:
        if os.path.isdir("/proc/self"):
            try:
                with open(f"/proc/{pid}/environ", "rb") as f:
                    return self._marker in f.read().split(b"\0")
            except OSError:
                # Gone since the listing, or not ours to read.
                return False
        # Without /proc: one of the run's processes when last looked, and still that process, not a reuse of its pid.
        # One `ps` for the pid rather than the whole scan again: this runs before every signal.
        remembered = self.remembered.get(pid)
        return remembered is not None and remembered == _ps(["ps", "-o", "lstart=", "-p", str(pid)]).strip()

    def look_before_stopping(self) -> None:
        """Without /proc, records what the run has now, so stopping its harness first cannot orphan any of it unseen.
        Linux reads the environment of every process, an orphan's included, so there is nothing to record."""
        if not os.path.isdir("/proc/self"):
            self._find_by_ps()

    def _find_by_ps(self) -> list[int]:
        """macOS has no /proc: `ps -E` prints a process's environment after its command, but only for non-Apple
        binaries, so the marked processes are widened to their descendants by parent pid."""
        environments = _ps(["ps", "-E", "-ww", "-x", "-o", "pid=,command="])
        parents = _ps(["ps", "-ax", "-o", "pid=,ppid="])
        starts = _parse_start_times(_ps(["ps", "-ax", "-o", "pid=,lstart="]))
        found = self.marked_and_descendants(environments, parents, starts)
        self.remembered.update({pid: starts[pid] for pid in found if pid in starts})
        return found

    def marked_and_descendants(self, environments: bytes, parents: bytes, starts: dict[int, bytes]) -> list[int]:
        """The rule on the output of the `ps` calls: kept apart from running them so it can be tested on any host.

        A process seen as part of the run stays part of it while it keeps the same start time, even once its
        parent is gone and it is reparented away: a stop signals the harness and so orphans what it started
        before the look that would find those.
        """
        roots = self._marked(environments)
        # A copy: another thread stopping the same run may be adding to it.
        roots += [pid for pid, start in list(self.remembered.items()) if starts.get(pid) == start]
        children: dict[int, list[int]] = {}
        for pid, ppid in _parent_pairs(parents):
            children.setdefault(ppid, []).append(pid)
        order = list(dict.fromkeys(roots))
        seen = set(order)
        for pid in order:  # grows as it is walked: breadth first
            for child in children.get(pid, []):
                if child not in seen:
                    seen.add(child)
                    order.append(child)
        # Deepest first: killing a parent reparents its children away from the run, and the re-check before each
        # signal would then no longer see them as its own.
        return [pid for pid in reversed(order) if pid != os.getpid()]

    def _marked(self, environments: bytes) -> list[int]:
        pids: list[int] = []
        for line in environments.splitlines():
            pid, _, rest = line.strip().partition(b" ")
            if pid.isdigit() and (b" " + self._marker + b" ") in (b" " + rest + b" "):
                pids.append(int(pid))
        return pids

    def signal(self, sig: int) -> int:
        """Sends sig to every process of the run, and says how many it found."""
        pids = self.find()
        for pid in pids:
            self._send(pid, sig)
        return len(pids)

    def _send(self, pid: int, sig: int) -> bool:
        if _has_pidfd():
            return self._send_by_pidfd(pid, sig)
        try:
            if not self.carries(pid):
                return False
            os.kill(pid, sig)
            return True
        except (ProcessLookupError, PermissionError):
            return False

    def _send_by_pidfd(self, pid: int, sig: int) -> bool:
        if sys.platform != "linux":  # for pyright: pidfd_open and pidfd_send_signal exist only there
            return False
        try:
            pidfd = os.pidfd_open(pid)
        except OSError:
            return False
        try:
            if not self.carries(pid):
                return False
            signal.pidfd_send_signal(pidfd, sig)
            return True
        except (ProcessLookupError, PermissionError):
            return False
        finally:
            os.close(pidfd)

    def kill_all(self) -> None:
        """KILL until none is left: a process that forks as it is killed is found on the next pass."""
        for _ in range(10):
            if not self.signal(signal.SIGKILL):
                return


def _has_pidfd() -> bool:
    # Not every Linux Python has them: uv's python-build-standalone builds omit both.
    return hasattr(os, "pidfd_open") and hasattr(signal, "pidfd_send_signal")


def _ps(argv: list[str]) -> bytes:
    try:
        return subprocess.run(argv, capture_output=True, check=False, timeout=10).stdout
    except (OSError, subprocess.SubprocessError):
        return b""


def _parent_pairs(out: bytes) -> list[tuple[int, int]]:
    pairs: list[tuple[int, int]] = []
    for line in out.splitlines():
        fields = line.split()
        if len(fields) == 2 and fields[0].isdigit() and fields[1].isdigit():
            pairs.append((int(fields[0]), int(fields[1])))
    return pairs


def _parse_start_times(out: bytes) -> dict[int, bytes]:
    times: dict[int, bytes] = {}
    for line in out.splitlines():
        pid, _, start = line.strip().partition(b" ")
        if pid.isdigit() and start:
            times[int(pid)] = start.strip()
    return times
