from __future__ import annotations

import logging
import tempfile
import unittest
from collections.abc import Mapping, Sequence
from pathlib import Path

from fakes import FakeAlerts, FakeClock
from slop_haus_supervisor.fleet.child_process import Child
from slop_haus_supervisor.fleet.phase import Phase, Reason
from slop_haus_supervisor.fleet.host_slot import CRASH_LOOP, RemoteControlConfig, RemoteControlDeps, RemoteControlSlot
from slop_haus_supervisor.workspace.config import ConfigError
from slop_haus_supervisor.workspace.host_files import host_lock
from slop_haus_supervisor.workspace.layout import Layout

CONFIG = RemoteControlConfig(backoffs=(10.0, 40.0, 100.0), min_session=60.0, crash_loop_failures=3, poll_interval=2.0)
SUPERVISOR_SECRET = "supervisor-only-secret-0001"


class FakeChild:
    def __init__(self) -> None:
        self.code: int | None = None
        self.terminated_with: float | None = None

    def poll(self) -> int | None:
        return self.code

    def terminate(self, stop_wait: float) -> None:
        self.terminated_with = stop_wait
        self.code = -15


class FakeSpawner:
    def __init__(self) -> None:
        self.spawned: list[tuple[list[str], Path, dict[str, str], FakeChild]] = []
        self.fail: OSError | None = None

    def spawn(self, argv: Sequence[str], cwd: Path, env: Mapping[str, str]) -> Child:
        if self.fail is not None:
            raise self.fail
        child = FakeChild()
        self.spawned.append((list(argv), cwd, dict(env), child))
        return child

    @property
    def last(self) -> FakeChild:
        return self.spawned[-1][3]


class FakeWorkspace:
    def __init__(self, directory: Path) -> None:
        self.directory = directory
        self.calls: list[str] = []
        self.error: Exception | None = None

    def ensure(self) -> None:
        self.calls.append("ensure")
        if self.error is not None:
            raise self.error
        (self.directory / ".slop-haus").mkdir(parents=True, exist_ok=True)

    def refresh(self) -> None:
        self.calls.append("refresh")


