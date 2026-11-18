from __future__ import annotations

import json
import os
import sys
import tempfile
import unittest
from dataclasses import replace
from pathlib import Path
from typing import Any

from fake_claude import FAKE_CLAUDE
from harness_fixtures import NO_CONVERSATION, NOW
from slop_haus_supervisor.harness.base import OutcomeKind, RunOutcome, RunSpec, SessionNotFound
from slop_haus_supervisor.harness.claude import ClaudeHarness, ProcessRun
from slop_haus_supervisor.harness.variant import InvalidVariant, parse_variant
from slop_haus_supervisor.ports import Clock

SERVER = "https://slop-haus.example"
WAIT = 30.0


class FixedClock(Clock):
    def now(self) -> float:
        return NOW


def process_running(pid: int) -> bool:
    try:
        with open(f"/proc/{pid}/stat", encoding="utf-8") as f:
            return f.read().rsplit(")", 1)[1].split()[0] != "Z"
    except OSError:
        return False


class ClaudeCase(unittest.TestCase):
    def setUp(self) -> None:
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.tmp = Path(tmp.name)
        self.root = self.tmp / "root"
        self.work = self.tmp / "work"
        self.work.mkdir()
        self.log = self.tmp / "log"
        self.log.mkdir()
        self.script = self.tmp / "fake_claude.py"
        self.script.write_text(FAKE_CLAUDE)
        self.runs: list[ProcessRun] = []
        self.addCleanup(self.stop_runs)

    def stop_runs(self) -> None:
        for run in self.runs:
            run.kill()

    def harness(self, mode: str = "", credentials: dict[str, str] | None = None, **env: str) -> ClaudeHarness:
        environ = {
            "PATH": os.environ["PATH"], "FAKE_DIR": str(self.log), "FAKE_MODE": mode,
            "SLOP_HAUS_TOKEN": "must-not-reach-the-run", "KEEP": "yes", **env,
        }
        return ClaudeHarness(
            self.root, SERVER, "the system prompt", command=[sys.executable, str(self.script)],
            denied_tools=["mcp__slop-haus__ticket_next"], environ=environ,
            credentials=credentials or {"CLAUDE_CODE_OAUTH_TOKEN": "tok"}, clock=FixedClock(), grace=2.0, start_timeout=WAIT, drain_seconds=0.2,
        )

    def spec(self, prompt: str = "work it", variant: str = "") -> RunSpec:
        return RunSpec("agent-1", self.work, 7, "ticket-7-a1", prompt, variant)

    def run_to_end(self, harness: ClaudeHarness, spec: RunSpec | None = None) -> RunOutcome:
        run = harness.start(spec or self.spec())
        assert isinstance(run, ProcessRun)
        self.runs.append(run)
        outcome = run.wait(WAIT)
        assert outcome is not None
        return outcome

    def invocations(self) -> list[dict[str, Any]]:
        return [json.loads(line) for line in (self.log / "invocations.jsonl").read_text().splitlines()]


