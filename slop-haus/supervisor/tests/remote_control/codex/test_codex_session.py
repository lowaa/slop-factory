from __future__ import annotations

import os
import sys
import tempfile
import threading
import unittest
from pathlib import Path
from typing import Any
from unittest import mock

from fake_codex_live import FAKE_CODEX_LIVE
from slop_haus_supervisor.harness.base import RunSpec
from slop_haus_supervisor.harness.codex_session import THREAD_READY, CodexSession
from slop_haus_supervisor.remote_control.host.codex import RemoteControlCodex

CONV = "11111111-2222-4333-8444-555555555555"


class CodexSessionTest(unittest.TestCase):
    """The session on its own: what the host never asks of it today (a message during a turn, a cancel)."""

    def setUp(self) -> None:
        self.tmp = tempfile.mkdtemp(prefix="codex-session-test-")
        self.addCleanup(__import__("shutil").rmtree, self.tmp, ignore_errors=True)
        self.script = os.path.join(self.tmp, "fake.py")
        Path(self.script).write_text(FAKE_CODEX_LIVE)
        patcher = mock.patch.dict(os.environ, {"FAKE_DIR": self.tmp})
        patcher.start()
        self.addCleanup(patcher.stop)

    def open(self, mode: str) -> CodexSession:
        os.environ["FAKE_MODE"] = mode
        harness = RemoteControlCodex(Path(self.tmp, "state"), "https://s.example", "brief", command=[sys.executable, self.script], grace=5.0)
        session = harness.open_session(RunSpec("rc", Path(self.tmp), 0, "", ""), CONV)
        assert isinstance(session, CodexSession)
        self.addCleanup(session.close)
        return session

    def read_until(self, session: CodexSession, method: str) -> list[dict[str, Any]]:
        seen: list[dict[str, Any]] = []
        result: list[list[dict[str, Any]]] = []

        def read() -> None:
            while (record := session.next_record()) is not None:
                seen.append(record)
                if record.get("method") == method:
                    break
            result.append(seen)

        reader = threading.Thread(target=read, daemon=True)
        reader.start()
        reader.join(30)
        self.assertTrue(result, f"never saw {method}; saw {[r.get('method') for r in seen]}")
        return seen

    def requests(self, method: str) -> list[dict[str, Any]]:
        import json
        with open(os.path.join(self.tmp, "received.jsonl")) as f:
            return [r["params"] for r in map(json.loads, f) if r.get("method") == method]

    def test_a_message_during_a_turn_steers_it(self) -> None:
        session = self.open("hold")
        session.send("one")
        self.read_until(session, "turn/started")
        session.send("two")
        seen = self.read_until(session, "turn/completed")
        self.assertEqual("turn-1", self.requests("turn/steer")[0]["expectedTurnId"])
        self.assertEqual(1, len(self.requests("turn/start")))
        texts = [r["params"]["item"]["text"] for r in seen if r.get("method") == "item/completed"]
        self.assertEqual(["echo: one+two"], texts)

    def test_a_compact_asked_before_the_thread_is_ready_is_sent_once_it_is(self) -> None:
        session = self.open("")
        session.compact()
        seen = self.read_until(session, "turn/completed")
        self.assertEqual([{"threadId": session.thread_id}], self.requests("thread/compact/start"))
        self.assertIn("turn/started", [r.get("method") for r in seen])

    def test_interrupt_ends_the_turn_interrupted(self) -> None:
        session = self.open("hold")
        session.send("one")
        self.read_until(session, "turn/started")
        self.assertTrue(session.interrupt())
        seen = self.read_until(session, "turn/completed")
        self.assertEqual("interrupted", seen[-1]["params"]["turn"]["status"])
        self.assertEqual({"threadId": session.thread_id, "turnId": "turn-1"}, self.requests("turn/interrupt")[0])

    def test_an_interrupt_before_codex_names_the_turn_says_it_sent_nothing(self) -> None:
        session = self.open("hold")
        self.assertFalse(session.interrupt(), "no thread yet")
        session.send("one")
        self.read_until(session, "turn/started")
        self.assertTrue(session.interrupt())

    def test_an_interrupt_that_lost_the_race_is_swallowed(self) -> None:
        session = self.open("late-interrupt")
        session.send("one")
        self.read_until(session, "turn/started")
        with self.assertNoLogs("slop_haus_supervisor.harness.codex_session", "WARNING"):
            self.assertTrue(session.interrupt())
            session.send("two")  # a steer: the stand-in still holds the turn
            self.read_until(session, "turn/completed")
        self.assertEqual(1, len(self.requests("turn/interrupt")))

    def test_the_session_says_when_the_thread_is_ready_and_whether_it_is_new(self) -> None:
        session = self.open("")
        (ready,) = [r for r in self.read_until(session, THREAD_READY) if r.get("method") == THREAD_READY]
        self.assertEqual({"threadId": session.thread_id, "fresh": False, "model": "gpt-test-1", "modelProvider": "openai"}, ready["params"])
