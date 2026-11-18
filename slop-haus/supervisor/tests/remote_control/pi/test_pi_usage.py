from __future__ import annotations

import json
import os
from unittest import mock

from host_fixtures import CONV
from pi_fixtures import PiHostCase


class PiUsageTest(PiHostCase):
    def test_usage_is_the_turns_change_in_pis_session_stats_and_not_the_totals(self) -> None:
        """pi's stats are cumulative for the session: two turns of 100 input tokens report 100 and
        then 200, and the conversation has used 200."""
        self.start()
        self.converse(2, "one")
        self.assertEqual(100, self.usage_record()["input_tokens"])
        self.converse(3, "two")
        record = self.usage_record()
        self.assertEqual(200, record["input_tokens"], "not 300, which is what adding the totals up would say")
        self.assertEqual(14, record["output_tokens"])
        self.assertEqual(20, record["cache_read_tokens"])
        self.assertEqual(2, record["requests"])

    def test_usage_names_pi_its_provider_and_model_and_goes_with_the_conversation(self) -> None:
        with mock.patch.dict(os.environ, {"FAKE_PROVIDER": "openai-codex"}):
            self.start()
            self.converse(2, "one")
        body = self.reporter.usage[-1]
        self.assertEqual("pi", body["harness"])
        self.assertEqual(CONV, body["conversation_id"])
        self.assertEqual(CONV, body["session_id"])
        record = self.usage_record()
        self.assertEqual("pi", record["harness"])
        self.assertEqual("openai-codex", record["provider"])
        self.assertEqual("claude-test-1", record["model"])

    def test_the_delta_is_taken_from_what_the_last_turn_ended_on_even_across_processes(self) -> None:
        """A resumed pi reports the whole session's totals, the first process's included; only
        this turn's share may be added."""
        self.start()
        self.converse(2, "one")
        self.clock.advance(301)
        self.host.tick()
        self.converse(3, "two")
        self.assertEqual(2, len(self.invocations()), "the idle stop really ended the process")
        self.assertEqual(200, self.usage_record()["input_tokens"], "the second process's first turn is 100, not 200")

    def test_the_delta_survives_a_restart_of_the_host(self) -> None:
        self.start()
        self.converse(2, "one")
        self.restart_host()
        self.converse(3, "two")
        self.assertEqual(200, self.usage_record()["input_tokens"])

    def test_an_interrupted_turns_spend_lands_on_the_next_turn(self) -> None:
        self.start()
        self.converse(2, "one")
        stats = os.path.join(self.logs, "stats.json")
        with open(stats) as f:
            state = json.load(f)
        state["input"] += 500  # spent by a turn that was disconnected before its stats were read
        with open(stats, "w") as f:
            json.dump(state, f)
        self.converse(3, "two")
        self.assertEqual(700, self.usage_record()["input_tokens"])

    def test_a_turn_whose_stats_pi_refused_ends_and_the_next_turn_counts_its_spend(self) -> None:
        self.start()
        open(os.path.join(self.logs, "stats-fail-once"), "w").close()
        self.converse(2, "one")
        self.assertEqual([], self.reporter.usage, "nothing to report without the stats")
        self.assertEqual("echo: one", self.reporter.said(), "the turn ended, and the reply was not held back")
        self.converse(3, "two")
        self.assertEqual(200, self.usage_record()["input_tokens"], "both turns, from the baseline the first never moved")

    def test_an_api_key_reports_what_it_cost(self) -> None:
        self.start()
        self.converse(2, "one")
        record = self.usage_record()
        self.assertEqual("metered", record["billing_mode"])
        self.assertEqual(1000, record["reported_cost_microdollars"])

    def test_a_subscription_login_reports_no_cost(self) -> None:
        self.auth({"anthropic": {"type": "oauth"}, "openrouter": {"type": "api_key", "key": "sk-x"}})
        self.start()
        self.converse(2, "one")
        record = self.usage_record()
        self.assertEqual("subscription", record["billing_mode"])
        self.assertNotIn("reported_cost_microdollars", record)
        self.assertNotIn("sk-x", json.dumps(self.reporter.usage), "the host reads the kind of login, never the key")

    def test_a_provider_with_a_key_and_not_a_login_is_metered(self) -> None:
        self.auth({"anthropic": {"type": "api_key", "key": "sk-x"}, "openai": {"type": "oauth"}})
        self.start()
        self.converse(2, "one")
        self.assertEqual("metered", self.usage_record()["billing_mode"])

    def test_a_login_that_is_not_json_is_metered(self) -> None:
        self.agent_dir.mkdir(parents=True)
        (self.agent_dir / "auth.json").write_text("not json")
        self.start()
        self.converse(2, "one")
        self.assertEqual("metered", self.usage_record()["billing_mode"])

    def test_tool_calls_and_their_errors_are_counted_under_the_model_that_made_them(self) -> None:
        self.start()
        self.converse(2, "tool please")
        self.assertEqual([{"tool": "read", "calls": 1, "errors": 1, "duration_ms": 0, "result_bytes": 0}], self.usage_record()["tools"])
