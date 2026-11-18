"""Recognising why a run failed from the text a harness leaves behind.

The strings are Claude Code's (2.1.303, read out of its binary and its stream-json schema) and
pi's; fixtures of each are in tests/harness/fixtures.py. A message worded differently is not
recognised and falls through to the caller's default, so a wrong guess costs a retry, not a loss.
"""

from __future__ import annotations

import re
from dataclasses import dataclass
from datetime import datetime, timedelta
from typing import Any
from zoneinfo import ZoneInfo, ZoneInfoNotFoundError

from slop_haus_supervisor.harness.base import OutcomeKind

USAGE_LIMIT = re.compile(
    r"usage limit reached"
    r"|hit your (?:[\w' -]{0,30} )?limit"
    r"|out of (?:extra )?usage"
    r"|team's shared budget"
    r"|limit resets",
    re.IGNORECASE,
)
AUTH_FAILED = re.compile(
    r"invalid api key|invalid x-api-key|please run /login|authentication_error|failed to authenticate"
    r"|\b401\b|oauth token (?:has )?(?:expired|been revoked)|credit balance is too low|not logged in"
    r'|unknown provider "|no models? available|no model selected|\bno api key\b',
    re.IGNORECASE,
)
TRANSIENT = re.compile(
    r"api error|\b(?:429|500|502|503|504|529)\b|overloaded|rate.?limit|timed? ?out|econn(?:reset|refused)"
    r"|enotfound|network|connection (?:error|reset|refused)|socket hang up|unable to connect|fetch failed"
    r"|internal server error|service unavailable",
    re.IGNORECASE,
)
# The refusals of a resume: a session this directory has no record of, or one another process holds.
RESUME_FAILED = re.compile(r"no conversation found|already in use", re.IGNORECASE)

# "Claude AI usage limit reached|1760000000", the older form with the reset as epoch seconds.
EPOCH_AFTER_PIPE = re.compile(r"\|(\d{9,11})\b")
# "resets 3pm (America/New_York)", "resets 3:30 pm (Europe/Berlin)".
CLOCK_RESET = re.compile(r"resets\s+(?:at\s+)?(\d{1,2})(?::(\d{2}))?\s*([ap]m)\s*\(([A-Za-z_/+-]+)\)", re.IGNORECASE)


@dataclass(frozen=True)
class Failure:
    kind: OutcomeKind
    reset_at: float | None = None


def is_resume_failure(text: str) -> bool:
    return RESUME_FAILED.search(text) is not None


def reset_time(text: str, now: float) -> float | None:
    """The reset a limit message names, as epoch seconds after `now`; None when it names none we read."""
    match = EPOCH_AFTER_PIPE.search(text)
    if match and float(match.group(1)) > now:
        return float(match.group(1))
    clock = CLOCK_RESET.search(text)
    if clock is None:
        return None
    try:
        zone = ZoneInfo(clock.group(4))
    except (ZoneInfoNotFoundError, ValueError):
        return None
    hour = int(clock.group(1)) % 12 + (12 if clock.group(3).lower() == "pm" else 0)
    local_now = datetime.fromtimestamp(now, zone)
    at = local_now.replace(hour=hour, minute=int(clock.group(2) or 0), second=0, microsecond=0)
    if at <= local_now:
        at += timedelta(days=1)
    return at.timestamp()


def classify_text(text: str, now: float) -> Failure | None:
    """Usage limit before auth before transient: the later patterns also match the earlier messages."""
    if USAGE_LIMIT.search(text):
        return Failure(OutcomeKind.USAGE_LIMIT, reset_time(text, now))
    if AUTH_FAILED.search(text):
        return Failure(OutcomeKind.AUTH_FAILED)
    if TRANSIENT.search(text):
        return Failure(OutcomeKind.TRANSIENT)
    return None


def rejected_reset(info: dict[str, Any], now: float) -> float | None:
    """The reset of a `rate_limit_event`'s rate_limit_info. It is never updated once emitted, so a
    reset already past says the limit is not current."""
    reset = info.get("resetsAt")
    if isinstance(reset, (int, float)) and not isinstance(reset, bool) and reset > now:
        return float(reset)
    return None
