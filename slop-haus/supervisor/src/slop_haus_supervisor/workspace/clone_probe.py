from __future__ import annotations

import os
import subprocess
from collections.abc import Callable
from dataclasses import dataclass

SSH_PROBE_SECONDS = 30


@dataclass(frozen=True)
class ProbeResult:
    works: bool
    reason: str


Probe = Callable[[str], ProbeResult]


def ssh_works(url: str) -> ProbeResult:
    env = {**os.environ, "GIT_TERMINAL_PROMPT": "0", "GIT_SSH_COMMAND": "ssh -o BatchMode=yes -o ConnectTimeout=10"}
    try:
        done = subprocess.run(
            ["git", "ls-remote", "--exit-code", url, "HEAD"],
            env=env, capture_output=True, text=True, check=False, timeout=SSH_PROBE_SECONDS,
        )  # fmt: skip
    except subprocess.TimeoutExpired:
        return ProbeResult(False, f"timed out after {SSH_PROBE_SECONDS}s")
    if done.returncode == 0:
        return ProbeResult(True, "git ls-remote over ssh succeeded")
    lines = done.stderr.strip().splitlines()
    first = lines[0] if lines else "no output"
    if "permission denied" in done.stderr.lower():
        return ProbeResult(False, f"auth refused: {first}")
    return ProbeResult(False, f"exit {done.returncode}: {first}")
