from __future__ import annotations

from dataclasses import dataclass

POLL_WAIT_SECONDS = 25
# Well inside the server's host timeout (three poll waits).
LEASE_REFRESH_SECONDS = 10.0


@dataclass
class Settings:
    idle_timeout: float = 300.0
    harness_grace: float = 5.0
    deliver_timeout: float = 60.0
    git_timeout: float = 120.0
    token_ttl: float = 30.0
    poll_wait: int = POLL_WAIT_SECONDS
    lease_refresh: float = LEASE_REFRESH_SECONDS
    # How long a turn may take to end after the harness was asked to stop it, before the host ends the
    # process and resumes the session instead.
    interrupt_timeout: float = 10.0
    min_report_interval: float = 0.25
    # How often the workspace layout is looked at again; it is sent only when it changed.
    layout_interval: float = 60.0
