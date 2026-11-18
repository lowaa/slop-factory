"""Against the real `claude`, with no login: no model is called and nothing is spent.

It shows the flags are accepted, the records parse and the real failure strings classify. Skipped
when `claude` is not installed. Marked `smoke`, so the default run leaves it out: `pytest -m smoke`.
"""

from __future__ import annotations

import os
import shutil
import tempfile
import unittest
from pathlib import Path

import pytest

from slop_haus_supervisor.harness.base import OutcomeKind, RunSpec, SessionNotFound
from slop_haus_supervisor.harness.claude import ClaudeHarness
from slop_haus_supervisor.steward.runner import steward_command


@pytest.mark.smoke
@unittest.skipUnless(shutil.which("claude"), "claude is not installed")
class TestRealClaude(unittest.TestCase):
    def setUp(self) -> None:
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        base = Path(tmp.name)
        (base / "work").mkdir()
        (base / "home").mkdir()
        self.spec = RunSpec("agent-1", base / "work", 1, "b", "say hi")
        # No credentials: a fresh config dir and home, and none of the caller's login variables.
        environ = {"PATH": os.environ["PATH"], "HOME": str(base / "home")}
        self.harness = ClaudeHarness(base / "root", "https://slop-haus.example", "be brief", environ=environ, grace=2.0)

    def test_resuming_a_session_that_does_not_exist(self) -> None:
        with self.assertRaises(SessionNotFound):
            self.harness.resume(self.spec, "11111111-1111-4111-8111-111111111111")

    def test_without_a_login_the_run_is_auth_failed(self) -> None:
        run = self.harness.start(self.spec)
        self.addCleanup(run.kill)
        outcome = run.wait(60)
        assert outcome is not None
        self.assertEqual(OutcomeKind.AUTH_FAILED, outcome.kind, outcome.detail)


if __name__ == "__main__":
    unittest.main()


@pytest.mark.smoke
@unittest.skipUnless(shutil.which("claude"), "claude is not installed")
class TestRealClaudeMaxTurns(unittest.TestCase):
    def test_the_installed_cli_accepts_the_stewards_turn_budget(self) -> None:
        # An unknown flag would exit with usage text; with a known one the run gets as far as the missing login.
        with tempfile.TemporaryDirectory() as tmp:
            base = Path(tmp)
            (base / "work").mkdir()
            (base / "home").mkdir()
            environ = {"PATH": os.environ["PATH"], "HOME": str(base / "home")}
            harness = ClaudeHarness(
                base / "root", "https://slop-haus.example", "be brief", command=steward_command(3), environ=environ, grace=2.0
            )
            run = harness.start(RunSpec("steward", base / "work", 0, "", "say hi"))
            self.addCleanup(run.kill)
            outcome = run.wait(60)
        assert outcome is not None
        self.assertEqual(OutcomeKind.AUTH_FAILED, outcome.kind, outcome.detail)
