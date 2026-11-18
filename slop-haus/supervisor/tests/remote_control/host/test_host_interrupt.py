from __future__ import annotations

import queue
import threading
from typing import Any

from host_fixtures import CONV, HostCase, item
from slop_haus_supervisor.harness.base import RunSpec
from slop_haus_supervisor.harness.process import HarnessSession


class FakeSession(HarnessSession):
    """A session the test drives: it decides what the harness says, and records what it was asked."""

    def __init__(self, native: bool) -> None:
        self.native = native
        self.sent: list[str] = []
        self.interrupts = 0
        self.closed = threading.Event()
        self.records: queue.Queue[dict[str, Any] | None] = queue.Queue()
        self.records.put({"type": "system", "subtype": "init", "apiKeySource": "none"})

    @property
    def session_id(self) -> str:
        return CONV

    def send(self, text: str) -> None:
        self.sent.append(text)

    def send_line(self, line: str) -> None:
        pass

    def interrupt(self) -> bool:
        self.interrupts += 1
        return self.native

    def next_record(self) -> dict[str, Any] | None:
        return self.records.get(timeout=3)

    def exit_code(self) -> int:
        return 0

    def stderr_tail(self) -> str:
        return ""

    def close(self) -> None:
        self.closed.set()
        self.records.put(None)

    def say(self, text: str) -> None:
        self.records.put({"type": "assistant", "message": {"id": "m", "content": [{"type": "text", "text": text}]}})

    def end_turn(self, is_error: bool = False) -> None:
        self.records.put({"type": "result", "subtype": "success", "is_error": is_error, "result": "x", "num_turns": 1,
                          "usage": {"input_tokens": 1, "output_tokens": 1},
                          "modelUsage": {"claude-test-1": {"inputTokens": 1, "outputTokens": 1, "cacheReadInputTokens": 0,
                                                           "cacheCreationInputTokens": 0, "thinkingTokens": 0, "costUSD": 0.001}}})


class FakeHarness:
    name = "claude"

    def __init__(self, native: bool) -> None:
        self.native = native
        self.sessions: list[FakeSession] = []

    def check_installed(self) -> None:
        pass

    def open_session(self, spec: RunSpec, session_id: str | None = None, resume: bool = False) -> HarnessSession:
        session = FakeSession(self.native)
        self.sessions.append(session)
        return session


class InterruptCase(HostCase):
    native = True

    def setUp(self) -> None:
        super().setUp()
        self.fake = FakeHarness(self.native)
        self.host = self.new_host(self.fake)

    def interrupt(self, seq: int, turn: int) -> None:
        self.handle(item(seq, "interrupt", turn=turn))

    def stopped_notes(self) -> int:
        return self.reporter.said().count("stopped by you")

    def settle(self, session: FakeSession) -> None:
        """The harness ends the turn the way it does after an interrupt."""
        session.end_turn(is_error=True)
        before = self.turns()
        self.pump(lambda: self.turns() > before)


