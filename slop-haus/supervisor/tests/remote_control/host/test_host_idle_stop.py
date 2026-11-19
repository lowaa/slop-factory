from __future__ import annotations

import os
import select
import shlex
import subprocess
import sys
import threading
from pathlib import Path

from host_fixtures import (
    CONV,
    FAILSAFE,
    HostCase,
    ended,
    flag_value,
    item,
    make_harness,
    process_gone,
    read_file,
    start_item,
    wait_for_file,
)
from process_state import process_running
from slop_haus_supervisor.remote_control.host.conversation import Conversation, ConversationState
from slop_haus_supervisor.remote_control.host.host import Host
from slop_haus_supervisor.remote_control.host.live_harness import HarnessEvent
from slop_haus_supervisor.remote_control.host.settings import Settings
from slop_haus_supervisor.workspace.host_files import IdleMarker, read_idle


class OwnSession:
    """A `sleep` in a session of its own, as the harness's Bash tool starts the model's commands, left
    behind by the shell command that started it (command()): no parent of the harness's.

    Its end is observed rather than polled for: it holds the write end of a FIFO this test holds the
    read end of, which reads as hung up once the process is gone for good (not merely signalled, nor
    still exiting)."""

    def __init__(self, directory: str, ignore_term: bool = False) -> None:
        self.pid_file = os.path.join(directory, "child.pid")
        self.fifo = os.path.join(directory, "child.alive")
        self.ignore_term = ignore_term
        os.mkfifo(self.fifo)
        # Non-blocking, so opening it does not wait for the writer.
        self.reader = os.open(self.fifo, os.O_RDONLY | os.O_NONBLOCK)

    def close(self) -> None:
        os.close(self.reader)

    def command(self) -> str:
        spawner = (
            "import subprocess, sys; "
            "p = subprocess.Popen([sys.executable, '-c', sys.argv[3], sys.argv[2]], start_new_session=True, "
            "stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL); "
            "p.stdout.readline(); open(sys.argv[1], 'w').write(str(p.pid))"
        )
        child = (
            "import os, signal, sys; fd = os.open(sys.argv[1], os.O_WRONLY); %s"
            "print('up', flush=True); signal.pause()"
        ) % ("signal.signal(signal.SIGTERM, signal.SIG_IGN); " if self.ignore_term else "")
        return " ".join(shlex.quote(a) for a in (sys.executable, "-c", spawner, self.pid_file, self.fifo, child))

    def pid(self) -> int:
        return int(read_file(self.pid_file))

    def gone(self) -> bool:
        ready, _, _ = select.select([self.reader], [], [], FAILSAFE)
        return bool(ready) and os.read(self.reader, 1) == b""


class IdleTest(HostCase):
    def test_an_idle_harness_is_stopped_and_the_next_message_resumes_it(self) -> None:
        self.start()
        self.converse(2, "hello")
        live = self.live()

        self.clock.advance(299)
        self.host.tick()
        self.assertIsNotNone(self.conv().live, "not yet")
        self.clock.advance(2)
        self.host.tick()
        self.assertIsNone(self.conv().live)
        self.assertTrue(ended(live), "the process is gone")

        self.converse(3, "back again")
        argv = self.invocations()[-1]["argv"]
        self.assertEqual(CONV, flag_value(argv, "--resume"))
        self.assertEqual(2, len(self.invocations()))
        self.assertIn("echo: back again", self.reporter.said())
        self.assertEqual([1, 2], [e["turn"] for e in self.reporter.of("turn_end")], "turns keep counting across the resume")

    def test_the_host_is_idle_for_a_supervisor_only_while_no_harness_is_alive(self) -> None:
        self.assertTrue(self.host.is_idle())
        self.start()
        self.message(2, "working")
        self.assertFalse(self.host.is_idle())
        self.clock.advance(10_000)
        self.host.tick()
        self.assertFalse(self.host.is_idle(), "a turn is still going")

    def test_a_harness_in_a_turn_is_not_idle(self) -> None:
        self.start()
        self.message(2, "working")
        self.clock.advance(10_000)
        self.host.tick()
        self.assertIsNotNone(self.conv().live)
        self.assertIsNone(self.host.idle_deadline())

    def test_the_idle_clock_starts_when_the_turn_ends(self) -> None:
        self.start()
        self.clock.advance(250)
        self.converse(2, "hello")
        self.clock.advance(250)
        self.host.tick()
        self.assertIsNotNone(self.conv().live, "250s after the turn, not 500s after the start")

    def test_idle_stop_does_not_tell_the_server_the_conversation_ended(self) -> None:
        self.start()
        self.clock.advance(1000)
        self.host.tick()
        self.assertEqual([], self.reporter.of("failed"))
        self.assertEqual(["ready", "repo_state"], [e["kind"] for e in self.reporter.events])


