from __future__ import annotations

import os
from unittest import mock

from host_fixtures import CONV, OTHER_CONV, HostCase, flag_value, item
from slop_haus_supervisor.remote_control.host.live_harness import LiveHarness


class MessageTest(HostCase):
    def test_streams_the_replies_text_and_ends_the_turn(self) -> None:
        self.start()
        self.converse(2, "hello")
        self.assertEqual("echo: hello", self.reporter.said(), "the deltas once, not again from the whole message")
        self.assertEqual([1], [e["turn"] for e in self.reporter.of("turn_end")])

    def test_text_from_a_message_with_no_partials_is_not_lost(self) -> None:
        with mock.patch.dict(os.environ, {"FAKE_HARNESS_NO_PARTIALS": "1"}):
            self.start()
            self.converse(2, "hello")
        self.assertEqual("echo: hello", self.reporter.said())

    def test_a_message_waits_for_the_turn_in_flight(self) -> None:
        """Claude Code folds messages queued on stdin into the turn in flight, which would answer
        two questions with one reply."""
        self.start()
        self.message(2, "first")
        self.message(3, "second")
        self.assertEqual(1, len(self.conv().pending), "the second is held, not written to the harness")
        self.pump(lambda: self.turns() == 2)
        self.assertEqual(
            [("first", 0), ("second", 1)], [(r["text"], r["results_before"]) for r in self.received()],
            "the second went in only after the first's result",
        )
        self.assertEqual("echo: firstecho: second", self.reporter.said())
        self.assertEqual(3, self.host.current_ack())

    def test_a_message_held_for_the_end_of_a_turn_is_not_acknowledged_until_it_is_sent(self) -> None:
        """If the host died with it held, it would be lost: the server deletes what is acknowledged."""
        self.start()
        self.message(2, "first")
        self.message(3, "second")
        self.assertEqual(1, self.host.current_ack(), "the one in the harness has no result yet, and the held one is not handled")
        self.handle(item(3, "message", text="second", message_seq=3))  # sent again by the server
        self.assertEqual(1, len(self.conv().pending), "held once")
        self.pump(lambda: self.turns() == 2)
        self.assertEqual(3, self.host.current_ack())
        self.assertEqual(["first", "second"], [r["text"] for r in self.received()])

    def test_a_message_sent_again_after_a_restart_is_not_asked_twice(self) -> None:
        self.start()
        self.converse(2, "once")
        self.restart_host()
        self.message(2, "once")  # acknowledged by nobody: the server sends it again
        self.assertFalse(self.conv().turn_active, "no second turn")
        self.assertIsNone(self.conv().live, "nothing was started for it")
        self.assertEqual(["once"], [r["text"] for r in self.received()])
        self.assertEqual(2, self.host.current_ack())

    def test_stopping_releases_what_was_held(self) -> None:
        self.start()
        self.message(2, "first")
        self.message(3, "second")
        self.handle(item(4, "stop"))
        self.assertEqual(4, self.host.current_ack())

    def test_a_message_for_a_conversation_the_host_does_not_hold_is_dropped(self) -> None:
        self.handle(item(1, "message", OTHER_CONV, text="hi"))
        self.assertEqual([], self.invocations())
        self.assertEqual(1, self.host.current_ack())

    def test_an_item_that_is_not_for_a_conversation_is_ignored_and_acknowledged(self) -> None:
        self.handle(item(1, "message", "../x", text="hi"), item(2, "mystery"))
        self.assertEqual([], self.invocations())
        self.assertEqual(2, self.host.current_ack())

    def test_a_message_after_a_host_restart_resumes_the_session_this_machine_made(self) -> None:
        self.start()
        self.converse(2, "hello")
        self.restart_host()
        self.converse(3, "back")
        self.assertEqual(CONV, flag_value(self.invocations()[-1]["argv"], "--resume"))
        self.assertEqual("echo: helloecho: back", self.reporter.said())

    def test_a_crashed_harness_is_reported_and_the_next_message_resumes_it(self) -> None:
        self.start()
        self.converse(2, "hello")  # the session exists by now
        self.message(3, "crash now")
        self.pump(lambda: any("exited" in e.get("text", "") for e in self.reporter.of("assistant")))
        said = self.reporter.said()
        self.assertIn("exited (status 3)", said)
        self.assertNotIn("secret", said, "no credential from stderr")
        self.assertEqual(2, len(self.reporter.of("turn_end")), "the cut-off turn is ended")

        self.converse(4, "again")
        self.assertEqual(CONV, flag_value(self.invocations()[-1]["argv"], "--resume"))

    def test_a_message_held_behind_a_turn_that_crashed_is_released(self) -> None:
        self.start()
        self.converse(2, "hello")
        self.message(3, "crash now")
        self.message(4, "held behind the crash")
        self.assertEqual(2, self.host.current_ack(), "in flight and held, so not acknowledged yet")
        self.pump(lambda: any("exited" in e.get("text", "") for e in self.reporter.of("assistant")))
        self.assertEqual(4, self.host.current_ack(), "dropped with the turn, so the server need not send it again")
        self.assertEqual(["hello", "crash now"], [r["text"] for r in self.received()])

    def test_an_error_result_is_shown_to_the_owner(self) -> None:
        self.start()
        self.converse(2, "error please")
        self.assertIn("API Error: 529 overloaded", self.reporter.said())

    def test_a_session_that_already_exists_is_resumed(self) -> None:
        with mock.patch.dict(os.environ, {"FAKE_HARNESS_MODE": "session-in-use"}):
            self.start()
            self.converse(2, "hello")
        self.assertEqual(
            ["--session-id", "--resume"],
            ["--resume" if "--resume" in r["argv"] else "--session-id" for r in self.invocations()],
        )
        self.assertEqual("echo: hello", self.reporter.said())

    def test_a_session_this_machine_lacks_starts_again_and_says_so(self) -> None:
        with mock.patch.dict(os.environ, {"FAKE_HARNESS_MODE": "no-conversation"}):
            self.start(resume=True)
            self.converse(2, "hello")
        self.assertIn("continues in a new one", self.reporter.said())
        self.assertIn("echo: hello", self.reporter.said())
        self.assertNotIn("error_during_execution", self.reporter.said(), "the refusal's result is not the owner's turn")


