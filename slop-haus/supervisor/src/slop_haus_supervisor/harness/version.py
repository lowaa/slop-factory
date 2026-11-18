"""What version of a harness the supervisor runs, for the lease's record of what an attempt ran with."""

from __future__ import annotations

import subprocess

# Short: the supervisor starts with it, and a harness that hangs on `--version` must not hang the start.
TIMEOUT_SECONDS = 10.0
MAX_LENGTH = 100


def detect_version(command: str) -> str:
    """The first line `<command> --version` prints, or "" when it cannot be read.

    Failing to read it is not an error: the record says "not recorded" and the harness itself is checked elsewhere.
    """
    try:
        done = subprocess.run([command, "--version"], capture_output=True, text=True, timeout=TIMEOUT_SECONDS, check=False)
    except (OSError, subprocess.SubprocessError):
        return ""
    if done.returncode != 0:
        return ""
    lines = done.stdout.strip().splitlines()
    return lines[0].strip()[:MAX_LENGTH] if lines else ""
