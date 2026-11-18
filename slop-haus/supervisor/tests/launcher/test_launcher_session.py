from __future__ import annotations

import contextlib
import os
import signal
import tempfile
import time
import unittest
from pathlib import Path

from fakes import FakeClock, FakeRunner
from slop_haus_supervisor.launcher.multiplexer import LauncherError, Multiplexer, Tmux
from slop_haus_supervisor.launcher.supervisor_session import Clock, SupervisorSession, busy_hosts_of, host_session_permits_start
from slop_haus_supervisor.service.instance_lock import InstanceLock
from slop_haus_supervisor.service.service_units import ServiceError, ServiceManager, UnitState
from slop_haus_supervisor.workspace.host_files import IDLE_MARKER_NAME, supervisor_lock
from slop_haus_supervisor.workspace.layout import Layout


class FakeMux(Multiplexer):
    binary = "screen"

    def __init__(self) -> None:
        self.sessions: set[str] = set()
        self.started: list[tuple[str, list[str]]] = []
        self.on_start: list[object] = []

    def exists(self, session: str) -> bool:
        return session in self.sessions

    def start_argv(self, session: str, command: list[str], cwd: Path) -> list[str]:
        raise AssertionError("start is overridden")

    def attach_argv(self, session: str) -> list[str]:
        return ["screen", "-r", session]

    def start(self, session: str, command: list[str], cwd: Path) -> None:
        self.started.append((session, command))
        for action in self.on_start:
            assert callable(action)
            action()


class FakeUnit(ServiceManager):
    """Records what the session asked of the manager; what happens to the lock is the test's to say."""

    def __init__(self) -> None:
        self.calls: list[str] = []
        self.running = False
        self.exit_status: int | None = None
        self.stopping = False
        self.state_calls = 0
        self.fail: ServiceError | None = None
        self.on_start: list[object] = []
        self.on_stop: list[object] = []

    @property
    def name(self) -> str:
        return "systemd"

    @property
    def unit_path(self) -> Path:
        return Path("/unit")

    def render(self, uv: str) -> str:
        raise AssertionError("not rendered")

    def _act(self, call: str, actions: list[object]) -> None:
        self.calls.append(call)
        if self.fail:
            raise self.fail
        for action in actions:
            assert callable(action)
            action()

    def load(self) -> None:
        self._act("load", [])

    def start(self) -> None:
        self._act("start", self.on_start)

    def stop(self) -> None:
        self._act("stop", self.on_stop)

    def unload(self) -> None:
        raise AssertionError("not unloaded")

    def state(self) -> UnitState:
        self.state_calls += 1
        return UnitState(self.running, self.exit_status, self.stopping)

    def recent_log(self, lines: int) -> list[str]:
        return []


def session_clock() -> Clock:
    """A session clock that sleeps by moving a fake clock, so no test waits."""
    fake = FakeClock(0.0)
    return Clock(fake.advance, fake, lambda: 1_000_000.0)