class RemoteControlSlotTest(unittest.TestCase):
    def setUp(self) -> None:
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.layout = Layout(Path(tmp.name))
        self.clock = FakeClock()
        self.spawner = FakeSpawner()
        self.alerts = FakeAlerts()
        self.missing = ""
        self.workspace = FakeWorkspace(self.layout.remote_control_workspace("rc1"))
        self.environ = {"PATH": "/usr/bin", "HOME": "/home/x", "SLOP_HAUS_TOKEN": SUPERVISOR_SECRET, "PYTHONPATH": "/evil"}
        self.slot = RemoteControlSlot("rc1", "claude", RemoteControlDeps(
            self.layout, self.workspace, self.spawner, self.clock, self.alerts,
            lambda _harness, _secrets: self.missing, self.environ, logging.getLogger("test"), config=CONFIG,
        ))
        self.addCleanup(self.slot.abandon_run)

    def crash(self, after: float, code: int = 1) -> None:
        self.clock.advance(after)
        self.spawner.last.code = code
        self.slot.step()

    def start(self) -> None:
        self.slot.step()
        self.assertEqual(Phase.RUNNING, self.slot.phase)

    def test_it_starts_one_host_after_ensure_and_the_sync_from_the_app_directory(self) -> None:
        self.start()

        argv, cwd, _env, _child = self.spawner.spawned[0]
        self.assertEqual(["ensure", "refresh"], self.workspace.calls)
        self.assertEqual(self.layout.app, cwd)
        self.assertEqual(["uv", "run"], argv[:2])
        self.assertIn("slop-haus-remote-control", argv)
        self.assertEqual(str(self.workspace.directory), argv[argv.index("--workspace") + 1])
        self.assertIn("--supervised", argv)

    def test_the_environment_is_built_from_scratch_with_the_layered_files(self) -> None:
        self.layout.secrets_file.write_text("BASE_SECRET=base-0002\nSHARED=base\n")
        self.layout.remote_control_dir.mkdir()
        (self.layout.remote_control_dir / "secrets.env").write_text("KIND_SECRET=kind-0003\nSHARED=kind\n")

        self.start()

        env = self.spawner.spawned[0][2]
        self.assertEqual("/usr/bin", env["PATH"])
        self.assertEqual("kind", env["SHARED"])
        self.assertEqual("base-0002", env["BASE_SECRET"])
        self.assertEqual("kind-0003", env["KIND_SECRET"])
        self.assertNotIn("SLOP_HAUS_TOKEN", env)
        self.assertNotIn("PYTHONPATH", env)

    def test_the_host_is_told_where_the_supervisors_logins_are(self) -> None:
        self.start()

        self.assertEqual(str(self.layout.root), self.spawner.spawned[0][2]["SLOP_HAUS_SHARED_LOGIN_ROOT"])

    def test_an_exit_is_a_crash_and_the_restart_waits_out_the_backoff(self) -> None:
        self.start()
        self.crash(after=100)

        self.assertEqual(Phase.BACKOFF, self.slot.phase)
        self.clock.advance(9)
        self.slot.step()
        self.assertEqual(1, len(self.spawner.spawned))
        self.clock.advance(1)
        self.slot.step()
        self.assertEqual(2, len(self.spawner.spawned))
        self.assertEqual(Phase.RUNNING, self.slot.phase)

    def test_a_clean_exit_counts_as_a_crash_too(self) -> None:
        self.start()
        self.crash(after=100, code=0)

        self.assertEqual(Phase.BACKOFF, self.slot.phase)

    def test_the_backoff_grows_and_a_long_session_resets_it(self) -> None:
        delays: list[float] = []
        self.start()
        for session in (100, 5, 5, 100):
            self.crash(after=session)
            self.assertEqual(Phase.BACKOFF, self.slot.phase)
            delays.append(self.slot.step())
            self.clock.advance(delays[-1])
            self.slot.step()

        # The first long session is forgotten at once; the short ones climb; the next long one starts over.
        self.assertEqual([10.0, 40.0, 100.0, 10.0], delays)

    def test_instant_failures_in_a_row_pause_the_slot_as_a_crash_loop(self) -> None:
        self.start()
        for _ in range(CONFIG.crash_loop_failures):
            self.assertNotEqual(Phase.PAUSED, self.slot.phase)
            self.crash(after=1)
            if self.slot.phase == Phase.BACKOFF:
                self.clock.advance(1000)
                self.slot.step()

        self.assertEqual(Phase.PAUSED, self.slot.phase)
        info = self.slot.pause_info()
        assert info is not None
        self.assertTrue(info[0].startswith(CRASH_LOOP), info[0])
        self.assertEqual(CRASH_LOOP, self.slot.snapshot().pause_reason.split(":")[0])
        self.assertEqual(len(self.spawner.spawned), CONFIG.crash_loop_failures)

    def test_a_crash_loop_is_cleared_by_a_person_and_the_host_starts_afresh(self) -> None:
        self.start()
        for _ in range(CONFIG.crash_loop_failures):
            self.crash(after=1)
            if self.slot.phase == Phase.BACKOFF:
                self.clock.advance(1000)
                self.slot.step()
        self.assertEqual(Phase.PAUSED, self.slot.phase)

        self.assertEqual("", self.slot.try_unpause(manual=True))
        self.slot.step()

        self.assertEqual(Phase.RUNNING, self.slot.phase)
        self.assertEqual(CONFIG.crash_loop_failures + 1, len(self.spawner.spawned))
        # A fresh count: one more quick exit is a backoff, not another pause.
        self.crash(after=1)
        self.assertEqual(Phase.BACKOFF, self.slot.phase)

    def test_a_long_session_between_quick_ones_keeps_it_out_of_a_crash_loop(self) -> None:
        self.start()
        for session in (1, 1, 100, 1, 1):
            self.crash(after=session)
            self.assertEqual(Phase.BACKOFF, self.slot.phase)
            self.clock.advance(1000)
            self.slot.step()

        self.assertEqual(Phase.RUNNING, self.slot.phase)

    def test_a_host_that_will_not_spawn_is_a_crash_too(self) -> None:
        self.spawner.fail = FileNotFoundError("uv")

        self.slot.step()

        self.assertEqual(Phase.BACKOFF, self.slot.phase)

    def test_a_workspace_that_cannot_be_set_up_says_why_until_one_start_gets_past_it(self) -> None:
        self.workspace.error = ConfigError("fatal: no route to the repo: the slot is not a checkout of it")

        with self.assertRaises(ConfigError):
            self.slot.step()

        snapshot = self.slot.snapshot()
        self.assertEqual(Phase.IDLE.value, snapshot.phase)
        self.assertEqual(Reason.BACKOFF_AFTER_ERROR.value, snapshot.reason)
        self.assertTrue(snapshot.reason_detail.startswith("fatal: no route to the repo"))
        self.workspace.error = None
        self.missing = "ANTHROPIC_API_KEY"
        self.slot.step()
        self.assertEqual(Phase.PAUSED, self.slot.phase)
        self.missing = ""
        self.slot.try_unpause(manual=True)
        self.assertEqual("", self.slot.snapshot().reason_detail, "the workspace was set up: its old error is not shown")

    def test_after_a_crash_a_restart_that_cannot_set_the_workspace_up_says_so_not_the_crash(self) -> None:
        self.start()
        self.crash(after=100)
        self.workspace.error = ConfigError("fatal: no route to the repo")
        self.clock.advance(10)

        with self.assertRaises(ConfigError):
            self.slot.step()

        self.assertEqual("fatal: no route to the repo", self.slot.snapshot().reason_detail)
        self.workspace.error = None
        self.slot.step()
        self.assertEqual(Phase.RUNNING, self.slot.phase)
        self.crash(after=100)
        self.assertEqual("host exited with status 1", self.slot.snapshot().reason_detail)

    def test_a_missing_login_pauses_with_the_secret_named_until_it_is_set(self) -> None:
        self.missing = "CLAUDE_CODE_OAUTH_TOKEN or ANTHROPIC_API_KEY"

        self.slot.step()

        self.assertEqual(Phase.PAUSED, self.slot.phase)
        self.assertEqual([], self.spawner.spawned)
        self.assertEqual("needs secret CLAUDE_CODE_OAUTH_TOKEN or ANTHROPIC_API_KEY on host", self.slot.snapshot().pause_reason)
        self.assertIn("needs secret", self.alerts.messages[0])
        self.assertNotEqual("", self.slot.try_unpause())
        self.missing = ""
        self.assertEqual("", self.slot.try_unpause())
        self.slot.step()
        self.assertEqual(Phase.RUNNING, self.slot.phase)

    def test_a_host_started_by_hand_holds_the_directory_and_the_supervisor_waits(self) -> None:
        self.workspace.ensure()
        with host_lock(self.workspace.directory):
            self.slot.step()

            self.assertEqual(Phase.PAUSED, self.slot.phase)
            self.assertEqual([], self.spawner.spawned)
            self.assertNotEqual("", self.slot.try_unpause())
        self.assertEqual("", self.slot.try_unpause())
        self.slot.step()
        self.assertEqual(Phase.RUNNING, self.slot.phase)

    def test_idle_is_reported_from_the_hosts_marker_and_when_there_is_no_host(self) -> None:
        marker = self.workspace.directory / ".slop-haus" / "conductor" / "idle"
        self.start()
        self.assertFalse(self.slot.snapshot().idle)

        marker.parent.mkdir(parents=True)
        marker.write_text("idle\n")
        self.slot.step()
        self.assertTrue(self.slot.snapshot().idle)

        marker.write_text("busy\n")
        self.slot.step()
        self.assertFalse(self.slot.snapshot().idle)

        self.crash(after=100)
        self.assertTrue(self.slot.snapshot().idle)

    def test_abandoning_ends_the_host_and_starts_nothing_more(self) -> None:
        self.start()

        self.slot.abandon_run()

        self.assertEqual(CONFIG.stop_grace, self.spawner.last.terminated_with)
        self.slot.step()
        self.assertEqual(1, len(self.spawner.spawned))
