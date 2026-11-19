from __future__ import annotations

import os
import subprocess
import tempfile
import unittest
from pathlib import Path

from slop_haus_supervisor.workspace.reconcile import parse_lsof_cwds, process_in_workspace


class ParseLsofCwdsTest(unittest.TestCase):
    OUT = "p120\nfcwd\nn/Users/a/ws\np403\nfcwd\nn/\np500\nfcwd\nn/Users/a/ws/repo\n"

    def test_every_process_cwd_is_listed(self) -> None:
        self.assertEqual([Path("/Users/a/ws"), Path("/"), Path("/Users/a/ws/repo")], parse_lsof_cwds(self.OUT, me=1))

    def test_this_process_is_left_out(self) -> None:
        self.assertEqual([Path("/Users/a/ws"), Path("/")], parse_lsof_cwds(self.OUT, me=500))

    def test_a_process_without_a_readable_cwd_is_skipped(self) -> None:
        self.assertEqual([], parse_lsof_cwds("p7\nfcwd\nn(readlink: Permission denied)\n", me=1))


class ProcessInWorkspaceTest(unittest.TestCase):
    """Against a real process, found through /proc on Linux and lsof on macOS."""

    def setUp(self) -> None:
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.ws = Path(tmp.name) / "ws"
        (self.ws / "repo").mkdir(parents=True)
        self.elsewhere = Path(tmp.name) / "elsewhere"
        self.elsewhere.mkdir()

    def sleeper(self, cwd: Path) -> None:
        # Popen returns once the child has exec'd, so its cwd is already set: nothing to wait for.
        proc = subprocess.Popen(["sleep", "600"], cwd=cwd)
        self.addCleanup(proc.wait)
        self.addCleanup(proc.kill)

    def test_a_process_inside_the_workspace_is_found(self) -> None:
        self.sleeper(self.ws / "repo")
        self.assertTrue(process_in_workspace(self.ws))

    def test_a_process_elsewhere_is_not(self) -> None:
        self.sleeper(self.elsewhere)
        self.assertFalse(process_in_workspace(self.ws))

    def test_this_process_does_not_count(self) -> None:
        old = os.getcwd()
        os.chdir(self.ws)
        self.addCleanup(os.chdir, old)
        self.assertFalse(process_in_workspace(self.ws))


if __name__ == "__main__":
    unittest.main()