class TestInvocation(ClaudeCase):
    def test_a_credential_value_in_the_harnesss_stderr_never_reaches_the_outcome(self) -> None:
        harness = self.harness(
            "stderr-only", credentials={"WORKER_KEY": "hunter2-value"}, FAKE_ERROR="auth failed for hunter2-value today"
        )

        outcome = self.run_to_end(harness)

        self.assertIn("auth failed for *** today", outcome.detail)
        self.assertNotIn("hunter2-value", outcome.detail)

    def test_a_credential_value_in_a_result_error_never_reaches_the_outcome(self) -> None:
        harness = self.harness(credentials={"WORKER_KEY": "hunter2-value"}, FAKE_ERROR="denied hunter2-value")

        outcome = self.run_to_end(harness)

        self.assertEqual("denied ***", outcome.detail)

    def test_operator_settings_reach_the_run_but_other_slop_haus_names_do_not(self) -> None:
        env = self.harness(SLOP_HAUS_TOKEN_IMPL="python", SLOP_HAUS_LABELS="gpu", SLOP_HAUS_SOMETHING="x").env(parse_variant(""))
        self.assertEqual("python", env["SLOP_HAUS_TOKEN_IMPL"])
        self.assertNotIn("SLOP_HAUS_SOMETHING", env)
        self.assertNotIn("SLOP_HAUS_LABELS", env)

    def test_flags_and_environment(self) -> None:
        outcome = self.run_to_end(self.harness(), self.spec(variant="model=claude-opus-4-7,effort=high,subagent_model=claude-haiku-4-5"))
        self.assertEqual(OutcomeKind.FINISHED, outcome.kind)
        (call,) = self.invocations()
        argv = call["argv"]

        def value(flag: str) -> str:
            return argv[argv.index(flag) + 1]

        for flag in ("-p", "--verbose", "--include-partial-messages", "--strict-mcp-config"):
            self.assertIn(flag, argv)
        self.assertEqual("stream-json", value("--input-format"))
        self.assertEqual("stream-json", value("--output-format"))
        self.assertEqual("bypassPermissions", value("--permission-mode"))
        self.assertEqual("the system prompt", value("--append-system-prompt"))
        self.assertNotIn("--resume", argv)
        self.assertEqual(self.work.as_posix(), Path(call["cwd"]).as_posix())
        denied = value("--disallowedTools").split(",")
        self.assertIn("Read(//**/.slop-haus/**)", denied)
        self.assertIn("Read(//**/.slop-haus.env)", denied)
        self.assertIn(f"Read(/{self.root}/secrets.env)", denied)
        self.assertIn(f"Read(/{self.root}/harness/**)", denied)
        self.assertIn("mcp__slop-haus__ticket_next", denied)
        env = call["env"]
        self.assertEqual("1", env["SLOP_HAUS_HOST_SESSION"])
        self.assertNotIn("SLOP_HAUS_TOKEN", env)
        self.assertEqual("1", env["CLAUDE_CODE_DISABLE_AUTO_MEMORY"])
        self.assertEqual("1", env["CLAUDE_CODE_DISABLE_BACKGROUND_TASKS"])
        self.assertEqual(str(self.root / "harness" / "claude"), env["CLAUDE_CONFIG_DIR"])
        self.assertEqual("tok", env["CLAUDE_CODE_OAUTH_TOKEN"])
        self.assertEqual("claude-opus-4-7", env["ANTHROPIC_MODEL"])
        self.assertEqual("high", env["CLAUDE_CODE_EFFORT_LEVEL"])
        self.assertEqual("claude-haiku-4-5", env["CLAUDE_CODE_SUBAGENT_MODEL"])
        self.assertEqual("1", env["CLAUDE_CODE_SUBAGENT_MODEL_FORCE"])

    def write_root_settings(self) -> Path:
        settings = self.work / ".claude" / "settings.json"
        settings.parent.mkdir(parents=True)
        settings.write_text("{}")
        return settings

    def test_a_run_below_the_workspace_is_given_the_workspaces_settings(self) -> None:
        settings = self.write_root_settings()
        repo = self.work / "repo"
        repo.mkdir()

        self.run_to_end(self.harness(), replace(self.spec(), directory=repo, workspace=self.work))

        (call,) = self.invocations()
        self.assertEqual(str(settings), call["argv"][call["argv"].index("--settings") + 1])

    def test_a_run_at_the_workspace_root_loads_its_settings_as_the_projects(self) -> None:
        self.write_root_settings()

        self.run_to_end(self.harness(), replace(self.spec(), workspace=self.work))

        (call,) = self.invocations()
        self.assertNotIn("--settings", call["argv"])

    def test_the_mcp_config_signs_in_with_the_workspaces_token_script(self) -> None:
        repo = self.work / "repo"
        repo.mkdir()

        self.run_to_end(self.harness(), replace(self.spec(), directory=repo, workspace=self.work))

        (call,) = self.invocations()
        config = json.loads(Path(call["argv"][call["argv"].index("--mcp-config") + 1]).read_text())
        self.assertIn(str(self.work / ".slop-haus" / "token.sh"), json.dumps(config))

    def test_a_workspace_without_settings_passes_none(self) -> None:
        repo = self.work / "repo"
        repo.mkdir()

        self.run_to_end(self.harness(), replace(self.spec(), directory=repo, workspace=self.work))

        (call,) = self.invocations()
        self.assertNotIn("--settings", call["argv"])

    def test_a_specs_system_prompt_replaces_the_harness_default(self) -> None:
        self.run_to_end(self.harness(), replace(self.spec(), system_prompt="the slot's briefing"))
        (call,) = self.invocations()
        argv = call["argv"]
        self.assertEqual("the slot's briefing", argv[argv.index("--append-system-prompt") + 1])

    def test_env_raises_the_bash_timeouts(self) -> None:
        env = self.harness().env(parse_variant(""))
        self.assertEqual("1800000", env["BASH_DEFAULT_TIMEOUT_MS"])
        self.assertEqual("3600000", env["BASH_MAX_TIMEOUT_MS"])

    def test_mcp_config_names_only_slop_haus(self) -> None:
        self.run_to_end(self.harness())
        (call,) = self.invocations()
        config = json.loads(Path(call["argv"][call["argv"].index("--mcp-config") + 1]).read_text())
        (name,) = config["mcpServers"]
        self.assertEqual("slop-haus", name)
        entry = config["mcpServers"][name]
        self.assertEqual(SERVER + "/mcp", entry["url"])
        self.assertEqual(f"{self.work}/.slop-haus/token.sh headers -server={SERVER}", entry["headersHelper"])

    def test_the_prompt_is_the_first_message(self) -> None:
        self.run_to_end(self.harness(), self.spec("do the thing"))
        self.assertEqual([{"text": "do the thing"}], [json.loads(x) for x in (self.log / "received.jsonl").read_text().splitlines()])

    def test_resume_passes_the_session(self) -> None:
        harness = self.harness()
        run = harness.resume(self.spec(), "33333333-3333-4333-8333-333333333333")
        assert isinstance(run, ProcessRun)
        self.runs.append(run)
        self.assertEqual(OutcomeKind.FINISHED, (run.wait(WAIT) or RunOutcome(OutcomeKind.CRASHED)).kind)
        argv = self.invocations()[0]["argv"]
        self.assertEqual("33333333-3333-4333-8333-333333333333", argv[argv.index("--resume") + 1])
        self.assertNotIn("--session-id", argv)
        self.assertEqual("33333333-3333-4333-8333-333333333333", run.session_id)

    def test_start_makes_a_new_session_id_each_time(self) -> None:
        harness = self.harness()
        first, second = harness.start(self.spec()), harness.start(self.spec())
        self.assertNotEqual(first.session_id, second.session_id)
        for run in (first, second):
            assert isinstance(run, ProcessRun)
            self.runs.append(run)

    def test_a_variant_that_would_be_a_flag_or_is_not_claudes_is_refused_before_launch(self) -> None:
        for variant in ("model=--dangerously-skip", "effort=turbo", "thinking=high", "provider=anthropic"):
            with self.subTest(variant), self.assertRaises(InvalidVariant):
                self.harness().start(self.spec(variant=variant))
        self.assertFalse((self.log / "invocations.jsonl").exists())


