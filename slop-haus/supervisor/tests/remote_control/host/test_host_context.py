from __future__ import annotations

import os
from typing import Any
from unittest import mock

from host_fixtures import CONV, HostCase, flag_value, item, wait_for_file


class ContextMeterTest(HostCase):
    def test_the_context_is_the_input_side_of_the_newest_request(self) -> None:
        """input + cache read + cache write of the turn's last assistant record, not the result's totals."""
        self.start()
        self.converse(2, "one")
        self.converse(3, "two")
        self.assertEqual([50, 51], [e["tokens"] for e in self.reporter.of("context")])
        self.assertEqual(51, self.conv().state.context_tokens)

    def test_an_unchanged_context_is_not_reported_again(self) -> None:
        self.start()
        self.converse(2, "nousage")
        self.converse(3, "nousage")
        self.assertEqual(2, len(self.reporter.of("context")), "the fake's cache read moves with each turn")
        self.conv().reported_context = self.conv().state.context_tokens
        before = len(self.reporter.of("context"))
        self.host.report_context(self.conv())
        self.assertEqual(before, len(self.reporter.of("context")))


class CompactTest(HostCase):
    def test_a_compact_sends_claude_codes_command_and_reports_the_divider(self) -> None:
        self.start()
        self.converse(2, "one")
        self.handle(item(3, "compact"))
        self.pump(lambda: self.turns() == 2)
        self.assertEqual(["one", "/compact"], [r["text"] for r in self.received()])
        self.assertEqual(1, len(self.reporter.of("context_compacted")))
        self.assertEqual(3, self.host.current_ack())
        self.assertFalse(self.conv().turn_active)

    def test_a_compact_the_harness_refuses_reports_why_and_no_divider(self) -> None:
        with mock.patch.dict(os.environ, {"FAKE_COMPACT_FAIL": "1"}):
            self.start()
            self.handle(item(2, "compact"))
            self.pump(lambda: self.turns() == 1)
        self.assertEqual([], self.reporter.of("context_compacted"))
        self.assertIn("Not enough messages to compact.", self.reporter.said())
        self.assertEqual(2, self.host.current_ack())

    def test_a_compact_is_unacknowledged_until_it_ends(self) -> None:
        self.start()
        self.converse(2, "one")
        self.handle(item(3, "compact"))
        self.assertEqual(2, self.host.current_ack(), "the harness has not answered yet")
        self.pump(lambda: self.turns() == 2)
        self.assertEqual(3, self.host.current_ack())

    def test_a_compact_sent_again_is_done_once(self) -> None:
        self.start()
        self.converse(2, "one")
        self.handle(item(3, "compact"))
        self.pump(lambda: self.turns() == 2)
        self.handle(item(3, "compact"))
        self.assertEqual(["one", "/compact"], [r["text"] for r in self.received()])

    def test_a_compact_of_a_conversation_the_host_does_not_hold_is_dropped(self) -> None:
        self.handle(item(2, "compact"))
        self.assertEqual([], self.reporter.of("context_compacted"))

    def test_the_host_offers_compact_when_its_harness_has_one(self) -> None:
        self.assertEqual(["interrupt", "compact"], self.host.capabilities(["interrupt"]))
        self.host.records.can_compact = False
        self.assertEqual(["interrupt"], self.host.capabilities(["interrupt"]))

    def test_a_compact_is_not_done_by_a_host_whose_harness_cannot(self) -> None:
        self.start()
        self.converse(2, "one")
        self.host.records.can_compact = False
        self.handle(item(3, "compact"))
        self.assertEqual(["one"], [r["text"] for r in self.received()])
        self.assertEqual([], self.reporter.of("context_compacted"))


class ClearTest(HostCase):
    def session_of(self, invocation: dict[str, Any]) -> tuple[str, str]:
        argv: list[str] = invocation["argv"]
        for flag in ("--session-id", "--resume"):
            if flag in argv:
                return flag, flag_value(argv, flag)
        raise AssertionError(f"no session flag in {argv!r}")

    def test_a_clear_starts_the_harness_on_a_new_session_and_never_resumes_the_old(self) -> None:
        self.start()
        self.converse(2, "one")
        self.handle(item(3, "clear"))
        self.assertIsNone(self.conv().live, "the old harness is stopped")
        self.assertEqual(1, len(self.reporter.of("context_cleared")))
        self.assertEqual(3, self.host.current_ack())
        self.converse(4, "two")
        first, second = self.invocations(2)
        self.assertEqual(("--session-id", CONV), self.session_of(first))
        flag, session = self.session_of(second)
        self.assertEqual("--session-id", flag, "a new session, not a resume")
        self.assertNotEqual(CONV, session)
        self.assertEqual(session, self.conv().state.session)

    def test_a_clear_resets_the_meter(self) -> None:
        self.start()
        self.converse(2, "one")
        self.handle(item(3, "clear"))
        self.assertEqual(0, self.conv().state.context_tokens)

    def test_later_restarts_resume_the_new_session_not_the_old(self) -> None:
        self.start()
        self.converse(2, "one")
        self.handle(item(3, "clear"))
        self.converse(4, "two")
        session = self.conv().state.session
        self.restart_host()
        self.converse(5, "three")
        flag, resumed = self.session_of(self.invocations(3)[-1])
        self.assertEqual(("--resume", session), (flag, resumed))

    def test_a_clear_of_a_conversation_the_host_does_not_hold_is_dropped(self) -> None:
        self.handle(item(2, "clear"))
        self.assertEqual([], self.reporter.of("context_cleared"))

    def test_a_clear_after_an_interrupt_that_has_not_landed_ends_the_turn_and_keeps_the_next_message(self) -> None:
        self.start()
        self.message(2, "hang:true")
        wait_for_file(self.logs + "/hanging")
        self.message(3, "after")
        self.handle(item(4, "interrupt", turn=1), item(5, "clear"))
        self.assertEqual([1], [e["turn"] for e in self.reporter.of("turn_end")], "the cut turn is reported, not lost")
        self.assertIn("stopped by you", self.reporter.said())
        self.assertEqual(1, len(self.reporter.of("context_cleared")))
        self.pump(lambda: self.turns() == 2)
        self.assertEqual("echo: after", self.reporter.said().split("stopped by you.")[-1])
        flag, session = self.session_of(self.invocations(2)[-1])
        self.assertEqual(("--session-id", self.conv().state.session), (flag, session))
        self.assertNotEqual(CONV, session)

    def test_a_clear_sent_again_is_done_once(self) -> None:
        self.start()
        self.converse(2, "one")
        self.handle(item(3, "clear"))
        session = self.conv().state.session
        self.handle(item(3, "clear"))
        self.assertEqual(session, self.conv().state.session)
        self.assertEqual(1, len(self.reporter.of("context_cleared")))
