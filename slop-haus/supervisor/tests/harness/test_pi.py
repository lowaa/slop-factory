from __future__ import annotations

import json
import os
import sys
import tempfile
import unittest
from pathlib import Path
from typing import Any

from fake_pi import FAKE_PI
from harness_fixtures import NOW
from slop_haus_supervisor.harness.base import OutcomeKind, RunOutcome, RunSpec
from slop_haus_supervisor.harness.pi import PI_TOOLS, SLOP_HAUS_TOOLS, PiHarness
from slop_haus_supervisor.harness.process import ProcessRun
from slop_haus_supervisor.harness.variant import InvalidVariant
from slop_haus_supervisor.ports import Clock

WAIT = 30.0


class FixedClock(Clock):
    def now(self) -> float:
        return NOW


class PiCase(unittest.TestCase):
    def setUp(self) -> None:
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.tmp = Path(tmp.name)
        self.root = self.tmp / "root"
        self.work = self.tmp / "work"
        self.work.mkdir()
        self.log = self.tmp / "log"
        self.log.mkdir()
        self.script = self.tmp / "fake_pi.py"
        self.script.write_text(FAKE_PI)
        self.extension = self.tmp / "slop-haus.ts"
        self.runs: list[ProcessRun] = []
        self.addCleanup(lambda: [run.kill() for run in self.runs])

    def harness(self, mode: str = "", **env: str) -> PiHarness:
        environ = {"PATH": os.environ["PATH"], "FAKE_DIR": str(self.log), "FAKE_MODE": mode,
                   "SLOP_HAUS_TOKEN": "must-not-reach-the-run", "KEEP": "yes", **env}
        return PiHarness(self.root, self.extension, "the system prompt", command=[sys.executable, str(self.script)],
                         environ=environ, credentials={"ANTHROPIC_API_KEY": "key"}, clock=FixedClock(), grace=2.0)

    def run_to_end(self, harness: PiHarness, variant: str = "", prompt: str = "work it") -> RunOutcome:
        run = harness.start(RunSpec("agent-1", self.work, 7, "ticket-7-a1", prompt, variant))
        assert isinstance(run, ProcessRun)
        self.runs.append(run)
        outcome = run.wait(WAIT)
        assert outcome is not None
        return outcome

    def invocations(self) -> list[dict[str, Any]]:
        return [json.loads(line) for line in (self.log / "invocations.jsonl").read_text().splitlines()]

    def received(self) -> list[dict[str, Any]]:
        return [json.loads(line) for line in (self.log / "received.jsonl").read_text().splitlines()]


