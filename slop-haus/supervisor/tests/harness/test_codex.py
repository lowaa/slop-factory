from __future__ import annotations

import json
import os
import sys
import tempfile
import unittest
from pathlib import Path
from typing import Any

from fake_codex import FAKE_CODEX
from harness_fixtures import NOW
from slop_haus_supervisor.harness.mcp_config import TOOL_CALL_TIMEOUT_MS
from slop_haus_supervisor.harness.base import OutcomeKind, RunOutcome, RunSpec, SessionNotFound
from slop_haus_supervisor.harness.codex import CodexHarness
from slop_haus_supervisor.harness.process import ProcessRun
from slop_haus_supervisor.harness.variant import InvalidVariant
from slop_haus_supervisor.ports import Clock

WAIT = 30.0
THREAD = "0192f5a0-1111-7222-8333-444455556666"
OTHER = "0192f5a0-9999-7222-8333-444455556666"


class FixedClock(Clock):
    def now(self) -> float:
        return NOW


class CodexCase(unittest.TestCase):
    def setUp(self) -> None:
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.tmp = Path(tmp.name)
        self.root = self.tmp / "root"
        self.work = self.tmp / "work"
        self.work.mkdir()
        self.log = self.tmp / "log"
        self.log.mkdir()
        self.script = self.tmp / "fake_codex.py"
        self.script.write_text(FAKE_CODEX)
        self.runs: list[ProcessRun] = []
        self.addCleanup(lambda: [run.kill() for run in self.runs])

    def harness(self, mode: str = "", **env: str) -> CodexHarness:
        environ = {"PATH": os.environ["PATH"], "FAKE_DIR": str(self.log), "FAKE_MODE": mode, "KEEP": "yes",
                   "SLOP_HAUS_TOKEN": "must-not-reach-the-run", "OPENAI_API_KEY": "sk-env", **env}
        return CodexHarness(self.root, "https://s/", command=[sys.executable, str(self.script)], environ=environ,
                            credentials={"CODEX_API_KEY": "sk-secret", "GITHUB_TOKEN": "gh"}, clock=FixedClock(), grace=2.0, start_timeout=WAIT)

    def spec(self, variant: str = "", prompt: str = "work it", system_prompt: str = "the briefing") -> RunSpec:
        return RunSpec("agent-1", self.work, 7, "ticket-7-a1", prompt, variant, system_prompt)

    def finish(self, run: Any) -> RunOutcome:
        assert isinstance(run, ProcessRun)
        self.runs.append(run)
        outcome = run.wait(WAIT)
        assert outcome is not None
        return outcome

    def run_to_end(self, harness: CodexHarness, variant: str = "") -> RunOutcome:
        return self.finish(harness.start(self.spec(variant)))

    def received(self) -> list[dict[str, Any]]:
        return [json.loads(line) for line in (self.log / "received.jsonl").read_text().splitlines()]

    def answers(self) -> list[dict[str, Any]]:
        return [json.loads(line) for line in (self.log / "answers.jsonl").read_text().splitlines()]

    def invocation(self) -> dict[str, Any]:
        (call,) = [json.loads(line) for line in (self.log / "invocations.jsonl").read_text().splitlines()]
        return call


