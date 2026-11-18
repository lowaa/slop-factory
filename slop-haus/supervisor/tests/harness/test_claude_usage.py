from __future__ import annotations

import re
import tempfile
import unittest
from pathlib import Path

from slop_haus_supervisor.harness.claude import usage_of
from slop_haus_supervisor.usage.baseline import SessionBaseline
from slop_haus_supervisor.usage.conversation import ConversationUsage

ALIASES = Path(__file__).parents[4] / "go-llm-usage" / "pricing" / "aliases.go"


def priced_names() -> set[str]:
    return set(re.findall(r'^\s*"([^"]+)":\s+"[^"]+",$', ALIASES.read_text(), re.M))


# A result as Claude Code writes it for a run whose Opus session ran a Sonnet subagent.
RESULT = {
    "num_turns": 4,
    "modelUsage": {
        "claude-opus-5-5": {"inputTokens": 100, "outputTokens": 30, "cacheReadInputTokens": 500, "cacheCreationInputTokens": 20, "costUSD": 0.5},
        "claude-sonnet-5-5": {"inputTokens": 40, "outputTokens": 10, "cacheReadInputTokens": 0, "cacheCreationInputTokens": 5, "costUSD": 0.1},
    },
}


class TestHeadlessUsage(unittest.TestCase):
    def test_each_model_is_its_own_entry_named_as_claude_code_names_it(self) -> None:
        usage = usage_of(RESULT, "subscription")
        self.assertEqual(["claude-opus-5-5", "claude-sonnet-5-5"], [m.model for m in usage.models])
        self.assertNotIn("unknown", {m.model for m in usage.models})

    def test_the_subagents_model_is_priced_as_that_model(self) -> None:
        names = {m.model for m in usage_of(RESULT, "subscription").models}
        self.assertLessEqual(names, priced_names(), "go-llm-usage/pricing/aliases.go no longer prices a model the fixture reports")

    def test_requests_are_what_the_result_says_not_the_count_of_reports(self) -> None:
        usage = ConversationUsage()
        usage.add_turn(RESULT)
        self.assertEqual(4, sum(m["requests"] for m in usage.models.values()))

    def test_a_resumed_run_reports_only_its_own_usage(self) -> None:
        # A resumed run's modelUsage is the session's cumulative: run 1 plus run 2.
        run_1 = {"num_turns": 1, "modelUsage": {"m": {"inputTokens": 2, "outputTokens": 3, "cacheCreationInputTokens": 20025, "costUSD": 0.25}}}
        run_2 = {"num_turns": 1, "modelUsage": {"m": {"inputTokens": 4, "outputTokens": 6, "cacheReadInputTokens": 13430, "cacheCreationInputTokens": 28666, "costUSD": 0.75}}}
        with tempfile.TemporaryDirectory() as tmp:
            baseline = SessionBaseline(Path(tmp) / "state" / "usage" / "s.json")
            usage_of(run_1, "subscription", baseline)
            # a process of its own, as every headless run is: only the file carries the baseline over
            second = usage_of(run_2, "subscription", SessionBaseline(baseline.path)).models[0].counters
        self.assertEqual((2, 3, 13430, 8641), tuple(second[k] for k in ("input", "output", "cache_read", "cache_write")))
        self.assertEqual(500_000, second["cost_micros"])


if __name__ == "__main__":
    unittest.main()