class TestPi(PiCase):
    def test_operator_settings_reach_the_run_but_other_slop_haus_names_do_not(self) -> None:
        env = self.harness(SLOP_HAUS_TOKEN_IMPL="python", SLOP_HAUS_LABELS="gpu", SLOP_HAUS_SOMETHING="x").env()
        self.assertEqual("python", env["SLOP_HAUS_TOKEN_IMPL"])
        self.assertNotIn("SLOP_HAUS_SOMETHING", env)
        self.assertNotIn("SLOP_HAUS_LABELS", env)

    def test_flags_and_environment(self) -> None:
        outcome = self.run_to_end(self.harness(), "provider=anthropic,model=claude-test-1,thinking=high")
        self.assertEqual(OutcomeKind.FINISHED, outcome.kind)
        (call,) = self.invocations()
        argv = call["argv"]

        def value(flag: str) -> str:
            return argv[argv.index(flag) + 1]

        self.assertEqual("rpc", value("--mode"))
        self.assertEqual(",".join(PI_TOOLS + SLOP_HAUS_TOOLS), value("--tools"))
        self.assertIn("--no-extensions", argv)
        self.assertEqual(str(self.extension), value("-e"))
        self.assertIn("--no-approve", argv)
        self.assertEqual("the system prompt", value("--append-system-prompt"))
        self.assertEqual(("anthropic", "claude-test-1", "high"), (value("--provider"), value("--model"), value("--thinking")))
        env = call["env"]
        self.assertEqual(str(self.root / "harness" / "pi"), env["PI_CODING_AGENT_DIR"])
        self.assertEqual("1", env["SLOP_HAUS_HOST_SESSION"])
        self.assertNotIn("SLOP_HAUS_TOKEN", env)

    def test_a_specs_system_prompt_replaces_the_harness_default(self) -> None:
        plan = self.harness().plan(RunSpec("agent-1", self.work, 7, "b", "go", system_prompt="the slot's briefing"), "sid")
        self.assertEqual("the slot's briefing", plan.argv[plan.argv.index("--append-system-prompt") + 1])

    def test_a_variant_that_is_not_pis_is_refused_before_launch(self) -> None:
        for variant in ("thinking=turbo", "effort=high", "subagent_model=x"):
            with self.subTest(variant), self.assertRaises(InvalidVariant):
                self.harness().start(RunSpec("agent-1", self.work, 7, "b", "go", variant))
        self.assertFalse((self.log / "invocations.jsonl").exists())

    def test_usage_is_the_change_in_session_stats(self) -> None:
        harness = self.harness()
        first = self.run_to_end(harness)
        second = self.run_to_end(harness)
        for outcome in (first, second):
            (entry,) = outcome.usage.models
            self.assertEqual(("anthropic", "claude-test-1", "metered"), (entry.provider, entry.model, entry.billing_mode))
            self.assertEqual((100, 7, 10, 0), tuple(entry.counters[k] for k in ("input", "output", "cache_read", "cache_write")))

    def test_the_prompt_is_sent(self) -> None:
        self.run_to_end(self.harness(), prompt="do the thing")
        prompts = [c for c in self.received() if c["type"] == "prompt"]
        self.assertEqual(["do the thing"], [p["message"] for p in prompts])

    def test_resume_reuses_the_session_id(self) -> None:
        run = self.harness().resume(RunSpec("agent-1", self.work, 7, "b", "go"), "sess-1")
        assert isinstance(run, ProcessRun)
        self.runs.append(run)
        run.wait(WAIT)
        argv = self.invocations()[0]["argv"]
        self.assertEqual("sess-1", argv[argv.index("--session-id") + 1])

    def test_errors_are_classified(self) -> None:
        for mode, error, kind in [
            ("error", "529 overloaded", OutcomeKind.TRANSIENT),
            ("error", "You've hit your limit", OutcomeKind.USAGE_LIMIT),
            ("reject", "No model selected", OutcomeKind.AUTH_FAILED),
            ("error", "something nobody has seen", OutcomeKind.CRASHED),
        ]:
            with self.subTest(error):
                outcome = self.run_to_end(self.harness(mode, FAKE_ERROR=error))
                self.assertEqual(kind, outcome.kind)
                self.assertIn(error, outcome.detail)

    def test_a_handled_prompt_finishes_without_waiting_for_a_run(self) -> None:
        self.assertEqual(OutcomeKind.FINISHED, self.run_to_end(self.harness("handled")).kind)

    def test_an_exit_before_the_turn_ends_is_classified_by_stderr(self) -> None:
        self.assertEqual(OutcomeKind.AUTH_FAILED, self.run_to_end(self.harness("exit-at-start", FAKE_ERROR="No API key for provider x")).kind)
        self.assertEqual(OutcomeKind.CRASHED, self.run_to_end(self.harness("exit-at-start", FAKE_ERROR="segfault")).kind)

    def test_a_dialog_is_cancelled_so_the_turn_is_not_held(self) -> None:
        self.assertEqual(OutcomeKind.FINISHED, self.run_to_end(self.harness("ui")).kind)
        responses = [c for c in self.received() if c["type"] == "extension_ui_response"]
        self.assertEqual([{"type": "extension_ui_response", "id": "ui-1", "cancelled": True}], responses)


if __name__ == "__main__":
    unittest.main()