class TestCodexRun(CodexCase):
    def test_a_run_goes_initialize_thread_turn_and_finishes_on_turn_completed(self) -> None:
        run = self.harness().start(self.spec("model=gpt-test,effort=high"))
        self.assertEqual(THREAD, run.session_id)
        self.assertEqual(OutcomeKind.FINISHED, self.finish(run).kind)
        methods = [m.get("method") for m in self.received()]
        self.assertEqual(["initialize", "initialized", "thread/start", "turn/start"], methods)
        thread, turn = self.received()[2]["params"], self.received()[3]["params"]
        self.assertEqual({"approvalPolicy": "never", "sandbox": "danger-full-access", "cwd": str(self.work), "model": "gpt-test",
                          "developerInstructions": "the briefing"}, {k: v for k, v in thread.items() if k != "config"})
        self.assertEqual({"threadId": THREAD, "input": [{"type": "text", "text": "work it"}], "effort": "high"}, turn)

    def test_worker_policy_is_in_the_thread_config(self) -> None:
        self.run_to_end(self.harness(), "effort=low")
        config = self.received()[2]["params"]["config"]
        self.assertEqual(False, config["features.multi_agent"])
        self.assertEqual(False, config["features.goals"])
        self.assertEqual("disabled", config["web_search"])
        self.assertEqual(["CLAUDE.md"], config["project_doc_fallback_filenames"])
        self.assertEqual(["ticket_next"], config["mcp_servers.slop-haus.disabled_tools"])
        self.assertEqual("https://s/mcp", config["mcp_servers.slop-haus.url"])
        self.assertEqual(TOOL_CALL_TIMEOUT_MS // 1000, config["mcp_servers.slop-haus.tool_timeout_sec"])
        self.assertEqual("low", config["model_reasoning_effort"])

    def test_the_process_gets_its_own_home_and_no_api_key(self) -> None:
        self.run_to_end(self.harness())
        call = self.invocation()
        self.assertEqual(["app-server"], call["argv"])
        env = call["env"]
        self.assertEqual(str(self.root / "harness" / "codex"), env["CODEX_HOME"])
        self.assertEqual("1", env["SLOP_HAUS_HOST_SESSION"])
        self.assertEqual("yes", env["KEEP"])
        self.assertNotIn("OPENAI_API_KEY", env)
        self.assertNotIn("CODEX_API_KEY", env)
        self.assertNotIn("SLOP_HAUS_TOKEN", env)
        self.assertTrue((self.root / "harness" / "codex").is_dir())

    def test_a_failed_turn_is_classified_from_its_error(self) -> None:
        outcome = self.run_to_end(self.harness("fail", FAKE_ERROR="unexpected status 401 Unauthorized: Missing bearer or basic authentication"))
        self.assertEqual(OutcomeKind.AUTH_FAILED, outcome.kind)
        self.assertIn("401 Unauthorized", outcome.detail)
        self.assertEqual(OutcomeKind.TRANSIENT, self.run_to_end(self.harness("fail", FAKE_ERROR="503 service unavailable")).kind)
        self.assertEqual(OutcomeKind.CRASHED, self.run_to_end(self.harness("fail", FAKE_ERROR="the model said no")).kind)

    def test_an_interrupted_turn_is_not_a_finish(self) -> None:
        self.assertEqual(OutcomeKind.CRASHED, self.run_to_end(self.harness("interrupted", FAKE_ERROR="stop")).kind)

    def test_an_error_that_codex_retries_does_not_fail_the_turn(self) -> None:
        self.assertEqual(OutcomeKind.FINISHED, self.run_to_end(self.harness("retry-error")).kind)

    def test_a_thread_id_that_is_no_uuid_fails_the_run(self) -> None:
        outcome = self.run_to_end(self.harness("bad-thread-id"))
        self.assertEqual(OutcomeKind.CRASHED, outcome.kind)
        self.assertIn("thread id", outcome.detail)

    def test_a_process_that_exits_at_start_is_classified_from_stderr(self) -> None:
        outcome = self.run_to_end(self.harness("exit-at-start", FAKE_ERROR="401 Unauthorized"))
        self.assertEqual(OutcomeKind.AUTH_FAILED, outcome.kind)

    def test_every_server_request_is_declined_and_the_turn_still_ends(self) -> None:
        asks = "item/commandExecution/requestApproval,item/fileChange/requestApproval,item/tool/requestUserInput,mcpServer/elicitation/request,item/permissions/requestApproval,applyPatchApproval,execCommandApproval,account/chatgptAuthTokens/refresh,something/new"
        self.assertEqual(OutcomeKind.FINISHED, self.run_to_end(self.harness("ask", FAKE_ASKS=asks)).kind)
        answers = self.answers()
        self.assertEqual(list(range(100, 109)), [a["id"] for a in answers])
        self.assertEqual({"decision": "decline"}, answers[0]["result"])
        self.assertEqual({"decision": "decline"}, answers[1]["result"])
        self.assertEqual({"answers": {}}, answers[2]["result"])
        self.assertEqual({"action": "decline"}, answers[3]["result"])
        self.assertEqual({"permissions": {}, "scope": "turn"}, answers[4]["result"])
        self.assertEqual({"decision": "denied"}, answers[5]["result"])
        self.assertEqual({"decision": "denied"}, answers[6]["result"])
        # What the client cannot do is an error, not an acceptance.
        self.assertEqual(-32601, answers[7]["error"]["code"])
        self.assertEqual(-32601, answers[8]["error"]["code"])

    def test_provider_and_thinking_are_not_codex_settings(self) -> None:
        for variant in ("thinking=high", "provider=openai", "subagent_model=gpt-x"):
            with self.subTest(variant), self.assertRaises(InvalidVariant):
                self.harness().start(self.spec(variant))


def breakdown(input: int, output: int, cached: int = 0) -> dict[str, int]:
    return {"totalTokens": input + output, "inputTokens": input, "cachedInputTokens": cached, "cacheWriteInputTokens": 0,
            "outputTokens": output, "reasoningOutputTokens": 0}


class TestCodexUsage(CodexCase):
    def login(self, auth_mode: str) -> None:
        home = self.root / "harness" / "codex"
        home.mkdir(parents=True)
        (home / "auth.json").write_text(json.dumps({"auth_mode": auth_mode, "tokens": {"access_token": "x"}}))

    def usage_env(self, **usage: dict[str, int]) -> str:
        return json.dumps(usage)

    def test_a_fresh_thread_run_carries_its_usage(self) -> None:
        self.login("chatgpt")
        total = breakdown(1000, 50, cached=600)
        outcome = self.run_to_end(self.harness(FAKE_USAGE=self.usage_env(total=total, last=total)))
        (entry,) = outcome.usage.models
        self.assertEqual(("openai", "gpt-test-1", "subscription"), (entry.provider, entry.model, entry.billing_mode))
        self.assertEqual({"requests": 1, "input": 400, "output": 50, "cache_read": 600}, {k: v for k, v in entry.counters.items() if v})

    def test_a_run_resumed_after_a_replay_counts_only_its_own_turn(self) -> None:
        self.login("apikey")
        replay = breakdown(100, 10)
        env = self.usage_env(replay=replay, total=breakdown(220, 30), last=breakdown(120, 20))
        outcome = self.finish(self.harness(FAKE_USAGE=env).resume(self.spec(), OTHER))
        (entry,) = outcome.usage.models
        self.assertEqual("metered", entry.billing_mode)
        self.assertEqual((120, 20), (entry.counters["input"], entry.counters["output"]))

    def test_a_run_resumed_without_a_replay_takes_the_baseline_from_total_less_last(self) -> None:
        self.login("chatgpt")
        env = self.usage_env(total=breakdown(220, 30), last=breakdown(120, 20))
        outcome = self.finish(self.harness(FAKE_USAGE=env).resume(self.spec(), OTHER))
        (entry,) = outcome.usage.models
        self.assertEqual((120, 20), (entry.counters["input"], entry.counters["output"]))

    def test_the_worker_is_recorded_under_codexs_provider_and_ignores_another_threads_usage(self) -> None:
        self.login("chatgpt")
        total = breakdown(10, 5)
        env = self.usage_env(total=total, last=total, other_thread=breakdown(9999, 9999))
        (entry,) = self.run_to_end(self.harness(FAKE_USAGE=env)).usage.models
        self.assertEqual(("openai", 10), (entry.provider, entry.counters["input"]))

    def test_an_interrupted_turn_still_reports_what_it_spent(self) -> None:
        self.login("chatgpt")
        total = breakdown(10, 5)
        outcome = self.run_to_end(self.harness("interrupted", FAKE_ERROR="stop", FAKE_USAGE=self.usage_env(total=total, last=total)))
        self.assertEqual(OutcomeKind.CRASHED, outcome.kind)
        self.assertEqual(10, outcome.usage.models[0].counters["input"])

    def test_a_failed_turn_still_reports_what_it_spent(self) -> None:
        self.login("chatgpt")
        total = breakdown(10, 5)
        outcome = self.run_to_end(self.harness("fail", FAKE_ERROR="boom", FAKE_USAGE=self.usage_env(total=total, last=total)))
        self.assertEqual(10, outcome.usage.models[0].counters["input"])

    def test_no_usage_notification_is_no_usage(self) -> None:
        self.assertEqual((), self.run_to_end(self.harness()).usage.models)


class TestCodexResume(CodexCase):
    def test_resume_sends_thread_resume_with_the_id_and_the_briefing(self) -> None:
        run = self.harness().resume(self.spec(), OTHER)
        self.assertEqual(OTHER, run.session_id)
        self.assertEqual(OutcomeKind.FINISHED, self.finish(run).kind)
        resume = self.received()[2]
        self.assertEqual("thread/resume", resume["method"])
        self.assertEqual(OTHER, resume["params"]["threadId"])
        self.assertEqual("the briefing", resume["params"]["developerInstructions"])
        self.assertEqual(OTHER, self.received()[3]["params"]["threadId"])

    def test_no_rollout_is_session_not_found(self) -> None:
        with self.assertRaises(SessionNotFound):
            self.harness("resume-missing").resume(self.spec(), OTHER)

    def test_another_resume_error_is_a_failure_not_a_missing_session(self) -> None:
        run = self.harness("resume-other-error", FAKE_ERROR="-32600 invalid params").resume(self.spec(), OTHER)
        self.assertEqual(OutcomeKind.CRASHED, self.finish(run).kind)

    def test_an_id_that_is_no_uuid_is_never_sent(self) -> None:
        for session in ("", "my-thread-name", "../x"):
            with self.assertRaises(SessionNotFound):
                self.harness().resume(self.spec(), session)
        self.assertFalse((self.log / "received.jsonl").exists())


class TestCodexLogin(CodexCase):
    def write_auth(self, text: str) -> None:
        home = self.root / "harness" / "codex"
        home.mkdir(parents=True, exist_ok=True)
        (home / "auth.json").write_text(text)

    def test_a_chatgpt_login_is_what_counts(self) -> None:
        harness = self.harness()
        self.assertIn("harness login codex", harness.missing_credentials())
        self.write_auth(json.dumps({"auth_mode": "apikey", "OPENAI_API_KEY": "sk-x"}))
        self.assertIn("ChatGPT", harness.missing_credentials())
        self.write_auth("not json")
        self.assertIn("ChatGPT", harness.missing_credentials())
        self.write_auth(json.dumps({"auth_mode": "chatgpt", "tokens": {"access_token": "a", "refresh_token": "r"}}))
        self.assertEqual("", harness.missing_credentials())
