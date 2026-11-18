from __future__ import annotations

import tempfile
import unittest
from pathlib import Path
from unittest import mock

from slop_haus_supervisor.remote_control.host import main as host_main
from slop_haus_supervisor.service.instance_lock import AlreadyRunning
from slop_haus_supervisor.workspace.host_files import SUPERVISOR, IdleMarker, host_lock, read_idle, supervisor_lock


class WorkspaceLockTest(unittest.TestCase):
    def setUp(self) -> None:
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.root = Path(tmp.name)
        (self.root / ".slop-haus").mkdir()

    def test_a_manual_launch_against_a_directory_the_supervisor_runs_is_refused_by_name(self) -> None:
        with supervisor_lock(self.root), self.assertLogs(host_main.log, "ERROR") as logged:
            code = host_main.main(["--workspace", str(self.root)])

        self.assertEqual(1, code)
        self.assertIn("managed by the supervisor on this machine", logged.output[0])

    def test_a_second_manual_launch_is_refused(self) -> None:
        with host_lock(self.root), self.assertLogs(host_main.log, "ERROR") as logged:
            code = host_main.main(["--workspace", str(self.root)])

        self.assertEqual(1, code)
        self.assertIn("already running", logged.output[0])
        self.assertNotIn("managed by the supervisor", logged.output[0])

    def test_a_supervised_host_is_refused_while_one_started_by_hand_runs(self) -> None:
        with host_lock(self.root), self.assertLogs(host_main.log, "ERROR"):
            self.assertEqual(1, host_main.main(["--workspace", str(self.root), "--supervised"]))

    def test_a_supervised_host_holds_the_lock_for_as_long_as_it_serves(self) -> None:
        def serving(_args: object, _root: Path) -> int:
            with self.assertRaises(AlreadyRunning) as refused:
                host_lock(self.root).__enter__()
            self.assertEqual(SUPERVISOR, refused.exception.owner)
            return 0

        with mock.patch.object(host_main, "serve", serving):
            self.assertEqual(0, host_main.main(["--workspace", str(self.root), "--supervised"]))
        with host_lock(self.root):
            pass


class IdleMarkerTest(unittest.TestCase):
    def setUp(self) -> None:
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.path = Path(tmp.name) / "conductor" / "idle"

    def test_the_marker_follows_what_the_host_publishes(self) -> None:
        marker = IdleMarker(self.path)

        marker.publish(True)
        self.assertTrue(read_idle(self.path))
        marker.publish(False)
        self.assertFalse(read_idle(self.path))

    def test_no_marker_is_not_idle(self) -> None:
        self.assertFalse(read_idle(self.path))

    def test_clearing_removes_it_and_a_later_publish_writes_again(self) -> None:
        marker = IdleMarker(self.path)
        marker.publish(True)
        marker.clear()
        self.assertFalse(read_idle(self.path))

        marker.publish(True)

        self.assertTrue(read_idle(self.path))


class MissingHarnessTest(unittest.TestCase):
    def exit_for_missing(self, *flags: str) -> tuple[int, str]:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / ".slop-haus").mkdir()
            harness = mock.Mock()
            harness.check_installed.side_effect = FileNotFoundError("pi")
            chosen = host_main.ChosenHarness(harness, None, "pi is not on PATH: install pi (https://pi.dev) first", "")
            with (
                mock.patch.object(host_main, "choose_harness", return_value=chosen),
                mock.patch.dict("os.environ", {"SLOP_HAUS_URL": "http://server.invalid"}),
                self.assertLogs(host_main.log, "ERROR") as logged,
            ):
                code = host_main.main(["--workspace", str(root), *flags])
        (line,) = logged.output
        self.assertIn("pi is not on PATH", line)
        self.assertNotIn("\n", line)
        return code, line

    def test_a_supervised_host_missing_its_harness_says_where_path_belongs(self) -> None:
        code, line = self.exit_for_missing("--supervised")
        self.assertEqual(127, code)
        self.assertIn("<root>/run.env", line)
        self.assertIn("~/.bashrc", line)

    def test_a_manual_host_is_not_told_to_edit_run_env(self) -> None:
        code, line = self.exit_for_missing()
        self.assertEqual(127, code)
        self.assertNotIn("run.env", line)
