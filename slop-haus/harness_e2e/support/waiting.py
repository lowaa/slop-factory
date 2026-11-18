"""Waiting on conditions. Nothing in this suite sleeps for a fixed time: it
polls a condition, and says what it was waiting for when the deadline passes."""

from __future__ import annotations

import os
import time
from typing import Callable, TypeVar

T = TypeVar("T")


class Timeout(AssertionError):
    pass


def default_timeout() -> float:
    # Short by default so a break fails fast; a slow machine raises it.
    return float(os.environ.get("HARNESS_E2E_TIMEOUT", "30"))


def wait_for(
    what: str,
    probe: Callable[[], T | None],
    timeout: float | None = None,
    interval: float = 0.2,
    diagnose: Callable[[], str] | None = None,
) -> T:
    """Polls probe until it returns something other than None/False."""
    deadline = time.monotonic() + (default_timeout() if timeout is None else timeout)
    while True:
        value = probe()
        if value:
            return value
        if time.monotonic() >= deadline:
            extra = f"\n{diagnose()}" if diagnose else ""
            raise Timeout(f"timed out waiting for {what}{extra}")
        time.sleep(interval)
