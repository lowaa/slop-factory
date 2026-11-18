from __future__ import annotations

import os
from typing import Any
from unittest import mock

from host_fixtures import CONV, HostCase
from slop_haus_supervisor.remote_control.host.conversation import ConversationState, StateStore
from slop_haus_supervisor.usage.conversation import USAGE_REVISION


class UsageTest(HostCase):
    def test_each_turn_reports_the_conversations_full_totals(self) -> None:
        self.start()
        self.converse(2, "one")
        self.converse(3, "two")

        first, second = self.reporter.usage
        self.assertEqual(CONV, second["conversation_id"], "charged by conversation")
        self.assertEqual(CONV, second["session_id"])
        self.assertEqual(CONV, second["transcript_key"])
        self.assertEqual("main", second["scope"])
        (r1,), (r2,) = first["records"], second["records"]
        self.assertEqual((100, 7, 10, 1), (r1["input_tokens"], r1["output_tokens"], r1["cache_read_tokens"], r1["requests"]))
        self.assertEqual(
            (200, 14, 20, 2), (r2["input_tokens"], r2["output_tokens"], r2["cache_read_tokens"], r2["requests"]),
            "totals so far, from a modelUsage that is cumulative for the session",
        )
        self.assertEqual("claude-test-1", r2["model"])
        self.assertEqual(("anthropic", "claude", 1), (r2["provider"], r2["harness"], r2["schema_version"]))

    def test_a_resumed_harness_adds_only_its_own_turn_to_the_totals(self) -> None:
        self.start()
        self.converse(2, "one")
        self.clock.advance(1000)
        self.host.tick()
        self.converse(3, "two")  # a new process resuming the session: its modelUsage carries on from 100
        self.assertEqual(2, len(self.invocations()), "the idle stop really ended the process")
        (record,) = self.reporter.usage[-1]["records"]
        self.assertEqual(200, record["input_tokens"])

    def test_totals_survive_a_restart_of_the_host(self) -> None:
        self.start()
        self.converse(2, "one")
        self.restart_host()
        self.converse(3, "two")
        (record,) = self.reporter.usage[-1]["records"]
        self.assertEqual(200, record["input_tokens"])
        self.assertEqual(2, self.reporter.of("turn_end")[-1]["turn"])

    def test_saved_totals_are_reported_again_when_the_host_picks_the_conversation_up(self) -> None:
        self.start()
        self.converse(2, "one")
        self.restart_host()
        before = len(self.reporter.usage)
        self.message(3, "two")
        self.assertEqual(before + 1, len(self.reporter.usage), "reported before the new turn adds to them")
        self.assertEqual(100, self.reporter.usage[before]["records"][0]["input_tokens"])

    def test_a_report_says_what_sent_it_and_in_which_revision(self) -> None:
        self.start()
        self.converse(2, "one")
        report = self.reporter.usage[-1]
        self.assertEqual(("host", USAGE_REVISION), (report["source"], report["reporter_revision"]))

    def downgrade_saved_usage(self, **changes: Any) -> None:
        """What a host from before the fix would have left on disk."""
        saved = self.host.store.load(CONV)
        assert saved is not None
        saved.usage = {k: v for k, v in saved.usage.items() if k not in ("revision", "key_suffix", "baseline_only")}
        saved.usage.update(changes)
        self.host.store.save(saved)

    def test_usage_saved_by_an_older_host_is_not_carried_on_but_reported_afresh_under_a_key_of_its_own(self) -> None:
        self.start()
        self.converse(2, "one")
        self.restart_host()  # saves the conversation as this host keeps it, so the older one's is written after
        self.downgrade_saved_usage()
        before = len(self.reporter.usage)
        self.converse(3, "two")
        self.assertEqual(before + 1, len(self.reporter.usage), "nothing was said again on pickup: no usage to say yet")
        report = self.reporter.usage[-1]
        self.assertEqual(f"{CONV}:r{USAGE_REVISION}", report["transcript_key"])
        self.assertEqual(CONV, report["session_id"])
        self.assertEqual(100, report["records"][0]["input_tokens"], "this turn's change, not the session's 200")

    def test_usage_saved_by_an_older_host_with_no_baseline_counts_nothing_for_its_first_result(self) -> None:
        self.start()
        self.converse(2, "one")
        self.restart_host()
        self.downgrade_saved_usage(reported={})
        before = len(self.reporter.usage)
        self.converse(3, "two")
        self.assertEqual(before, len(self.reporter.usage), "the session's total so far is not counted")
        self.converse(4, "three")
        self.assertEqual(100, self.reporter.usage[-1]["records"][0]["input_tokens"])

    def test_a_subscription_login_reports_no_cost(self) -> None:
        self.start()
        self.converse(2, "one")
        (record,) = self.reporter.usage[-1]["records"]
        self.assertEqual("subscription", record["billing_mode"])
        self.assertNotIn("reported_cost_microdollars", record)

    def test_an_api_key_reports_what_was_charged(self) -> None:
        with mock.patch.dict(os.environ, {"FAKE_KEY_SOURCE": "ANTHROPIC_API_KEY"}):
            self.start()
            self.converse(2, "one")
        (record,) = self.reporter.usage[-1]["records"]
        self.assertEqual("metered", record["billing_mode"])
        self.assertEqual(1000, record["reported_cost_microdollars"])

    def test_a_turn_that_spent_nothing_reports_no_usage_because_the_server_refuses_an_empty_one(self) -> None:
        self.start()
        self.converse(2, "nousage")
        self.assertEqual([], self.reporter.usage)
        self.assertEqual(1, self.turns(), "the turn still ended")

    def test_tool_calls_and_their_errors_are_counted(self) -> None:
        self.start()
        self.converse(2, "tool please")
        (record,) = self.reporter.usage[-1]["records"]
        self.assertEqual([{"tool": "Read", "calls": 1, "errors": 1, "duration_ms": 0, "result_bytes": 0}], record["tools"])


class StateStoreTest(HostCase):
    def test_a_conversation_comes_back_as_it_was_saved(self) -> None:
        store = StateStore(os.path.join(self.tmp, "states"))
        store.save(ConversationState(id=CONV, work_seq=4, created=True, turns=3, base="abc"))
        loaded = store.load(CONV)
        assert loaded is not None
        self.assertEqual((4, True, 3, "abc"), (loaded.work_seq, loaded.created, loaded.turns, loaded.base))

    def test_fields_this_host_does_not_know_are_ignored_and_the_id_is_the_requested_one(self) -> None:
        store = StateStore(os.path.join(self.tmp, "states"))
        os.makedirs(store.directory)
        with open(store.path(CONV), "w") as f:
            f.write('{"id": "someone-else", "turns": 2, "pi_model": "x"}')
        loaded = store.load(CONV)
        assert loaded is not None
        self.assertEqual((CONV, 2), (loaded.id, loaded.turns))

    def test_a_missing_or_garbled_file_is_no_conversation(self) -> None:
        store = StateStore(os.path.join(self.tmp, "states"))
        self.assertIsNone(store.load(CONV))
        os.makedirs(store.directory)
        for body in ("not json", "[1]"):
            with open(store.path(CONV), "w") as f:
                f.write(body)
            self.assertIsNone(store.load(CONV), body)
