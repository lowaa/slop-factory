from __future__ import annotations

import unittest
from collections.abc import Callable, Mapping
from pathlib import Path

from slop_haus_supervisor.launcher.multiplexer import LauncherError, Screen, Tmux, choose
from slop_haus_supervisor.ports import ProcessResult


class FakeBinary:
    """Stands in for the screen/tmux executables: records the argv and answers what the test scripted."""

    def __init__(self, *answers: ProcessResult) -> None:
        self.calls: list[list[str]] = []
        self._answers = list(answers)

    def run(self, argv: list[str], cwd: Path, timeout: float | None = None, env: Mapping[str, str] | None = None) -> ProcessResult:
        self.calls.append(argv)
        return self._answers.pop(0) if self._answers else ProcessResult(0, "", "")


class MultiplexerTest(unittest.TestCase):
    def test_screen_session_is_found_by_its_exact_name(self) -> None:
        listing = "There are screens on:\n\t123.slop-haus-acme\t(Detached)\n\t456.slop-haus-acme-2\t(Detached)\n"

        self.assertTrue(Screen(FakeBinary(ProcessResult(1, listing, ""))).exists("slop-haus-acme"))
        self.assertFalse(Screen(FakeBinary(ProcessResult(1, listing, ""))).exists("slop-haus"))
        self.assertFalse(Screen(FakeBinary(ProcessResult(1, "No Sockets found.\n", ""))).exists("slop-haus-acme"))

    def test_tmux_session_is_looked_up_by_exact_name(self) -> None:
        binary = FakeBinary(ProcessResult(0, "", ""))

        self.assertTrue(Tmux(binary).exists("s"))
        self.assertEqual([["tmux", "has-session", "-t", "=s"]], binary.calls)

    def test_start_failure_carries_the_multiplexers_message(self) -> None:
        with self.assertRaisesRegex(LauncherError, "no server"):
            Tmux(FakeBinary(ProcessResult(1, "", "no server"))).start("s", ["x"], Path("/"))

    def test_screen_is_preferred_and_neither_is_explained(self) -> None:
        def installed(*names: str) -> Callable[[str], str | None]:
            return lambda name: f"/bin/{name}" if name in names else None

        self.assertIsInstance(choose(FakeBinary(), installed("screen", "tmux")), Screen)
        self.assertIsInstance(choose(FakeBinary(), installed("tmux")), Tmux)
        with self.assertRaisesRegex(LauncherError, "neither screen nor tmux"):
            choose(FakeBinary(), installed())
