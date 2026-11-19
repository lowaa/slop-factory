"""Whether a process is running, on a host with /proc (Linux) and on one without (macOS)."""

from __future__ import annotations

import os
import subprocess


def process_running(pid: int) -> bool:
    """Whether the process is there and not a zombie waiting to be reaped."""
    try:
        os.kill(pid, 0)
    except ProcessLookupError:
        return False
    except PermissionError:
        pass
    if os.path.isdir("/proc/self"):
        # A pid gone since the kill check has no stat file: gone, not a reason to try ps (the test image has none).
        try:
            with open(f"/proc/{pid}/stat", encoding="utf-8") as f:
                return f.read().rsplit(")", 1)[1].split()[0] != "Z"
        except OSError:
            return False
    # No /proc (macOS): ps prints the state letters, and nothing at all for a pid that is gone.
    state = subprocess.run(["ps", "-o", "stat=", "-p", str(pid)], capture_output=True, text=True, check=False).stdout.strip()
    return bool(state) and not state.startswith("Z")


def command_line(pid: int) -> str:
    """The process's arguments, or "" while it is mid-exec or gone.

    Linux fills in a process's arguments and its environment together at the end of an exec, so once the new
    command line shows, /proc/<pid>/environ is readable too; before that, a fork that has not yet exec'd shows its
    parent's.
    """
    if os.path.isdir("/proc/self"):
        try:
            with open(f"/proc/{pid}/cmdline", "rb") as f:
                return " ".join(arg.decode(errors="replace") for arg in f.read().split(b"\0") if arg)
        except OSError:
            return ""
    return subprocess.run(["ps", "-o", "args=", "-p", str(pid)], capture_output=True, text=True, check=False).stdout.strip()
