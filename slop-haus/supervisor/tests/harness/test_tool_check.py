from __future__ import annotations

import unittest
from pathlib import Path

from fakes import FakeRunner
from slop_haus_supervisor.harness.tool_check import ToolChecker, login_shell_which, read_tool_list
from slop_haus_supervisor.ports import ProcessResult

RUN_ENV = Path("/r/run.env")


def NONE(name: str) -> str | None:
    return None


class ToolCheckerTest(unittest.TestCase):
    def test_a_tool_the_service_finds_is_fine(self) -> None:
        lookup = ToolChecker({"pi": "/s/pi"}.get, NONE).lookup("pi")

        self.assertTrue(lookup.found)
        self.assertEqual("", lookup.problem(RUN_ENV))

    def test_a_tool_only_the_login_shell_has_names_its_folder_and_the_fix(self) -> None:
        lookup = ToolChecker(NONE, {"pi": "/h/pi-node/bin/pi"}.get).lookup("pi")

        self.assertFalse(lookup.found)
        self.assertEqual("/h/pi-node/bin", lookup.path_fix)
        self.assertEqual(
            "pi is not on this service's PATH (found at /h/pi-node/bin in a login shell): "
            "add that folder to PATH in /r/run.env and restart the supervisor",
            lookup.problem(RUN_ENV),
        )

    def test_a_tool_nowhere_has_no_folder_to_offer(self) -> None:
        lookup = ToolChecker(NONE, NONE).lookup("pi")

        self.assertEqual("", lookup.path_fix)
        self.assertIn("not installed", lookup.problem(RUN_ENV))

    def test_the_login_shell_is_asked_for_the_tool_and_its_last_line_is_the_answer(self) -> None:
        runner = FakeRunner(lambda argv: ProcessResult(0, "motd noise\n/h/bin/pi\n", ""))

        self.assertEqual("/h/bin/pi", login_shell_which(runner, Path("/h"))("pi"))
        self.assertEqual("pi", runner.calls[0][-1])

    def test_the_login_shell_is_asked_once_per_tool(self) -> None:
        runner = FakeRunner(lambda argv: ProcessResult(0, "/h/bin/pi\n", ""))
        find = login_shell_which(runner, Path("/h"))

        find("pi")
        find("pi")

        self.assertEqual(1, len(runner.calls))

    def test_an_alias_or_failure_is_not_a_path(self) -> None:
        self.assertIsNone(login_shell_which(FakeRunner(lambda argv: ProcessResult(0, "alias pi=x\n", "")), Path("/h"))("pi"))
        self.assertIsNone(login_shell_which(FakeRunner(lambda argv: ProcessResult(1, "", "")), Path("/h"))("pi"))


class ReadToolListTest(unittest.TestCase):
    def test_names_comments_and_odd_lines(self) -> None:
        import tempfile

        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "slot-tools.txt"
            path.write_text("go\n# comment\nnode  # toolchain\n\n/bin/sh\n-x\n")

            self.assertEqual(["go", "node"], read_tool_list(path))
            self.assertEqual([], read_tool_list(Path(tmp) / "absent"))
