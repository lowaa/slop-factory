from __future__ import annotations

import os
from unittest import mock

from host_fixtures import ended, process_gone, process_running, read_file, wait_for_file
from pi_fixtures import PiHostCase
from slop_haus_supervisor.remote_control.host.live_harness import LiveHarness
from test_host_idle_stop import OwnSession


class PiShutdownTest(PiHostCase):
    def own_session(self) -> OwnSession:
        child = OwnSession(self.logs)
        self.addCleanup(child.close)
        return child

    def test_shutdown_ends_pi_and_the_commands_it_started(self) -> None:
        self.start()
        child = os.path.join(self.logs, "child.pid")
        self.converse(2, "run: sleep 60 >/dev/null 2>&1 & echo $! > " + child)
        pid = int(read_file(child))
        self.assertTrue(process_running(pid))
        live = self.live()
        self.host.shutdown()
        self.assertTrue(ended(live), "pi is gone")
        self.assertTrue(process_gone(pid), "a command pi started would go on running in the working copy")

    def test_shutdown_ends_a_command_that_left_pis_session(self) -> None:
        self.start()
        child = self.own_session()
        self.converse(2, "run: " + child.command())
        self.assertTrue(process_running(child.pid()))
        self.host.shutdown()
        self.assertTrue(child.gone(), "the group signal cannot reach it: the run's token has to")


class PiExitedBeforeTheWriteTest(PiHostCase):
    """pi that exits at start-up (no API key, say) races the host's first write, as Claude Code does:
    a write to it is not an error, and the exit is what the owner is told."""

    def test_a_write_to_pi_that_already_exited_is_not_an_error(self) -> None:
        with mock.patch.dict(os.environ, {"FAKE_MODE": "exit-at-start", "FAKE_ERROR": "pi: no API key for provider"}):
            self.start()
            live = self.live()
            live.session.exit_code()
            live.send("hello")  # raises if a broken pipe fails the turn

    def test_the_owner_is_told_why_pi_exited_and_the_turn_ends(self) -> None:
        real_send = LiveHarness.send

        def send_after_exit(live: LiveHarness, text: str) -> None:
            live.session.exit_code()
            real_send(live, text)

        with mock.patch.dict(os.environ, {"FAKE_MODE": "exit-at-start", "FAKE_ERROR": "pi: no API key for provider"}), \
                mock.patch.object(LiveHarness, "send", send_after_exit):
            self.start()
            self.converse(2, "hello")
        self.assertEqual([], self.reporter.of("failed"))
        self.assertIn("the harness exited (status 1)", self.reporter.said())
        self.assertIn("no API key", self.reporter.said(), "pi's own words reach the owner")
        self.assertEqual(1, self.turns())
        self.assertEqual(1, len(self.invocations()), "and pi is not started again as if a guess had been wrong")

    def test_pi_that_closed_its_stdin_but_lives_is_ended_and_reported(self) -> None:
        with mock.patch.dict(os.environ, {"FAKE_MODE": "close-stdin"}):
            self.start()
            live = self.live()
            wait_for_file(os.path.join(self.logs, "closed"))
            self.message(2, "hello")
            self.pump(lambda: self.turns() == 1)
        self.assertTrue(ended(live))
        self.assertIn("the harness exited", self.reporter.said())