class TestOutcomes(ClaudeCase):
    def test_finished_with_usage_per_model(self) -> None:
        outcome = self.run_to_end(self.harness())
        self.assertEqual(OutcomeKind.FINISHED, outcome.kind)
        by_model = {u.model: u for u in outcome.usage.models}
        self.assertEqual(["claude-test-1", "claude-test-2"], sorted(by_model))
        first, second = by_model["claude-test-1"], by_model["claude-test-2"]
        self.assertEqual(("anthropic", "subscription"), (first.provider, first.billing_mode))
        self.assertEqual((100, 7, 10, 3, 2, 10000), tuple(first.counters[k] for k in ("input", "output", "cache_read", "cache_write", "requests", "cost_micros")))
        self.assertEqual((20, 2, 0, 1, 0), tuple(second.counters[k] for k in ("input", "output", "cache_read", "cache_write", "requests")))

    def test_an_api_key_run_is_metered(self) -> None:
        outcome = self.run_to_end(self.harness(FAKE_API_KEY_SOURCE="ANTHROPIC_API_KEY"))
        self.assertEqual({"metered"}, {u.billing_mode for u in outcome.usage.models})

    def test_result_text_is_classified(self) -> None:
        for error, kind in [
            ("Claude AI usage limit reached|1760900000", OutcomeKind.USAGE_LIMIT),
            ("Not logged in · Please run /login", OutcomeKind.AUTH_FAILED),
            ("API Error: 529 overloaded_error", OutcomeKind.TRANSIENT),
            ("something nobody has seen before", OutcomeKind.CRASHED),
        ]:
            with self.subTest(error):
                outcome = self.run_to_end(self.harness(FAKE_ERROR=error))
                self.assertEqual(kind, outcome.kind)
                self.assertIn(error, outcome.detail)
                self.assertEqual(5, outcome.usage.models[0].counters["input"])

    def test_a_run_stopped_by_max_turns_is_over_budget_whether_or_not_it_is_flagged_an_error(self) -> None:
        for is_error in ("0", "1"):
            with self.subTest(is_error=is_error):
                outcome = self.run_to_end(self.harness("max-turns", FAKE_IS_ERROR=is_error))
                self.assertEqual(OutcomeKind.OVER_BUDGET, outcome.kind)
                self.assertIn("--max-turns", outcome.detail)

    def test_usage_limit_reset_from_the_text(self) -> None:
        outcome = self.run_to_end(self.harness(FAKE_ERROR="Claude AI usage limit reached|1760900000"))
        self.assertEqual(1_760_900_000.0, outcome.reset_at)

    def test_a_rejected_rate_limit_event_gives_the_reset(self) -> None:
        reset = int(NOW) + 3600
        outcome = self.run_to_end(self.harness("limit-event", FAKE_RESET=str(reset), FAKE_ERROR="blocked"))
        self.assertEqual(OutcomeKind.USAGE_LIMIT, outcome.kind)
        self.assertEqual(float(reset), outcome.reset_at)

    def test_a_reset_already_past_is_not_reported(self) -> None:
        outcome = self.run_to_end(self.harness("limit-event", FAKE_RESET=str(int(NOW) - 60), FAKE_ERROR="blocked"))
        self.assertEqual(OutcomeKind.USAGE_LIMIT, outcome.kind)
        self.assertIsNone(outcome.reset_at)

    def test_exit_without_a_result_is_a_crash_with_stderr_cleaned(self) -> None:
        outcome = self.run_to_end(self.harness("crash"))
        self.assertEqual(OutcomeKind.CRASHED, outcome.kind)
        self.assertIn("status 3", outcome.detail)
        self.assertIn("boom", outcome.detail)
        self.assertNotIn("secret", outcome.detail)

    def test_stderr_written_after_stdout_closed_is_still_read(self) -> None:
        outcome = self.run_to_end(self.harness("late-stderr", FAKE_ERROR="Invalid API key \u00b7 Please run /login"))
        self.assertEqual(OutcomeKind.AUTH_FAILED, outcome.kind)

    def test_a_command_holding_stdout_open_does_not_hold_the_run(self) -> None:
        outcome = self.run_to_end(self.harness("exit-leaving-child"))
        self.assertEqual(OutcomeKind.CRASHED, outcome.kind)
        self.assertIn("status 3", outcome.detail)

    def test_exit_without_a_result_is_still_classified_by_stderr(self) -> None:
        outcome = self.run_to_end(self.harness("stderr-only", FAKE_ERROR="Invalid API key · Please run /login"))
        self.assertEqual(OutcomeKind.AUTH_FAILED, outcome.kind)

    def test_kill_ends_the_run_and_everything_it_started(self) -> None:
        run = self.harness("hang").start(self.spec())
        assert isinstance(run, ProcessRun)
        self.runs.append(run)
        self.wait_for_file("hanging")
        self.assertIsNone(run.wait(0.01))
        child = int((self.log / "child.pid").read_text())
        self.assertTrue(process_running(child))
        run.kill()
        outcome = run.wait(WAIT)
        assert outcome is not None
        self.assertEqual(OutcomeKind.KILLED, outcome.kind)
        self.wait_for_exit(child)

    def test_a_finished_run_leaves_nothing_behind(self) -> None:
        outcome = self.run_to_end(self.harness("finish-with-child"))
        self.assertEqual(OutcomeKind.FINISHED, outcome.kind)
        self.wait_for_exit(int((self.log / "child.pid").read_text()))

    def wait_for_exit(self, pid: int) -> None:
        # Signal delivery and reaping are asynchronous, so the child can outlive the run's outcome briefly.
        import time

        deadline = time.monotonic() + WAIT
        while process_running(pid):
            self.assertLess(time.monotonic(), deadline, f"process {pid} still running")
            time.sleep(0.01)

    def wait_for_file(self, name: str) -> None:
        import time

        deadline = time.monotonic() + WAIT
        while not (self.log / name).exists():
            self.assertLess(time.monotonic(), deadline, f"{name} never appeared")
            time.sleep(0.01)


