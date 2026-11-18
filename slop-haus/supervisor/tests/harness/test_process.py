from __future__ import annotations

import sys
import tempfile
import unittest
from collections.abc import Callable
from pathlib import Path
from typing import Any

from slop_haus_supervisor.harness.base import OutcomeKind, RunOutcome
from slop_haus_supervisor.harness.process import HarnessProcess, ProcessRun


class ExplodingProtocol:
    started = True
    session_missing = False

    def begin(self, write: Callable[[str], None]) -> None:
        write("{}")

    def on_record(self, record: dict[str, Any], write: Callable[[str], None]) -> RunOutcome | None:
        raise ValueError("cannot make sense of this")

    def on_exit(self, code: int, stderr: str) -> RunOutcome:
        return RunOutcome(OutcomeKind.CRASHED)


class TestProcessRun(unittest.TestCase):
    def test_a_protocol_error_ends_the_run_instead_of_hanging_it(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            echo = "import sys\nfor line in sys.stdin:\n    print(line, flush=True)\n"
            process = HarnessProcess([sys.executable, "-c", echo], {}, Path(tmp))
            run = ProcessRun(process, ExplodingProtocol(), "s", grace=2.0)
            outcome = run.wait(30)
        assert outcome is not None
        self.assertEqual(OutcomeKind.CRASHED, outcome.kind)
        self.assertIn("cannot make sense", outcome.detail)


class TestHarnessProcessWrite(unittest.TestCase):
    def test_a_write_the_live_harness_cannot_take_ends_it(self) -> None:
        """A harness that closed its stdin but lives on would never exit to say why."""
        closed = "import json, os, signal\nos.close(0)\nprint(json.dumps({'type': 'up'}), flush=True)\nsignal.pause()\n"
        with tempfile.TemporaryDirectory() as tmp:
            process = HarnessProcess([sys.executable, "-c", closed], {}, Path(tmp))
            self.addCleanup(process.terminate, 2.0)
            self.assertEqual({"type": "up"}, process.next_record(), "it has closed stdin by now")
            process.write("hello")
            self.assertIsNone(process.next_record(), "the process is gone, so its records end")
            self.assertNotEqual(0, process.exit_code())


if __name__ == "__main__":
    unittest.main()
