import os
import stat
import tempfile
import unittest
from pathlib import Path

from slop_haus_supervisor.harness.version import detect_version


class DetectVersionTest(unittest.TestCase):
    def tool(self, script: str) -> str:
        directory = Path(tempfile.mkdtemp())
        path = directory / "tool"
        path.write_text("#!/bin/sh\n" + script + "\n", encoding="utf-8")
        path.chmod(path.stat().st_mode | stat.S_IEXEC)
        self.addCleanup(lambda: (path.unlink(), directory.rmdir()))
        return str(path)

    def test_the_first_line_of_what_the_tool_prints(self) -> None:
        self.assertEqual("2.1.0 (Claude Code)", detect_version(self.tool("echo '2.1.0 (Claude Code)'; echo more")))

    def test_a_tool_that_fails_has_no_version(self) -> None:
        self.assertEqual("", detect_version(self.tool("echo oops; exit 1")))

    def test_a_missing_tool_has_no_version(self) -> None:
        self.assertEqual("", detect_version(os.path.join(tempfile.gettempdir(), "no-such-tool-here")))

    def test_a_long_line_is_cut(self) -> None:
        self.assertEqual(100, len(detect_version(self.tool("printf 'x%.0s' $(seq 1 300)"))))
