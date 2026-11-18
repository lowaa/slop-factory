from __future__ import annotations

import logging
import tempfile
import threading
import unittest
from pathlib import Path

from fakes import FakeAlerts, FakeClock
from slop_haus_supervisor.fleet.host_retirement import HostRetirement
from slop_haus_supervisor.fleet.host_slot import IDLE_MARKER, RemoteControlDeps, RemoteControlSlot
from slop_haus_supervisor.fleet.phase import Phase
from slop_haus_supervisor.fleet.scheduler import Drainable, Scheduler, SchedulerConfig
from slop_haus_supervisor.workspace.layout import Layout
from test_host_slot import CONFIG, FakeSpawner, FakeWorkspace


def mark(slot: RemoteControlSlot, state: str) -> None:
    marker = slot.directory / IDLE_MARKER
    marker.parent.mkdir(parents=True, exist_ok=True)
    marker.write_text(state + "\n")


class FakeRetirer:
    def __init__(self, directory: Path) -> None:
        self.directory = directory
        self.retired: list[str] = []
        self.directory_existed: list[bool] = []

    def retire_agent(self, name: str) -> None:
        self.directory_existed.append(self.directory.exists())
        self.retired.append(name)


class RecordingDrainable:
    """A slot that records what the scheduler tells it, and stays running until told it may end."""

    def __init__(self) -> None:
        self.agent = "rc1"
        self.directory = Path("/rc1")
        self.phase = Phase.RUNNING
        self.drains = 0
        self.held: list[bool] = []
        self.steps_while_draining = 0

    def begin_drain(self) -> None:
        self.drains += 1

    def set_held(self, held: bool) -> None:
        self.held.append(held)

    def step(self) -> float:
        if self.drains:
            self.steps_while_draining += 1
            self.phase = Phase.IDLE
        return 0.0

    def abandon_run(self) -> None: ...

    def release_lock_for_stop(self) -> None: ...

    def pause_info(self) -> None:
        return None


class FakeRepo:
    def __init__(self) -> None:
        self.unpushed = False

    def has_unpushed_anywhere(self, directory: Path) -> bool:
        return self.unpushed


class DrainTest(unittest.TestCase):
    def setUp(self) -> None:
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.layout = Layout(Path(tmp.name))
        self.spawner = FakeSpawner()
        self.slot = RemoteControlSlot("rc1", "claude", RemoteControlDeps(
            self.layout, FakeWorkspace(self.layout.remote_control_workspace("rc1")), self.spawner, FakeClock(), FakeAlerts(),
            lambda _harness, _secrets: "", {"PATH": "/usr/bin"}, logging.getLogger("test"), config=CONFIG,
        ))
        self.addCleanup(self.slot.abandon_run)
        self.slot.step()
        self.assertEqual(Phase.RUNNING, self.slot.phase)

    def test_a_drain_lets_a_live_session_finish_and_then_stops_the_host(self) -> None:
        mark(self.slot, "busy")
        self.slot.begin_drain()

        self.slot.step()
        self.assertEqual(Phase.RUNNING, self.slot.phase)
        self.assertIsNone(self.spawner.last.terminated_with)

        mark(self.slot, "idle")
        self.slot.step()
        self.assertEqual(Phase.IDLE, self.slot.phase)
        self.assertEqual(CONFIG.stop_grace, self.spawner.last.terminated_with)

    def test_a_host_slot_is_drainable_by_the_scheduler(self) -> None:
        self.assertIsInstance(self.slot, Drainable)

    def test_a_drained_host_is_not_started_again(self) -> None:
        mark(self.slot, "idle")
        self.slot.begin_drain()
        self.slot.step()

        self.slot.step()

        self.assertEqual(1, len(self.spawner.spawned))
        self.assertEqual(Phase.IDLE, self.slot.phase)

    def test_a_hold_stops_a_host_between_sessions_and_the_release_starts_one(self) -> None:
        self.slot.set_held(True)
        mark(self.slot, "busy")
        self.slot.step()
        self.assertEqual(Phase.RUNNING, self.slot.phase)

        mark(self.slot, "idle")
        self.slot.step()
        self.assertEqual(Phase.IDLE, self.slot.phase)
        self.slot.step()
        self.assertEqual(1, len(self.spawner.spawned))

        self.slot.set_held(False)
        self.slot.step()
        self.assertEqual(2, len(self.spawner.spawned))
        self.assertEqual(Phase.RUNNING, self.slot.phase)

    def test_a_drain_that_outlasts_its_deadline_ends_the_session(self) -> None:
        mark(self.slot, "busy")
        scheduler = Scheduler([self.slot], threading.Event(), logging.getLogger("test"), SchedulerConfig(idle_gap=0.0, drain_timeout=0.0, join_grace=5.0))
        scheduler.drain()
        scheduler.run()

        self.assertIsNotNone(self.spawner.last.terminated_with)

    def test_the_scheduler_tells_a_host_once_to_hold_and_to_drain(self) -> None:
        slot = RecordingDrainable()
        scheduler = Scheduler([slot], threading.Event(), logging.getLogger("test"), SchedulerConfig(idle_gap=0.0))  # pyright: ignore[reportArgumentType]

        scheduler.hold("rc1", True)
        scheduler.hold("rc1", False)
        scheduler.drain()
        scheduler.run()

        self.assertEqual([True, False], slot.held)
        self.assertEqual(1, slot.drains)
        self.assertEqual(1, slot.steps_while_draining)


class RetirementTest(unittest.TestCase):
    def setUp(self) -> None:
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.layout = Layout(Path(tmp.name))
        self.directory = self.layout.remote_control_workspace("rc1")
        self.retirer = FakeRetirer(self.directory)
        self.repo = FakeRepo()
        self.directory.mkdir(parents=True)
        (self.directory / "file").write_text("x")
        self.retirement = HostRetirement(self.layout, self.retirer, self.repo, logging.getLogger("test"))

    def test_a_drained_host_is_retired_on_the_server_and_its_directory_deleted(self) -> None:
        result = self.retirement.retire("rc1")

        self.assertEqual(["rc1"], self.retirer.retired)
        self.assertEqual([True], self.retirer.directory_existed, "the server is asked before the directory goes")
        self.assertFalse(result.kept_directory)
        self.assertFalse(self.directory.exists())

    def test_unpushed_work_keeps_the_directory_but_the_token_is_still_revoked(self) -> None:
        self.repo.unpushed = True

        with self.assertLogs("test", "WARNING"):
            result = self.retirement.retire("rc1")

        self.assertEqual(["rc1"], self.retirer.retired)
        self.assertTrue(result.kept_directory)
        self.assertTrue((self.directory / "file").exists())

    def test_the_token_is_revoked_only_after_the_host_has_stopped(self) -> None:
        spawner = FakeSpawner()
        slot = RemoteControlSlot("rc1", "claude", RemoteControlDeps(
            self.layout, FakeWorkspace(self.directory), spawner, FakeClock(), FakeAlerts(),
            lambda _harness, _secrets: "", {"PATH": "/usr/bin"}, logging.getLogger("test"), config=CONFIG,
        ))
        self.addCleanup(slot.abandon_run)
        slot.step()
        mark(slot, "busy")
        slot.begin_drain()
        slot.step()
        self.assertEqual([], self.retirer.retired)

        mark(slot, "idle")
        slot.step()
        self.assertEqual(Phase.IDLE, slot.phase)
        self.retirement.retire("rc1")

        self.assertEqual(["rc1"], self.retirer.retired)
        self.assertIsNotNone(spawner.last.terminated_with)
