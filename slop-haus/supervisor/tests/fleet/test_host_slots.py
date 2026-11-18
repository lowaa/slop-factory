from __future__ import annotations

import logging
import tempfile
import unittest
from pathlib import Path

from fakes import FakeAlerts, FakeClock
from slop_haus_supervisor.fleet.host_slot import RemoteControlDeps
from slop_haus_supervisor.fleet.host_slots import RemoteControlSlots, missing_login
from slop_haus_supervisor.harness.codex_login import CodexSlotGuard
from slop_haus_supervisor.workspace.config import ConfigError
from slop_haus_supervisor.workspace.layout import Layout
from slop_haus_supervisor.workspace.provisioned import Kind
from test_host_slot import FakeSpawner, FakeWorkspace


class RemoteControlSlotsTest(unittest.TestCase):
    def setUp(self) -> None:
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.layout = Layout(Path(tmp.name))
        self.made: list[str] = []
        deps = RemoteControlDeps(
            self.layout, FakeWorkspace(self.layout.root), FakeSpawner(), FakeClock(), FakeAlerts(),
            lambda _h, _s: "", {}, logging.getLogger("test"),
        )
        self.slots = RemoteControlSlots(deps, lambda name: self.record(name))

    def record(self, name: str) -> FakeWorkspace:
        self.made.append(name)
        return FakeWorkspace(self.layout.remote_control_workspace(name))

    def test_a_supervisor_launched_remote_control_gets_a_slot(self) -> None:
        slot = self.slots.build(Kind.REMOTE_CONTROL, "supervisor", "rc1", "claude")

        assert slot is not None
        self.assertEqual("rc1", slot.agent)
        self.assertEqual(self.layout.remote_control_workspace("rc1"), slot.directory)

    def test_a_manual_remote_control_is_left_alone(self) -> None:
        self.assertIsNone(self.slots.build(Kind.REMOTE_CONTROL, "manual", "rc1", "claude"))
        self.assertIsNone(self.slots.build(Kind.REMOTE_CONTROL, "", "rc1", "claude"))
        self.assertEqual([], self.made)

    def test_other_kinds_are_not_hosts_whatever_their_launch(self) -> None:
        self.assertIsNone(self.slots.build(Kind.SLOT, "supervisor", "w1", "claude"))
        self.assertIsNone(self.slots.build(Kind.STEWARD, "supervisor", "steward", "claude"))

    def test_a_name_that_is_no_directory_name_never_becomes_a_process(self) -> None:
        for name in ("../escape", "a/b", "", ".hidden", "x y"):
            with self.subTest(name=name), self.assertRaises(ConfigError):
                self.slots.build(Kind.REMOTE_CONTROL, "supervisor", name, "claude")
        self.assertEqual([], self.made)

    def test_a_harness_a_host_cannot_run_is_refused(self) -> None:
        with self.assertRaises(ConfigError):
            self.slots.build(Kind.REMOTE_CONTROL, "supervisor", "rc1", "emacs")


class CodexGateTest(unittest.TestCase):
    def setUp(self) -> None:
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.layout = Layout(Path(tmp.name))
        self.deps = RemoteControlDeps(
            self.layout, FakeWorkspace(self.layout.root), FakeSpawner(), FakeClock(), FakeAlerts(),
            lambda _h, _s: "", {}, logging.getLogger("test"),
        )

    def slots(self, environ: dict[str, str], others: int) -> RemoteControlSlots:
        guard = CodexSlotGuard(environ, logging.getLogger("test"))
        return RemoteControlSlots(self.deps, lambda _n: FakeWorkspace(self.layout.root), guard, lambda: others)

    def test_a_codex_session_beside_a_codex_worker_is_refused_until_the_login_is_verified(self) -> None:
        with self.assertRaises(ConfigError):
            self.slots({}, others=1).build(Kind.REMOTE_CONTROL, "supervisor", "rc1", "codex")

        self.assertIsNotNone(self.slots({"SLOP_HAUS_CODEX_SHARED_LOGIN": "verified"}, others=1).build(Kind.REMOTE_CONTROL, "supervisor", "rc1", "codex"))

    def test_a_lone_codex_session_and_other_harnesses_are_not_counted(self) -> None:
        self.assertIsNotNone(self.slots({}, others=0).build(Kind.REMOTE_CONTROL, "supervisor", "rc1", "codex"))
        self.assertIsNotNone(self.slots({}, others=3).build(Kind.REMOTE_CONTROL, "supervisor", "rc2", "pi"))
        self.assertIsNotNone(self.slots({}, others=3).build(Kind.REMOTE_CONTROL, "supervisor", "rc3", "claude"))


class MissingLoginTest(unittest.TestCase):
    def test_claude_needs_one_of_its_two_secrets(self) -> None:
        check = missing_login(Path("/nonexistent-root"), "https://s")

        self.assertIn("CLAUDE_CODE_OAUTH_TOKEN", check("claude", {}))
        self.assertEqual("", check("claude", {"ANTHROPIC_API_KEY": "k"}))
        self.assertIn("CLAUDE_CODE_OAUTH_TOKEN", check("claude", {"ANTHROPIC_API_KEY": ""}))
