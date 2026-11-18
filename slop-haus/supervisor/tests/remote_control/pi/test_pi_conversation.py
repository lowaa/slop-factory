from __future__ import annotations

import os
from unittest import mock

from host_fixtures import CONV, flag_value, item
from pi_fixtures import PiHostCase


class PiConversationTest(PiHostCase):
    def prompts(self) -> list[str]:
        return [r["message"] for r in self.received() if r["type"] == "prompt"]

    def test_a_message_is_a_prompt_and_the_reply_is_streamed_back(self) -> None:
        self.start()
        self.converse(2, "hello")
        self.assertEqual(["hello"], self.prompts())
        self.assertEqual("echo: hello", self.reporter.said(), "streamed once, not again from the finished message")
        self.assertEqual([1], [e["turn"] for e in self.reporter.of("turn_end")])
        self.assertEqual(1, len(self.reporter.of("repo_state")) - 1, "and the repo state follows the turn")

    def test_a_reply_that_was_not_streamed_is_taken_from_the_finished_message(self) -> None:
        with mock.patch.dict(os.environ, {"FAKE_MODE": "no-streaming"}):
            self.start()
            self.converse(2, "hello")
        self.assertEqual("echo: hello", self.reporter.said())

    def test_a_second_message_waits_for_the_turn_in_flight(self) -> None:
        self.start()
        self.message(2, "one")
        self.message(3, "two")
        self.pump(lambda: self.turns() == 2)
        self.assertEqual(["one", "two"], self.prompts())
        self.assertEqual("echo: oneecho: two", self.reporter.said())
        self.assertEqual(["prompt", "-settled-", "get_session_stats"] * 2, [r["type"] for r in self.received()],
                         "the second prompt goes in only once the first turn has settled and its stats are in")

    def test_the_turn_ends_only_once_pi_has_settled_and_its_stats_are_in(self) -> None:
        self.start()
        self.converse(2, "hello")
        self.assertEqual(["prompt", "-settled-", "get_session_stats"], [r["type"] for r in self.received()],
                         "stats are asked for after the turn settled, not before")

    def test_a_turn_that_ended_in_an_error_is_shown_to_the_owner(self) -> None:
        self.start()
        self.converse(2, "error please")
        self.assertIn("529 overloaded", self.reporter.said())
        self.assertEqual(1, self.turns())

    def test_a_message_pi_refuses_ends_its_turn_with_the_reason(self) -> None:
        with mock.patch.dict(os.environ, {"FAKE_MODE": "reject", "FAKE_ERROR": "No model selected"}):
            self.start()
            self.converse(2, "hello")
        self.assertIn("No model selected", self.reporter.said())
        self.assertEqual([], self.reporter.usage, "nothing was spent")
        self.assertEqual([], [r for r in self.received() if r["type"] == "get_session_stats"])

    def test_a_message_an_extension_handled_ends_its_turn_without_waiting_for_a_run(self) -> None:
        self.start()
        self.converse(2, "/handled")
        self.assertEqual(1, self.turns())
        self.assertEqual("", self.reporter.said())

    def test_a_dialog_nobody_can_answer_is_cancelled_so_the_turn_is_not_held(self) -> None:
        with mock.patch.dict(os.environ, {"FAKE_MODE": "ui"}):
            self.start()
            self.converse(2, "hello")
        (answer,) = [r for r in self.received() if r["type"] == "extension_ui_response"]
        self.assertEqual({"type": "extension_ui_response", "id": "ui-1", "cancelled": True}, answer)

    def test_the_session_survives_the_idle_stop_and_the_next_message_starts_pi_again(self) -> None:
        self.start()
        self.converse(2, "hello")
        self.clock.advance(301)
        self.host.tick()
        self.assertIsNone(self.conv().live)
        self.converse(3, "back")
        runs = self.invocations(2)
        self.assertEqual([CONV, CONV], [flag_value(r["argv"], "--session-id") for r in runs])

    def test_a_stop_ends_pi_but_keeps_the_session_for_the_next_message(self) -> None:
        self.start()
        self.converse(2, "hello")
        self.handle(item(3, "stop"))
        self.assertIsNone(self.host.conv)
        self.assertEqual([], self.reporter.of("failed"))

        self.handle(item(4, "start", resume=True))
        self.converse(5, "again")
        self.assertEqual("echo: helloecho: again", self.reporter.said())
