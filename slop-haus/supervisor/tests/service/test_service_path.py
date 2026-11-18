from __future__ import annotations

import tempfile
import unittest
from pathlib import Path

from fakes import FakeRunner
from slop_haus_supervisor.ports import ProcessResult
from slop_haus_supervisor.service.service_path import LAUNCHD_PATH, SYSTEMD_FALLBACK_PATH, ServicePath, prepend_to_run_env
from slop_haus_supervisor.workspace.envfile import EnvFileError
from slop_haus_supervisor.workspace.layout import Layout


class ServicePathTest(unittest.TestCase):
    def setUp(self) -> None:
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.root = Path(tmp.name)
        self.layout = Layout(self.root)
        self.manager_path = "PATH=/usr/bin:/bin\nHOME=/h\n"

    def path(self, platform: str = "linux", fail: bool = False) -> ServicePath:
        runner = FakeRunner(lambda _argv: ProcessResult(1 if fail else 0, self.manager_path, ""))
        return ServicePath(self.root / "run.env", runner, self.root, platform, lambda name, path: next(
            (f"{d}/{name}" for d in path.split(":") if (d, name) in {("/usr/bin", "git"), ("/h/.local/bin", "claude")}), None
        ))

    def test_systemd_hosts_use_the_user_managers_path_not_the_shells(self) -> None:
        self.assertEqual("/usr/bin:/bin", self.path().effective("/h/.local/bin"))
        self.assertIsNone(self.path().find("claude", "/h/.local/bin"))

    def test_run_env_path_goes_in_front(self) -> None:
        (self.root / "run.env").write_text("PATH=/h/.local/bin\n")

        self.assertEqual("/h/.local/bin:/usr/bin:/bin", self.path().effective(""))
        self.assertEqual("/h/.local/bin/claude", self.path().find("claude", ""))

    def test_a_user_manager_that_cannot_be_asked_gets_systemds_default(self) -> None:
        self.assertEqual(SYSTEMD_FALLBACK_PATH, self.path(fail=True).launcher_path())

    def test_launchd_has_its_minimal_path(self) -> None:
        self.assertEqual(LAUNCHD_PATH, self.path("darwin").launcher_path())

    def test_an_unknown_launcher_falls_back_to_the_shells_path(self) -> None:
        self.assertEqual("/shell", self.path("win32").effective("/shell"))

    def test_prepending_adds_once_and_keeps_the_rest(self) -> None:
        (self.root / "run.env").write_text("PATH=/opt/go/bin\nKEEP=1\n")

        self.assertTrue(prepend_to_run_env(self.layout, "/h/.local/bin"))
        self.assertFalse(prepend_to_run_env(self.layout, "/h/.local/bin"))

        text = (self.root / "run.env").read_text()
        self.assertIn("PATH=/h/.local/bin:/opt/go/bin", text)
        self.assertIn("KEEP=1", text)

    def test_a_relative_folder_is_refused(self) -> None:
        with self.assertRaises(EnvFileError):
            prepend_to_run_env(self.layout, "bin")
