from __future__ import annotations

import json
import os
import sys
import tempfile
import time
import unittest
from unittest import mock
from pathlib import Path

from process_state import process_running as alive
from slop_haus_supervisor.harness.base import RunSpec
from slop_haus_supervisor.harness.claude import ClaudeHarness
from slop_haus_supervisor.harness.pi import PiHarness
from slop_haus_supervisor.harness.process import HarnessSession
from slop_haus_supervisor.harness.variant import InvalidVariant

# Answers every message with a partial-text record and a result, and leaves a child running.
MULTI_TURN = r'''
import json, os, subprocess, sys
child = subprocess.Popen(["sleep", "60"], start_new_session=True)
open(os.path.join(os.environ["FAKE_DIR"], "child.pid"), "w").write(str(child.pid))
open(os.path.join(os.environ["FAKE_DIR"], "argv.json"), "w").write(json.dumps(sys.argv[1:]))
print(json.dumps({"type": "system", "subtype": "init"}), flush=True)
for n, line in enumerate(sys.stdin, 1):
    text = json.loads(line)["message"]["content"][0]["text"]
    print(json.dumps({"type": "stream_event", "n": n, "text": text}), flush=True)
    print(json.dumps({"type": "result", "n": n, "is_error": False}), flush=True)
'''
WAIT = 30.0


class SessionCase(unittest.TestCase):
    def setUp(self) -> None:
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.tmp = Path(tmp.name)
        self.work = self.tmp / "work"
        self.work.mkdir()
        self.log = self.tmp / "log"
        self.log.mkdir()
        self.script = self.tmp / "multi_turn.py"
        self.script.write_text(MULTI_TURN)
        self.sessions: list[HarnessSession] = []
        self.addCleanup(lambda: [s.close() for s in self.sessions])

    def claude(self) -> ClaudeHarness:
        environ = {"PATH": os.environ["PATH"], "FAKE_DIR": str(self.log), "SLOP_HAUS_TOKEN": "secret", "KEEP": "yes"}
        return ClaudeHarness(self.tmp / "root", "https://slop-haus.example", "sys", command=[sys.executable, str(self.script)],
                             environ=environ, credentials={"CLAUDE_CODE_OAUTH_TOKEN": "tok"}, grace=2.0)

    def spec(self, variant: str = "") -> RunSpec:
        return RunSpec("agent-1", self.work, 7, "ticket-7-a1", "unused", variant)

    def next_record(self, session: HarnessSession) -> dict[str, object]:
        record = session.next_record()
        assert record is not None
        return record


class TestClaudeSession(SessionCase):
    def test_turns_are_sent_and_records_come_back_in_order(self) -> None:
        session = self.claude().open_session(self.spec())
        self.sessions.append(session)
        self.assertEqual("init", self.next_record(session)["subtype"])
        session.send("first")
        session.send("second")
        got = [self.next_record(session) for _ in range(4)]
        self.assertEqual([("stream_event", 1), ("result", 1), ("stream_event", 2), ("result", 2)],
                         [(r["type"], r["n"]) for r in got])
        self.assertEqual(["first", "second"], [r["text"] for r in got if r["type"] == "stream_event"])

    def test_close_kills_what_the_harness_left_running_and_is_repeatable(self) -> None:
        session = self.claude().open_session(self.spec())
        self.sessions.append(session)
        self.next_record(session)
        pid = int((self.log / "child.pid").read_text())
        self.assertTrue(alive(pid))
        session.close()
        session.close()
        deadline = time.monotonic() + WAIT
        while alive(pid) and time.monotonic() < deadline:
            time.sleep(0.05)
        self.assertFalse(alive(pid))
        self.assertIsNone(session.next_record())
        with self.assertRaises(RuntimeError):
            session.send("late")

    def test_a_protocol_line_is_written_as_it_is_and_not_as_a_user_message(self) -> None:
        session = self.claude().open_session(self.spec())
        self.sessions.append(session)
        self.next_record(session)
        session.send_line(json.dumps({"message": {"content": [{"text": "raw"}]}}))
        self.assertEqual("raw", self.next_record(session)["text"])
        session.close()
        with self.assertRaises(RuntimeError):
            session.send_line("{}")

    def test_the_session_id_is_the_one_given_or_a_fresh_one(self) -> None:
        given = self.claude().open_session(self.spec(), "11111111-1111-4111-8111-111111111111")
        fresh = self.claude().open_session(self.spec())
        self.sessions += [given, fresh]
        self.assertEqual("11111111-1111-4111-8111-111111111111", given.session_id)
        self.assertNotEqual(given.session_id, fresh.session_id)

    def test_resuming_launches_with_resume_and_the_given_id(self) -> None:
        session = self.claude().open_session(self.spec(), "sid-9", resume=True)
        self.sessions.append(session)
        self.next_record(session)
        argv = json.loads((self.log / "argv.json").read_text())
        self.assertEqual("sid-9", argv[argv.index("--resume") + 1])
        self.assertNotIn("--session-id", argv)

    def test_exit_code_and_stderr_are_reported(self) -> None:
        self.script.write_text("import sys; sys.stderr.write('boom\\n'); sys.exit(3)")
        session = self.claude().open_session(self.spec(), "sid-8")
        self.sessions.append(session)
        self.assertIsNone(session.next_record())
        self.assertEqual(3, session.exit_code())
        self.assertEqual("boom", session.stderr_tail())

    def test_resume_needs_a_session_id(self) -> None:
        with self.assertRaises(ValueError):
            self.claude().open_session(self.spec(), resume=True)


