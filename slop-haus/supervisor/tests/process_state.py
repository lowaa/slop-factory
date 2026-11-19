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
