from __future__ import annotations

import logging
import math
import re
from datetime import UTC, datetime
from collections.abc import Mapping
from typing import Any, cast

from slop_haus_supervisor.harness.base import ModelUsage, Usage

# go-llm-usage's label patterns: the server refuses a whole report over one value outside them
# (fixtures/record_labels.json, which tests/usage runs). \Z and not $, which also matches before a
# trailing newline.
MODEL = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._:/@+\[\]-]{0,127}\Z")
PI_PROVIDER = re.compile(r"^[a-z0-9][a-z0-9._-]{0,31}\Z")
TOOL = re.compile(r"^[A-Za-z0-9_][A-Za-z0-9_.:-]{0,127}\Z")

log = logging.getLogger(__name__)


def now_iso() -> str:
    return datetime.now(UTC).strftime("%Y-%m-%dT%H:%M:%SZ")


# What the server is told with every usage report (`reporter_revision`), so it can flag usage from a
# reporter that over-counts by what sent it and not by when it arrived: a host runs a fix only once it has
# been updated and restarted. Bump it with the server's MIN_REPORTER_REVISION when a fix means an older
# reporter's usage is wrong. 1: a resumed session's usage is the change since its last result, and a
# session saved by an older host starts afresh.
USAGE_REVISION = 1

COUNTERS = ("requests", "input", "output", "cache_read", "cache_write", "reasoning", "cost_micros")
MODEL_USAGE_FIELDS = {
    "input": "inputTokens", "output": "outputTokens", "cache_read": "cacheReadInputTokens",
    "cache_write": "cacheCreationInputTokens", "reasoning": "thinkingTokens",
}


def as_float(value: Any) -> float:
    if isinstance(value, bool) or not isinstance(value, (int, float)):
        return 0.0
    return float(value) if math.isfinite(value) else 0.0


def as_int(value: Any) -> int:
    """A count from a harness record: zero for anything that is not a finite,
    positive number (json.loads accepts Infinity and NaN)."""
    if isinstance(value, bool) or not isinstance(value, (int, float)):
        return 0
    return int(value) if math.isfinite(value) and value > 0 else 0


