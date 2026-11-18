from __future__ import annotations

import os
import threading

from host_fixtures import CONV, FAILSAFE, HostCase, item, wait_for_file


class DurabilityTest(HostCase):
    def wait_for_received(self, count: int) -> None:
        ready = threading.Event()
        for _ in range(FAILSAFE * 200):
            if len(self.received()) >= count:
                return
            ready.wait(0.005)
        self.fail("the harness never received the message")

    def test_the_session_is_recorded_as_made_when_the_harness_says_so(self) -> None:
        """A host that dies before it can save again must still resume the session, not start a second one."""
        self.start()
        self.message(2, "hello")
        self.pump(lambda: self.conv().state.created)
        self.assertEqual(0, self.turns(), "the turn has not ended: nothing else has saved the state")
        state = self.host.store.load(CONV)
        assert state is not None
        self.assertTrue(state.created)

    def test_a_message_is_not_handled_until_its_turn_has_a_result(self) -> None:
        """If the harness or the host dies mid-turn the message must come round again."""
        self.start()
        self.message(2, "hang:true")
        wait_for_file(os.path.join(self.logs, "hanging"))
        self.assertEqual(1, self.host.current_ack(), "the message is in flight, not handled")
        state = self.host.store.load(CONV)
        assert state is not None
        self.assertEqual(0, state.last_message_seq)

    def test_a_message_in_flight_when_the_host_restarts_is_asked_again(self) -> None:
        self.start()
        self.message(2, "hang:true")
        wait_for_file(os.path.join(self.logs, "hanging"))
        self.pump(lambda: self.conv().state.created)
        self.restart_host()
        self.message(2, "hang:true")  # not acknowledged, so the server sends it again
        self.wait_for_received(2)
        self.assertEqual(["hang:true", "hang:true"], [r["text"] for r in self.received()])

    def test_a_message_whose_turn_the_harness_died_in_is_handled(self) -> None:
        """The owner is told the harness exited; asking again behind their back would be worse."""
        self.start()
        self.message(2, "crash")
        self.pump(lambda: "the harness exited" in self.reporter.said())
        self.assertEqual(2, self.host.current_ack())
        state = self.host.store.load(CONV)
        assert state is not None
        self.assertEqual(2, state.last_message_seq)

    def test_a_message_whose_turn_a_stop_cut_short_is_handled(self) -> None:
        self.start()
        self.message(2, "hang:true")
        wait_for_file(os.path.join(self.logs, "hanging"))
        self.handle(item(3, "stop"))
        self.assertEqual(3, self.host.current_ack())
        state = self.host.store.load(CONV)
        assert state is not None
        self.assertEqual(2, state.last_message_seq)

    def test_a_message_is_handled_once_its_turn_ends(self) -> None:
        self.start()
        self.converse(2, "hello")
        state = self.host.store.load(CONV)
        assert state is not None
        self.assertEqual(2, state.last_message_seq)
        self.assertEqual(2, self.host.current_ack())
