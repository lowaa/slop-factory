from __future__ import annotations

import unittest

from slop_haus_supervisor.usage.billing import METERED, SUBSCRIPTION, codex_billing_mode
from slop_haus_supervisor.usage.codex import CodexUsage, codex_counters


def breakdown(input: int, cached: int, output: int, reasoning: int = 0, written: int = 0) -> dict[str, int]:
    return {"totalTokens": input + output, "inputTokens": input, "cachedInputTokens": cached,
            "cacheWriteInputTokens": written, "outputTokens": output, "reasoningOutputTokens": reasoning}


def notification(total: dict[str, int], last: dict[str, int]) -> dict[str, object]:
    return {"threadId": "t", "turnId": "u",
            "tokenUsage": {"total": total, "last": last, "modelContextWindow": 272000}}


class CodexUsageTest(unittest.TestCase):
    def test_counters_split_cached_input_out_of_input(self) -> None:
        got = codex_counters(breakdown(input=1000, cached=600, output=50, reasoning=20, written=100))
        self.assertEqual(
            {"input": 300, "output": 50, "reasoning": 20, "cache_read": 600, "cache_write": 100, "cost_micros": 0}, got)

    def test_fresh_thread_counts_everything(self) -> None:
        usage = CodexUsage()
        usage.on_turn_started()
        first = breakdown(100, 0, 10)
        usage.on_notification(notification(first, first))
        self.assertEqual(100, usage.counters()["input"])
        second = breakdown(250, 0, 30)
        usage.on_notification(notification(second, breakdown(150, 0, 20)))
        self.assertEqual({"input": 250, "output": 30, "reasoning": 0, "cache_read": 0, "cache_write": 0, "cost_micros": 0},
                         usage.counters())

    def test_resume_replay_before_turn_started_is_the_baseline(self) -> None:
        usage = CodexUsage()
        # Replay of the first 100-token turn: total == last, before this run's turn/started.
        replay = breakdown(100, 0, 0)
        usage.on_notification(notification(replay, replay))
        usage.on_turn_started()
        usage.on_notification(notification(breakdown(220, 0, 0), breakdown(120, 0, 0)))
        self.assertEqual(120, usage.counters()["input"])

    def test_resume_without_replay_takes_total_less_last_as_the_baseline(self) -> None:
        usage = CodexUsage()
        usage.on_turn_started()
        usage.on_notification(notification(breakdown(1300, 1000, 160, 50, 60), breakdown(300, 200, 60, 20, 10)))
        self.assertEqual({"input": 90, "output": 60, "reasoning": 20, "cache_read": 200, "cache_write": 10,
                          "cost_micros": 0}, usage.counters())
        usage.on_notification(notification(breakdown(1400, 1000, 200, 60, 60), breakdown(100, 0, 40, 10, 0)))
        self.assertEqual((190, 100), (usage.counters()["input"], usage.counters()["output"]))

    def test_resumed_thread_cache_and_reasoning_deltas(self) -> None:
        usage = CodexUsage()
        usage.on_notification(notification(breakdown(1000, 800, 100, 30, 50), breakdown(1000, 800, 100, 30, 50)))
        usage.on_turn_started()
        usage.on_notification(notification(breakdown(1300, 1000, 160, 50, 60), breakdown(300, 200, 60, 20, 10)))
        self.assertEqual({"input": 90, "output": 60, "reasoning": 20, "cache_read": 200, "cache_write": 10,
                          "cost_micros": 0}, usage.counters())

    def test_nothing_reported(self) -> None:
        self.assertEqual({}, CodexUsage().counters())

    def test_malformed_notifications_are_ignored(self) -> None:
        usage = CodexUsage()
        usage.on_notification(notification(breakdown(100, 0, 10), breakdown(100, 0, 10)))
        before = usage.counters()
        bad: list[object] = [None, [], {}, {"tokenUsage": "x"}, {"tokenUsage": {"last": {}}}]
        for params in bad:
            usage.on_notification(params)
        self.assertEqual(before, usage.counters())
        self.assertEqual({}, CodexUsage().counters())


class CodexBillingModeTest(unittest.TestCase):
    def test_modes(self) -> None:
        self.assertEqual(SUBSCRIPTION, codex_billing_mode("chatgpt"))
        self.assertEqual(METERED, codex_billing_mode("apikey"))
        self.assertEqual(METERED, codex_billing_mode(None))
        self.assertEqual(METERED, codex_billing_mode("other"))
