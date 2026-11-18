"""What the supervisor's Codex code depends on in the real `codex app-server`, against the scripted Responses API.

Each test is one assumption of the Codex harness that a release of Codex could break: the usage numbers
are thread-cumulative (so the supervisor needs a baseline), a turn can be interrupted while the model is still
answering, a thread with no rollout is refused with the words the supervisor matches, a header helper is run again
when a call is answered 401, and the turn's effort reaches the model as `reasoning.effort`. The client is the
test's own (support/codex_app_server.py), so a failure is about Codex and not about the supervisor."""

from __future__ import annotations

import os
import stat
import subprocess
import unittest
import uuid

from scripted_model.scenario import Scenario, ToolCall, Turn, Usage
from support.codex_app_server import AppServer
from support.harness_case import MODEL, HarnessCase
from support.header_mcp import ANSWER, TOOL, HeaderMcp
from support.waiting import wait_for

# thread/resume's error for a thread with no rollout file (harness/codex_protocol.py NO_ROLLOUT).
NO_ROLLOUT = "no rollout found for thread id"


@unittest.skipUnless(os.environ.get("HARNESS") == "codex", "the Codex app-server protocol")
class CodexAppServer(HarnessCase):
    def setUp(self) -> None:
        super().setUp()
        self.home = self.tmp / "home"
        self.codex_home = self.home / ".codex"
        self.work = self.tmp / "work"
        self.work.mkdir()
        self.adapter.prepare(self.home, self.work, self.stack.model_server)
        self.servers = 0

    def app_server(self, args: list[str] | None = None) -> AppServer:
        self.servers += 1
        env = {
            **{k: v for k, v in os.environ.items() if k in ("PATH", "LANG", "LC_ALL")},
            "HOME": str(self.home),
            **self.adapter.env(self.stack.model_server),
        }
        server = AppServer(self.codex_home, self.work, env, self.logs / f"app-server-{self.servers}.log", args)
        self.addCleanup(server.close)
        return server

    def thread_id(self, answer: dict) -> str:
        self.assertNotIn("error", answer, f"the thread was refused: {answer}")
        return answer["result"]["thread"]["id"]

    def test_the_image_has_the_pinned_version(self) -> None:
        version = subprocess.run(["codex", "--version"], capture_output=True, text=True, check=True).stdout
        if self.stack.harness_version != "latest":
            self.assertIn(self.stack.harness_version, version, "the pin did not take")

    def test_usage_is_thread_cumulative_and_a_resume_replays_where_the_thread_stood(self) -> None:
        self.model.load(
            Scenario(
                "two turns on one thread",
                (Turn(text="one", usage=Usage(100, 10)), Turn(text="two", usage=Usage(120, 12))),
            )
        )
        first = self.app_server()
        thread = self.thread_id(first.start_thread(self.work, MODEL))
        first.start_turn(thread, "go", effort=self.adapter.thinking)
        first.wait_turn_completed(thread)
        started = [u["tokenUsage"] for u in first.notifications("thread/tokenUsage/updated")]
        self.assertEqual([(u["total"]["inputTokens"], u["last"]["inputTokens"]) for u in started], [(100, 100)])
        first.close()

        # The supervisor starts a process per run, and the thread is kept in CODEX_HOME.
        second = self.app_server()
        self.assertEqual(self.thread_id(second.start_thread(self.work, MODEL, resume_id=thread)), thread)
        second.start_turn(thread, "again")
        second.wait_turn_completed(thread)
        records = second.records
        updates = [(n, r["params"]["tokenUsage"]) for n, r in enumerate(records) if r.get("method") == "thread/tokenUsage/updated"]
        turn_started = next(n for n, r in enumerate(records) if r.get("method") == "turn/started")

        # Before the new turn starts, Codex replays where the thread stood: the previous turn's usage under the previous
        # turn's id. Taking total - last of the first notification as the run's baseline would be 0 here, and the run
        # would be billed for the whole thread.
        replay_at, replay = updates[0]
        self.assertLess(replay_at, turn_started, "no replay of the thread's usage before the resumed turn started")
        self.assertEqual((replay["total"]["inputTokens"], replay["last"]["inputTokens"]), (100, 100))
        final = updates[-1][1]
        self.assertEqual(final["total"]["inputTokens"], 220, "the total is not the thread's, across the resume")
        self.assertEqual(final["last"]["inputTokens"], 120)
        self.assertEqual(final["total"]["outputTokens"] - replay["total"]["outputTokens"], 12)

        seen = self.model.conversation()
        self.assertEqual(self.adapter.thinking_in(seen[0].raw), self.adapter.thinking, "turn/start's effort did not reach the request")
        self.assertEqual(self.model.errors(), [])

    def test_resuming_a_thread_with_no_rollout_says_so(self) -> None:
        answer = self.app_server().start_thread(self.work, MODEL, resume_id=str(uuid.uuid4()))
        self.assertIn("error", answer, f"Codex resumed a thread that does not exist: {answer}")
        self.assertIn(NO_ROLLOUT, answer["error"]["message"])

    def test_a_turn_can_be_interrupted_while_the_model_is_answering(self) -> None:
        self.model.load(Scenario("held", (Turn(text="late", gate="hold", usage=Usage(10, 1)),)))
        server = self.app_server()
        thread = self.thread_id(server.start_thread(self.work, MODEL))
        turn = server.start_turn(thread, "go")["result"]["turn"]["id"]
        wait_for("the model to be asked", lambda: self.model.conversation(), diagnose=lambda: f"model errors: {self.model.errors()}")
        answer = server.request("turn/interrupt", {"threadId": thread, "turnId": turn})
        self.assertNotIn("error", answer, f"the interrupt was refused: {answer}")
        done = server.wait_turn_completed(thread)
        self.assertEqual(done["turn"]["status"], "interrupted")
        self.model.open_gate("hold")

    def test_the_header_helper_runs_again_when_a_call_is_answered_401(self) -> None:
        mcp = HeaderMcp(expired={"tok-1"})
        self.addCleanup(mcp.close)
        helper = self.tmp / "headers.sh"
        helper.write_text(
            '#!/bin/sh\nc="$(dirname "$0")/count"\nn=$(($(cat "$c" 2>/dev/null || echo 0) + 1))\necho $n > "$c"\n'
            'printf \'{"Authorization":"Bearer tok-%s"}\' "$n"\n'
        )
        helper.chmod(helper.stat().st_mode | stat.S_IXUSR)
        self.model.load(Scenario("a call after expiry", (Turn(tool_calls=(ToolCall(TOOL),)), Turn(text="done"))))
        server = self.app_server()
        config = {"mcp_servers.header.url": mcp.url, "mcp_servers.header.http_headers_helper": str(helper)}
        thread = self.thread_id(server.start_thread(self.work, MODEL, config))
        server.start_turn(thread, "go")
        done = server.wait_turn_completed(thread)
        self.assertEqual(done["turn"]["status"], "completed", f"the turn did not complete: {done}")

        calls = [(s.authorization, s.status) for s in mcp.seen if s.method == "tools/call"]
        self.assertEqual(calls, [("Bearer tok-1", 401), ("Bearer tok-2", 200)], "the helper was not run again for the call that got a 401")
        self.assertTrue(all(s.authorization for s in mcp.seen), "a request went out without the helper's header")
        seen = self.model.conversation()
        self.assertIn(TOOL, seen[0].tool_names)
        self.assertTrue(any(ANSWER in result for result in seen[1].tool_results), "the call's result did not reach the model")
        self.assertEqual(self.model.errors(), [])


if __name__ == "__main__":
    unittest.main()