class NativeInterruptTest(InterruptCase):
    def test_interrupting_the_named_turn_asks_the_harness_and_reports_the_end_as_stopped_by_you(self) -> None:
        self.start()
        self.message(2, "work")
        session = self.fake.sessions[0]
        self.interrupt(3, 1)
        self.assertEqual(1, session.interrupts)
        self.assertTrue(self.conv().turn_active, "the turn ends through the harness's records, not at once")
        self.settle(session)
        self.assertEqual(1, self.stopped_notes())
        self.assertNotIn("ended with an error", self.reporter.said())
        self.assertFalse(session.closed.is_set(), "the process is kept")
        self.assertEqual(3, self.host.current_ack())

    def test_an_interrupt_for_a_turn_that_is_not_current_is_ignored(self) -> None:
        self.start()
        self.message(2, "work")
        session = self.fake.sessions[0]
        self.interrupt(3, 2)
        self.interrupt(4, 0)
        self.handle(item(5, "interrupt"))
        self.handle(item(6, "interrupt", turn=True))
        self.assertEqual(0, session.interrupts)

    def test_an_interrupt_after_the_turn_ended_is_ignored_and_never_stops_the_next_turn(self) -> None:
        self.start()
        self.message(2, "one")
        session = self.fake.sessions[0]
        session.end_turn()
        self.pump(lambda: self.turns() == 1)
        self.interrupt(3, 1)
        self.assertEqual(0, session.interrupts)
        self.message(4, "two")
        self.interrupt(5, 1)  # the late one again, now that turn 2 runs
        self.assertEqual(0, session.interrupts)
        session.end_turn()
        self.pump(lambda: self.turns() == 2)
        self.assertEqual(0, self.stopped_notes())

    def test_a_repeated_interrupt_asks_once(self) -> None:
        self.start()
        self.message(2, "work")
        session = self.fake.sessions[0]
        self.interrupt(3, 1)
        self.interrupt(4, 1)
        self.assertEqual(1, session.interrupts)
        self.settle(session)
        self.assertEqual(1, self.stopped_notes())

    def test_the_flag_does_not_carry_to_the_next_turn(self) -> None:
        self.start()
        self.message(2, "one")
        session = self.fake.sessions[0]
        self.interrupt(3, 1)
        self.settle(session)
        self.message(4, "two")
        session.end_turn(is_error=True)
        self.pump(lambda: self.turns() == 2)
        self.assertEqual(1, self.stopped_notes())
        self.assertIn("ended with an error", self.reporter.said(), "a failure of the next turn is still an error")

    def test_a_message_queued_during_the_turn_is_sent_after_the_stop(self) -> None:
        self.start()
        self.message(2, "first")
        self.message(3, "second")
        session = self.fake.sessions[0]
        self.interrupt(4, 1)
        self.assertEqual(["first"], session.sent)
        self.settle(session)
        self.assertEqual(["first", "second"], session.sent)
        self.assertTrue(self.conv().turn_active)

    def test_a_stop_later_in_the_batch_wins(self) -> None:
        self.start()
        self.message(2, "work")
        session = self.fake.sessions[0]
        self.handle(item(3, "interrupt", turn=1), item(4, "stop"))
        self.assertEqual(0, session.interrupts)
        self.assertTrue(session.closed.is_set())
        self.assertEqual(4, self.host.current_ack())

    def test_a_stop_after_an_interrupt_clears_it(self) -> None:
        self.start()
        self.message(2, "work")
        self.interrupt(3, 1)
        self.handle(item(4, "stop"))
        self.assertIsNone(self.host.conv)

    def test_the_turns_usage_is_still_recorded(self) -> None:
        self.start()
        self.message(2, "work")
        session = self.fake.sessions[0]
        self.interrupt(3, 1)
        self.settle(session)
        self.assertEqual(1, len(self.reporter.usage))

    def test_a_harness_that_ignores_the_interrupt_is_ended_and_resumed_once_the_timeout_passes(self) -> None:
        self.start()
        self.message(2, "first")
        self.message(3, "second")
        self.interrupt(4, 1)
        first = self.fake.sessions[0]
        self.clock.advance(self.host.settings.interrupt_timeout - 1)
        self.host.tick()
        self.assertFalse(first.closed.is_set(), "still within the time it was given")
        self.clock.advance(2)
        self.host.tick()
        self.assertTrue(first.closed.is_set())
        self.assertEqual(1, self.stopped_notes())
        self.assertEqual(1, self.turns())
        self.assertEqual(2, len(self.fake.sessions), "the held message resumed the session in a new process")
        self.assertEqual(["second"], self.fake.sessions[1].sent)

    def test_the_harness_ending_the_turn_in_time_cancels_the_timeout(self) -> None:
        self.start()
        self.message(2, "work")
        session = self.fake.sessions[0]
        self.interrupt(3, 1)
        self.settle(session)
        self.clock.advance(self.host.settings.interrupt_timeout + 1)
        self.host.tick()
        self.assertFalse(session.closed.is_set())
        self.assertEqual(1, len(self.fake.sessions))


class NoNativeInterruptTest(InterruptCase):
    native = False

    def test_without_a_native_interrupt_the_process_is_ended_and_the_session_resumes_with_the_next_message(self) -> None:
        self.start()
        self.message(2, "work")
        first = self.fake.sessions[0]
        self.interrupt(3, 1)
        self.assertTrue(first.closed.is_set())
        self.assertFalse(self.conv().turn_active)
        self.assertEqual(1, self.stopped_notes())
        self.assertEqual([1], [e["turn"] for e in self.reporter.of("turn_end")])
        self.assertEqual(3, self.host.current_ack(), "the cut turn settled its message, and the interrupt is acknowledged")
        self.message(4, "again")
        self.assertEqual(["again"], self.fake.sessions[1].sent)
        self.assertEqual(1, self.conv().state.turns)

    def test_a_pending_message_goes_to_the_resumed_process(self) -> None:
        self.start()
        self.message(2, "first")
        self.message(3, "second")
        self.interrupt(4, 1)
        self.assertEqual(["second"], self.fake.sessions[1].sent)


class UnsupportedWorkTest(HostCase):
    def test_a_work_kind_the_host_does_not_know_is_reported_as_unsupported_and_acknowledged(self) -> None:
        self.handle(item(1, "frobnicate"))

        self.assertEqual([{"kind": "unsupported", "conversation": CONV, "work_kind": "frobnicate"}], self.reporter.of("unsupported"))
        self.assertEqual(1, self.host.current_ack(), "or it would come round for ever")

    def test_a_known_kind_is_not_reported_as_unsupported(self) -> None:
        self.start()
        self.handle(item(2, "stop"))

        self.assertEqual([], self.reporter.of("unsupported"))