class HarnessExitedBeforeTheWriteTest(HostCase):
    """The host's write to a harness that refuses at start-up (it exits without reading stdin) races
    that exit. These force the write to lose it, rather than hoping the scheduler does."""

    def exit_before_every_write(self) -> None:
        real_send = LiveHarness.send
        first: list[LiveHarness] = []

        def send_after_exit(live: LiveHarness, text: str) -> None:
            # Only the first harness is the one that refuses; the one the host starts in its place
            # stays up for the message.
            if not first:
                first.append(live)
                live.session.exit_code()
            real_send(live, text)

        patcher = mock.patch.object(LiveHarness, "send", send_after_exit)
        patcher.start()
        self.addCleanup(patcher.stop)

    def test_a_write_to_an_exited_harness_is_not_an_error(self) -> None:
        with mock.patch.dict(os.environ, {"FAKE_HARNESS_MODE": "session-in-use"}):
            self.start()
            live = self.live()
            live.session.exit_code()
            live.send("hello")  # raises if the broken pipe is treated as a failure

    def test_a_session_that_already_exists_is_resumed(self) -> None:
        self.exit_before_every_write()
        with mock.patch.dict(os.environ, {"FAKE_HARNESS_MODE": "session-in-use"}):
            self.start()
            self.converse(2, "hello")
        self.assertEqual([], self.reporter.of("failed"))
        self.assertEqual(
            ["--session-id", "--resume"],
            ["--resume" if "--resume" in r["argv"] else "--session-id" for r in self.invocations()],
        )
        self.assertEqual("echo: hello", self.reporter.said())

    def test_a_session_this_machine_lacks_starts_again_and_says_so(self) -> None:
        self.exit_before_every_write()
        with mock.patch.dict(os.environ, {"FAKE_HARNESS_MODE": "no-conversation"}):
            self.start(resume=True)
            self.converse(2, "hello")
        self.assertEqual([], self.reporter.of("failed"))
        self.assertIn("continues in a new one", self.reporter.said())
        self.assertIn("echo: hello", self.reporter.said())

    def test_a_harness_that_closed_its_stdin_but_lives_is_ended_and_reported(self) -> None:
        with mock.patch.dict(os.environ, {"FAKE_HARNESS_MODE": "close-stdin"}):
            self.start()
            live = self.live()
            self.pump(lambda: live.started)
            self.message(2, "hello")
            self.pump(lambda: self.turns() == 1)
        self.assertIn("the harness exited", self.reporter.said())
