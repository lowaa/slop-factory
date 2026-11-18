from __future__ import annotations

import unittest

from slop_haus_supervisor.usage.conversation import USAGE_REVISION, ConversationUsage


def result(**model_usage: dict[str, object]) -> dict[str, object]:
    return {"num_turns": 1, "modelUsage": model_usage}


class UsageTest(unittest.TestCase):
    def test_a_turn_is_the_change_in_the_processs_cumulative_model_usage(self) -> None:
        usage = ConversationUsage()
        usage.add_turn(result(m={"inputTokens": 100, "outputTokens": 7, "cacheReadInputTokens": 10, "costUSD": 0.001}))
        usage.add_turn(result(m={"inputTokens": 300, "outputTokens": 14, "cacheReadInputTokens": 20, "costUSD": 0.002}))
        self.assertEqual(
            {"requests": 2, "input": 300, "output": 14, "cache_read": 20, "cache_write": 0, "reasoning": 0, "cost_micros": 2000},
            usage.models["m"],
        )

    def test_a_resumed_process_starts_from_the_sessions_total_and_adds_only_its_own(self) -> None:
        # Recorded from Claude Code 2.1.303: the second run's modelUsage is run 1 plus run 2.
        usage = ConversationUsage()
        usage.add_turn(result(m={"inputTokens": 2, "outputTokens": 3, "cacheCreationInputTokens": 20025}))
        again = ConversationUsage(usage.dump())  # the host restarted; the resumed process reports the session's total
        again.add_turn(result(m={"inputTokens": 4, "outputTokens": 6, "cacheReadInputTokens": 13430, "cacheCreationInputTokens": 28666}))
        self.assertEqual(4, again.models["m"]["input"])
        self.assertEqual(13430, again.models["m"]["cache_read"])
        self.assertEqual(28666, again.models["m"]["cache_write"])

    def test_a_cumulative_below_the_baseline_counts_whole(self) -> None:
        usage = ConversationUsage()
        usage.add_turn(result(m={"inputTokens": 500, "costUSD": 0.5}))
        usage.add_turn(result(m={"inputTokens": 100, "costUSD": 0.1}))
        self.assertEqual(600, usage.models["m"]["input"])
        self.assertEqual(600_000, usage.models["m"]["cost_micros"])

    def test_totals_survive_a_dump_and_load(self) -> None:
        usage = ConversationUsage()
        usage.add_turn(result(m={"inputTokens": 100}))
        usage.add_call("m", "Read")
        again = ConversationUsage(usage.dump())
        again.add_turn(result(m={"inputTokens": 200}))
        (record,) = again.records("subscription")
        self.assertEqual(200, record["input_tokens"])
        self.assertEqual(usage.started_at, again.started_at)
        self.assertEqual(1, record["tools"][0]["calls"])

    def test_usage_saved_by_this_revision_carries_on(self) -> None:
        usage = ConversationUsage()
        usage.add_turn(result(m={"inputTokens": 100}))
        again = ConversationUsage.resume(usage.dump())
        again.add_turn(result(m={"inputTokens": 300}))
        self.assertEqual((300, ""), (again.models["m"]["input"], again.key_suffix))

    def test_usage_saved_by_an_older_host_starts_afresh_from_its_baseline(self) -> None:
        saved = {"models": {"m": {"input": 9_000_000, "requests": 1}}, "reported": {"m": {"inputTokens": 100, "costMicros": 0}}}
        usage = ConversationUsage.resume(saved)
        self.assertEqual(({}, f"r{USAGE_REVISION}"), (usage.models, usage.key_suffix))
        usage.add_turn(result(m={"inputTokens": 130}))
        self.assertEqual(30, usage.models["m"]["input"], "only the change since the older host's last result")

    def test_usage_saved_by_an_older_host_with_no_baseline_counts_from_the_next_result_on(self) -> None:
        usage = ConversationUsage.resume({"models": {"m": {"input": 9_000_000}}})
        usage.add_turn(result(m={"inputTokens": 5_000_000}))
        self.assertEqual({}, usage.models, "the session's total so far is not this host's to count")
        usage.add_turn(result(m={"inputTokens": 5_000_040}))
        self.assertEqual(40, usage.models["m"]["input"])
        self.assertFalse(ConversationUsage(usage.dump()).baseline_only)

    def test_the_baseline_only_state_survives_a_restart_before_the_first_result(self) -> None:
        saved = ConversationUsage.resume({"models": {"m": {"input": 9}}}).dump()
        again = ConversationUsage.resume(saved)
        again.add_turn(result(m={"inputTokens": 77}))
        self.assertEqual({}, again.models)

    def test_a_subscription_login_reports_no_cost(self) -> None:
        usage = ConversationUsage()
        usage.add_turn(result(m={"inputTokens": 1, "costUSD": 0.5}))
        (record,) = usage.records("subscription")
        self.assertEqual(("subscription", "anthropic", "claude", 1), (record["billing_mode"], record["provider"], record["harness"], record["schema_version"]))
        self.assertNotIn("reported_cost_microdollars", record)

    def test_an_api_key_reports_what_was_charged(self) -> None:
        usage = ConversationUsage()
        usage.add_turn(result(m={"inputTokens": 1, "costUSD": 0.001}))
        (record,) = usage.records("metered")
        self.assertEqual(1000, record["reported_cost_microdollars"])

    def test_tool_calls_and_their_errors_are_counted(self) -> None:
        usage = ConversationUsage()
        usage.add_turn(result(m={"inputTokens": 1}))
        usage.add_call("m", "Read")
        usage.add_error("m", "Read")
        usage.add_call("m", "bad tool!")
        (record,) = usage.records("subscription")
        self.assertEqual([{"tool": "Read", "calls": 1, "errors": 1, "duration_ms": 0, "result_bytes": 0}], record["tools"])

    def test_requests_go_to_the_model_that_wrote_the_most(self) -> None:
        usage = ConversationUsage()
        usage.add_turn(result(small={"outputTokens": 1}, big={"outputTokens": 9}))
        self.assertEqual((0, 1), (usage.models["small"]["requests"], usage.models["big"]["requests"]))

    def test_reasoning_never_exceeds_output(self) -> None:
        usage = ConversationUsage()
        usage.add_turn(result(m={"outputTokens": 3, "thinkingTokens": 10}))
        (record,) = usage.records("subscription")
        self.assertEqual(3, record["reasoning_tokens"])

    def test_pi_deltas_carry_their_provider_and_billing_mode(self) -> None:
        usage = ConversationUsage()
        usage.add_delta("openai", "gpt-5", "metered", {"input": 4, "cost_micros": 7, "requests": 1})
        usage.add_delta("Bad Provider", "gpt-5", "metered", {"input": 100})
        (record,) = usage.records("subscription", harness="pi")
        self.assertEqual(("openai", "metered", "pi", 4, 7), (record["provider"], record["billing_mode"], record["harness"], record["input_tokens"], record["reported_cost_microdollars"]))

    def test_usage_of_an_interrupted_turn_adds_nothing(self) -> None:
        usage = ConversationUsage()
        usage.add_turn(result(m={"inputTokens": 10, "outputTokens": 2}))
        usage.add_turn({"num_turns": 0, "modelUsage": {}})
        usage.add_turn(result(m={"inputTokens": 10, "outputTokens": 2}))
        self.assertEqual(10, usage.models["m"]["input"])

    def test_a_malformed_result_cannot_crash_or_make_a_negative_count(self) -> None:
        usage = ConversationUsage()
        usage.reported = {"m": {"inputTokens": 50}}
        usage.add_turn(
            {"modelUsage": {"m": {"inputTokens": 10, "outputTokens": "x", "costUSD": None}, "bad model name!": {"inputTokens": 5}, "n": 3}},
        )
        self.assertEqual(10, usage.models["m"]["input"], "below the baseline counts whole, never negative")
        self.assertEqual(0, usage.models["m"]["output"])
        self.assertNotIn("bad model name!", usage.models)
        usage.add_turn({"modelUsage": "nope"})

    def test_non_finite_and_non_numeric_counts_and_costs_cannot_crash(self) -> None:
        usage = ConversationUsage()
        usage.add_turn(
            result(m={"inputTokens": float("inf"), "outputTokens": float("nan"), "costUSD": float("inf")}, n={"inputTokens": 1, "costUSD": "abc"}),
        )
        self.assertEqual((0, 0, 0), (usage.models["m"]["input"], usage.models["m"]["output"], usage.models["m"]["cost_micros"]))
        self.assertEqual((1, 0), (usage.models["n"]["input"], usage.models["n"]["cost_micros"]))