class TestResume(ClaudeCase):
    def test_no_conversation_found_is_session_not_found(self) -> None:
        harness = self.harness("no-conversation", FAKE_ERROR=NO_CONVERSATION)
        with self.assertRaises(SessionNotFound):
            harness.resume(self.spec(), "11111111-1111-4111-8111-111111111111")

    def test_the_refusal_is_read_from_the_result_when_stderr_says_nothing(self) -> None:
        harness = self.harness("no-conversation", FAKE_ERROR=NO_CONVERSATION, FAKE_QUIET_STDERR="1")
        with self.assertRaises(SessionNotFound):
            harness.resume(self.spec(), "11111111-1111-4111-8111-111111111111")

    def test_session_already_in_use_is_session_not_found(self) -> None:
        harness = self.harness("session-in-use")
        with self.assertRaises(SessionNotFound):
            harness.resume(self.spec(), "11111111-1111-4111-8111-111111111111")

    def test_a_resume_that_fails_otherwise_is_a_run_not_a_missing_session(self) -> None:
        run = self.harness(FAKE_ERROR="API Error: 529 overloaded_error").resume(self.spec(), "11111111-1111-4111-8111-111111111111")
        assert isinstance(run, ProcessRun)
        self.runs.append(run)
        outcome = run.wait(WAIT)
        assert outcome is not None
        self.assertEqual(OutcomeKind.TRANSIENT, outcome.kind)


if __name__ == "__main__":
    unittest.main()
