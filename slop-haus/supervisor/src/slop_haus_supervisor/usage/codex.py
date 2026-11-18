from __future__ import annotations

from typing import Any

from slop_haus_supervisor.harness.text import as_dict
from slop_haus_supervisor.usage.billing import counter_change
from slop_haus_supervisor.usage.conversation import as_int

CODEX_PROVIDER = "openai"


def codex_counters(breakdown: dict[str, Any]) -> dict[str, int]:
    """One `ThreadTokenUsage` breakdown (`total` or `last`) as usage counters.

    OpenAI counts cached input inside `inputTokens`, so it is taken out to leave the counters
    disjoint like Claude's and pi's. reasoningOutputTokens is also inside `outputTokens`,
    as `reasoning` is for the other harnesses.
    There is no cost: Codex reports none and no price table exists, so the board shows tokens.
    """
    cached = as_int(breakdown.get("cachedInputTokens"))
    written = as_int(breakdown.get("cacheWriteInputTokens"))
    return {
        "input": max(as_int(breakdown.get("inputTokens")) - cached - written, 0),
        "output": as_int(breakdown.get("outputTokens")), "reasoning": as_int(breakdown.get("reasoningOutputTokens")),
        "cache_read": cached, "cache_write": written,
        "cost_micros": 0,
    }


def turn_baseline(total: dict[str, int], last: dict[str, int]) -> dict[str, int]:
    """The thread's standing before a turn, from the turn's first usage notification when nothing
    came before it (no replay): `total - last` takes that notification's own request away."""
    return counter_change(total, last)


class CodexUsage:
    """A run's usage from `thread/tokenUsage/updated` notifications.

    `total` is cumulative for the thread, also after a resume, so the run's own usage is the
    change from where the thread stood before the run began. The baseline is, in order:
    - the total of the newest notification before `turn/started`: after `thread/resume` Codex
      replays the thread's standing then (`total - last` of that would be 0);
    - else `total - last` of the turn's first notification, for a resume that replays nothing;
    - for a fresh thread both are 0.
    """

    def __init__(self) -> None:
        self._baseline: dict[str, int] | None = None
        self._latest: dict[str, int] | None = None
        self._turn_started = False

    def on_turn_started(self) -> None:
        """The run's turn began: the thread's standing so far is not the run's."""
        self._turn_started = True
        if self._latest is not None and self._baseline is None:
            self._baseline = self._latest

    def on_notification(self, params: object) -> None:
        """Takes the `params` of a `thread/tokenUsage/updated`; anything malformed is ignored."""
        usage = as_dict((as_dict(params) or {}).get("tokenUsage")) or {}
        total = as_dict(usage.get("total"))
        if total is None:
            return
        counters = codex_counters(total)
        if self._latest is None and self._turn_started:
            self._baseline = turn_baseline(counters, codex_counters(as_dict(usage.get("last")) or {}))
        self._latest = counters

    def counters(self) -> dict[str, int]:
        """Usage since the run began; empty when no notification arrived."""
        if self._latest is None:
            return {}
        return counter_change(self._latest, self._baseline or {})