class TestPlans(SessionCase):
    def test_claude_plan_is_what_a_run_launches(self) -> None:
        plan = self.claude().plan(self.spec("model=claude-opus-4-7,effort=high"), "sid-1", resume=True)
        self.assertEqual(self.work, plan.cwd)
        self.assertEqual("sid-1", plan.argv[plan.argv.index("--resume") + 1])
        self.assertIn("--strict-mcp-config", plan.argv)
        self.assertTrue(any(a.startswith("Read(//**/.slop-haus/**)") for a in plan.argv[plan.argv.index("--disallowedTools") + 1].split(",")))
        self.assertEqual("claude-opus-4-7", plan.env["ANTHROPIC_MODEL"])
        self.assertEqual("tok", plan.env["CLAUDE_CODE_OAUTH_TOKEN"])
        self.assertNotIn("SLOP_HAUS_TOKEN", plan.env)
        mcp = json.loads(Path(plan.argv[plan.argv.index("--mcp-config") + 1]).read_text())
        self.assertEqual(["slop-haus"], list(mcp["mcpServers"]))

    def test_claude_plan_refuses_a_pi_variant_and_an_unsafe_agent_name(self) -> None:
        with self.assertRaises(InvalidVariant):
            self.claude().plan(self.spec("provider=acme"), "sid", resume=False)
        with self.assertRaises(ValueError):
            self.claude().plan(RunSpec("../x", self.work, 7, "b", "p"), "sid", resume=False)

    def test_pi_plan_and_session(self) -> None:
        pi = PiHarness(self.tmp / "root", self.tmp / "ext.ts", "sys", command=[sys.executable, "-c", "import sys; print(sys.stdin.readline().strip(), flush=True)  # echoes the encoded prompt", "--"],
                       environ={"PATH": os.environ["PATH"], "SLOP_HAUS_TOKEN": "secret"}, credentials={"ANTHROPIC_API_KEY": "key"}, grace=2.0)
        plan = pi.plan(self.spec("provider=acme,model=m"), "sid-2")
        self.assertEqual("acme", plan.argv[plan.argv.index("--provider") + 1])
        self.assertEqual("sid-2", plan.argv[plan.argv.index("--session-id") + 1])
        self.assertEqual("key", plan.env["ANTHROPIC_API_KEY"])
        self.assertNotIn("SLOP_HAUS_TOKEN", plan.env)
        with self.assertRaises(InvalidVariant):
            pi.plan(self.spec("effort=high"), "sid-2")
        session = pi.open_session(self.spec(), "sid-2")
        self.sessions.append(session)
        session.send("hello")
        self.assertEqual({"type": "prompt", "message": "hello"}, self.next_record(session))


# Logs each line it is sent; answers a control_request as Claude Code does, then ends the turn.
INTERRUPTIBLE = r'''
import json, os, sys
for line in sys.stdin:
    message = json.loads(line)
    with open(os.path.join(os.environ["FAKE_DIR"], "received.jsonl"), "a") as f:
        f.write(line)
    if message.get("type") == "control_request":
        print(json.dumps({"type": "control_response", "response": {"subtype": "success", "request_id": message["request_id"]}}), flush=True)
        print(json.dumps({"type": "result", "is_error": True}), flush=True)
'''


class TestInterrupt(SessionCase):
    def received(self) -> list[dict[str, object]]:
        return [json.loads(line) for line in (self.log / "received.jsonl").read_text().splitlines()]

    def received_or_empty(self) -> list[dict[str, object]]:
        path = self.log / "received.jsonl"
        return self.received() if path.exists() and path.read_text().endswith("\n") else []

    def test_a_session_with_no_native_interrupt_says_so(self) -> None:
        base = HarnessSession(mock.Mock(), str, "sid")
        self.assertFalse(base.interrupt())

    def test_claude_is_sent_a_control_request_and_its_answer_is_not_a_record(self) -> None:
        self.script.write_text(INTERRUPTIBLE)
        session = self.claude().open_session(self.spec())
        self.sessions.append(session)
        self.assertTrue(session.interrupt())
        self.assertTrue(session.interrupt())
        self.assertEqual({"type": "result", "is_error": True}, self.next_record(session),
                         "the control_response is consumed, so the first record the caller sees is the result")
        self.assertEqual({"type": "result", "is_error": True}, self.next_record(session))
        requests = self.received()
        self.assertEqual({"type": "control_request", "request": {"subtype": "interrupt"}},
                         {k: requests[0][k] for k in ("type", "request")})
        self.assertNotEqual(requests[0]["request_id"], requests[1]["request_id"])

    def test_pi_is_sent_abort(self) -> None:
        self.script.write_text(INTERRUPTIBLE)
        harness = PiHarness(self.tmp / "root", self.tmp / "ext.ts", "sys", command=[sys.executable, str(self.script)], grace=2.0,
                           environ={"PATH": os.environ["PATH"], "FAKE_DIR": str(self.log)})
        session = harness.open_session(self.spec())
        self.sessions.append(session)
        self.assertTrue(session.interrupt())
        deadline = time.monotonic() + WAIT
        # The fake creates the file before writing the line, so existence alone can catch it empty.
        while not self.received_or_empty() and time.monotonic() < deadline:
            time.sleep(0.02)
        self.assertEqual([{"type": "abort"}], self.received())