class SessionTest(unittest.TestCase):
    def setUp(self) -> None:
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.layout = Layout(Path(tmp.name))
        self.mux = FakeMux()
        self.said: list[str] = []
        self.kills: list[tuple[int, int]] = []
        self.lock: InstanceLock | None = None
        self.on_kill: list[object] = []
        self.addCleanup(self.release)

    def session(self, mux: Multiplexer | None = None) -> SupervisorSession:
        return SupervisorSession(
            self.layout, "acme", mux or self.mux, self.said.append, session_clock(), self.kill, {"uv": "/opt/uv"}.get
        )

    def kill(self, pid: int, sig: int) -> None:
        self.kills.append((pid, sig))
        for action in self.on_kill:
            assert callable(action)
            action()

    def take_lock(self) -> None:
        self.lock = InstanceLock(self.layout.root / ".run.lock")
        self.lock.__enter__()

    def release(self) -> None:
        if self.lock is not None:
            self.lock.__exit__(None, None, None)
            self.lock = None

    def test_start_runs_the_supervisor_detached_with_the_log_file(self) -> None:
        self.mux.on_start.append(self.take_lock)

        code = self.session().start()

        self.assertEqual(0, code)
        name, command = self.mux.started[0]
        self.assertEqual("slop-haus-acme", name)
        self.assertEqual(["/opt/uv", "run"], command[:2])
        self.assertEqual(["--log-file", str(self.layout.root / "supervisor.log")], command[-2:])
        self.assertEqual(["--root", str(self.layout.root)], command[-4:-2])

    def test_host_session_gate_refuses_by_default_and_warns_with_the_override(self) -> None:
        env = {"SLOP_HAUS_HOST_SESSION": "1"}

        self.assertFalse(host_session_permits_start(env, False, self.said.append))
        self.assertIn("refusing to start", self.said[0])
        self.assertIn("--allow-host-session", self.said[1])
        self.said.clear()
        self.assertTrue(host_session_permits_start(env, True, self.said.append))
        self.assertTrue(self.said[0].startswith("warning: this is an agent session"))
        self.said.clear()
        self.assertTrue(host_session_permits_start({}, False, self.said.append))
        self.assertEqual([], self.said)

    def test_tmux_gets_the_command_as_one_shell_string(self) -> None:
        command = self.session(Tmux(FakeRunner())).command()

        self.assertEqual(1, len(command))
        self.assertTrue(command[0].startswith("/opt/uv run "))

    def test_start_refuses_a_second_instance_and_says_how_to_attach(self) -> None:
        self.mux.sessions.add("slop-haus-acme")

        self.assertEqual(1, self.session().start())

        self.assertEqual([], self.mux.started)
        self.assertIn("slop-haus attach", self.said[0])

    def test_start_refuses_when_a_foreground_run_holds_the_tenant(self) -> None:
        self.take_lock()

        self.assertEqual(1, self.session().start())

        self.assertEqual([], self.mux.started)
        self.assertIn(f"pid {os.getpid()}", self.said[0])

    def test_start_shows_the_log_when_the_supervisor_does_not_stay_up(self) -> None:
        (self.layout.root / "supervisor.log").write_text("boom: no slots\n")
        self.mux.sessions.add("gone")

        self.assertEqual(1, self.session().start())

        self.assertIn("  boom: no slots", self.said)

    def test_stop_drains_and_waits_for_the_lock_to_go(self) -> None:
        self.take_lock()
        self.on_kill.append(self.release)
        self.on_kill.append(lambda: self.mux.sessions.discard("slop-haus-acme"))

        self.assertEqual(0, self.session().stop())

        self.assertEqual([(os.getpid(), signal.SIGTERM)], self.kills)
        self.assertEqual("stopped", self.said[-1])

    def test_stop_now_sends_the_second_signal_too(self) -> None:
        self.take_lock()
        self.on_kill.append(self.release)
        self.on_kill.append(lambda: self.mux.sessions.discard("slop-haus-acme"))

        self.session().stop(now=True)

        self.assertEqual(2, len(self.kills))

    def test_stop_gives_up_after_the_timeout_and_names_stop_now(self) -> None:
        self.take_lock()

        self.assertEqual(1, self.session().stop(timeout=5))

        self.assertIn("stop --now", self.said[-1])

    def test_stop_when_not_running_signals_nobody(self) -> None:
        self.assertEqual(0, self.session().stop())

        self.assertEqual([], self.kills)
        self.assertEqual(["not running"], self.said)

    def test_status_renders_each_slots_phase_and_the_last_error(self) -> None:
        self.take_lock()
        self.mux.sessions.add("slop-haus-acme")
        now = time.time()

        def fmt(t: float) -> str:
            return time.strftime("%Y-%m-%d %H:%M:%S", time.localtime(t))

        (self.layout.root / "supervisor.log").write_text(
            f"{fmt(now - 5000)},000 INFO slop-haus-supervisor: running 2 slot(s) for https://s\n"
            f"{fmt(now - 4000)},000 INFO slop-haus-supervisor: box-1: phase idle -> working (ticket #7)\n"
            f"{fmt(now - 100)},000 ERROR slop-haus-supervisor: it broke\n"
        )
        session = self.session()
        session._clock.wall = lambda: now  # pyright: ignore[reportPrivateUsage]

        lines = session.status_lines()

        self.assertIn("  box-1: working (ticket #7) for 1h06m", lines)
        self.assertIn("  last error (1m ago): it broke", lines)
        self.assertTrue(lines[0].startswith("  session: screen slop-haus-acme"))

    def test_status_of_a_stopped_supervisor_says_how_to_start_it(self) -> None:
        self.assertEqual(["  start it with `slop-haus start`"], self.session().status_lines())

    def unit_session(self) -> tuple[SupervisorSession, FakeUnit]:
        unit = FakeUnit()
        session = SupervisorSession(
            self.layout, "acme", self.mux, self.said.append, session_clock(), self.kill, {"uv": "/opt/uv"}.get, unit=unit
        )
        return session, unit

    def test_start_with_a_unit_starts_the_unit_and_not_a_multiplexer_session(self) -> None:
        session, unit = self.unit_session()
        unit.on_start.append(self.take_lock)

        self.assertEqual(0, session.start())

        self.assertEqual(["load", "start"], unit.calls)
        self.assertEqual([], self.mux.started)
        self.assertIn("started under systemd", self.said[0])

    def test_start_with_a_unit_refuses_when_the_tenant_is_already_held(self) -> None:
        session, unit = self.unit_session()
        self.take_lock()

        self.assertEqual(1, session.start())

        self.assertEqual([], unit.calls)

    def test_start_with_a_unit_shows_the_log_when_the_unit_exits(self) -> None:
        (self.layout.root / "supervisor.log").write_text("boom: no slots\n")
        session, unit = self.unit_session()
        unit.exit_status = 78

        self.assertEqual(1, session.start())

        # It gave up on seeing the exit, not by polling to the deadline.
        self.assertEqual(1, unit.state_calls)

        self.assertIn("  boom: no slots", self.said)

    def test_a_manager_failure_is_a_launcher_error(self) -> None:
        session, unit = self.unit_session()
        unit.fail = ServiceError("systemctl failed")

        with self.assertRaises(LauncherError):
            session.start()

    def test_stop_with_a_unit_stops_the_unit_and_signals_nobody(self) -> None:
        session, unit = self.unit_session()
        self.take_lock()
        unit.running = True
        unit.on_stop.append(self.release)

        self.assertEqual(0, session.stop())

        self.assertEqual(["stop"], unit.calls)
        self.assertEqual([], self.kills)
        self.assertEqual("stopped (systemd)", self.said[-1])

    def test_stop_with_a_unit_reports_a_supervisor_that_outlived_it(self) -> None:
        session, unit = self.unit_session()
        self.take_lock()
        unit.running = True

        self.assertEqual(1, session.stop())

    def test_stop_now_with_a_unit_signals_the_process(self) -> None:
        session, unit = self.unit_session()
        self.take_lock()
        unit.running = True
        self.on_kill.append(self.release)

        self.assertEqual(0, session.stop(now=True))

        self.assertEqual([], unit.calls)
        self.assertEqual(2, len(self.kills))

    def test_stop_with_a_unit_signals_a_supervisor_the_unit_does_not_own(self) -> None:
        session, unit = self.unit_session()
        self.take_lock()
        self.on_kill.append(self.release)

        self.assertEqual(0, session.stop())

        self.assertEqual([], unit.calls)
        self.assertEqual(1, len(self.kills))

    def test_stop_with_a_unit_during_its_drain_asks_the_unit_and_does_not_signal(self) -> None:
        session, unit = self.unit_session()
        self.take_lock()
        unit.stopping = True
        unit.on_stop.append(self.release)

        self.assertEqual(0, session.stop())

        self.assertEqual(["stop"], unit.calls)
        self.assertEqual([], self.kills)

    def test_stop_with_a_unit_waits_for_a_late_lock_release_up_to_the_timeout(self) -> None:
        session, unit = self.unit_session()
        self.take_lock()
        unit.running = True

        self.assertEqual(1, session.stop(timeout=5))

        self.assertIn("after 5s", self.said[-1])

    def test_stop_with_a_unit_and_nothing_running_still_stops_the_unit(self) -> None:
        session, unit = self.unit_session()

        self.assertEqual(0, session.stop())

        self.assertEqual(["stop"], unit.calls)
        self.assertEqual([], self.kills)

    def test_restart_with_a_unit_stops_then_starts_it_without_signals(self) -> None:
        session, unit = self.unit_session()
        self.take_lock()
        unit.running = True
        unit.on_stop.append(self.release)
        unit.on_start.append(self.take_lock)

        self.assertEqual(0, session.restart(False))

        self.assertEqual(["stop", "load", "start"], unit.calls)
        self.assertEqual([], self.kills)

    def arm_restart(self) -> None:
        self.busy: list[list[str]] = []
        self.take_lock()
        self.mux.sessions.add("slop-haus-acme")
        self.on_kill.append(self.release)
        self.on_kill.append(lambda: self.mux.sessions.discard("slop-haus-acme"))
        self.mux.on_start.append(self.take_lock)

    def restart(self, when_idle: bool, deadline: float = 3600.0) -> int:
        session = SupervisorSession(
            self.layout, "acme", self.mux, self.said.append, session_clock(), self.kill, {"uv": "/opt/uv"}.get,
            busy_hosts=lambda: self.busy.pop(0) if self.busy else [],
        )
        return session.restart(when_idle, deadline, timeout=10)

    def test_restart_when_idle_waits_until_the_hosts_report_idle_and_then_stops_and_starts(self) -> None:
        self.arm_restart()
        self.busy = [["rc1"], ["rc1"], []]

        self.assertEqual(0, self.restart(True))

        self.assertEqual(1, len(self.kills))
        self.assertEqual(1, len(self.mux.started))
        self.assertTrue(any("waiting for rc1" in line for line in self.said))
        self.assertEqual(0, len(self.busy))

    def test_restart_when_idle_gives_up_at_the_deadline_and_leaves_the_supervisor_running(self) -> None:
        self.arm_restart()
        self.busy = [["rc1"]] * 1000

        self.assertEqual(1, self.restart(True, deadline=30))

        self.assertEqual([], self.kills)
        self.assertEqual([], self.mux.started)
        self.assertTrue(any("not restarted" in line for line in self.said))

    def test_restart_without_when_idle_it_does_not_look_at_the_hosts(self) -> None:
        self.arm_restart()
        self.busy = [["rc1"]]

        self.assertEqual(0, self.restart(False))

        self.assertEqual([["rc1"]], self.busy)
        self.assertEqual(1, len(self.kills))

    def host(self, name: str, running: bool, marker: str | None) -> contextlib.ExitStack:
        directory = self.layout.remote_control_workspace(name)
        (directory / ".slop-haus").mkdir(parents=True)
        if marker is not None:
            (directory / IDLE_MARKER_NAME).parent.mkdir(parents=True, exist_ok=True)
            (directory / IDLE_MARKER_NAME).write_text(marker + "\n")
        stack = contextlib.ExitStack()
        self.addCleanup(stack.close)
        if running:
            stack.enter_context(supervisor_lock(directory))
        return stack

    def test_busy_hosts_are_the_running_ones_that_do_not_say_idle(self) -> None:
        self.host("working", True, "busy")
        self.host("starting", True, None)
        self.host("between", True, "idle")
        self.host("dead", False, "busy")

        self.assertEqual(["starting", "working"], busy_hosts_of(self.layout))

    def test_no_hosts_directory_means_nothing_is_busy(self) -> None:
        self.assertEqual([], busy_hosts_of(self.layout))