class StopTest(HostCase):
    def own_session(self, ignore_term: bool = False) -> OwnSession:
        child = OwnSession(self.logs, ignore_term)
        self.addCleanup(child.close)
        return child

    def hang(self, command: str) -> None:
        self.message(2, "hang: " + command)
        wait_for_file(os.path.join(self.logs, "hanging"))

    def test_stop_ends_the_harness(self) -> None:
        self.start()
        live = self.live()
        self.handle(item(2, "stop"))
        self.assertTrue(ended(live))
        self.assertIsNone(self.host.conv)
        self.assertEqual(2, self.host.current_ack())

    def test_stop_cuts_a_turn_in_progress_and_reports_nothing_of_its_own(self) -> None:
        self.start()
        self.hang("true")
        live = self.live()
        kinds_before = [e["kind"] for e in self.reporter.events]

        self.handle(item(3, "stop"))

        self.assertTrue(ended(live), "the process is gone, which is what stops the turn")
        self.assertEqual(
            kinds_before + ["repo_state"], [e["kind"] for e in self.reporter.events],
            "no failed, no ended: the conversation is only disconnected",
        )

    def test_stop_ends_what_the_models_commands_started(self) -> None:
        self.start()
        child = os.path.join(self.logs, "child.pid")
        self.hang("sleep 60 >/dev/null 2>&1 & echo $! > " + child)
        pid = int(read_file(child))
        self.assertTrue(process_running(pid))

        self.handle(item(3, "stop"))

        self.assertTrue(process_gone(pid), "a command left running would go on writing under the next session")

    def test_stop_ends_a_command_that_left_the_harnesss_session(self) -> None:
        self.start()
        child = self.own_session()
        self.converse(2, "run: " + child.command())
        pid = child.pid()
        self.assertTrue(process_running(pid))

        self.handle(item(3, "stop"))

        self.assertTrue(child.gone(), "the group signal cannot reach it: the run's token has to")

    def test_a_harness_that_dies_on_its_own_does_not_leave_the_models_commands_running(self) -> None:
        self.start()
        child = self.own_session()
        self.converse(2, "run: " + child.command())
        self.assertTrue(process_running(child.pid()))

        self.message(3, "crash now")
        self.pump(lambda: any("exited" in e.get("text", "") for e in self.reporter.of("assistant")))

        self.assertTrue(child.gone())

    def test_stop_ends_a_command_that_ignores_the_term(self) -> None:
        self.start()
        child = self.own_session(ignore_term=True)
        self.converse(2, "run: " + child.command())
        self.assertTrue(process_running(child.pid()))

        self.handle(item(3, "stop"))

        self.assertTrue(child.gone(), "only a KILL ends it")

    def test_stop_leaves_other_processes_of_the_user_alone(self) -> None:
        self.start()
        bystander = subprocess.Popen(["sleep", "60"], start_new_session=True)
        self.addCleanup(bystander.wait)
        self.addCleanup(bystander.kill)
        self.handle(item(2, "stop"))
        self.assertTrue(process_running(bystander.pid))

    def test_the_session_survives_a_stop_so_the_next_start_resumes_it(self) -> None:
        self.start()
        self.converse(2, "hello")
        self.handle(item(3, "stop"))
        self.assertTrue(os.path.exists(self.host.store.path(CONV)))

        # The server queues a resume start, then the message, for the next send.
        self.handle(start_item(4, resume=True), item(5, "message", text="back again", message_seq=5))
        self.pump(lambda: self.turns() == 2)

        self.assertEqual(CONV, flag_value(self.invocations()[-1]["argv"], "--resume"))
        self.assertEqual("echo: helloecho: back again", self.reporter.said())
        self.assertEqual([1, 2], [e["turn"] for e in self.reporter.of("turn_end")], "turns keep counting")

    def test_a_session_started_and_stopped_mid_first_turn_is_still_resumed(self) -> None:
        self.start()
        self.hang("true")
        self.pump(lambda: self.conv().state.created)
        self.handle(item(3, "stop"))
        state = self.host.store.load(CONV)
        assert state is not None
        self.assertTrue(state.created, "the session exists on this machine, whatever turn it was on")

    def test_a_start_stopped_later_in_the_same_batch_is_never_started(self) -> None:
        self.handle(start_item(1), item(2, "message", text="hi"), item(3, "stop"))
        self.assertEqual([], self.invocations())
        self.assertEqual([], self.reporter.events)
        self.assertEqual(3, self.host.current_ack())

    def test_a_stop_then_a_start_in_one_batch_starts(self) -> None:
        self.start()
        self.handle(item(2, "stop"), start_item(3, resume=True))
        self.assertEqual(3, self.reporter.of("ready")[-1]["work_seq"])
        self.assertIsNotNone(self.conv().live)
        self.assertEqual(3, self.host.current_ack())

    def test_a_late_record_from_a_stopped_harness_reports_nothing(self) -> None:
        self.start()
        live = self.live()
        generation = self.conv().generation
        self.handle(item(2, "stop"))
        before = list(self.reporter.events)
        conv = Conversation(ConversationState(id=CONV), self.host.store)
        conv.generation = generation
        self.host.conv = conv
        self.host.on_harness(HarnessEvent(CONV, generation, {"type": "result", "modelUsage": {}}))
        self.assertEqual(before, self.reporter.events)
        self.assertTrue(ended(live))

    def test_the_exit_of_a_harness_the_host_ended_is_not_taken_for_the_new_ones(self) -> None:
        self.start()
        old = self.live()
        old_generation = self.conv().generation
        self.handle(item(2, "stop"))
        self.handle(start_item(3, resume=True))
        before = list(self.reporter.events)
        seen: list[HarnessEvent] = []

        def old_exit_handled() -> bool:
            return any(e.record is None and e.generation == old_generation for e in seen)

        while not old_exit_handled():
            event = self.host.inbox.get(timeout=FAILSAFE)
            assert isinstance(event, HarnessEvent)
            seen.append(event)
            self.host.dispatch(event)

        self.assertTrue(ended(old))
        self.assertEqual(before, self.reporter.events, "no 'the harness exited' for the old one")
        self.assertIsNotNone(self.conv().live, "and the new harness is still the conversation's")


class _SignallingMarker(IdleMarker):
    def __init__(self, path: Path) -> None:
        super().__init__(path)
        self.published = threading.Event()

    def publish(self, idle: bool) -> None:
        super().publish(idle)
        self.published.set()


class IdleMarkerRunTest(HostCase):
    def test_the_run_loop_publishes_idle_and_clears_the_marker_when_it_ends(self) -> None:
        marker = _SignallingMarker(Path(self.tmp) / "idle")
        stopping = threading.Event()
        host = Host(self.root, self.reporter, make_harness(self.root, self.script), Settings(harness_grace=5), stopping=stopping, idle_marker=marker)
        self.addCleanup(host.shutdown)
        thread = threading.Thread(target=host.run)
        thread.start()
        try:
            self.assertTrue(marker.published.wait(10))
            self.assertTrue(read_idle(marker.path))
        finally:
            stopping.set()
            thread.join(10)

        self.assertFalse(marker.path.exists())