class ConversationUsage:
    """A conversation's usage totals, from each turn's `result` record.

    A result's modelUsage is cumulative for the whole session, not the process
    (found by running the harness against a stand-in API: two turns of 100 and
    200 input tokens reported 300 for the second; and a `--resume` of a session
    starts from the earlier processes' total, Claude Code 2.1.303), where its
    `usage` is the turn's alone and covers no model by name. So a turn is the
    change in modelUsage since the session's last result, and that last result
    (`reported`) is persisted with the totals: a baseline kept per process would
    add the whole session again at every restart. Totals are persisted too,
    because the server stores what a report says in full: a host that restarted
    and reported only the new process's would shrink the conversation's usage.
    """

    @classmethod
    def resume(cls, data: dict[str, Any] | None) -> ConversationUsage:
        """The usage saved by a host, as this host carries on with it.

        Totals saved by an older host may be inflated (a session carried its pre-fix total across the fix,
        and the server refuses an average per request that high however honest the new turns), and the
        stored row for them is flagged. So they are not carried on: the usage starts afresh under a key of
        its own, from the baseline the older host saved, or from the next result where it saved none.
        """
        if not data or data.get("revision") == USAGE_REVISION:
            return cls(data)
        reported: dict[str, Any] = data.get("reported") or {}
        return cls({"reported": reported, "baseline_only": not reported, "key_suffix": f"r{USAGE_REVISION}"})

    def __init__(self, data: dict[str, Any] | None = None) -> None:
        data = data or {}
        # The next result only sets the baseline: nothing to take it away from.
        self.baseline_only: bool = bool(data.get("baseline_only", False))
        # Appended to the conversation's transcript key, for a row of its own.
        self.key_suffix: str = data.get("key_suffix", "")
        self.models: dict[str, dict[str, int]] = data.get("models", {})
        self.tools: dict[str, dict[str, list[int]]] = data.get("tools", {})
        self.reported: dict[str, dict[str, float]] = data.get("reported", {})
        self.started_at: str = data.get("started_at") or now_iso()
        # Per model, what only pi tells: its provider and how it was paid for.
        self.meta: dict[str, dict[str, str]] = data.get("meta", {})

    def dump(self) -> dict[str, Any]:
        return {
            "models": self.models, "tools": self.tools, "reported": self.reported, "started_at": self.started_at,
            "meta": self.meta, "revision": USAGE_REVISION, "baseline_only": self.baseline_only, "key_suffix": self.key_suffix,
        }

    def add_delta(self, provider: str, model: str, billing_mode: str, delta: Mapping[str, int]) -> None:
        """One turn's usage of a model, already a change and not a total: what
        pi's session stats come to once the host has taken the last ones away."""
        if not MODEL.match(model or "") or not PI_PROVIDER.match(provider or ""):
            return
        totals = self.models.setdefault(model, {name: 0 for name in COUNTERS})
        for name in COUNTERS:
            totals[name] += max(as_int(delta.get(name)), 0)
        self.meta[model] = {"provider": provider, "billing_mode": billing_mode}

    def add_turn(self, result: dict[str, Any]) -> None:
        reported = self.reported
        model_usage = result.get("modelUsage")
        if not isinstance(model_usage, dict):
            return
        turns = as_int(result.get("num_turns"))
        heaviest, heaviest_output = None, -1
        for raw_model, raw_cur in cast(dict[Any, Any], model_usage).items():
            if not isinstance(raw_cur, dict) or not isinstance(raw_model, str) or not MODEL.match(raw_model):
                continue
            model = raw_model
            cur = cast(dict[str, Any], raw_cur)
            prev = reported.get(model, {})
            if self.baseline_only:
                reported[model] = {field: as_int(cur.get(field)) for field in MODEL_USAGE_FIELDS.values()}
                reported[model]["costMicros"] = as_int(round(as_float(cur.get("costUSD")) * 1e6))
                continue
            cost = as_int(round(as_float(cur.get("costUSD")) * 1e6))
            if cost < int(prev.get("costMicros", 0)) or any(
                as_int(cur.get(field)) < as_int(prev.get(field)) for field in MODEL_USAGE_FIELDS.values()
            ):
                # A cumulative that went down is a session of its own (a new one, or a reset), not a
                # negative turn: its whole value is new.
                log.warning("modelUsage of %s fell below the last reported; counting it whole", model)
                prev = {}
            totals = self.models.setdefault(model, {name: 0 for name in COUNTERS})
            delta_output = 0
            for name, field in MODEL_USAGE_FIELDS.items():
                delta = max(as_int(cur.get(field)) - as_int(prev.get(field)), 0)
                totals[name] += delta
                if name == "output":
                    delta_output = delta
            totals["cost_micros"] += max(cost - int(prev.get("costMicros", 0)), 0)
            reported[model] = {field: as_int(cur.get(field)) for field in MODEL_USAGE_FIELDS.values()}
            reported[model]["costMicros"] = cost
            if delta_output > heaviest_output:
                heaviest, heaviest_output = model, delta_output
        self.baseline_only = False
        if heaviest is not None:
            self.models[heaviest]["requests"] += turns

    def add_call(self, model: str, tool: str) -> None:
        if MODEL.match(model or "") and TOOL.match(tool or ""):
            self.tools.setdefault(model, {}).setdefault(tool, [0, 0])[0] += 1

    def add_error(self, model: str, tool: str) -> None:
        if MODEL.match(model or "") and TOOL.match(tool or ""):
            self.tools.setdefault(model, {}).setdefault(tool, [0, 0])[1] += 1

    def as_usage(self, billing_mode: str) -> Usage:
        """The totals as a harness reports a run's usage, for a caller that sends them on."""
        return Usage(tuple(
            ModelUsage(
                self.meta.get(model, {}).get("provider", "anthropic"), model,
                self.meta.get(model, {}).get("billing_mode", billing_mode), dict(totals),
            )
            for model, totals in sorted(self.models.items())
        ))

    def records(self, billing_mode: str, harness: str = "claude") -> list[dict[str, Any]]:
        ended = now_iso()
        out: list[dict[str, Any]] = []
        for model, totals in sorted(self.models.items()):
            meta = self.meta.get(model, {})
            billing_mode_of_model = meta.get("billing_mode", billing_mode)
            record: dict[str, Any] = {
                "schema_version": 1, "provider": meta.get("provider", "anthropic"), "model": model, "harness": harness,
                "billing_mode": billing_mode_of_model, "requests": totals["requests"],
                "input_tokens": totals["input"], "output_tokens": totals["output"],
                "cache_read_tokens": totals["cache_read"], "cache_write_tokens": totals["cache_write"],
                "reasoning_tokens": min(totals["reasoning"], totals["output"]),
                "started_at": self.started_at, "ended_at": ended,
            }
            if billing_mode_of_model == "metered":
                record["reported_cost_microdollars"] = totals["cost_micros"]
            tools = self.tools.get(model, {})
            if tools:
                record["tools"] = [
                    {"tool": tool, "calls": calls, "errors": errors, "duration_ms": 0, "result_bytes": 0}
                    for tool, (calls, errors) in sorted(tools.items())
                ]
            out.append(record)
        return out
