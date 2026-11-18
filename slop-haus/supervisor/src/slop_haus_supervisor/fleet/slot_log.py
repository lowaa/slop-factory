"""What a slot says about itself: lifecycle lines, scrubbed reasons, and the throttles that keep a quiet slot quiet."""

from __future__ import annotations

import logging
import re

from slop_haus_supervisor.fleet import policy
from slop_haus_supervisor.harness.text import SecretMasker
from slop_haus_supervisor.ports import Clock

# A slot that is offered work and refused every poll would otherwise log one line per poll.
REFUSAL_LOG_INTERVAL = 10 * 60.0
# An idle slot says so this often, so a quiet log never means the slot is unknown.
IDLE_LOG_INTERVAL = 15 * 60.0
_MAX_REASON = 512

# A git error can quote a remote URL with credentials in it.
_URL_USERINFO = re.compile(r"(?<=://)[^/\s@]+@")


def prompt_kind(prompt: str) -> str:
    if prompt.startswith("Work ticket #"):
        return "initial"
    known = {
        policy.NUDGE_PROMPT: "nudge",
        policy.LOCK_HELD_PROMPT: "lock held",
        policy.MESSAGE_PROMPT: "message",
        policy.HANDOVER_PROMPT: "handover",
    }
    if prompt in known:
        return known[prompt]
    return "checks finished" if prompt.startswith("checks finished") else "other"


class SlotLog:
    def __init__(self, agent: str, logger: logging.Logger, masker: SecretMasker, clock: Clock) -> None:
        self.agent = agent
        self._logger = logger
        self._masker = masker
        self._clock = clock
        self.last_poll = "not polled yet"
        self._next_idle_log = clock.now() + IDLE_LOG_INTERVAL
        self._refusals_logged: dict[tuple[int | None, str], float] = {}

    def info(self, message: str, *args: object) -> None:
        self._logger.info("slot=%s " + message, self.agent, *args)

    def warning(self, message: str, *args: object) -> None:
        self._logger.warning("slot=%s " + message, self.agent, *args)

    def clean(self, text: str) -> str:
        """Text of outside origin, made safe for a log line: masked, one line, bounded."""
        one_line = " ".join(_URL_USERINFO.sub("", self._masker.mask(text)).split())
        return one_line if len(one_line) <= _MAX_REASON else one_line[:_MAX_REASON] + "..."

    @property
    def refusals_remembered(self) -> int:
        return len(self._refusals_logged)

    def idle_entered(self) -> None:
        self._next_idle_log = self._clock.now() + IDLE_LOG_INTERVAL

    def idle_heartbeat(self) -> None:
        now = self._clock.now()
        if now >= self._next_idle_log:
            self._next_idle_log = now + IDLE_LOG_INTERVAL
            self.info("idle; last poll: %s", self.last_poll)

    def refusal(self, ticket_id: int | None, reason: str) -> None:
        reason = self.clean(reason)
        self.last_poll = f"offered #{ticket_id} and refused: {reason}"
        now = self._clock.now()
        key = (ticket_id, reason)
        # Expired entries go so a long-lived slot does not keep every refusal it ever saw.
        self._refusals_logged = {k: t for k, t in self._refusals_logged.items() if now - t < REFUSAL_LOG_INTERVAL}
        last = self._refusals_logged.get(key)
        if last is None or now - last >= REFUSAL_LOG_INTERVAL:
            self._refusals_logged[key] = now
            self.info("claim of ticket #%s refused: %s", ticket_id, reason)
        self.idle_heartbeat()
